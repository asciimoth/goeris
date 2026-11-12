package fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"reflect"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
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

func TestCompressedFileRoundTripAndDeterminism(t *testing.T) {
	for _, content := range [][]byte{
		nil, []byte("short"), bytes.Repeat([]byte("compressible text\n"), 80000), deterministicBytes(100000),
	} {
		var firstCap eris.Capability
		var firstBlocks eris.BlockSet
		for run := 0; run < 2; run++ {
			blocks := make(eris.BlockSet)
			builder := NewBuilder(blocks, eris.Secret{1, 2, 3})
			if run == 1 {
				if err := builder.AddSymlink("link", "file"); err != nil {
					t.Fatal(err)
				}
			}
			var input io.Reader = bytes.NewReader(content)
			if run == 1 {
				input = smallChunkReader{Reader: input, max: 37}
			}
			if err := builder.AddCompressedFile("file", input, true); err != nil {
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
			raw, err := erisreader.NewReader(fileEntry.Capability, blocks)
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
			cap, err := builder.Finish()
			if err != nil {
				t.Fatal(err)
			}
			image, err := OpenFS(cap, blocks)
			if err != nil {
				t.Fatal(err)
			}
			got, err := stdfs.ReadFile(image, "file")
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("round trip %d bytes: %v", len(content), err)
			}
			info, err := image.Stat("file")
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
	if err := nilBuilder.AddCompressedFile("file", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
	if err := builder.AddCompressedFile("file", nil, false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	if err := builder.AddCompressedFile(".", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidPath) {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("file", "target"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddCompressedFile("file", bytes.NewReader(nil), false); !errors.Is(err, ErrPathConflict) {
		t.Fatal(err)
	}
	sentinel := errors.New("sentinel")
	builder = NewBuilder(make(eris.BlockSet), eris.Secret{})
	if err := builder.AddCompressedFile("file", errorReader{sentinel}, false); !errors.Is(err, sentinel) {
		t.Fatalf("source error: %v", err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("source error added entry")
	}
	builder = NewBuilder(errorSink{sentinel}, eris.Secret{})
	if err := builder.AddCompressedFile("file", bytes.NewReader(deterministicBytes(100000)), false); !errors.Is(err, sentinel) {
		t.Fatalf("sink error: %v", err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("sink error added entry")
	}
	if _, err := writeLzip(failWriter{sentinel}, bytes.NewReader(nil)); !errors.Is(err, sentinel) {
		t.Fatalf("output error: %v", err)
	}
	if _, err := writeLzip(shortWriter{}, bytes.NewReader(nil)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short output: %v", err)
	}
	for _, call := range []int{2, 3} {
		writer := &failOnCall{failAt: call, err: sentinel}
		if _, err := writeLzip(writer, bytes.NewReader([]byte("payload"))); !errors.Is(err, sentinel) {
			t.Fatalf("write %d: %v", call, err)
		}
	}
	// Fail while the compressor writes content, before it writes the end marker.
	writer := &failOnCall{failAt: 2, err: sentinel}
	input := &countReader{reader: bytes.NewReader(autoRandomBytes(9 << 20))}
	if _, err := writeLzip(writer, input); !errors.Is(err, sentinel) {
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
