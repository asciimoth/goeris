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

	"github.com/asciimoth/goeris"
)

// modelCache uses one slice in access order, independently of the cache's two
// linked lists. It checks both byte accounting and the exact eviction victims.
type modelEntry struct {
	ref   eris.Reference
	block eris.Block
	high  bool
}
type modelCache struct {
	capacity int
	entries  []modelEntry // Oldest first, regardless of priority.
}

func (m *modelCache) remove(ref eris.Reference) {
	for i, e := range m.entries {
		if e.ref == ref {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			return
		}
	}
}
func (m *modelCache) find(ref eris.Reference) (modelEntry, bool) {
	for _, e := range m.entries {
		if e.ref == ref {
			return e, true
		}
	}
	return modelEntry{}, false
}
func (m *modelCache) access(ref eris.Reference, block eris.Block, high bool) {
	if len(block.Data) > m.capacity {
		return
	}
	if old, ok := m.find(ref); ok {
		high = high || old.high
		block = old.block
	}
	high = high || block.Options.HighPriority
	if !high {
		protected := 0
		for _, e := range m.entries {
			if e.high {
				protected += len(e.block.Data)
			}
		}
		if len(block.Data) > m.capacity-protected {
			return
		}
	}
	m.remove(ref)
	m.entries = append(m.entries, modelEntry{ref, block, high})
	for {
		used := 0
		for _, e := range m.entries {
			used += len(e.block.Data)
		}
		if used <= m.capacity {
			return
		}
		victim := 0
		for i, e := range m.entries {
			if !e.high {
				victim = i
				break
			}
		}
		m.entries = append(m.entries[:victim], m.entries[victim+1:]...)
	}
}
func (m *modelCache) check(t testing.TB, c *Cache) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) != len(m.entries) {
		t.Fatalf("entry count: got %d want %d", len(c.entries), len(m.entries))
	}
	used := 0
	var regular, high []eris.Reference
	for _, want := range m.entries {
		used += len(want.block.Data)
		element := c.entries[want.ref]
		if element == nil {
			t.Fatalf("missing modeled entry %s", want.ref)
		}
		got := element.Value.(*entry)
		if got.ref != want.ref || got.high != want.high || got.block.Options != want.block.Options || !bytes.Equal(got.block.Data, want.block.Data) {
			t.Fatal("entry differs from model")
		}
		if want.high {
			high = append(high, want.ref)
		} else {
			regular = append(regular, want.ref)
		}
	}
	if c.used != used || c.used > c.size {
		t.Fatalf("byte usage: %d want %d <= %d", c.used, used, c.size)
	}
	for _, class := range []struct {
		high bool
		refs []eris.Reference
	}{{false, regular}, {true, high}} {
		var actual []eris.Reference
		for e := c.order(class.high).Back(); e != nil; e = e.Prev() {
			actual = append(actual, e.Value.(*entry).ref)
		}
		if !reflect.DeepEqual(actual, class.refs) {
			t.Fatal("LRU order differs from model")
		}
	}
}

