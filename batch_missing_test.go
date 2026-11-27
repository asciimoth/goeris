package eris

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestGetBlockMissingThenCorrupt(t *testing.T) {
	ctx := context.Background()
	store, puts := batchBlocks()
	refs, err := store.PutBlock(ctx, puts)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := store.Blocks[refs[2]]
	corrupt.Data[0] ^= 1
	requests := []BlockRequest{{Reference: Reference{99}}, {Reference: refs[1]}, {Reference: refs[2]}, {Reference: refs[3]}}
	got, err := store.GetBlock(ctx, requests)
	if len(got) != 2 || !errors.Is(err, ErrMissingBlock) || !errors.Is(err, ErrInvalidBlock) {
		t.Fatal(len(got), err)
	}
	if got[0].Data != nil || got[0].Options != (BlockOptions{}) || VerifyBlock(got[1].Data, refs[1]) != nil || got[1].Options != puts[1].Options {
		t.Fatal("read positions or options changed")
	}
}

func TestReaderMissingLeaf(t *testing.T) {
	for _, missing := range []int{0, 2, 5} {
		content := make([]byte, 5*1024+37)
		for i := range content {
			content[i] = byte(i/1024 + 1)
		}
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		source := &batchProbe{BlockSet: blocks}
		reader, err := NewBlockCapabilityStore(source).NewReaderWithSize(ctx, capability, int64(len(content)), BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pair, err := reader.(*Reader).leafPair(int64(missing))
		if err != nil {
			t.Fatal(err)
		}
		delete(blocks.Blocks, pair.Reference)
		returnedLater := false
		source.readHook = func(requests []BlockRequest) ([]Block, error) {
			got, err := blocks.GetBlock(ctx, requests)
			for i, request := range requests {
				if request.Reference == pair.Reference && i+1 < len(got) {
					returnedLater = got[i+1].Data != nil
				}
			}
			return got, err
		}
		got := bytes.Repeat([]byte{0xcc}, len(content))
		n, err := reader.Read(got)
		if n != missing*1024 || !errors.Is(err, ErrMissingBlock) || !bytes.Equal(got[:n], content[:n]) {
			t.Fatal(n, err)
		}
		if !bytes.Equal(got[n:], bytes.Repeat([]byte{0xcc}, len(got)-n)) {
			t.Fatal("copied content across a missing leaf")
		}
		if missing < 5 {
			if !returnedLater {
				t.Fatal("storage did not return available leaves after the gap")
			}
			offset := (missing + 1) * 1024
			n, err = reader.ReadAt(got[:17], int64(offset))
			if n != 17 || err != nil || !bytes.Equal(got[:17], content[offset:offset+17]) {
				t.Fatal("later content cannot be read", n, err)
			}
		}
		_ = reader.Close()
	}
}

func TestReaderBatchInternalFailurePrefix(t *testing.T) {
	// Cross an internal-node boundary within one read. A failure in the next
	// subtree must preserve the bytes available before that boundary.
	const blockSize = int(BlockSize1KiB)
	content := make([]byte, 40*blockSize+17)
	for i := range content {
		content[i] = byte(i/blockSize + 1)
	}
	for _, corrupt := range []bool{false, true} {
		for _, limit := range []int{1, 3, 64} {
			blocks := BlockSet{Blocks: make(map[Reference]Block)}
			capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			node, err := DecryptNode(blocks.Blocks[capability.Root.Reference].Data, capability.Root, capability.Level)
			if err != nil {
				t.Fatal(err)
			}
			children, err := ParseInternalNode(node)
			if err != nil {
				t.Fatal(err)
			}
			failed := children[1].Reference
			wantErr := ErrMissingBlock
			if corrupt {
				blocks.Blocks[failed].Data[0] ^= 1
				wantErr = ErrInvalidBlock
			} else {
				delete(blocks.Blocks, failed)
			}
			blocks.MaxBatchSize = limit
			reader, err := NewBlockCapabilityStore(blocks).NewReaderWithSize(context.Background(), capability, int64(len(content)), BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			arity, _ := BlockSize1KiB.Arity()
			offset := (arity-1)*blockSize + 21
			wantN := blockSize - 21
			got := bytes.Repeat([]byte{0xcc}, 3*blockSize)
			n, err := reader.ReadAt(got, int64(offset))
			if n != wantN || !errors.Is(err, wantErr) || !bytes.Equal(got[:n], content[offset:offset+n]) {
				t.Fatalf("corrupt=%v, limit=%d: read %d bytes, error %v", corrupt, limit, n, err)
			}
			if !bytes.Equal(got[n:], bytes.Repeat([]byte{0xcc}, len(got)-n)) {
				t.Fatal("read changed bytes after the failed subtree")
			}
			_ = reader.Close()
		}
	}
}

func FuzzGetBlockMissing(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 0}, uint8(10), uint8(0))
	f.Add([]byte{0, 1, 2, 3}, uint8(0), uint8(4))
	f.Add([]byte{3, 2, 1, 0}, uint8(15), uint8(0))
	f.Add([]byte{}, uint8(15), uint8(1))
	f.Fuzz(func(t *testing.T, order []byte, present, limit uint8) {
		if len(order) > 128 {
			order = order[:128]
		}
		ctx := context.Background()
		store, puts := batchBlocks()
		refs, err := store.PutBlock(ctx, puts)
		if err != nil {
			t.Fatal(err)
		}
		for i, ref := range refs {
			if present&(1<<i) == 0 {
				delete(store.Blocks, ref)
			}
		}
		refs = append(refs, Reference{99})
		store.MaxBatchSize = int(limit)
		requests := make([]BlockRequest, len(order))
		for i, which := range order {
			requests[i] = BlockRequest{Reference: refs[which%5], Options: BlockOptions{Root: true, HighPriority: true}}
		}
		got, err := store.GetBlock(ctx, requests)
		if limit > 0 && len(requests) > int(limit) {
			if len(got) != 0 || !errors.Is(err, ErrBatchTooLarge) {
				t.Fatal(len(got), err)
			}
			return
		}
		if len(got) != len(requests) {
			t.Fatalf("returned %d results for %d requests", len(got), len(requests))
		}
		missing := false
		for i, request := range requests {
			want, ok := store.Blocks[request.Reference]
			if !ok {
				missing = true
				if got[i].Data != nil || got[i].Options != (BlockOptions{}) {
					t.Fatal("missing result is not zero", i)
				}
			} else if !bytes.Equal(got[i].Data, want.Data) || got[i].Options != want.Options {
				t.Fatal("available block or options lost", i)
			}
		}
		if missing {
			if !errors.Is(err, ErrMissingBlock) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzReaderMissingLeaf(f *testing.F) {
	f.Add(uint8(5), uint8(0), uint8(0))
	f.Add(uint8(18), uint8(17), uint8(3))
	f.Add(uint8(5), uint8(5), uint8(1))
	f.Fuzz(func(t *testing.T, full, missing, limit uint8) {
		full %= 48
		index := int(missing) % (int(full) + 1)
		content := make([]byte, int(full)*1024+17)
		for i := range content {
			content[i] = byte(i/1024 + 1)
		}
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		blocks.MaxBatchSize = int(limit)
		reader, err := NewBlockCapabilityStore(blocks).NewReaderWithSize(context.Background(), capability, int64(len(content)), BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pair, err := reader.(*Reader).leafPair(int64(index))
		if err != nil {
			t.Fatal(err)
		}
		delete(blocks.Blocks, pair.Reference)
		got := bytes.Repeat([]byte{0xcc}, len(content))
		n, err := reader.Read(got)
		if n != index*1024 || !errors.Is(err, ErrMissingBlock) || !bytes.Equal(got[:n], content[:n]) {
			t.Fatal(n, index, err)
		}
		if !bytes.Equal(got[n:], bytes.Repeat([]byte{0xcc}, len(got)-n)) {
			t.Fatal("copied content across a missing leaf")
		}
		_ = reader.Close()
	})
}
