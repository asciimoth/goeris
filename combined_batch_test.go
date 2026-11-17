package eris

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

type combinedBatchProbe struct {
	BlockSet
	gets    [][]BlockRequest
	puts    [][]PutBlockRequest
	updates [][]BlockRequest
	get     func(context.Context, []BlockRequest) ([]Block, error)
	put     func(context.Context, []PutBlockRequest) ([]Reference, error)
	update  func(context.Context, []BlockRequest) ([]bool, error)
}

type combinedLimitProbe struct {
	BlockStore
	limit func() int
}

func (s combinedLimitProbe) BatchSize() int { return s.limit() }

func (s *combinedBatchProbe) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	s.gets = append(s.gets, slices.Clone(requests))
	if s.get != nil {
		return s.get(ctx, requests)
	}
	return s.BlockSet.GetBlock(ctx, requests)
}

func (s *combinedBatchProbe) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	s.puts = append(s.puts, slices.Clone(requests))
	if s.put != nil {
		return s.put(ctx, requests)
	}
	return s.BlockSet.PutBlock(ctx, requests)
}

func (s *combinedBatchProbe) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	s.updates = append(s.updates, slices.Clone(requests))
	if s.update != nil {
		return s.update(ctx, requests)
	}
	return s.BlockSet.UpdateBlockOptions(ctx, requests)
}

func TestCombinedBatchSizeFromChildren(t *testing.T) {
	source := func(limit int) any { return struct{ BlockSource }{BlockSet{MaxBatchSize: limit}} }
	sink := func(limit int) any { return struct{ BlockSink }{BlockSet{MaxBatchSize: limit}} }
	for _, test := range []struct {
		name    string
		entries []any
		want    int
	}{
		{"empty", nil, 1},
		{"skipped", []any{nil, struct{}{}}, 1},
		{"all unlimited", []any{source(0), sink(0), BlockSet{}}, 0},
		{"mixed", []any{BlockSet{}, source(8), sink(4), BlockSet{}}, 4},
		{"finite", []any{source(8), sink(4)}, 4},
		{"read limit", []any{source(2), sink(8)}, 2},
		{"write limit", []any{source(8), sink(2)}, 2},
		{"single request", []any{BlockSet{}, source(8), sink(1)}, 1},
		{"negative normalized", []any{combinedLimitProbe{limit: func() int { return -1 }}, sink(8)}, 1},
		{"nested", []any{NewCombinedBlockStore(source(8), sink(3)), BlockSet{}}, 3},
		{"nested unlimited", []any{NewCombinedBlockStore(source(0), sink(0)), BlockSet{}}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewCombinedBlockStore(test.entries...)
			if got := store.BatchSize(); got != test.want {
				t.Fatalf("BatchSize() = %d, want %d", got, test.want)
			}
			if test.want == 0 {
				return
			}
			// The shared limit applies to every operation, even when only a
			// read-only or write-only child supplies the smallest limit.
			ctx := context.Background()
			requests := make([]BlockRequest, test.want+1)
			if got, err := store.GetBlock(ctx, requests); len(got) != 0 || err != ErrBatchTooLarge {
				t.Fatal(got, err)
			}
			if got, err := store.PutBlock(ctx, make([]PutBlockRequest, len(requests))); len(got) != 0 || err != ErrBatchTooLarge {
				t.Fatal(got, err)
			}
			if got, err := store.UpdateBlockOptions(ctx, requests); len(got) != 0 || err != ErrBatchTooLarge {
				t.Fatal(got, err)
			}
		})
	}
}

func TestCombinedBatchSizeTracksChildren(t *testing.T) {
	child := &BlockSet{}
	inner := NewCombinedBlockStore(child, BlockSet{})
	outer := NewCombinedBlockStore(BlockSet{MaxBatchSize: 8}, inner)
	for _, limit := range []int{0, 4, 1, 16, 0} {
		child.MaxBatchSize = limit
		want := 8
		if limit > 0 {
			want = min(want, limit)
		}
		if inner.BatchSize() != limit || outer.BatchSize() != want {
			t.Fatal("nested limits did not follow the child", inner.BatchSize(), outer.BatchSize())
		}
	}
}

