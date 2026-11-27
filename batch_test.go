package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"testing"
)

func batchBlocks() (BlockSet, []PutBlockRequest) {
	store := BlockSet{Blocks: make(map[Reference]Block)}
	requests := make([]PutBlockRequest, 4)
	for i := range requests {
		data := bytes.Repeat([]byte{byte(i)}, int(BlockSize1KiB))
		ref, _ := ReferenceOf(data)
		requests[i] = PutBlockRequest{Data: data, Expected: &ref, TTL: TTL(i + 1), Options: BlockOptions{HighPriority: i&1 != 0, Root: i&2 != 0}}
	}
	return store, requests
}

func TestBlockBatches(t *testing.T) {
	ctx := context.Background()
	store, puts := batchBlocks()
	refs, err := store.PutBlock(ctx, puts)
	if err != nil || len(refs) != 4 {
		t.Fatal(refs, err)
	}
	gets := make([]BlockRequest, len(refs))
	for i, ref := range refs {
		gets[i] = BlockRequest{Reference: ref, Options: BlockOptions{Root: true, HighPriority: true}}
	}
	blocks, err := store.GetBlock(ctx, gets)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range blocks {
		if refs[i] != *puts[i].Expected || !bytes.Equal(b.Data, puts[i].Data) || b.Options != puts[i].Options {
			t.Fatalf("result %d differs", i)
		}
	}
	// A caller cannot change stored data through input or output slices.
	puts[0].Data[0] ^= 1
	blocks[1].Data[0] ^= 1
	if _, err := store.GetBlock(ctx, gets); err != nil {
		t.Fatal("data was not copied", err)
	}
	updates := slices.Clone(gets)
	for i := range updates {
		updates[i].Options = BlockOptions{}
	}
	changed, err := store.UpdateBlockOptions(ctx, updates)
	if err != nil || !slices.Equal(changed, []bool{false, true, true, true}) {
		t.Fatal(changed, err)
	}
	// Duplicate updates apply in request order.
	dup := []BlockRequest{{Reference: refs[0], Options: BlockOptions{Root: true}}, {Reference: refs[0]}, {Reference: Reference{99}}}
	changed, err = store.UpdateBlockOptions(ctx, dup)
	if err != nil || !slices.Equal(changed, []bool{true, true, false}) {
		t.Fatal(changed, err)
	}
}

func TestBatchFailurePrefix(t *testing.T) {
	ctx := context.Background()
	store, puts := batchBlocks()
	puts[2].Expected = &Reference{99}
	refs, err := store.PutBlock(ctx, puts)
	if !errors.Is(err, ErrInvalidBlock) || len(refs) != 2 || len(store.Blocks) != 2 {
		t.Fatal(refs, err)
	}
	requests := []BlockRequest{{Reference: refs[0]}, {Reference: Reference{99}}, {Reference: refs[1]}}
	got, err := store.GetBlock(ctx, requests)
	if !errors.Is(err, ErrMissingBlock) || len(got) != 3 || got[1].Data != nil || got[1].Options != (BlockOptions{}) {
		t.Fatal(got, err)
	}
	if VerifyBlock(got[0].Data, refs[0]) != nil || VerifyBlock(got[2].Data, refs[1]) != nil {
		t.Fatal("available blocks were not returned at their request positions")
	}
	block := store.Blocks[refs[1]]
	block.Data[0] ^= 1
	got, err = store.GetBlock(ctx, []BlockRequest{{Reference: refs[0]}, {Reference: refs[1]}})
	if !errors.Is(err, ErrInvalidBlock) || len(got) != 1 {
		t.Fatal(got, err)
	}
}

func TestBlockSetBatchLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, limit := range []int{-1, 0, 1, 2, 4} {
		store, puts := batchBlocks()
		store.MaxBatchSize = limit
		if store.BatchSize() < 0 {
			t.Fatal("negative reported limit")
		}
		if result, err := store.GetBlock(ctx, nil); len(result) != 0 || err != nil {
			t.Fatal(result, err)
		}
		if result, err := store.PutBlock(ctx, nil); len(result) != 0 || err != nil {
			t.Fatal(result, err)
		}
		if result, err := store.UpdateBlockOptions(ctx, nil); len(result) != 0 || err != nil {
			t.Fatal(result, err)
		}
		requests := make([]BlockRequest, 4)
		want := context.Canceled
		if limit != 0 && limit < 4 {
			want = ErrBatchTooLarge
		}
		if result, err := store.GetBlock(ctx, requests); len(result) != 0 || !errors.Is(err, want) {
			t.Fatal(result, err)
		}
		if result, err := store.PutBlock(ctx, puts); len(result) != 0 || !errors.Is(err, want) {
			t.Fatal(result, err)
		}
		if result, err := store.UpdateBlockOptions(ctx, requests); len(result) != 0 || !errors.Is(err, want) {
			t.Fatal(result, err)
		}
		if len(store.Blocks) != 0 {
			t.Fatal("rejected batch changed storage")
		}
	}
}

// batchProbe records storage calls without splitting a batch into single calls.
type batchProbe struct {
	BlockSet
	reads, writes []int
	writeHook     func([]PutBlockRequest) ([]Reference, error)
	readHook      func([]BlockRequest) ([]Block, error)
}

func (s *batchProbe) PutBlock(ctx context.Context, r []PutBlockRequest) ([]Reference, error) {
	s.writes = append(s.writes, len(r))
	if s.writeHook != nil {
		return s.writeHook(r)
	}
	return s.BlockSet.PutBlock(ctx, r)
}
func (s *batchProbe) GetBlock(ctx context.Context, r []BlockRequest) ([]Block, error) {
	s.reads = append(s.reads, len(r))
	if s.readHook != nil {
		return s.readHook(r)
	}
	return s.BlockSet.GetBlock(ctx, r)
}

func TestStreamBatches(t *testing.T) {
	data := make([]byte, 150*1024+23)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	for _, limit := range []int{0, 1, 2, 3, 63, 64, 65} {
		t.Run(string(rune('A'+limit)), func(t *testing.T) {
			store := &batchProbe{BlockSet: BlockSet{Blocks: make(map[Reference]Block), MaxBatchSize: limit}}
			contentStore := NewBlockCapabilityStore(store)
			cap, err := contentStore.EncodeContent(context.Background(), bytes.NewReader(data), Secret{}, BlockSize1KiB, BlockOptions{HighPriority: true})
			if err != nil {
				t.Fatal(err)
			}
			blocks := BlockSet{Blocks: make(map[Reference]Block)}
			want, _ := NewBlockCapabilityStore(blocks).Encode(t.Context(), data, Secret{}, BlockSize1KiB, BlockOptions{})
			if cap != want || len(store.Blocks) != len(blocks.Blocks) {
				t.Fatal("encoding changed")
			}
			if limit != 1 && slices.Max(store.writes) <= 1 {
				t.Fatal("writes were not batched")
			}
			for _, n := range store.writes {
				if n > 64 || limit > 0 && n > limit {
					t.Fatal("write limit exceeded", n)
				}
			}
			reader, err := contentStore.NewReaderWithSize(context.Background(), cap, int64(len(data)), BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(data))
			n, err := reader.Read(got)
			if err != nil || n != len(got) || !bytes.Equal(got, data) {
				t.Fatal(n, err)
			}
			if limit != 1 && slices.Max(store.reads) <= 1 {
				t.Fatal("reads were not batched")
			}
			for _, n := range store.reads {
				if n > 64 || limit > 0 && n > limit {
					t.Fatal("read limit exceeded", n)
				}
			}
		})
	}
}

