package fs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/asciimoth/goeris"
	"github.com/ulikunitz/xz/lzma"
)

type smallChunkReader struct {
	io.Reader
	max int
}

func (reader smallChunkReader) Read(p []byte) (int, error) {
	return reader.Reader.Read(p[:min(len(p), reader.max)])
}

type failWriter struct{ err error }

func (writer failWriter) Write([]byte) (int, error) { return 0, writer.err }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type failOnCall struct {
	calls, failAt int
	err           error
}

func (writer *failOnCall) Write(p []byte) (int, error) {
	writer.calls++
	if writer.calls == writer.failAt {
		return 0, writer.err
	}
	return len(p), nil
}

type compressionTestStore struct {
	eris.CapabilityStore
	encode func(io.Reader) error
}

func (s compressionTestStore) EncodeContentWithOptions(_ context.Context, input io.Reader, _ eris.Secret, _ eris.EncodeOptions) (eris.Capability, error) {
	return eris.Capability{}, s.encode(input)
}

type compressionTestInput func([]byte) (int, error)

func (input compressionTestInput) Read(p []byte) (int, error) { return input(p) }

func TestCompressedFileStopsReadingAfterStoreFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sentinel := errors.New("storage failure")
		started, resume := make(chan struct{}), make(chan struct{})
		reads := 0
		source := compressionTestInput(func(p []byte) (int, error) {
			reads++
			if reads > 1 {
				return 0, io.EOF
			}
			close(started)
			<-resume
			p[0] = 'a'
			return 1, nil
		})
		store := compressionTestStore{encode: func(input io.Reader) error {
			var header [6]byte
			if _, err := io.ReadFull(input, header[:]); err != nil {
				return err
			}
			<-started
			return sentinel
		}}
		builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
		done := make(chan error, 1)
		go func() { done <- builder.AddCompressedFile(t.Context(), "file", source, false) }()
		synctest.Wait() // The store has returned while the first input read is blocked.
		close(resume)
		if err := <-done; !errors.Is(err, sentinel) {
			t.Fatalf("AddCompressedFile: %v", err)
		}
		if reads != 1 || len(builder.Index().Entries) != 0 {
			t.Fatalf("after storage failure: %d input reads, %d entries", reads, len(builder.Index().Entries))
		}
	})
}

func TestCompressedFileClosesPipeWhenStorePanics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sentinel := errors.New("storage panic")
		var pipe io.Reader
		store := compressionTestStore{encode: func(input io.Reader) error {
			pipe = input
			panic(sentinel)
		}}
		builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
		func() {
			defer func() {
				if got := recover(); got != sentinel {
					t.Errorf("recovered %v, want %v", got, sentinel)
				}
			}()
			_ = builder.AddCompressedFile(t.Context(), "file", errorReader{sentinel}, false)
		}()
		if pipe == nil {
			t.Fatal("store did not receive its input")
		}
		if n, err := pipe.Read(make([]byte, 6)); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			_, _ = io.Copy(io.Discard, pipe) // Let the worker exit if the pipe is still open.
			t.Errorf("pipe read after panic = %d, %v", n, err)
		}
	})
}

