package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

// testStore serializes BlockSet access and records calls. Hooks run outside its
// lock so tests can stop storage I/O while the cache continues to serve hits.
type testStore struct {
	mu       sync.Mutex
	set      eris.BlockSet
	limit    int
	reads    []eris.BlockRequest
	contexts []context.Context
	puts     []eris.PutBlockRequest
	updates  []eris.BlockRequest
	get      func(context.Context, []eris.BlockRequest) ([]eris.Block, error)
	put      func(context.Context, []eris.PutBlockRequest) ([]eris.Reference, error)
	update   func(context.Context, []eris.BlockRequest) ([]bool, error)
}

func newStore() *testStore {
	return &testStore{set: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
}

func (s *testStore) BatchSize() int { return s.limit }
func (*testStore) PrefetchBlock(context.Context, []eris.BlockRequest) {
	panic("cache must issue reads, not forward hints")
}
func (s *testStore) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	s.mu.Lock()
	s.reads = append(s.reads, requests...)
	s.contexts = append(s.contexts, ctx)
	s.mu.Unlock()
	if s.get != nil {
		return s.get(ctx, requests)
	}
	return s.read(ctx, requests)
}
func (s *testStore) read(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.GetBlock(ctx, requests)
}
func (s *testStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	s.mu.Lock()
	s.puts = append(s.puts, requests...)
	s.mu.Unlock()
	if s.put != nil {
		return s.put(ctx, requests)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.PutBlock(ctx, requests)
}
func (s *testStore) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	s.mu.Lock()
	s.updates = append(s.updates, requests...)
	s.mu.Unlock()
	if s.update != nil {
		return s.update(ctx, requests)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.UpdateBlockOptions(ctx, requests)
}
func (s *testStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads)
}
func (s *testStore) add(id byte, size int, options eris.BlockOptions) eris.BlockRequest {
	data := bytes.Repeat([]byte{id}, size)
	ref, _ := eris.ReferenceOf(data)
	s.mu.Lock()
	s.set.Blocks[ref] = eris.Block{Data: data, Options: options}
	s.mu.Unlock()
	return eris.BlockRequest{Reference: ref}
}
func newCache(t testing.TB, source eris.BlockSource, capacity int) *Cache {
	t.Helper()
	c, err := New(source, Config{CapacityBytes: capacity, Workers: 2, QueueSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func readOne(t testing.TB, c *Cache, r eris.BlockRequest) eris.Block {
	t.Helper()
	blocks, err := c.GetBlock(context.Background(), []eris.BlockRequest{r})
	if err != nil || len(blocks) != 1 {
		t.Fatalf("GetBlock: %v, %v", blocks, err)
	}
	return blocks[0]
}
func contains(c *Cache, ref eris.Reference) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[ref] != nil
}
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker")
	}
	var zero T
	return zero
}

func TestNew(t *testing.T) {
	good := Config{CapacityBytes: 1024, Workers: 1, QueueSize: 1}
	for _, config := range []Config{{}, {0, 1, 1}, {-1, 1, 1}, {1024, 0, 1}, {1024, -1, 1}, {1024, 1, 0}, {1024, 1, -1}} {
		if c, err := New(newStore(), config); c != nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("New(%+v): %v, %v", config, c, err)
		}
	}
	if c, err := New(nil, good); c != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("nil source: %v, %v", c, err)
	}
	for _, limit := range []int{-1, 0, 1, 7} {
		s := newStore()
		s.limit = limit
		c := newCache(t, s, 1024)
		want := limit
		if want < 0 {
			want = 1
		}
		if c.BatchSize() != want {
			t.Fatalf("BatchSize = %d, want %d", c.BatchSize(), want)
		}
	}
}

