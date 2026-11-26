package eris_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
)

// Capture options at the write boundary. The encoder must not rely on an
// option update or a second write to mark a root.
type rootSink struct {
	t *testing.T
	eris.BlockSet
	batches int
	refs    []eris.Reference
	options []eris.BlockOptions
}

func (s *rootSink) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.PutBlock(ctx, s.BlockSet, block, ttl, options, expected...)
	s.refs = append(s.refs, ref)
	s.options = append(s.options, options)
	return ref, err
}

func (s *rootSink) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	s.t.Fatal("encoder tried to update options after writing")
	return false, nil
}

func TestEncodedRootOptions(t *testing.T) {
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		arity, _ := size.Arity()
		lengths := []int{0, 1, int(size) - 1, int(size), int(size) + 1, (arity - 1) * int(size), arity*int(size) - 1, arity * int(size), (arity + 1) * int(size)}
		if size == eris.BlockSize1KiB {
			lengths = append(lengths, arity*arity*int(size)-1, arity*arity*int(size), (arity*arity+1)*int(size))
		}
		for _, length := range lengths {
			t.Run(fmt.Sprintf("%d/%d", size, length), func(t *testing.T) {
				content := make([]byte, length)
				_, _ = rand.New(rand.NewSource(1)).Read(content)
				want, blocks, err := eris.Encode(content, eris.Secret{}, size)
				if err != nil {
					t.Fatal(err)
				}
				for ref, block := range blocks.Blocks {
					if block.Options.Root != (ref == want.Root.Reference) {
						t.Fatal("incorrect in-memory root flag")
					}
				}
				for _, options := range []eris.BlockOptions{{}, {HighPriority: true}, {Root: true}} {
					sink := &rootSink{t: t, BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
					got, err := eris.EncodeContent(context.Background(), bytes.NewReader(content), eris.Secret{}, size, sink, options)
					if err != nil || got != want {
						t.Fatalf("encode: %v, %v", got, err)
					}
					if len(sink.refs) > 1 && sink.batches >= len(sink.refs) {
						t.Fatal("root boundary test did not use batches")
					}
					if len(sink.refs) != len(blocks.Blocks) {
						t.Fatal("encoder wrote a block more than once")
					}
					for i, ref := range sink.refs {
						wantOptions := options
						wantOptions.Root = options.Root || ref == got.Root.Reference
						if sink.options[i] != wantOptions {
							t.Fatalf("write %d: got %v, want %v", i, sink.options[i], wantOptions)
						}
					}
				}
			})
		}
	}
}

