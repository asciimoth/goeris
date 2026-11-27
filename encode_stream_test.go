package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/asciimoth/goeris/internal/batch"
)

func TestEncodeContentShortReadsMatchByteEncoding(t *testing.T) {
	t.Parallel()
	for _, size := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
		for _, length := range []int{0, 1, int(size), int(size) + 17, int(size)*17 + 3} {
			content := deterministicBytes(length)
			wantBlocks := BlockSet{Blocks: make(map[Reference]Block)}
			wantCap, err := NewBlockCapabilityStore(wantBlocks).Encode(t.Context(), content, Secret{1, 2, 3}, size, BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			blocks := BlockSet{Blocks: make(map[Reference]Block)}
			input := smallChunkReader{Reader: bytes.NewReader(content), max: 37}
			gotCap, err := NewBlockCapabilityStore(blocks).EncodeContent(t.Context(), input, Secret{1, 2, 3}, size, BlockOptions{})
			if err != nil || gotCap != wantCap || !reflect.DeepEqual(blocks, wantBlocks) {
				t.Fatalf("size %d length %d mismatch: %v", size, length, err)
			}
		}
	}
}

func TestMultiLevelEncodingCompatibility(t *testing.T) {
	t.Parallel()
	// The original in-memory encoder produced this three-level tree.
	const want = "urn:eris:BIBQLXRNQHXV2QYI2AXXFPT7NOCNN3ZB5BL4HTWTK5WIGTQHGJTQ7DATXIPUKHSW6T2EVHQMLLO6N7EGURQD44QYLV3KZ4EKZNOMW3TVFA"
	content := deterministicBytes(int(BlockSize1KiB)*257 + 3)
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	store := NewBlockCapabilityStore(blocks)
	got, err := store.EncodeContent(t.Context(), bytes.NewReader(content), Secret{9}, BlockSize1KiB, BlockOptions{})
	if err != nil || got.String() != want || len(blocks.Blocks) != 19 {
		t.Fatalf("encoded %v in %d distinct blocks: %v", got, len(blocks.Blocks), err)
	}
	decoded, err := store.Decode(t.Context(), got, BlockOptions{})
	if err != nil || !bytes.Equal(decoded, content) {
		t.Fatalf("multi-level decode returned %d bytes: %v", len(decoded), err)
	}
}

func TestEncodeContentErrors(t *testing.T) {
	sentinel := errors.New("sentinel")
	if _, err := newTestStore().EncodeContent(context.Background(), nil, Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := NewBlockCapabilityStore(nil).EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("nil sink error = %v", err)
	}
	if _, err := newTestStore().EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, 1, BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid size error = %v", err)
	}
	if _, err := newTestStore().EncodeContent(context.Background(), errorReader{sentinel}, Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("reader error = %v", err)
	}
	if _, err := NewBlockCapabilityStore(NewCombinedBlockStore(errorSink{sentinel})).EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("sink error = %v", err)
	}
	for _, tc := range []struct {
		name           string
		blocks, failAt int
	}{
		{"second leaf", 1, 2},
		{"parent", 2, 4},
		{"full parent", 16, 17},
		{"final leaf parent", 15, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &countingSink{failAt: tc.failAt, err: sentinel}
			store := NewBlockCapabilityStore(NewCombinedBlockStore(sink))
			input := bytes.NewReader(make([]byte, tc.blocks*int(BlockSize1KiB)))
			if _, err := store.EncodeContent(t.Context(), input, Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, sentinel) {
				t.Fatalf("write %d: %v", tc.failAt, err)
			}
		})
	}
	if _, err := (&treeEncoder{ctx: context.Background()}).putNode(nil, 0, BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid node error = %v", err)
	}
	encoder := treeEncoder{blockSize: BlockSize1KiB, sink: BlockSet{Blocks: make(map[Reference]Block)}}
	if err := encoder.add(256, Pair{}); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("excessive encoder level error = %v", err)
	}
	if _, err := encoder.parent(255); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("excessive parent level error = %v", err)
	}
	if _, err := newTestStore().NewReader(context.Background(), Capability{}, BlockOptions{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("invalid capability error = %v", err)
	}
}

func TestEncodeContentRecommended(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 16383, 16384, 16385, 32768, 65536, 17*32768 + 3} {
		content := deterministicBytes(size)
		secret := Secret{1, 2, 3}
		wantBlocks := BlockSet{Blocks: make(map[Reference]Block)}
		want, err := NewBlockCapabilityStore(wantBlocks).EncodeRecommended(t.Context(), content, secret, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []io.Reader{
			bytes.NewReader(content),
			&terminalEOFReader{data: content},
			smallChunkReader{Reader: bytes.NewReader(content), max: 37},
		} {
			blocks := BlockSet{Blocks: make(map[Reference]Block)}
			got, err := NewBlockCapabilityStore(blocks).EncodeContentRecommended(context.Background(), input, secret, BlockOptions{})
			if err != nil || got != want || !reflect.DeepEqual(blocks, wantBlocks) {
				t.Fatalf("size %d, input %T: capability or blocks differ: %v", size, input, err)
			}
		}
	}
}

func TestEncodeContentRecommendedErrors(t *testing.T) {
	store := newTestStore()
	if _, err := store.EncodeContentRecommended(t.Context(), nil, Secret{}, BlockOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil input: %v", err)
	}
	sentinel := errors.New("storage failed")
	for _, size := range []int{0, 16384, 65536} {
		capability, err := NewBlockCapabilityStore(NewCombinedBlockStore(errorSink{sentinel})).EncodeContentRecommended(context.Background(), bytes.NewReader(make([]byte, size)), Secret{}, BlockOptions{})
		if !errors.Is(err, sentinel) || capability != (Capability{}) {
			t.Fatalf("size %d: failed write returned %v, %v", size, capability, err)
		}
	}
}

func TestEncodeContentFinalParentError(t *testing.T) {
	sentinel := errors.New("storage failed")
	// Completing the last partial subtree can fill and flush its parent.
	sink := &countingSink{failAt: 258, err: sentinel}
	store := NewBlockCapabilityStore(NewCombinedBlockStore(sink))
	input := bytes.NewReader(make([]byte, 240*1024))
	if _, err := store.EncodeContent(t.Context(), input, Secret{}, BlockSize1KiB, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("final parent: %v", err)
	}
}

type countingSink struct {
	calls  int
	failAt int
	err    error
}

func (sink *countingSink) putBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, _ ...Reference) (Reference, error) {
	sink.calls++
	if sink.calls == sink.failAt {
		return Reference{}, sink.err
	}
	return ReferenceOf(block)
}

func (sink *countingSink) updateBlockOptions(context.Context, Reference, TTL, BlockOptions) (bool, error) {
	return false, nil
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type errorSink struct{ err error }

func (sink errorSink) putBlock(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error) {
	return Reference{}, sink.err
}

func (sink errorSink) updateBlockOptions(context.Context, Reference, TTL, BlockOptions) (bool, error) {
	return false, nil
}

// BatchSize reports the maximum number of requests per call.
func (s *countingSink) BatchSize() int { return 1 }

// PutBlock processes requests in order and returns the completed prefix.
func (s *countingSink) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s *countingSink) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s errorSink) BatchSize() int { return 1 }

// PutBlock processes requests in order and returns the completed prefix.
func (s errorSink) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s errorSink) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}
