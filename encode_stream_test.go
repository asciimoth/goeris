package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestEncodeContentMatchesCoreEncoder(t *testing.T) {
	t.Parallel()
	for _, size := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
		for _, length := range []int{0, 1, int(size), int(size) + 17, int(size)*17 + 3} {
			content := deterministicBytes(length)
			wantCap, wantBlocks, err := Encode(content, Secret{1, 2, 3}, size)
			if err != nil {
				t.Fatal(err)
			}
			blocks := make(BlockSet)
			gotCap, err := EncodeContent(context.Background(), bytes.NewReader(content), Secret{1, 2, 3}, size, blocks, BlockOptions{})
			if err != nil || gotCap != wantCap || !reflect.DeepEqual(blocks, wantBlocks) {
				t.Fatalf("size %d length %d mismatch: %v", size, length, err)
			}
		}
	}
}

func TestIncrementalEncoderMatchesMultiLevelCoreTree(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(int(BlockSize1KiB)*257 + 3)
	wantCap, wantBlocks, err := Encode(content, Secret{9}, BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	blocks := make(BlockSet)
	gotCap, err := EncodeContent(context.Background(), bytes.NewReader(content), Secret{9}, BlockSize1KiB, blocks, BlockOptions{})
	if err != nil || gotCap != wantCap || !reflect.DeepEqual(blocks, wantBlocks) {
		t.Fatalf("multi-level encoding mismatch: %v", err)
	}
}

func TestEncodeContentErrors(t *testing.T) {
	sentinel := errors.New("sentinel")
	if _, err := EncodeContent(context.Background(), nil, Secret{}, BlockSize1KiB, make(BlockSet), BlockOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, BlockSize1KiB, nil, BlockOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil sink error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, 1, make(BlockSet), BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid size error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), errorReader{sentinel}, Secret{}, BlockSize1KiB, make(BlockSet), BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("reader error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(nil), Secret{}, BlockSize1KiB, errorSink{sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("sink error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(make([]byte, BlockSize1KiB)), Secret{}, BlockSize1KiB, &countingSink{failAt: 2, err: sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("second leaf sink error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(make([]byte, BlockSize1KiB*2)), Secret{}, BlockSize1KiB, &countingSink{failAt: 4, err: sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("parent sink error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(make([]byte, BlockSize1KiB*16)), Secret{}, BlockSize1KiB, &countingSink{failAt: 17, err: sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("full parent sink error = %v", err)
	}
	if _, err := EncodeContent(context.Background(), bytes.NewReader(make([]byte, BlockSize1KiB*15)), Secret{}, BlockSize1KiB, &countingSink{failAt: 17, err: sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("final leaf parent sink error = %v", err)
	}
	if _, err := putNode(context.Background(), nil, Secret{}, 0, make(BlockSet), BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid node error = %v", err)
	}
	encoder := treeEncoder{blockSize: BlockSize1KiB, sink: make(BlockSet)}
	if err := encoder.add(256, Pair{}); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("excessive encoder level error = %v", err)
	}
	if _, err := encoder.parent(255); !errors.Is(err, ErrInvalidTree) {
		t.Fatalf("excessive parent level error = %v", err)
	}
	if _, err := NewReader(context.Background(), Capability{}, make(BlockSet), BlockOptions{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("invalid capability error = %v", err)
	}
}

func TestEncodeContentRecommended(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 16383, 16384, 16385, 32768, 65536, 17*32768 + 3} {
		content := deterministicBytes(size)
		secret := Secret{1, 2, 3}
		want, wantBlocks, err := EncodeRecommended(content, secret)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []io.Reader{
			bytes.NewReader(content),
			&terminalEOFReader{data: content},
			smallChunkReader{Reader: bytes.NewReader(content), max: 37},
		} {
			blocks := make(BlockSet)
			got, err := EncodeContentRecommended(context.Background(), input, secret, blocks, BlockOptions{})
			if err != nil || got != want || !reflect.DeepEqual(blocks, wantBlocks) {
				t.Fatalf("size %d, input %T: capability or blocks differ: %v", size, input, err)
			}
		}
	}
}

func TestEncodeContentRecommendedErrors(t *testing.T) {
	for _, input := range []io.Reader{nil, bytes.NewReader(nil)} {
		var sink BlockSink
		if input == nil {
			sink = make(BlockSet)
		}
		if _, err := EncodeContentRecommended(context.Background(), input, Secret{}, sink, BlockOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("nil input or sink: %v", err)
		}
	}
	sentinel := errors.New("storage failed")
	for _, size := range []int{0, 16384, 65536} {
		capability, err := EncodeContentRecommended(context.Background(), bytes.NewReader(make([]byte, size)), Secret{}, errorSink{sentinel}, BlockOptions{})
		if !errors.Is(err, sentinel) || capability != (Capability{}) {
			t.Fatalf("size %d: failed write returned %v, %v", size, capability, err)
		}
	}
}

func TestEncodeContentFinalParentError(t *testing.T) {
	sentinel := errors.New("storage failed")
	// Completing the last partial subtree can fill and flush its parent.
	if _, err := EncodeContent(context.Background(), bytes.NewReader(make([]byte, 240*1024)), Secret{}, BlockSize1KiB, &countingSink{failAt: 258, err: sentinel}, BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("final parent: %v", err)
	}
}

type countingSink struct {
	calls  int
	failAt int
	err    error
}

func (sink *countingSink) PutBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, _ ...Reference) (Reference, error) {
	sink.calls++
	if sink.calls == sink.failAt {
		return Reference{}, sink.err
	}
	return ReferenceOf(block)
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type errorSink struct{ err error }

func (sink errorSink) PutBlock(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error) {
	return Reference{}, sink.err
}