func TestReadOwnershipAndOptions(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{Root: true})
	r.TTL = 7
	r.Options.HighPriority = true
	c := newCache(t, s, 1024)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "value")
	blocks, err := c.GetBlock(ctx, []eris.BlockRequest{r})
	if err != nil {
		t.Fatal(err)
	}
	if s.contexts[0] != ctx || s.reads[0] != r {
		t.Fatal("request or context changed")
	}
	blocks[0].Data[0] = 9
	cached := readOne(t, c, r)
	if cached.Data[0] != 1 || cached.Options != (eris.BlockOptions{Root: true}) {
		t.Fatalf("bad cached result: %+v", cached.Options)
	}
	cached.Data[0] = 8
	if readOne(t, c, r).Data[0] != 1 || s.count() != 1 {
		t.Fatal("cache does not own its bytes")
	}
	if !c.entries[r.Reference].Value.(*entry).high {
		t.Fatal("read hint did not protect entry")
	}
}

func TestEviction(t *testing.T) {
	for _, high := range []bool{false, true} {
		t.Run(fmt.Sprint("LRU/", high), func(t *testing.T) {
			s := newStore()
			c := newCache(t, s, 2048)
			a := s.add(1, 1024, eris.BlockOptions{HighPriority: high})
			b := s.add(2, 1024, eris.BlockOptions{HighPriority: high})
			d := s.add(3, 1024, eris.BlockOptions{HighPriority: high})
			readOne(t, c, a)
			readOne(t, c, b)
			readOne(t, c, a)
			readOne(t, c, d)
			if !contains(c, a.Reference) || contains(c, b.Reference) || !contains(c, d.Reference) {
				t.Fatal("wrong LRU victim")
			}
		})
	}
	t.Run("priority and promotion", func(t *testing.T) {
		s := newStore()
		c := newCache(t, s, 2048)
		a := s.add(1, 1024, eris.BlockOptions{})
		b := s.add(2, 1024, eris.BlockOptions{HighPriority: true})
		d := s.add(3, 1024, eris.BlockOptions{})
		readOne(t, c, a)
		readOne(t, c, b)
		a.Options.HighPriority = true
		readOne(t, c, a)
		readOne(t, c, d)
		if !contains(c, a.Reference) || !contains(c, b.Reference) || contains(c, d.Reference) {
			t.Fatal("regular entry displaced priority entry")
		}
		d.Options.HighPriority = true
		readOne(t, c, d)
		if contains(c, b.Reference) || !contains(c, d.Reference) {
			t.Fatal("high-priority LRU failed")
		}
	})
	t.Run("regular before older high", func(t *testing.T) {
		s := newStore()
		c := newCache(t, s, 2048)
		a := s.add(1, 1024, eris.BlockOptions{HighPriority: true})
		b := s.add(2, 1024, eris.BlockOptions{})
		d := s.add(3, 1024, eris.BlockOptions{})
		readOne(t, c, a)
		readOne(t, c, b)
		readOne(t, c, d)
		if !contains(c, a.Reference) || contains(c, b.Reference) {
			t.Fatal("priority was not preserved")
		}
	})
	t.Run("byte limit and oversized block", func(t *testing.T) {
		s := newStore()
		c := newCache(t, s, 32768)
		for i := byte(1); i < 5; i++ {
			readOne(t, c, s.add(i, 1024, eris.BlockOptions{}))
		}
		big := s.add(9, 32768, eris.BlockOptions{})
		readOne(t, c, big)
		if c.used != 32768 || len(c.entries) != 1 {
			t.Fatal("large block did not evict enough entries")
		}
		small := newCache(t, s, 1023)
		readOne(t, small, big)
		readOne(t, small, s.add(10, 1024, eris.BlockOptions{}))
		if small.used != 0 || len(small.entries) != 0 {
			t.Fatal("oversized block retained")
		}
	})
}