// FuzzCacheOperations compares reads, writes, explicit option changes, failed
// writes, canceled reads, and cached hints against a separate eviction model.
func FuzzCacheOperations(f *testing.F) {
	f.Add(uint16(2), []byte{0, 0, 1, 1, 0, 2, 2, 0, 3, 1, 7, 2, 6, 0})
	f.Add(uint16(32), []byte{0, 7, 0, 0, 0, 1, 1, 6, 3, 6, 0, 6, 2, 5, 7, 5})
	f.Add(uint16(0), []byte{})
	f.Add(uint16(32), []byte{0, 0, 0, 1, 0, 2, 0, 7}) // Reject a large regular block without evicting smaller ones.
	f.Fuzz(func(t *testing.T, capacity uint16, operations []byte) {
		if len(operations) > 512 {
			operations = operations[:512]
		}
		s := newStore()
		requests := make([]eris.BlockRequest, 8)
		stored := make(map[eris.Reference]eris.Block)
		for i := range requests {
			size := 1024
			if i >= 6 {
				size = 32768
			}
			requests[i] = s.add(byte(i+1), size, eris.BlockOptions{HighPriority: i%3 == 0, Root: i%2 == 0})
			stored[requests[i].Reference] = clone(s.set.Blocks[requests[i].Reference])
		}
		limit := int(capacity%66)*1024 + 1
		c := newCache(t, s, limit)
		model := modelCache{capacity: limit}
		for i := 0; i+1 < len(operations); i += 2 {
			op, bits := operations[i], operations[i+1]
			r := requests[int(bits)%len(requests)]
			r.TTL = eris.TTL(op)
			r.Options = eris.BlockOptions{HighPriority: bits&16 != 0, Root: bits&32 != 0}
			block := stored[r.Reference]
			switch op % 8 {
			case 0, 1:
				before := s.count()
				_, hit := model.find(r.Reference)
				got := readOne(t, c, r)
				if got.Options != block.Options || !bytes.Equal(got.Data, block.Data) {
					t.Fatal("read differs from storage model")
				}
				wantCalls := before
				if !hit {
					wantCalls++
				}
				if s.count() != wantCalls {
					t.Fatal("unexpected source read")
				}
				model.access(r.Reference, block, r.Options.HighPriority)
				got.Data[0] ^= 255 // Returned bytes must never change the cache.
			case 2:
				data := append([]byte(nil), block.Data...)
				refs, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: data, Options: r.Options, TTL: r.TTL, Expected: &r.Reference}})
				if err != nil || len(refs) != 1 || refs[0] != r.Reference {
					t.Fatalf("put: %v %v", refs, err)
				}
				block.Options.HighPriority = block.Options.HighPriority || r.Options.HighPriority
				block.Options.Root = block.Options.Root || r.Options.Root
				stored[r.Reference] = block
				model.remove(r.Reference)
				data[0] ^= 255
			case 3:
				changed, err := c.UpdateBlockOptions(context.Background(), []eris.BlockRequest{r})
				if err != nil || len(changed) != 1 || changed[0] != (block.Options != r.Options) {
					t.Fatalf("update: %v %v", changed, err)
				}
				block.Options = r.Options
				stored[r.Reference] = block
				model.remove(r.Reference)
			case 4:
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if got, err := c.GetBlock(ctx, []eris.BlockRequest{r}); len(got) != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation ignored")
				}
			case 5:
				got, err := c.GetBlock(context.Background(), []eris.BlockRequest{{}})
				if len(got) != 1 || got[0].Data != nil || !errors.Is(err, eris.ErrMissingBlock) {
					t.Fatal("missing contract failed")
				}
			case 6:
				wrong := eris.Reference{}
				if _, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: block.Data, Expected: &wrong}}); !errors.Is(err, eris.ErrInvalidBlock) {
					t.Fatal("bad expected reference accepted")
				}
			case 7:
				if _, hit := model.find(r.Reference); hit {
					c.PrefetchBlock(context.Background(), []eris.BlockRequest{r})
					model.access(r.Reference, block, r.Options.HighPriority)
				}
			}
			model.check(t, c)
		}
	})
}

// FuzzCacheBatchResults checks missing slots, stopping errors, malformed source
// results, cancellation, batch limits, and request ownership in arbitrary order.
func FuzzCacheBatchResults(f *testing.F) {
	f.Add(byte(0), false, []byte{2, 0, 2, 1, 3})
	f.Add(byte(1), true, []byte{0, 1})
	f.Add(byte(0), false, []byte{4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, limitByte byte, canceled bool, sequence []byte) {
		if len(sequence) > 128 {
			sequence = sequence[:128]
		}
		s := newStore()
		s.limit = int(limitByte % 9)
		refs := make([]eris.BlockRequest, 8)
		blocks := make([]eris.Block, 8)
		codes := make(map[eris.Reference]int)
		for i := range refs {
			refs[i] = s.add(byte(i+1), 1024, eris.BlockOptions{HighPriority: i%2 == 0})
			blocks[i] = s.set.Blocks[refs[i].Reference]
			codes[refs[i].Reference] = i
		}
		failure := errors.New("source failure")
		missing := fmt.Errorf("wrapped: %w", eris.ErrMissingBlock)
		s.get = func(_ context.Context, reqs []eris.BlockRequest) ([]eris.Block, error) {
			i := codes[reqs[0].Reference]
			switch i {
			case 0, 1:
				return []eris.Block{blocks[i]}, nil
			case 2:
				return []eris.Block{{}}, missing
			case 3:
				return nil, failure
			case 4:
				return nil, errors.Join(missing, context.Canceled)
			case 5:
				return nil, nil
			case 6:
				return []eris.Block{{Data: []byte{1}}}, nil
			default:
				return []eris.Block{{}, {}}, nil
			}
		}
		c := newCache(t, s, 2048)
		requests := make([]eris.BlockRequest, len(sequence))
		for i, v := range sequence {
			requests[i] = refs[v%8]
			requests[i].TTL = eris.TTL(v)
			requests[i].Options.Root = v&16 != 0
		}
		copyRequests := append([]eris.BlockRequest{}, requests...)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if canceled {
			cancel()
		}
		got, err := c.GetBlock(ctx, requests)
		if !reflect.DeepEqual(requests, copyRequests) {
			t.Fatal("request slice changed")
		}
		var expected []eris.Block
		var expectedErr error
		if s.limit > 0 && len(requests) > s.limit {
			expectedErr = eris.ErrBatchTooLarge
		} else {
			var firstMissing error
			for _, r := range requests {
				if canceled {
					expectedErr = context.Canceled
					break
				}
				i := codes[r.Reference]
				switch i {
				case 0, 1:
					expected = append(expected, blocks[i])
				case 2:
					expected = append(expected, eris.Block{})
					firstMissing = missing
				case 3:
					expectedErr = failure
				case 4:
					expectedErr = errors.Join(missing, context.Canceled)
				case 5, 7:
					expectedErr = eris.ErrInvalidBatchResult
				case 6:
					expectedErr = eris.ErrInvalidBlock
				}
				if expectedErr != nil {
					break
				}
			}
			expectedErr = errors.Join(firstMissing, expectedErr)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("results differ: got %d want %d", len(got), len(expected))
		}
		if (err == nil) != (expectedErr == nil) {
			t.Fatalf("error: %v want %v", err, expectedErr)
		}
		for _, sentinel := range []error{failure, eris.ErrMissingBlock, eris.ErrInvalidBlock, eris.ErrInvalidBatchResult, eris.ErrBatchTooLarge, context.Canceled} {
			if errors.Is(err, sentinel) != errors.Is(expectedErr, sentinel) {
				t.Fatalf("error %v differs from %v for %v", err, expectedErr, sentinel)
			}
		}
	})
}

