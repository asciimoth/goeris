package fs

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/asciimoth/goeris"
)

type batchStore struct {
	eris.BlockSet
	reads, writes []int
}

func (s *batchStore) PutBlock(ctx context.Context, r []eris.PutBlockRequest) ([]eris.Reference, error) {
	s.writes = append(s.writes, len(r))
	return s.BlockSet.PutBlock(ctx, r)
}
func (s *batchStore) GetBlock(ctx context.Context, r []eris.BlockRequest) ([]eris.Block, error) {
	s.reads = append(s.reads, len(r))
	return s.BlockSet.GetBlock(ctx, r)
}

func TestFileSystemBatches(t *testing.T) {
	for _, limit := range []int{0, 2, 7, 64, 100} {
		store := &batchStore{BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block), MaxBatchSize: limit}}
		ctx := context.Background()
		options := eris.BlockOptions{HighPriority: true}
		builder := NewBuilder(store, eris.Secret{}, options)
		data := make([]byte, 70*int(eris.BlockSize32KiB)+10)
		for i := range data {
			data[i] = byte(i / int(eris.BlockSize32KiB))
		}
		if err := builder.AddFile(ctx, "file", bytes.NewReader(data), false); err != nil {
			t.Fatal(err)
		}
		file := builder.Index().Entries[0]
		if store.Blocks[file.Capability.Root.Reference].Options.Root {
			t.Fatal("file root was marked as index root")
		}
		cap, err := builder.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !store.Blocks[cap.Root.Reference].Options.Root {
			t.Fatal("index root not marked")
		}
		image, err := OpenFS(ctx, cap, store, options)
		if err != nil {
			t.Fatal(err)
		}
		got, err := image.ReadFile(ctx, "file")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal(err)
		}
		if slices.Max(store.writes) < 2 || slices.Max(store.reads) < 2 {
			t.Fatal("filesystem did not batch storage", store.writes, store.reads)
		}
		for _, sizes := range [][]int{store.reads, store.writes} {
			for _, n := range sizes {
				if n > 64 || limit > 0 && n > limit {
					t.Fatal("batch limit", n)
				}
			}
		}
		for _, block := range store.Blocks {
			if !block.Options.HighPriority {
				t.Fatal("priority lost")
			}
		}
	}
}

func TestFileSinkBatchOptions(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block), MaxBatchSize: 2}
	sink := fileBlockSink{BlockSink: blocks, root: true}
	requests := []eris.PutBlockRequest{{Data: make([]byte, 1024)}, {Data: bytes.Repeat([]byte{1}, 1024), Options: eris.BlockOptions{HighPriority: true}}}
	refs, err := sink.PutBlock(ctx, requests)
	if err != nil {
		t.Fatal(err)
	}
	if requests[0].Options.Root || requests[1].Options.Root {
		t.Fatal("caller options changed")
	}
	if blocks.Blocks[refs[0]].Options != (eris.BlockOptions{Root: true}) || blocks.Blocks[refs[1]].Options != (eris.BlockOptions{Root: true, HighPriority: true}) {
		t.Fatal("per-block options lost")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := sink.PutBlock(ctx, nil); len(got) != 0 || err != nil {
		t.Fatal(got, err)
	}
	if got, err := sink.PutBlock(ctx, requests); len(got) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if got, err := sink.PutBlock(ctx, append(requests, requests[0])); len(got) != 0 || !errors.Is(err, eris.ErrBatchTooLarge) {
		t.Fatal(got, err)
	}
}