func TestReadBatchErrors(t *testing.T) {
	s := newStore()
	c := newCache(t, s, 4096)
	a := s.add(1, 1024, eris.BlockOptions{})
	missing := eris.BlockRequest{}
	stop := errors.New("stop")
	bad := eris.BlockRequest{Reference: eris.Reference{1}}
	s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
		if requests[0] == bad {
			return nil, stop
		}
		return s.read(ctx, requests)
	}
	blocks, err := c.GetBlock(context.Background(), []eris.BlockRequest{missing, a, missing, bad, a})
	if len(blocks) != 3 || blocks[0].Data != nil || blocks[1].Data == nil || blocks[2].Data != nil || !errors.Is(err, eris.ErrMissingBlock) || !errors.Is(err, stop) {
		t.Fatalf("prefix=%d error=%v", len(blocks), err)
	}
	blocks, err = c.GetBlock(context.Background(), []eris.BlockRequest{missing, a})
	if len(blocks) != 2 || !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing slots: %d, %v", len(blocks), err)
	}
	if len(c.entries) != 1 {
		t.Fatal("failed reads cached")
	}
}

func TestInvalidSourceResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		result []eris.Block
		want   error
	}{
		{"short", nil, eris.ErrInvalidBatchResult},
		{"long", []eris.Block{{}, {}}, eris.ErrInvalidBatchResult},
		{"empty", []eris.Block{{}}, eris.ErrInvalidBlock},
		{"wrong hash", []eris.Block{{Data: make([]byte, 1024)}}, eris.ErrInvalidBlock},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStore()
			c := newCache(t, s, 2048)
			s.get = func(context.Context, []eris.BlockRequest) ([]eris.Block, error) { return test.result, nil }
			blocks, err := c.GetBlock(context.Background(), []eris.BlockRequest{{Reference: eris.Reference{1}}})
			if len(blocks) != 0 || !errors.Is(err, test.want) || len(c.entries) != 0 {
				t.Fatalf("result=%v error=%v", blocks, err)
			}
		})
	}
}

func TestCancellationLimitsAndClose(t *testing.T) {
	s := newStore()
	s.limit = 1
	c := newCache(t, s, 1024)
	r := s.add(1, 1024, eris.BlockOptions{})
	data := readOne(t, c, r).Data
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, closed := range []bool{false, true} {
		if closed {
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, test := range []struct {
			name string
			call func(context.Context, int) (int, error)
		}{
			{"get", func(ctx context.Context, n int) (int, error) { v, e := c.GetBlock(ctx, repeat(r, n)); return len(v), e }},
			{"put", func(ctx context.Context, n int) (int, error) {
				v, e := c.PutBlock(ctx, repeat(eris.PutBlockRequest{Data: data}, n))
				return len(v), e
			}},
			{"update", func(ctx context.Context, n int) (int, error) {
				v, e := c.UpdateBlockOptions(ctx, repeat(r, n))
				return len(v), e
			}},
		} {
			if n, err := test.call(ctx, 0); n != 0 || err != nil {
				t.Fatalf("%s empty: %d %v", test.name, n, err)
			}
			if n, err := test.call(ctx, 2); n != 0 || !errors.Is(err, eris.ErrBatchTooLarge) {
				t.Fatalf("%s oversize: %d %v", test.name, n, err)
			}
			if n, err := test.call(ctx, 1); n != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("%s canceled: %d %v", test.name, n, err)
			}
			if closed {
				if n, err := test.call(context.Background(), 1); n != 0 || !errors.Is(err, fs.ErrClosed) {
					t.Fatalf("%s closed: %d %v", test.name, n, err)
				}
			}
		}
	}
	if c.used != 0 || len(c.entries) != 0 {
		t.Fatal("Close retained blocks")
	}
	var zero Cache
	if _, err := zero.GetBlock(context.Background(), []eris.BlockRequest{r}); !errors.Is(err, fs.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := zero.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: data}}); !errors.Is(err, fs.ErrClosed) {
		t.Fatal(err)
	}
	zero.PrefetchBlock(context.Background(), []eris.BlockRequest{r})
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}
func repeat[T any](value T, n int) []T {
	result := make([]T, n)
	for i := range result {
		result[i] = value
	}
	return result
}

