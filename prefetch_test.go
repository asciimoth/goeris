package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"reflect"
	"testing"
)

type prefetchProbe struct {
	BlockSource
	limit    int
	hints    [][]BlockRequest
	contexts []context.Context
	onHint   func()
}

func (s *prefetchProbe) BatchSize() int { return s.limit }
func (s *prefetchProbe) PrefetchBlock(ctx context.Context, requests []BlockRequest) {
	s.hints = append(s.hints, append([]BlockRequest(nil), requests...))
	s.contexts = append(s.contexts, ctx)
	if s.onHint != nil {
		s.onHint()
	}
}

func TestCombinedPrefetch(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "context")
	requests := []BlockRequest{{Reference: Reference{1}, TTL: LocalTTL, Options: BlockOptions{Root: true}}, {Reference: Reference{2}, TTL: 3, Options: BlockOptions{HighPriority: true}}}
	a, b := &prefetchProbe{limit: 2}, &prefetchProbe{limit: 3}
	nested := NewCombinedBlockStore(b)
	store := NewCombinedBlockStore("ignored", a, nested)
	store.PrefetchBlock(ctx, requests)
	for _, probe := range []*prefetchProbe{a, b} {
		if len(probe.hints) != 1 || !reflect.DeepEqual(probe.hints[0], requests) || probe.contexts[0] != ctx {
			t.Fatal("hints changed or not broadcast")
		}
	}
	store.PrefetchBlock(ctx, append(requests, BlockRequest{}))
	store.PrefetchBlock(ctx, nil)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	store.PrefetchBlock(canceled, requests)
	if len(a.hints) != 1 || len(b.hints) != 1 {
		t.Fatal("invalid batch forwarded")
	}
	requests[0].TTL = 7
	if a.hints[0][0].TTL != LocalTTL {
		t.Fatal("request alias")
	}
	var empty *CombinedBlockStore
	empty.PrefetchBlock(ctx, requests)
	NewCombinedBlockStore().PrefetchBlock(ctx, requests[:1])
	live, stop := context.WithCancel(ctx)
	defer stop()
	a.onHint = stop
	store.PrefetchBlock(live, requests)
	if len(a.hints) != 2 || len(b.hints) != 1 {
		t.Fatal("cancellation between children ignored")
	}
}

func TestPrefetchTraversal(t *testing.T) {
	for _, size := range []int{0, 1024, 17001, 300000} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i/1024 + i%251)
		}
		cap, blocks, err := Encode(data, Secret{}, BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{-1, 0, 1, 3, 100} {
			probe := &prefetchProbe{BlockSource: blocks, limit: limit}
			options := BlockOptions{HighPriority: true, Root: true}
			if err := Prefetch(context.Background(), cap, probe, 7, options); err != nil {
				t.Fatal(err)
			}
			hinted := map[Reference]bool{}
			for _, requests := range probe.hints {
				if len(requests) > 64 || limit > 0 && len(requests) > limit || limit < 0 && len(requests) > 1 {
					t.Fatal("hint limit")
				}
				for _, r := range requests {
					hinted[r.Reference] = true
					if r.TTL != 7 || r.Options != options {
						t.Fatal("lost options")
					}
				}
			}
			for ref := range blocks.Blocks {
				if !hinted[ref] {
					t.Fatal("unhinted block")
				}
			}
		}
	}
}

