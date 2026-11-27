package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestReadFileDoesNotAllocateDeclaredSize(t *testing.T) {
	// The standard fs.ReadFile fallback allocates Size()+1 bytes before Read.
	// A maximum native int makes that allocation panic on both 32 and 64 bits.
	size := int64(int(^uint(0) >> 1))
	image := compressedImage(t, lzipMember(t, []byte("small")), size)
	got, err := stdfs.ReadFile(NewIOFS(context.Background(), image), "file")
	if string(got) != "small" || !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("forged size: got %q, %v", got, err)
	}
}

func TestReadFileSizeHintPreservesResult(t *testing.T) {
	const size = 1024
	ctx := t.Context()
	capability := pathTestCapability(t)
	index := Index{Entries: []Entry{
		{Path: []string{"file"}, Capability: capability, Size: size},
		{Path: []string{"link"}, Type: Symlink, Target: "file"},
	}}
	data, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("storage read failed")
	for _, tc := range []struct {
		name    string
		length  int
		readErr error
		delay   bool
	}{
		{name: "EOF with final data", length: size, readErr: io.EOF},
		{name: "error with partial data", length: size / 2, readErr: failed},
		{name: "error after all data", length: size, readErr: failed, delay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Repeat("x", tc.length)
			reader := &readFileTestReader{
				imageResultReader: imageResultReader{Reader: strings.NewReader(content), size: size},
				readErr:           tc.readErr,
				delay:             tc.delay,
			}
			backend := &imageResultStore{index: data, reader: reader}
			image, err := OpenFS(ctx, capability, backend, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := image.ReadFile(ctx, "link")
			if string(got) != content || !reader.closed {
				t.Fatalf("ReadFile returned %d bytes and closed = %v, want %d bytes and closed", len(got), reader.closed, len(content))
			}
			if tc.readErr == io.EOF {
				if err != nil {
					t.Fatalf("ReadFile = %v, want success", err)
				}
				return
			}
			var pathErr *stdfs.PathError
			if !errors.Is(err, failed) || !errors.As(err, &pathErr) || pathErr.Op != "read" || pathErr.Path != "link" {
				t.Fatalf("ReadFile = %v, want storage error for read link", err)
			}
		})
	}
}

type readFileTestReader struct {
	imageResultReader
	readErr error
	delay   bool
}

func (r *readFileTestReader) Read(p []byte) (int, error) {
	n, err := r.imageResultReader.Read(p)
	if r.Len() != 0 || (r.delay && n > 0) {
		return n, err
	}
	return n, r.readErr
}

func TestReadFileSizeHintChecksLzipTrailer(t *testing.T) {
	content := bytes.Repeat([]byte("content"), 150)
	stream := lzipMember(t, content)
	stream[len(stream)-20] ^= 1 // Corrupt the trailer checksum.
	image := compressedImage(t, stream, int64(len(content)))
	got, err := image.ReadFile(t.Context(), "file")
	if !bytes.Equal(got, content) || !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("ReadFile = %d bytes, %v, want %d bytes and invalid compression", len(got), err, len(content))
	}
}

func BenchmarkImageReadFile(b *testing.B) {
	for _, size := range []int{64, 1023, 8192, 32767, 131072} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			data := bytes.Repeat([]byte{'a'}, size)
			store := newTestStore()
			builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
			if err := builder.AddFile(b.Context(), "file", bytes.NewReader(data), false); err != nil {
				b.Fatal(err)
			}
			cap, err := builder.Finish(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			image, err := OpenFS(b.Context(), cap, store, eris.BlockOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(size))
			for b.Loop() {
				if got, err := image.ReadFile(b.Context(), "file"); err != nil || len(got) != size {
					b.Fatalf("ReadFile = %d bytes, %v", len(got), err)
				}
			}
		})
	}
}