func TestCapabilityStoreEncodeRootOptions(t *testing.T) {
	smallBlock := int(eris.BlockSize1KiB)
	smallArity, _ := eris.BlockSize1KiB.Arity()
	for _, tc := range []struct {
		name     string
		length   int
		size     eris.BlockSize
		wantSize eris.BlockSize
	}{
		{"empty", 0, 0, eris.BlockSize1KiB},
		{"single block", smallBlock - 1, eris.BlockSize1KiB, eris.BlockSize1KiB},
		{"padding block", smallBlock, eris.BlockSize1KiB, eris.BlockSize1KiB},
		{"full parent root", smallArity*smallBlock - 1, eris.BlockSize1KiB, eris.BlockSize1KiB},
		{"below recommendation threshold", (16 << 10) - 1, 0, eris.BlockSize1KiB},
		{"at recommendation threshold", 16 << 10, 0, eris.BlockSize32KiB},
		{"recommended large blocks", 64 << 10, 0, eris.BlockSize32KiB},
		{"multiple levels", 64 << 10, eris.BlockSize1KiB, eris.BlockSize1KiB},
		{"full root at level two", smallArity*smallArity*smallBlock - 1, eris.BlockSize1KiB, eris.BlockSize1KiB},
		{"explicit large block below threshold", 1024, eris.BlockSize32KiB, eris.BlockSize32KiB},
		{"explicit large blocks", 64 << 10, eris.BlockSize32KiB, eris.BlockSize32KiB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := make([]byte, tc.length)
			_, _ = rand.New(rand.NewSource(1)).Read(content)
			secret := eris.Secret{1}
			want, wantBlocks, err := eris.Encode(content, secret, tc.wantSize)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []struct {
				name    string
				options eris.EncodeOptions
			}{
				{"automatic root", eris.EncodeOptions{}},
				{"automatic root with high priority", eris.EncodeOptions{BlockOptions: eris.BlockOptions{HighPriority: true}}},
				{"automatic root with Root true", eris.EncodeOptions{BlockOptions: eris.BlockOptions{Root: true}}},
				{"preserve false", eris.EncodeOptions{PreserveRoot: true, BlockOptions: eris.BlockOptions{HighPriority: true}}},
				{"preserve true", eris.EncodeOptions{PreserveRoot: true, BlockOptions: eris.BlockOptions{HighPriority: true, Root: true}}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block), MaxBatchSize: 4}
					sink := &rootSink{t: t, BlockSet: blocks}
					store := eris.NewBlockCapabilityStore(sink)
					options := mode.options
					options.BlockSize = tc.size
					input := iotest.DataErrReader(bytes.NewReader(content))
					got, err := store.EncodeContentWithOptions(t.Context(), input, secret, options)
					if err != nil || got != want {
						t.Fatalf("encode = %v, %v; want %v", got, err, want)
					}
					if len(sink.refs) != len(wantBlocks.Blocks) || len(sink.Blocks) != len(wantBlocks.Blocks) {
						t.Fatalf("wrote %d blocks with %d distinct references; want %d", len(sink.refs), len(sink.Blocks), len(wantBlocks.Blocks))
					}
					for i, ref := range sink.refs {
						wantOptions := options.BlockOptions
						if !options.PreserveRoot && ref == want.Root.Reference {
							wantOptions.Root = true
						}
						if sink.options[i] != wantOptions {
							t.Errorf("write %d: options = %+v; want %+v", i, sink.options[i], wantOptions)
						}
						if wantBlock, ok := wantBlocks.Blocks[ref]; !ok || !bytes.Equal(sink.Blocks[ref].Data, wantBlock.Data) {
							t.Errorf("write %d: unexpected encrypted block %v", i, ref)
						}
					}
				})
			}
		})
	}
}

func TestCapabilityStoreEncodePreservesStoredRoot(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	data := []byte("shared content")
	options := eris.EncodeOptions{PreserveRoot: true}
	first, err := store.EncodeContentWithOptions(t.Context(), bytes.NewReader(data), eris.Secret{}, options)
	if block, ok := blocks.Blocks[first.Root.Reference]; err != nil || !ok || block.Options.Root {
		t.Fatalf("first encode = %v, %v; want an unmarked root", first, err)
	}
	marked, err := store.EncodeRecommended(t.Context(), data, eris.Secret{}, eris.BlockOptions{})
	if err != nil || marked != first || !blocks.Blocks[marked.Root.Reference].Options.Root {
		t.Fatalf("standalone encode = %v, %v; want the same capability with a marked root", marked, err)
	}
	last, err := store.EncodeContentWithOptions(t.Context(), bytes.NewReader(data), eris.Secret{}, options)
	if err != nil || last != marked || !blocks.Blocks[last.Root.Reference].Options.Root {
		t.Fatalf("repeat encode = %v, %v; want the root to remain marked", last, err)
	}
}

func TestBlockSetRootOptions(t *testing.T) {
	ctx := context.Background()
	store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	want := eris.BlockOptions{}
	for _, options := range []eris.BlockOptions{{}, {Root: true}, {HighPriority: true}, {}} {
		if _, err := eris.PutBlock(ctx, store, block, 0, options); err != nil {
			t.Fatal(err)
		}
		want.Root = want.Root || options.Root
		want.HighPriority = want.HighPriority || options.HighPriority
		_, err, stored := eris.GetBlock(ctx, store, ref, 0, eris.BlockOptions{Root: true})
		if err != nil || stored != want {
			t.Fatalf("get: %v, %v; want %v", err, stored, want)
		}
	}
	for _, options := range []eris.BlockOptions{{HighPriority: true}, {Root: true}, {}, {}} {
		changed, err := eris.UpdateBlockOptions(ctx, store, ref, 0, options)
		if err != nil || changed != (options != want) || store.Blocks[ref].Options != options {
			t.Fatalf("update: %v, %v", changed, err)
		}
		want = options
	}
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *rootSink) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	s.batches++
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s *rootSink) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}