func TestWriteThrough(t *testing.T) {
	s := newStore()
	c := newCache(t, s, 4096)
	r := s.add(1, 1024, eris.BlockOptions{HighPriority: true, Root: true})
	block := readOne(t, c, r)
	request := eris.PutBlockRequest{Data: block.Data, TTL: 9, Expected: &r.Reference}
	ctx := context.Background()
	refs, err := c.PutBlock(ctx, []eris.PutBlockRequest{request})
	if err != nil || len(refs) != 1 || refs[0] != r.Reference || !reflect.DeepEqual(s.puts[0], request) || contains(c, r.Reference) {
		t.Fatalf("put: %v, %v", refs, err)
	}
	if got := readOne(t, c, r); got.Options != block.Options {
		t.Fatal("put lost stored true flags")
	}
	r.TTL = 3
	changed, err := c.UpdateBlockOptions(ctx, []eris.BlockRequest{r})
	if err != nil || !reflect.DeepEqual(changed, []bool{true}) || s.updates[0] != r || contains(c, r.Reference) {
		t.Fatalf("update: %v %v", changed, err)
	}
	if got := readOne(t, c, r); got.Options != (eris.BlockOptions{}) {
		t.Fatal("explicit clear was not visible")
	}
	changed, err = c.UpdateBlockOptions(ctx, []eris.BlockRequest{r, {}})
	if err != nil || !reflect.DeepEqual(changed, []bool{false, false}) {
		t.Fatalf("unchanged/absent: %v %v", changed, err)
	}
}

func TestWriteErrors(t *testing.T) {
	s := newStore()
	c := newCache(t, s, 2048)
	r := s.add(1, 1024, eris.BlockOptions{})
	data := readOne(t, c, r).Data
	wrong := eris.Reference{}
	for _, req := range []eris.PutBlockRequest{{Data: []byte{1}}, {Data: data, Expected: &wrong}} {
		if result, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: data}, req, {Data: data}}); len(result) != 1 || !errors.Is(err, eris.ErrInvalidBlock) {
			t.Fatalf("invalid write: %v %v", result, err)
		}
	}
	readOnly := newCache(t, struct{ eris.BlockSource }{s}, 1024)
	if _, err := readOnly.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: data}}); !errors.Is(err, eris.ErrNoBlockStorage) {
		t.Fatal(err)
	}
	if _, err := readOnly.UpdateBlockOptions(context.Background(), []eris.BlockRequest{r}); !errors.Is(err, eris.ErrNoBlockStorage) {
		t.Fatal(err)
	}
	stop := errors.New("write failed")
	for _, test := range []struct {
		name      string
		count     int
		err, want error
		wrong     bool
	}{
		{"short", 0, nil, eris.ErrInvalidBatchResult, false},
		{"long", 2, nil, eris.ErrInvalidBatchResult, false},
		{"too many on error", 2, stop, eris.ErrInvalidBatchResult, false},
		{"failure", 0, stop, stop, false},
		{"wrong reference", 1, nil, eris.ErrInvalidBlock, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s.put = func(context.Context, []eris.PutBlockRequest) ([]eris.Reference, error) {
				ref := r.Reference
				if test.wrong {
					ref = wrong
				}
				return repeat(ref, test.count), test.err
			}
			readOne(t, c, r)
			if refs, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: data}}); len(refs) != 0 || !errors.Is(err, test.want) || contains(c, r.Reference) {
				t.Fatalf("put: %v %v", refs, err)
			}
			if test.wrong {
				return
			}
			s.update = func(context.Context, []eris.BlockRequest) ([]bool, error) { return repeat(false, test.count), test.err }
			readOne(t, c, r)
			if result, err := c.UpdateBlockOptions(context.Background(), []eris.BlockRequest{r}); len(result) != 0 || !errors.Is(err, test.want) || contains(c, r.Reference) {
				t.Fatalf("update: %v %v", result, err)
			}
		})
	}
}