func TestCompressedFileRoundTripAndDeterminism(t *testing.T) {
	for _, content := range [][]byte{
		nil, []byte("short"), bytes.Repeat([]byte("compressible text\n"), 80000), deterministicBytes(100000),
	} {
		var firstCap eris.Capability
		var firstBlocks eris.BlockSet
		for run := 0; run < 2; run++ {
			blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
			store := eris.NewBlockCapabilityStore(blocks)
			builder := NewBuilder(store, eris.Secret{1, 2, 3}, eris.BlockOptions{})
			if run == 1 {
				if err := builder.AddSymlink("link", "file"); err != nil {
					t.Fatal(err)
				}
			}
			var input io.Reader = bytes.NewReader(content)
			if run == 1 {
				input = smallChunkReader{Reader: input, max: 37}
			}
			if err := builder.AddCompressedFile(context.Background(), "file", input, true); err != nil {
				t.Fatalf("compress %d bytes: %v", len(content), err)
			}
			if run == 0 {
				if err := builder.AddSymlink("link", "file"); err != nil {
					t.Fatal(err)
				}
			}
			index := builder.Index()
			if len(index.Entries) != 2 {
				t.Fatalf("entry count = %d", len(index.Entries))
			}
			fileEntry := index.Entries[0]
			if fileEntry.Type == Symlink {
				fileEntry = index.Entries[1]
			}
			if fileEntry.Compression != Lzip || fileEntry.Size != int64(len(content)) {
				t.Fatalf("compressed metadata = %#v", index)
			}
			raw, err := store.NewReader(context.Background(), fileEntry.Capability, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			compressed, err := io.ReadAll(raw)
			_ = raw.Close()
			if err != nil || !bytes.HasPrefix(compressed, []byte("LZIP\x01\x17")) {
				t.Fatalf("Lzip header: %x, %v", compressed[:min(len(compressed), 6)], err)
			}
			if got := binary.LittleEndian.Uint64(compressed[len(compressed)-8:]); got != uint64(len(compressed)) {
				t.Fatalf("Lzip member size %d, want %d", got, len(compressed))
			}
			cap, err := builder.Finish(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			image, err := OpenFS(context.Background(), cap, store, eris.BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := stdfs.ReadFile(NewIOFS(context.Background(), image), "file")
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("round trip %d bytes: %v", len(content), err)
			}
			info, err := image.Stat(context.Background(), "file")
			if err != nil || info.Size() != int64(len(content)) || info.Mode() != 0o555 {
				t.Fatalf("stat: %v, %v", info, err)
			}
			if run == 0 {
				firstCap, firstBlocks = cap, blocks
			} else if cap != firstCap || !reflect.DeepEqual(blocks, firstBlocks) {
				t.Fatalf("same content, secret, and metadata produced different ERIS image at %d bytes", len(content))
			}
		}
	}
}

func TestCompressedFileErrors(t *testing.T) {
	var nilBuilder *Builder
	if err := nilBuilder.AddCompressedFile(context.Background(), "file", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddCompressedFile(context.Background(), "file", nil, false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	if err := builder.AddCompressedFile(context.Background(), ".", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidPath) {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("file", "target"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddCompressedFile(context.Background(), "file", bytes.NewReader(nil), false); !errors.Is(err, ErrPathConflict) {
		t.Fatal(err)
	}
	sentinel := errors.New("sentinel")
	builder = NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddCompressedFile(context.Background(), "file", errorReader{sentinel}, false); !errors.Is(err, sentinel) {
		t.Fatalf("source error: %v", err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("source error added entry")
	}
	builder = NewBuilder(eris.NewBlockCapabilityStore(eris.NewCombinedBlockStore(errorSink{sentinel})), eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddCompressedFile(context.Background(), "file", bytes.NewReader(deterministicBytes(100000)), false); !errors.Is(err, sentinel) {
		t.Fatalf("sink error: %v", err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("sink error added entry")
	}
	if _, err := writeLzip(context.Background(), failWriter{sentinel}, bytes.NewReader(nil)); !errors.Is(err, sentinel) {
		t.Fatalf("output error: %v", err)
	}
	if _, err := writeLzip(context.Background(), shortWriter{}, bytes.NewReader(nil)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short output: %v", err)
	}
	for _, call := range []int{2, 3} {
		writer := &failOnCall{failAt: call, err: sentinel}
		if _, err := writeLzip(context.Background(), writer, bytes.NewReader([]byte("payload"))); !errors.Is(err, sentinel) {
			t.Fatalf("write %d: %v", call, err)
		}
	}
	// Fail while the compressor writes content, before it writes the end marker.
	writer := &failOnCall{failAt: 2, err: sentinel}
	input := &countReader{reader: bytes.NewReader(autoRandomBytes(9 << 20))}
	if _, err := writeLzip(context.Background(), writer, input); !errors.Is(err, sentinel) {
		t.Fatalf("compressed body write: %v", err)
	}
	if input.size >= 9<<20 {
		t.Fatal("compressor consumed the whole source after an output failure")
	}
	var body bytes.Buffer
	stripper := &lzipBodyWriter{output: &body, discard: 13}
	if n, err := stripper.Write(make([]byte, 13)); n != 13 || err != nil || body.Len() != 0 {
		t.Fatalf("header removal: %d bytes, %v", n, err)
	}
	if n, err := stripper.Write([]byte("body")); n != 4 || err != nil || body.String() != "body" {
		t.Fatalf("body write: %d bytes, %v", n, err)
	}
}

func TestLzipMemberBounds(t *testing.T) {
	for _, tc := range []struct {
		body lzipBodyWriter
		dict int
		want error
	}{
		{lzipBodyWriter{output: io.Discard}, -1, nil},
		{lzipBodyWriter{output: io.Discard, discard: 1024}, 4096, ErrInvalidCompression},
		{lzipBodyWriter{output: io.Discard, written: math.MaxInt64 - 25, discard: lzma.HeaderLen}, 4096, ErrInvalidCompression},
	} {
		_, err := tc.body.encode(t.Context(), bytes.NewReader(nil), lzma.WriterConfig{DictCap: tc.dict})
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Fatal(err)
		}
	}
}

func TestLzipBodySizeLimit(t *testing.T) {
	var output bytes.Buffer
	body := lzipBodyWriter{output: &output, written: math.MaxInt64 - 27}
	if n, err := body.Write([]byte("x")); err != nil || n != 1 {
		t.Fatalf("last valid byte: %d, %v", n, err)
	}
	// This chunk would also wrap the signed byte count.
	if n, err := body.Write(make([]byte, 32)); !errors.Is(err, ErrInvalidCompression) || n != 0 {
		t.Fatalf("oversized write: %d, %v", n, err)
	}
	if output.String() != "x" || body.written != math.MaxInt64-26 {
		t.Fatal("oversized write changed the output or byte count")
	}
}