func TestCombinedSmallCallsDoNotQueryLimits(t *testing.T) {
	blocks, puts := batchBlocks()
	child := combinedLimitProbe{BlockStore: blocks, limit: func() int {
		t.Fatal("small call queried a child limit")
		return 0
	}}
	store := NewCombinedBlockStore(child)
	ctx := context.Background()
	if _, err := store.PutBlock(ctx, puts[:1]); err != nil {
		t.Fatal(err)
	}
	request := []BlockRequest{{Reference: *puts[0].Expected}}
	if _, err := store.GetBlock(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBlockOptions(ctx, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.GetBlock(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutBlock(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBlockOptions(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedUnlimitedBatch(t *testing.T) {
	blocks, puts := batchBlocks()
	child := &combinedBatchProbe{BlockSet: blocks}
	store := NewCombinedBlockStore(child, BlockSet{})
	// The automatic encoder limit of 64 must not limit direct storage calls.
	puts = slices.Repeat(puts, 17)
	refs, err := store.PutBlock(context.Background(), puts)
	if err != nil || len(refs) != len(puts) || len(child.puts) != 1 || len(child.puts[0]) != len(puts) {
		t.Fatal(len(refs), err, len(child.puts))
	}
	requests := make([]BlockRequest, len(refs))
	for i, ref := range refs {
		requests[i] = BlockRequest{Reference: ref}
	}
	got, err := store.GetBlock(context.Background(), requests)
	if err != nil || len(got) != len(requests) || len(child.gets) != 1 || len(child.gets[0]) != len(requests) {
		t.Fatal(len(got), err, len(child.gets))
	}
	changed, err := store.UpdateBlockOptions(context.Background(), requests)
	if err != nil || len(changed) != len(requests) || len(child.updates) != 1 || len(child.updates[0]) != len(requests) {
		t.Fatal(len(changed), err, len(child.updates))
	}
}

func TestCombinedNativeBatchLimits(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 2, 3, 4, 8} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			blocks, puts := batchBlocks()
			original := slices.Clone(puts)
			blocks.MaxBatchSize = limit
			child := &combinedBatchProbe{BlockSet: blocks}
			ctx := context.Background()
			child.put = func(gotCtx context.Context, r []PutBlockRequest) ([]Reference, error) {
				if gotCtx != ctx {
					t.Fatal("context changed")
				}
				return blocks.PutBlock(gotCtx, r)
			}
			store := NewCombinedBlockStore(child)
			if limit := store.BatchSize(); limit > 0 && limit < len(puts) {
				puts = puts[:limit]
				original = original[:limit]
			}
			refs, err := store.PutBlock(ctx, puts)
			if err != nil || len(refs) != len(puts) || !reflect.DeepEqual(puts, original) {
				t.Fatal(refs, err)
			}
			gets := make([]BlockRequest, len(refs))
			for i, ref := range refs {
				gets[i] = BlockRequest{Reference: ref, TTL: puts[i].TTL, Options: puts[i].Options}
			}
			got, err := store.GetBlock(ctx, gets)
			if err != nil || len(got) != len(puts) {
				t.Fatal(got, err)
			}
			for i, block := range got {
				if !bytes.Equal(block.Data, puts[i].Data) || block.Options != puts[i].Options {
					t.Fatal("read result changed", i)
				}
			}
			updates := slices.Clone(gets)
			for i := range updates {
				updates[i].Options.Root = !updates[i].Options.Root
			}
			changed, err := store.UpdateBlockOptions(ctx, updates)
			if err != nil || !slices.Equal(changed, []bool{true, true, true, true}[:len(puts)]) {
				t.Fatal(changed, err)
			}
			if !reflect.DeepEqual(slices.Concat(child.puts...), puts) ||
				!reflect.DeepEqual(slices.Concat(child.gets...), gets) ||
				!reflect.DeepEqual(slices.Concat(child.updates...), updates) {
				t.Fatal("child requests changed")
			}
			want := limit
			if want < 0 {
				want = 1
			} else if want == 0 || want > 4 {
				want = 4
			}
			if len(child.puts[0]) != want || len(child.gets[0]) != want || len(child.updates[0]) != want {
				t.Fatal("child batch limit was not used")
			}
		})
	}
}

func TestCombinedNativeBatchBoundaries(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 4} {
		child := &combinedBatchProbe{BlockSet: BlockSet{MaxBatchSize: limit}}
		store := NewCombinedBlockStore(child)
		if store.BatchSize() != child.BatchSize() {
			t.Fatal("unexpected reported limit", store.BatchSize())
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, size := range []int{0, 1, 2, 4, 5} {
			var want error
			if store.BatchSize() > 0 && size > store.BatchSize() {
				want = ErrBatchTooLarge
			} else if size > 0 {
				want = context.Canceled
			}
			got, err := store.GetBlock(ctx, make([]BlockRequest, size))
			if len(got) != 0 || err != want {
				t.Fatal(limit, size, got, err)
			}
			refs, err := store.PutBlock(ctx, make([]PutBlockRequest, size))
			if len(refs) != 0 || err != want {
				t.Fatal(limit, size, refs, err)
			}
			changed, err := store.UpdateBlockOptions(ctx, make([]BlockRequest, size))
			if len(changed) != 0 || err != want {
				t.Fatal(limit, size, changed, err)
			}
		}
		if len(child.gets)+len(child.puts)+len(child.updates) != 0 {
			t.Fatal("boundary checks called child storage")
		}
	}
	store := &CombinedBlockStore{}
	if got, err := store.GetBlock(context.Background(), make([]BlockRequest, 1)); len(got) != 0 || err != ErrNoBlockStorage {
		t.Fatal(got, err)
	}
}

func TestCombinedNativeWriteFallback(t *testing.T) {
	for _, failFallback := range []bool{false, true} {
		t.Run(fmt.Sprint(failFallback), func(t *testing.T) {
			blocks, puts := batchBlocks()
			firstFailure, lastFailure := errors.New("first failed"), errors.New("last failed")
			first := &combinedBatchProbe{BlockSet: blocks}
			first.put = func(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
				for i, r := range requests {
					if *r.Expected == *puts[1].Expected {
						got, err := blocks.PutBlock(ctx, requests[:i])
						if err != nil {
							t.Fatal(err)
						}
						return got, firstFailure
					}
				}
				return blocks.PutBlock(ctx, requests)
			}
			last := &combinedBatchProbe{BlockSet: BlockSet{Blocks: make(map[Reference]Block)}}
			if failFallback {
				last.put = func(context.Context, []PutBlockRequest) ([]Reference, error) { return nil, lastFailure }
			}
			store := NewCombinedBlockStore(nil, first, struct{}{}, last)
			callbacks := 0
			store.OnError = func(index int, err error) {
				callbacks++
				if index != 1 || err != firstFailure {
					t.Fatal(index, err)
				}
			}
			refs, err := store.PutBlock(context.Background(), puts)
			if failFallback {
				if err != lastFailure || len(refs) != 1 || len(first.puts) != 1 || len(blocks.Blocks) != 1 {
					t.Fatal(refs, err, first.puts)
				}
			} else if err != nil || len(refs) != 4 || len(first.puts) != 2 || len(first.puts[1]) != 2 || len(blocks.Blocks) != 3 {
				t.Fatal(refs, err, first.puts)
			}
			if callbacks != 1 || len(last.puts) != 1 || len(last.puts[0]) != 1 || *last.puts[0][0].Expected != *puts[1].Expected {
				t.Fatal("successful writes were retried", callbacks, last.puts)
			}
		})
	}
}

func TestCombinedNativeReadFallback(t *testing.T) {
	ctx := context.Background()
	blocks, puts := batchBlocks()
	refs, err := blocks.PutBlock(ctx, puts)
	if err != nil {
		t.Fatal(err)
	}
	firstBlocks := BlockSet{Blocks: map[Reference]Block{refs[0]: blocks.Blocks[refs[0]], refs[3]: blocks.Blocks[refs[3]]}}
	first := &combinedBatchProbe{BlockSet: firstBlocks}
	last := &combinedBatchProbe{BlockSet: blocks}
	store := NewCombinedBlockStore(first, last)
	requests := []BlockRequest{{Reference: refs[0]}, {Reference: refs[1]}, {Reference: refs[2]}, {Reference: refs[3]}, {Reference: Reference{99}}}
	got, err := store.GetBlock(ctx, requests)
	if !errors.Is(err, ErrMissingBlock) || len(got) != 5 || got[4].Data != nil {
		t.Fatal(got, err)
	}
	for i := range 4 {
		if !bytes.Equal(got[i].Data, puts[i].Data) || got[i].Options != puts[i].Options {
			t.Fatal("read order or options changed", i)
		}
	}
	if len(first.gets) != 1 || len(last.gets) != 2 || len(last.gets[0]) != 2 || len(last.gets[1]) != 1 {
		t.Fatal("missing reads were not grouped", first.gets, last.gets)
	}
}

func TestCombinedNativeUpdateFallback(t *testing.T) {
	ctx := context.Background()
	blocks, puts := batchBlocks()
	refs, err := blocks.PutBlock(ctx, puts)
	if err != nil {
		t.Fatal(err)
	}
	first := &combinedBatchProbe{BlockSet: BlockSet{Blocks: map[Reference]Block{refs[0]: blocks.Blocks[refs[0]]}}}
	last := &combinedBatchProbe{BlockSet: blocks}
	store := NewCombinedBlockStore(first, last)
	requests := make([]BlockRequest, 4)
	for i, ref := range refs {
		options := puts[i].Options
		options.Root = !options.Root
		requests[i] = BlockRequest{Reference: ref, Options: options}
	}
	changed, err := store.UpdateBlockOptions(ctx, requests)
	if err != nil || !slices.Equal(changed, []bool{true, true, true, true}) {
		t.Fatal(changed, err)
	}
	if len(first.updates) != 1 || len(last.updates) != 1 || len(last.updates[0]) != 3 {
		t.Fatal("unchanged updates were not grouped", first.updates, last.updates)
	}
	// The successful first update must not reach the fallback store.
	if blocks.Blocks[refs[0]].Options != puts[0].Options {
		t.Fatal("successful update was repeated")
	}
}

func TestCombinedNativeReadMissingThenFailure(t *testing.T) {
	for _, recoverFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverFailure), func(t *testing.T) {
			ctx := context.Background()
			blocks, puts := batchBlocks()
			refs, err := blocks.PutBlock(ctx, puts)
			if err != nil {
				t.Fatal(err)
			}
			missing := fmt.Errorf("first missing: %w", ErrMissingBlock)
			lastMissing := fmt.Errorf("last missing: %w", ErrMissingBlock)
			failure, lastFailure := errors.New("read stopped"), errors.New("fallback stopped")
			first := &combinedBatchProbe{BlockSet: blocks}
			first.get = func(ctx context.Context, requests []BlockRequest) ([]Block, error) {
				if len(requests) == 4 {
					return []Block{{}, blocks.Blocks[refs[1]]}, errors.Join(missing, failure)
				}
				return blocks.GetBlock(ctx, requests)
			}
			last := &combinedBatchProbe{BlockSet: blocks}
			last.get = func(ctx context.Context, requests []BlockRequest) ([]Block, error) {
				if requests[0].Reference == refs[0] {
					return []Block{{}}, lastMissing
				}
				if !recoverFailure {
					return nil, lastFailure
				}
				return blocks.GetBlock(ctx, requests)
			}
			store := NewCombinedBlockStore(first, last)
			requests := make([]BlockRequest, 4)
			for i, ref := range refs {
				requests[i].Reference = ref
			}
			got, err := store.GetBlock(ctx, requests)
			if !errors.Is(err, lastMissing) || errors.Is(err, failure) || got[0].Data != nil {
				t.Fatal(got, err)
			}
			if recoverFailure {
				if len(got) != 4 || err != lastMissing || len(first.gets) != 2 {
					t.Fatal(got, err, first.gets)
				}
			} else if len(got) != 2 || !errors.Is(err, lastFailure) || len(first.gets) != 1 {
				t.Fatal(got, err, first.gets)
			}
		})
	}
}