func TestStreamBatchErrors(t *testing.T) {
	failure := errors.New("write failed")
	for _, hook := range []func([]PutBlockRequest) ([]Reference, error){
		func([]PutBlockRequest) ([]Reference, error) { return nil, failure },
		func([]PutBlockRequest) ([]Reference, error) { return nil, nil },
		func(r []PutBlockRequest) ([]Reference, error) { return make([]Reference, len(r)), nil },
	} {
		store := &batchProbe{writeHook: hook}
		cap, err := NewBlockCapabilityStore(store).EncodeContent(context.Background(), bytes.NewReader([]byte("short")), Secret{}, BlockSize1KiB, BlockOptions{})
		if err == nil || cap != (Capability{}) {
			t.Fatal(cap, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	encoder := treeEncoder{ctx: ctx, writes: []PutBlockRequest{{}}}
	if err := encoder.flush(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReadBatchErrors(t *testing.T) {
	data := make([]byte, 8*1024)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap, _ := NewBlockCapabilityStore(blocks).Encode(t.Context(), data, Secret{}, BlockSize1KiB, BlockOptions{})
	for _, failure := range []error{io.EOF, ErrMissingBlock} {
		store := &batchProbe{BlockSet: blocks}
		store.readHook = func(r []BlockRequest) ([]Block, error) {
			if len(r) == 1 {
				return blocks.GetBlock(context.Background(), r)
			}
			if failure == ErrMissingBlock {
				got, err := blocks.GetBlock(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				got[2] = Block{}
				return got, failure
			}
			got, err := blocks.GetBlock(context.Background(), r[:2])
			if err != nil {
				t.Fatal(err)
			}
			return got, failure
		}
		reader, _ := NewBlockCapabilityStore(store).NewReaderWithSize(context.Background(), cap, int64(len(data)), BlockOptions{})
		got := make([]byte, len(data))
		n, err := reader.Read(got)
		want := failure
		if failure == io.EOF {
			want = io.ErrUnexpectedEOF
		}
		if n != 2048 || !errors.Is(err, want) || !bytes.Equal(got[:n], data[:n]) {
			t.Fatal(n, err)
		}
	}
}

func FuzzBatchRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), uint8(0), false, uint16(0))
	f.Add(bytes.Repeat([]byte("batch"), 1000), uint8(3), false, uint16(1023))
	f.Add(bytes.Repeat([]byte("large"), 14000), uint8(2), true, uint16(123))
	f.Fuzz(func(t *testing.T, data []byte, limit uint8, large bool, start uint16) {
		if len(data) > 256*1024 {
			t.Skip()
		}
		size := BlockSize1KiB
		if large {
			size = BlockSize32KiB
		}
		store := &batchProbe{BlockSet: BlockSet{Blocks: make(map[Reference]Block), MaxBatchSize: int(limit)}}
		contentStore := NewBlockCapabilityStore(store)
		cap, err := contentStore.EncodeContent(context.Background(), bytes.NewReader(data), Secret{}, size, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		want, _ := NewBlockCapabilityStore(blocks).Encode(t.Context(), data, Secret{}, size, BlockOptions{})
		if cap != want || !maps.EqualFunc(store.Blocks, blocks.Blocks, equalBlocks) {
			t.Fatal("batch encoding differs")
		}
		reader, err := contentStore.NewReaderWithSize(context.Background(), cap, int64(len(data)), BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		offset := int(start) % (len(data) + 1)
		got := make([]byte, len(data)-offset+1)
		n, err := reader.ReadAt(got, int64(offset))
		if err != io.EOF || n != len(data)-offset || !bytes.Equal(got[:n], data[offset:]) {
			t.Fatal(n, err)
		}
		for _, calls := range [][]int{store.reads, store.writes} {
			for _, n := range calls {
				if n > 64 || limit > 0 && n > int(limit) {
					t.Fatal("batch limit", n)
				}
			}
		}
	})
}

func FuzzBatchOptions(f *testing.F) {
	f.Add([]byte{1, 2, 3, 3, 2, 1}, uint8(0), false)
	f.Add([]byte{0, 1, 2, 3}, uint8(3), true)
	f.Fuzz(func(t *testing.T, actions []byte, limit uint8, bad bool) {
		if len(actions) > 128 {
			actions = actions[:128]
		}
		store := BlockSet{Blocks: make(map[Reference]Block), MaxBatchSize: int(limit)}
		puts := make([]PutBlockRequest, len(actions))
		model := make(map[Reference]BlockOptions)
		for i, a := range actions {
			data := bytes.Repeat([]byte{a % 4}, 1024)
			ref, _ := ReferenceOf(data)
			puts[i] = PutBlockRequest{Data: data, Expected: &ref, Options: BlockOptions{HighPriority: a&1 != 0, Root: a&2 != 0}}
		}
		failureAt := len(puts)
		if bad && len(puts) > 0 {
			failureAt = len(puts) / 2
			puts[failureAt].Expected = &Reference{99}
		}
		refs, err := store.PutBlock(context.Background(), puts)
		if limit > 0 && len(puts) > int(limit) {
			if !errors.Is(err, ErrBatchTooLarge) || len(store.Blocks) != 0 {
				t.Fatal(err)
			}
			return
		}
		if len(refs) != failureAt || bad && len(puts) > 0 && !errors.Is(err, ErrInvalidBlock) || (!bad || len(puts) == 0) && err != nil {
			t.Fatal(len(refs), failureAt, err)
		}
		for _, r := range puts[:failureAt] {
			prev := model[*r.Expected]
			prev.Root = prev.Root || r.Options.Root
			prev.HighPriority = prev.HighPriority || r.Options.HighPriority
			model[*r.Expected] = prev
		}
		updates := make([]BlockRequest, len(refs))
		want := make([]bool, len(refs))
		for i, ref := range refs {
			options := BlockOptions{Root: actions[i]&4 != 0, HighPriority: actions[i]&8 != 0}
			want[i] = model[ref] != options
			model[ref] = options
			updates[i] = BlockRequest{Reference: ref, Options: options}
		}
		got, err := store.UpdateBlockOptions(context.Background(), updates)
		if err != nil || !slices.Equal(got, want) {
			t.Fatal(got, want, err)
		}
		blocks, err := store.GetBlock(context.Background(), updates)
		if err != nil {
			t.Fatal(err)
		}
		for i, block := range blocks {
			if block.Options != model[refs[i]] || VerifyBlock(block.Data, refs[i]) != nil {
				t.Fatal("model mismatch")
			}
		}
	})
}

func TestBatchCancellationBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, _ := batchBlocks()
	if _, err := NewBlockCapabilityStore(store).EncodeContent(ctx, bytes.NewReader(nil), Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	encoder := treeEncoder{ctx: ctx}
	if _, err := encoder.putNode(nil, 0, BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Cancellation after a successful full write must stop before the next read.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	probe := &batchProbe{BlockSet: store}
	probe.MaxBatchSize = 1
	probe.writeHook = func(r []PutBlockRequest) ([]Reference, error) {
		refs, err := store.PutBlock(ctx, r)
		cancel()
		return refs, err
	}
	if _, err := NewBlockCapabilityStore(probe).EncodeContent(ctx, bytes.NewReader(make([]byte, 2048)), Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, context.Canceled) || len(probe.writes) != 1 {
		t.Fatal(err, probe.writes)
	}
}

func TestReadBatchValidationAndCancellation(t *testing.T) {
	data := make([]byte, 4*1024)
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap, _ := NewBlockCapabilityStore(blocks).Encode(t.Context(), data, Secret{}, BlockSize1KiB, BlockOptions{})
	for _, mode := range []string{"short block", "cancel after root", "cancel after batch"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &batchProbe{BlockSet: blocks}
			store.readHook = func(r []BlockRequest) ([]Block, error) {
				got, err := blocks.GetBlock(ctx, r)
				if len(r) > 1 && mode == "short block" {
					got[0].Data = nil
				}
				if len(r) == 1 && mode == "cancel after root" || len(r) > 1 && mode == "cancel after batch" {
					cancel()
				}
				return got, err
			}
			reader, _ := NewBlockCapabilityStore(store).NewReaderWithSize(ctx, cap, int64(len(data)), BlockOptions{})
			n, err := reader.Read(make([]byte, len(data)))
			want := context.Canceled
			if mode == "short block" {
				want = ErrInvalidBlockSize
				if !errors.Is(err, ErrInvalidBlock) {
					t.Fatalf("short block in batch: %v", err)
				}
			}
			if n != 0 || !errors.Is(err, want) {
				t.Fatal(n, err)
			}
		})
	}
}
