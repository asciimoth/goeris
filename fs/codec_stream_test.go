package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/asciimoth/goeris"
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

func TestEncodingStopsAtEOFWithData(t *testing.T) {
	for _, size := range []int{0, 1023, 1024, 16383, 16384, 32768, 65536} {
		content := autoRandomBytes(size)
		for _, mode := range []string{"plain", "compressed", "auto", "auto-compressed"} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				content := content
				if mode == "auto-compressed" {
					content = make([]byte, size)
				}
				var capabilities [2]eris.Capability
				for i, input := range []io.Reader{bytes.NewReader(content), &terminalEOFReader{data: content}} {
					builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
					add := builder.AddFile
					switch mode {
					case "compressed":
						add = builder.AddCompressedFile
					case "auto", "auto-compressed":
						add = builder.AddAutoFile
					}
					if err := add(context.Background(), "file", input, false); err != nil {
						t.Fatal(err)
					}
					var err error
					capabilities[i], err = builder.Finish(context.Background())
					if err != nil {
						t.Fatal(err)
					}
				}
				if capabilities[0] != capabilities[1] {
					t.Fatal("EOF with data changed the image capability")
				}
			})
		}
	}
}

func (reader *intermittentReader) Read(p []byte) (int, error) {
	if reader.empty > 0 {
		reader.empty--
		return 0, nil
	}
	reader.empty = 99
	return reader.reader.Read(p[:min(len(p), 1)])
}

func TestBuilderPreservesEOFErrors(t *testing.T) {
	for _, mode := range []string{"plain", "compressed", "auto"} {
		for _, size := range []int{0, 1024, 16384, AutoCompressionSampleSize, AutoCompressionSampleSize + 1} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
				cause := errors.Join(io.EOF, io.ErrUnexpectedEOF)
				input := &finalErrorReader{data: make([]byte, size), err: cause}
				add := builder.AddFile
				switch mode {
				case "compressed":
					add = builder.AddCompressedFile
				case "auto":
					add = builder.AddAutoFile
				}
				if err := add(context.Background(), "file", input, false); !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("source failure became normal EOF: %v", err)
				}
				if len(builder.Index().Entries) != 0 {
					t.Fatal("failed source added an entry")
				}
			})
		}
	}
}

// Eventually returns an error so a regression fails instead of hanging tests.
type stalledReader struct{ calls int }

func (reader *stalledReader) Read([]byte) (int, error) {
	reader.calls++
	if reader.calls > 100 {
		return 0, errors.New("read continued after 100 empty reads")
	}
	return 0, nil
}

func TestBuilderRejectsStalledInput(t *testing.T) {
	for _, mode := range []string{"plain", "compressed", "auto"} {
		for _, size := range []int{0, AutoCompressionSampleSize} {
			t.Run(fmt.Sprintf("%s/%d", mode, size), func(t *testing.T) {
				builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
				input := io.MultiReader(bytes.NewReader(make([]byte, size)), &stalledReader{})
				add := builder.AddFile
				switch mode {
				case "compressed":
					add = builder.AddCompressedFile
				case "auto":
					add = builder.AddAutoFile
				}
				if err := add(context.Background(), "file", input, false); !errors.Is(err, io.ErrNoProgress) {
					t.Fatalf("stalled input: %v", err)
				}
				if len(builder.Index().Entries) != 0 {
					t.Fatal("stalled input added an entry")
				}
			})
		}
	}
}

func TestCompressionToleratesTemporaryEmptyReads(t *testing.T) {
	content := []byte("temporary empty reads")
	var want, got bytes.Buffer
	if _, err := writeLzip(context.Background(), &want, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	input := &intermittentReader{reader: bytes.NewReader(content), empty: 99}
	if _, err := writeLzip(context.Background(), &got, input); err != nil || !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("temporary empty reads changed compression: %v", err)
	}
}

func TestBuilderReadErrors(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 1025, 16384, 32768} {
		content := autoRandomBytes(size)
		for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("read error"), errors.Join(io.EOF, io.ErrUnexpectedEOF), fmt.Errorf("source failed: %w", io.EOF)} {
			builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
			source := &finalErrorReader{data: content, err: cause}
			if err := builder.AddFile(context.Background(), "file", source, false); !errors.Is(err, cause) {
				t.Fatalf("builder: %v", err)
			}
			if len(builder.Index().Entries) != 0 {
				t.Fatal("read error added an entry")
			}
		}
	}
}
