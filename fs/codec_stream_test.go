package fs

import (
	"bytes"
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
		for _, blockSize := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
			want, _, err := eris.Encode(content, eris.Secret{}, blockSize)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodeContent(&terminalEOFReader{data: content}, eris.Secret{}, blockSize, make(eris.BlockSet))
			if err != nil || got != want {
				t.Errorf("size %d, block size %d: %v", size, blockSize, err)
			}
		}
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
					if err := add("file", input, false); err != nil {
						t.Fatal(err)
					}
					var err error
					capabilities[i], err = builder.Finish()
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

func TestEncodeContentReadTermination(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 1025, 16384, 32768} {
		content := autoRandomBytes(size)
		want, _, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		source := &finalErrorReader{data: content, err: io.EOF}
		got, err := EncodeContent(source, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet))
		if err != nil || got != want {
			t.Fatalf("EOF with %d bytes changed encoding: %v", size, err)
		}
		for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("read error"), errors.Join(io.EOF, io.ErrUnexpectedEOF), fmt.Errorf("source failed: %w", io.EOF)} {
			source = &finalErrorReader{data: content, err: cause}
			if _, err := EncodeContent(source, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet)); !errors.Is(err, cause) {
				t.Fatalf("%d bytes with %v: %v", size, cause, err)
			}
			builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
			source = &finalErrorReader{data: content, err: cause}
			if err := builder.AddFile("file", source, false); !errors.Is(err, cause) {
				t.Fatalf("builder: %v", err)
			}
			if len(builder.Index().Entries) != 0 {
				t.Fatal("read error added an entry")
			}
		}
	}
	if _, err := EncodeContent(noProgressReader{}, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet)); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("no progress: %v", err)
	}
	content := []byte("temporary empty reads")
	source := &intermittentReader{reader: bytes.NewReader(content), empty: 99}
	cap, err := EncodeContent(source, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet))
	want, _, encodeErr := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil || encodeErr != nil || cap != want {
		t.Fatalf("empty reads changed encoding: %v", err)
	}
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
				if err := add("file", input, false); !errors.Is(err, io.ErrUnexpectedEOF) {
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
				if err := add("file", input, false); !errors.Is(err, io.ErrNoProgress) {
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
	if _, err := writeLzip(&want, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	input := &intermittentReader{reader: bytes.NewReader(content), empty: 99}
	if _, err := writeLzip(&got, input); err != nil || !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("temporary empty reads changed compression: %v", err)
	}
}
