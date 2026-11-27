package fs

import (
	"bytes"
	"context"
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
		// A write-only block backend must keep its batch size.
		sink := struct{ eris.BlockSink }{store}
		builder := NewBuilder(eris.NewBlockCapabilityStore(eris.NewCombinedBlockStore(sink)), eris.Secret{}, options)
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
		image, err := OpenFS(ctx, cap, eris.NewBlockCapabilityStore(store), options)
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