func TestPrefetchErrors(t *testing.T) {
	ctx := context.Background()
	cap, blocks, _ := Encode(bytes.Repeat([]byte{3}, 2048), Secret{}, BlockSize1KiB)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		cap    Capability
		source BlockSource
		ctx    context.Context
		want   error
	}{
		{Capability{}, blocks, ctx, ErrInvalidCapability}, {cap, nil, ctx, ErrInvalidCapability},
		{cap, blocks, canceled, context.Canceled}, {cap, BlockSet{}, ctx, ErrMissingBlock},
		{cap, malformedSource(make([]byte, 32768)), ctx, ErrInvalidBlockSize},
	} {
		if err := Prefetch(tc.ctx, tc.cap, tc.source, NoTTL, BlockOptions{}); !errors.Is(err, tc.want) {
			t.Fatalf("error %v want %v", err, tc.want)
		}
	}
	bad := cap
	bad.Root.Key[0] ^= 1
	if err := Prefetch(ctx, bad, blocks, NoTTL, BlockOptions{}); !errors.Is(err, ErrInvalidRootKey) {
		t.Fatal(err)
	}
	leaf, leafBlocks, _ := Encode(nil, Secret{}, BlockSize1KiB)
	probe := &prefetchProbe{BlockSource: leafBlocks}
	live, stop := context.WithCancel(ctx)
	defer stop()
	probe.onHint = stop
	if err := Prefetch(live, leaf, probe, NoTTL, BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReaderPrefetchAndSeek(t *testing.T) {
	data := make([]byte, 100*1024)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	cap, blocks, _ := Encode(data, Secret{}, BlockSize1KiB)
	probe := &prefetchProbe{BlockSource: blocks, limit: 3}
	reader, err := NewReaderWithSize(context.Background(), cap, probe, int64(len(data)), BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Seek(20*1024, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if len(probe.hints) != 1 || probe.hints[0][0].Reference != cap.Root.Reference {
		t.Fatal("seek must hint unknown root")
	}
	if _, err = reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if reader.readAhead != 0 {
		t.Fatal("random access speculates")
	}
	if _, err = reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if reader.readAhead != 4 {
		t.Fatal("sequential access not detected")
	}
	for range 5 {
		if _, err = reader.Read(make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if reader.readAhead != 32 {
		t.Fatal("window cap")
	}
	pos, _ := reader.Seek(0, io.SeekCurrent)
	if err := reader.Prefetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := reader.Seek(0, io.SeekCurrent)
	if pos != after {
		t.Fatal("prefetch moved position")
	}
	_, _ = reader.Seek(3*1024, io.SeekStart)
	_, _ = reader.Read(make([]byte, 1))
	if reader.readAhead != 0 {
		t.Fatal("window not reset")
	}
	reader.hintRange(cap.Root, cap.Level, 1000000, 1000001)
	reader.hintRange(cap.Root, cap.Level, 1, 1)
	_ = reader.Close()
	if err := reader.Prefetch(context.Background()); !errors.Is(err, stdfs.ErrClosed) {
		t.Fatal(err)
	}
}

func TestPrefetchDoesNotRequireLeaves(t *testing.T) {
	data := make([]byte, 60000)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	cap, blocks, _ := Encode(data, Secret{}, BlockSize1KiB)
	reader, err := NewReader(context.Background(), cap, blocks, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < reader.leaves; i++ {
		pair, err := reader.leafPair(i)
		if err != nil {
			t.Fatal(err)
		}
		delete(blocks.Blocks, pair.Reference)
	}
	if err := Prefetch(context.Background(), cap, blocks, NoTTL, BlockOptions{}); err != nil {
		t.Fatalf("read a leaf: %v", err)
	}
}

func FuzzPrefetchTree(f *testing.F) {
	f.Add([]byte("content"), uint8(1), false)
	f.Add(make([]byte, 17000), uint8(3), true)
	f.Fuzz(func(t *testing.T, data []byte, limit uint8, corrupt bool) {
		if len(data) > 1<<18 {
			t.Skip()
		}
		cap, blocks, err := Encode(data, Secret{}, BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		if corrupt {
			cap.Root.Key[0] ^= 1
		}
		probe := &prefetchProbe{BlockSource: blocks, limit: int(limit % 65)}
		err = Prefetch(context.Background(), cap, probe, NoTTL, BlockOptions{})
		if corrupt && cap.Level > 0 {
			if !errors.Is(err, ErrInvalidRootKey) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		for _, requests := range probe.hints {
			if len(requests) > 64 || probe.limit > 0 && len(requests) > probe.limit {
				t.Fatal("oversized batch")
			}
		}
	})
}

func FuzzCombinedPrefetch(f *testing.F) {
	f.Add(uint8(2), uint8(3), uint8(2), false)
	f.Add(uint8(0), uint8(0), uint8(65), true)
	f.Fuzz(func(t *testing.T, aLimit, bLimit, count uint8, cancelAfterFirst bool) {
		a, b := &prefetchProbe{limit: int(aLimit % 65)}, &prefetchProbe{limit: int(bLimit % 65)}
		store := NewCombinedBlockStore(a, NewCombinedBlockStore(b))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if cancelAfterFirst {
			a.onHint = cancel
		}
		requests := make([]BlockRequest, int(count))
		for i := range requests {
			requests[i] = BlockRequest{Reference: Reference{byte(i)}, TTL: TTL(i), Options: BlockOptions{Root: i%2 == 0}}
		}
		store.PrefetchBlock(ctx, requests)
		valid := count > 0 && (store.BatchSize() == 0 || int(count) <= store.BatchSize())
		if (len(a.hints) == 1) != valid || (len(b.hints) == 1) != (valid && !cancelAfterFirst) {
			t.Fatal("invalid broadcast")
		}
		if valid && !reflect.DeepEqual(a.hints[0], requests) {
			t.Fatal("changed requests")
		}
	})
}