func TestCombinedNativeCancellation(t *testing.T) {
	for _, cancelInCallback := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelInCallback), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			blocks, puts := batchBlocks()
			first := &combinedBatchProbe{BlockSet: blocks}
			failure := errors.New("child failed")
			first.put = func(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
				got, err := blocks.PutBlock(ctx, requests[:1])
				if err != nil {
					t.Fatal(err)
				}
				if !cancelInCallback {
					cancel()
				}
				return got, failure
			}
			last := &combinedBatchProbe{BlockSet: blocks}
			store := NewCombinedBlockStore(first, last)
			callbacks := 0
			store.OnError = func(index int, err error) {
				callbacks++
				if index != 0 || err != failure {
					t.Fatal(index, err)
				}
				cancel()
			}
			refs, err := store.PutBlock(ctx, puts)
			if len(refs) != 1 || err != context.Canceled || callbacks != 1 || len(last.puts) != 0 {
				t.Fatal(refs, err, callbacks, last.puts)
			}
		})
	}
}

func TestCombinedNativeMalformedResults(t *testing.T) {
	for _, count := range []int{0, 1, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx := context.Background()
			store := NewCombinedBlockStore(responseStore{count: count})
			if got, err := store.GetBlock(ctx, make([]BlockRequest, 4)); len(got) != 0 || err != ErrInvalidBatchResult {
				t.Fatal(got, err)
			}
			if got, err := store.PutBlock(ctx, make([]PutBlockRequest, 4)); len(got) != 0 || err != ErrInvalidBatchResult {
				t.Fatal(got, err)
			}
			if got, err := store.UpdateBlockOptions(ctx, make([]BlockRequest, 4)); len(got) != 0 || err != ErrInvalidBatchResult {
				t.Fatal(got, err)
			}
		})
	}
}