// FuzzCachePrefetchLifecycle varies queue pressure, worker counts, cancellation,
// writes, reads, and shutdown. It checks data integrity and bounded cache state
// under concurrent calls, and is also useful with Go's race detector.
func FuzzCachePrefetchLifecycle(f *testing.F) {
	f.Add(byte(1), byte(1), []byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add(byte(3), byte(8), []byte{2, 2, 2, 1, 0, 5, 6, 7, 4, 3})
	f.Add(byte(2), byte(3), []byte{8, 16, 7, 10, 24, 21, 0})
	f.Fuzz(func(t *testing.T, workers, queue byte, operations []byte) {
		if len(operations) > 64 {
			operations = operations[:64]
		}
		s := newStore()
		requests := make([]eris.BlockRequest, 8)
		data := make(map[eris.Reference][]byte)
		for i := range requests {
			requests[i] = s.add(byte(i), 1024, eris.BlockOptions{HighPriority: i%2 == 0})
			data[requests[i].Reference] = append([]byte(nil), s.set.Blocks[requests[i].Reference].Data...)
		}
		c, err := New(s, Config{CapacityBytes: 3072, Workers: int(workers%4) + 1, QueueSize: int(queue%8) + 1})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		var wg sync.WaitGroup
		for lane := range 3 {
			wg.Go(func() {
				for _, op := range operations {
					if lane == 2 && op%11 == 10 {
						_ = c.Close()
					}
					r := requests[(int(op)+lane)%len(requests)]
					r.TTL = eris.TTL(op)
					r.Options.HighPriority = op&16 != 0
					ctx, cancel := context.WithCancel(context.Background())
					if op%5 == 0 {
						cancel()
					}
					c.PrefetchBlock(ctx, []eris.BlockRequest{r, r})
					if op%3 == 0 {
						got, err := c.GetBlock(ctx, []eris.BlockRequest{r})
						if err == nil {
							if len(got) != 1 || eris.VerifyBlock(got[0].Data, r.Reference) != nil {
								t.Error("invalid returned block")
							}
						} else if !errors.Is(err, context.Canceled) && !errors.Is(err, fs.ErrClosed) {
							t.Error(err)
						}
					}
					if op%7 == 0 {
						got, err := c.UpdateBlockOptions(ctx, []eris.BlockRequest{r})
						if err == nil {
							if len(got) != 1 {
								t.Error("invalid update result count")
							}
						} else if !errors.Is(err, context.Canceled) && !errors.Is(err, fs.ErrClosed) {
							t.Error(err)
						}
					}
					if op%8 == 0 {
						got, err := c.PutBlock(ctx, []eris.PutBlockRequest{{Data: data[r.Reference], TTL: r.TTL, Options: r.Options}})
						if err == nil {
							if len(got) != 1 || got[0] != r.Reference {
								t.Error("invalid put result")
							}
						} else if !errors.Is(err, context.Canceled) && !errors.Is(err, fs.ErrClosed) {
							t.Error(err)
						}
					}
					cancel()
				}
			})
		}
		wg.Wait()
		c.mu.Lock()
		used := 0
		for ref, e := range c.entries {
			block := e.Value.(*entry).block
			used += len(block.Data)
			if eris.VerifyBlock(block.Data, ref) != nil {
				t.Error("invalid cached block")
			}
		}
		if used != c.used || used > c.size || c.regular.Len()+c.high.Len() != len(c.entries) {
			t.Error("invalid cache accounting")
		}
		c.mu.Unlock()
		var closeWG sync.WaitGroup
		for range 3 {
			closeWG.Go(func() { _ = c.Close() })
		}
		closeWG.Wait()
		if c.used != 0 || len(c.entries) != 0 || len(c.queue) != 0 {
			t.Fatal("shutdown retained work")
		}
	})
}