func TestInvalidReadResultWithError(t *testing.T) {
	s := newStore()
	s.get = func(context.Context, []eris.BlockRequest) ([]eris.Block, error) {
		return []eris.Block{{}, {}}, eris.ErrMissingBlock
	}
	c := newCache(t, s, 1024)
	got, err := c.GetBlock(context.Background(), []eris.BlockRequest{{}})
	if len(got) != 0 || !errors.Is(err, eris.ErrInvalidBatchResult) {
		t.Fatalf("invalid failed result: %v %v", got, err)
	}
}

func TestSourceDataOwnership(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{})
	shared := s.set.Blocks[r.Reference]
	s.get = func(context.Context, []eris.BlockRequest) ([]eris.Block, error) {
		return []eris.Block{shared}, nil
	}
	c := newCache(t, s, 1024)
	readOne(t, c, r).Data[0] = 2
	if shared.Data[0] != 1 {
		t.Fatal("caller changed source data")
	}
	shared.Data[0] = 3
	if readOne(t, c, r).Data[0] != 1 {
		t.Fatal("source changed cache data")
	}
}

func TestClosedPutBeforeValidation(t *testing.T) {
	c := newCache(t, newStore(), 1024)
	_ = c.Close()
	if _, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{}}); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("closed put: %v", err)
	}
}

func TestRejectedAdmissionPreservesEntries(t *testing.T) {
	s := newStore()
	c := newCache(t, s, 32<<10)
	protected := s.add(1, 1024, eris.BlockOptions{HighPriority: true})
	older := s.add(2, 1024, eris.BlockOptions{})
	newer := s.add(3, 1024, eris.BlockOptions{})
	large := s.add(4, 32768, eris.BlockOptions{})
	for _, request := range []eris.BlockRequest{protected, older, newer} {
		readOne(t, c, request)
	}
	readOne(t, c, large)
	if contains(c, large.Reference) || !contains(c, older.Reference) || !contains(c, newer.Reference) || !contains(c, protected.Reference) {
		t.Fatal("a rejected block changed existing cache entries")
	}
	if c.used != 3*1024 || c.regular.Back().Value.(*entry).ref != older.Reference || c.regular.Front().Value.(*entry).ref != newer.Reference {
		t.Fatal("a rejected block changed accounting or recency")
	}
}

func TestFailedWritesReloadStoredOptions(t *testing.T) {
	for _, operation := range []string{"put", "update"} {
		t.Run(operation, func(t *testing.T) {
			s := newStore()
			r := s.add(1, 1024, eris.BlockOptions{})
			c := newCache(t, s, 1024)
			block := readOne(t, c, r)
			failure := errors.New("storage changed before failure")
			ctx := t.Context()
			r.TTL = 11
			r.Options = eris.BlockOptions{HighPriority: true, Root: true}
			s.put = func(gotCtx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
				if gotCtx != ctx || requests[0].TTL != r.TTL || requests[0].Options != r.Options {
					t.Error("write context or options changed")
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if _, err := s.set.PutBlock(gotCtx, requests); err != nil {
					t.Fatal(err)
				}
				return nil, failure
			}
			s.update = func(gotCtx context.Context, requests []eris.BlockRequest) ([]bool, error) {
				if gotCtx != ctx || requests[0] != r {
					t.Error("update context or options changed")
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				if _, err := s.set.UpdateBlockOptions(gotCtx, requests); err != nil {
					t.Fatal(err)
				}
				return nil, failure
			}
			var err error
			if operation == "put" {
				_, err = c.PutBlock(ctx, []eris.PutBlockRequest{{Data: block.Data, TTL: r.TTL, Options: r.Options}})
			} else {
				_, err = c.UpdateBlockOptions(ctx, []eris.BlockRequest{r})
			}
			if !errors.Is(err, failure) {
				t.Fatalf("write error: %v", err)
			}
			if got := readOne(t, c, r); got.Options != r.Options || s.count() != 2 {
				t.Fatal("failed write left stale options in cache")
			}
		})
	}
}