func TestCombinedNativeMissingErrorPreserved(t *testing.T) {
	storageError := errors.New("storage detail")
	missing := errors.Join(fmt.Errorf("missing: %w", ErrMissingBlock), storageError)
	child := &combinedBatchProbe{}
	child.get = func(_ context.Context, requests []BlockRequest) ([]Block, error) {
		return make([]Block, len(requests)), missing
	}
	store := NewCombinedBlockStore(child)
	got, err := store.GetBlock(context.Background(), make([]BlockRequest, 4))
	if len(got) != 4 || err != missing {
		t.Fatal("missing error changed", got, err)
	}
}

func TestCombinedNativeCompletedBatchError(t *testing.T) {
	ctx := context.Background()
	blocks, puts := batchBlocks()
	refs, err := blocks.PutBlock(ctx, puts)
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]BlockRequest, len(refs))
	for i, ref := range refs {
		requests[i] = BlockRequest{Reference: ref, Options: BlockOptions{Root: true}}
	}
	failure := errors.New("completed batch failed")
	first := &combinedBatchProbe{BlockSet: blocks}
	first.get = func(ctx context.Context, requests []BlockRequest) ([]Block, error) {
		got, err := blocks.GetBlock(ctx, requests)
		if err != nil {
			t.Fatal(err)
		}
		return got, failure
	}
	first.put = func(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
		got, err := blocks.PutBlock(ctx, requests)
		if err != nil {
			t.Fatal(err)
		}
		return got, failure
	}
	first.update = func(ctx context.Context, requests []BlockRequest) ([]bool, error) {
		got, err := blocks.UpdateBlockOptions(ctx, requests)
		if err != nil {
			t.Fatal(err)
		}
		return got, failure
	}
	last := &combinedBatchProbe{BlockSet: blocks}
	store := NewCombinedBlockStore(first, last)
	store.OnError = func(int, error) { t.Fatal("reported the returned error") }
	if got, err := store.GetBlock(ctx, requests); len(got) != len(requests) || err != failure {
		t.Fatal(len(got), err)
	}
	if got, err := store.PutBlock(ctx, puts); !slices.Equal(got, refs) || err != failure {
		t.Fatal(got, err)
	}
	// All results are true so none need fallback for unchanged options.
	for i := range requests {
		requests[i].Options.Root = !puts[i].Options.Root
	}
	if got, err := store.UpdateBlockOptions(ctx, requests); !slices.Equal(got, []bool{true, true, true, true}) || err != failure {
		t.Fatal(got, err)
	}
	if len(last.gets)+len(last.puts)+len(last.updates) != 0 {
		t.Fatal("completed requests were retried")
	}
}

func TestCombinedNativeNestedStreaming(t *testing.T) {
	ctx := context.Background()
	child := &combinedBatchProbe{BlockSet: BlockSet{Blocks: make(map[Reference]Block), MaxBatchSize: 3}}
	inner := NewCombinedBlockStore(child)
	store := NewCombinedBlockStore(inner)
	data := make([]byte, 20*1024+19)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	capability, err := EncodeContent(ctx, bytes.NewReader(data), Secret{}, BlockSize1KiB, store, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithSize(ctx, capability, store, int64(len(data)), BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})
	got := make([]byte, len(data))
	if n, err := reader.Read(got); n != len(data) || err != nil || !bytes.Equal(got, data) {
		t.Fatal(n, err)
	}
	maxPut, maxGet := 0, 0
	for _, requests := range child.puts {
		maxPut = max(maxPut, len(requests))
	}
	for _, requests := range child.gets {
		maxGet = max(maxGet, len(requests))
	}
	if maxPut != 3 || maxGet != 3 {
		t.Fatal("nested stores did not forward batches", maxPut, maxGet)
	}
}
