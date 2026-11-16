package eris

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

// intermittentReader models legal empty reads followed by byte-sized progress.
type intermittentReader struct {
	reader io.Reader
	empty  int
}

// A read after EOF can block on a live source. Fail here to detect that read
// without blocking the test.
type terminalEOFReader struct {
	data []byte
	done bool
}

func (reader *terminalEOFReader) Read(p []byte) (int, error) {
	if reader.done {
		return 0, errors.New("read after EOF")
	}
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	if len(reader.data) == 0 {
		reader.done = true
		return n, io.EOF
	}
	return n, nil
}

func (reader *intermittentReader) Read(p []byte) (int, error) {
	if reader.empty > 0 {
		reader.empty--
		return 0, nil
	}
	reader.empty = 99
	return reader.reader.Read(p[:min(len(p), 1)])
}

// finalErrorReader reports the error together with the last available bytes.
// Unlike io.MultiReader, it has no later read on which to report that error.
type finalErrorReader struct {
	data []byte
	err  error
}

func (reader *finalErrorReader) Read(p []byte) (int, error) {
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	if len(reader.data) == 0 {
		return n, reader.err
	}
	return n, nil
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

func TestEncodeContentStopsAtEOFWithData(t *testing.T) {
	for _, size := range []int{0, 1023, 1024, 16383, 16384, 32768, 65536} {
		content := deterministicBytes(size)
		for _, blockSize := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
			want, _, err := Encode(content, Secret{}, blockSize)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodeContent(context.Background(), &terminalEOFReader{data: content}, Secret{}, blockSize, make(BlockSet))
			if err != nil || got != want {
				t.Errorf("size %d, block size %d: %v", size, blockSize, err)
			}
		}
	}
}

func TestEncodeContentReadTermination(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 1025, 16384, 32768} {
		content := deterministicBytes(size)
		want, _, err := Encode(content, Secret{}, BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		source := &finalErrorReader{data: content, err: io.EOF}
		got, err := EncodeContent(context.Background(), source, Secret{}, BlockSize1KiB, make(BlockSet))
		if err != nil || got != want {
			t.Fatalf("EOF with %d bytes changed encoding: %v", size, err)
		}
		for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("read error"), errors.Join(io.EOF, io.ErrUnexpectedEOF), fmt.Errorf("source failed: %w", io.EOF)} {
			source = &finalErrorReader{data: content, err: cause}
			if _, err := EncodeContent(context.Background(), source, Secret{}, BlockSize1KiB, make(BlockSet)); !errors.Is(err, cause) {
				t.Fatalf("%d bytes with %v: %v", size, cause, err)
			}
			source = &finalErrorReader{data: content, err: cause}
			capability, err := EncodeContentRecommended(context.Background(), source, Secret{}, make(BlockSet))
			if !errors.Is(err, cause) || capability != (Capability{}) {
				t.Fatalf("recommended, %d bytes with %v: %v", size, cause, err)
			}
		}
	}
	if _, err := EncodeContent(context.Background(), noProgressReader{}, Secret{}, BlockSize1KiB, make(BlockSet)); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("no progress: %v", err)
	}
	for _, size := range []int{0, 1 << 14} {
		input := io.MultiReader(bytes.NewReader(make([]byte, size)), noProgressReader{})
		if _, err := EncodeContentRecommended(context.Background(), input, Secret{}, make(BlockSet)); !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("recommended, no progress after %d bytes: %v", size, err)
		}
	}
	content := []byte("temporary empty reads")
	source := &intermittentReader{reader: bytes.NewReader(content), empty: 99}
	cap, err := EncodeContent(context.Background(), source, Secret{}, BlockSize1KiB, make(BlockSet))
	want, _, encodeErr := Encode(content, Secret{}, BlockSize1KiB)
	if err != nil || encodeErr != nil || cap != want {
		t.Fatalf("empty reads changed encoding: %v", err)
	}
	source = &intermittentReader{reader: bytes.NewReader(content), empty: 99}
	cap, err = EncodeContentRecommended(context.Background(), source, Secret{}, make(BlockSet))
	if err != nil || cap != want {
		t.Fatalf("empty reads changed recommended encoding: %v", err)
	}
}
