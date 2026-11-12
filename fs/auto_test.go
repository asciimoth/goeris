package fs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"reflect"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
)

func autoRandomBytes(n int) []byte {
	data := make([]byte, n)
	x := uint32(1)
	for i := range data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x)
	}
	return data
}

func TestAutoCompressionVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/auto-compression-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name         string `json:"name"`
		Length       int    `json:"length"`
		Recipe       string `json:"recipe"`
		RandomPrefix int    `json:"random_prefix"`
		SHA256       string `json:"sha256"`
		ProbeSize    int    `json:"probe_size"`
		Score        int    `json:"score"`
		Compression  string `json:"compression"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if AutoCompressionVersion != 1 || AutoCompressionSampleSize != 32768 {
		t.Fatal("policy version 1 constants changed")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			sample := make([]byte, v.Length)
			switch v.Recipe {
			case "zeros":
			case "cycle":
				for i := range sample {
					sample[i] = byte(i)
				}
			case "xorshift32":
				sample = autoRandomBytes(v.Length)
			case "random-prefix":
				copy(sample, autoRandomBytes(v.RandomPrefix))
			default:
				t.Fatalf("unknown recipe %q", v.Recipe)
			}
			digest := sha256.Sum256(sample)
			if hex.EncodeToString(digest[:]) != v.SHA256 {
				t.Fatal("input recipe hash mismatch")
			}
			if got := autoProbeSize(sample); got != v.ProbeSize || got+26 != v.Score {
				t.Fatalf("probe size %d, want %d", got, v.ProbeSize)
			}
			want := Uncompressed
			if v.Compression == "lzip" {
				want = Lzip
			} else if v.Compression != "uncompressed" {
				t.Fatal("unknown compression")
			}
			if got := ChooseCompression(sample); got != want {
				t.Fatalf("mode %d, want %d", got, want)
			}
			if len(sample) == AutoCompressionSampleSize {
				extended := append(bytes.Clone(sample), autoRandomBytes(10000)...)
				if got := ChooseCompression(extended); got != want {
					t.Fatal("tail changed decision")
				}
			}
		})
	}
}

// referenceAutoProbe emits tokens, using a backward search instead of the
// production hash table. A colliding slot must stop the search even when an
// older position would be an exact match. This is an independent, slower oracle.
func referenceAutoProbe(sample []byte) []byte {
	slot := func(i int) uint64 {
		key := uint64(sample[i]) | uint64(sample[i+1])<<8 | uint64(sample[i+2])<<16 | uint64(sample[i+3])<<24
		return ((key * 0x9e3779b1) & 0xffffffff) >> 18
	}
	var out, literals []byte
	flush := func() {
		if len(literals) != 0 {
			out = append(out, byte(len(literals)-1))
			out = append(out, literals...)
			literals = literals[:0]
		}
	}
	for i := 0; i < len(sample); {
		length, previous := 0, -1
		if i+4 <= len(sample) {
			for q := i - 1; q >= 0; q-- {
				if slot(q) != slot(i) {
					continue
				}
				if bytes.Equal(sample[q:q+4], sample[i:i+4]) {
					previous = q
					length = 4
					for length < 131 && i+length < len(sample) && sample[q+length] == sample[i+length] {
						length++
					}
				}
				break
			}
		}
		if length != 0 {
			flush()
			out = append(out, 128+byte(length-4))
			out = binary.LittleEndian.AppendUint16(out, uint16(i-previous-1))
			i += length
		} else {
			literals = append(literals, sample[i])
			i++
			if len(literals) == 128 {
				flush()
			}
		}
	}
	flush()
	return out
}

func decodeAutoProbe(t testing.TB, encoded []byte) []byte {
	t.Helper()
	var out []byte
	for len(encoded) > 0 {
		head := encoded[0]
		encoded = encoded[1:]
		if head < 128 {
			length := int(head) + 1
			if length > len(encoded) {
				t.Fatal("truncated literal")
			}
			out = append(out, encoded[:length]...)
			encoded = encoded[length:]
		} else {
			if len(encoded) < 2 {
				t.Fatal("truncated match")
			}
			distance := int(binary.LittleEndian.Uint16(encoded)) + 1
			encoded = encoded[2:]
			if distance > len(out) {
				t.Fatal("invalid distance")
			}
			for range int(head) - 128 + 4 {
				out = append(out, out[len(out)-distance])
			}
		}
	}
	return out
}

func TestAutoProbeReference(t *testing.T) {
	for _, sample := range [][]byte{
		nil, []byte("abc"), []byte("abcdabcd"), bytes.Repeat([]byte("a"), 1024),
		autoRandomBytes(2048), append(autoRandomBytes(856), make([]byte, 168)...),
		bytes.Repeat(autoRandomBytes(137), 12),
	} {
		encoded := referenceAutoProbe(sample)
		if got := autoProbeSize(sample); got != len(encoded) {
			t.Fatalf("size %d, reference %d", got, len(encoded))
		}
		if decoded := decodeAutoProbe(t, encoded); !bytes.Equal(decoded, sample) {
			t.Fatal("reference token round trip failed")
		}
	}
}

func buildAutoImage(t testing.TB, input io.Reader, executable, reverse bool) (eris.Capability, eris.BlockSet, Entry) {
	t.Helper()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{1, 2, 3})
	if reverse {
		if err := builder.AddSymlink("link", "file"); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.AddAutoFile("file", input, executable); err != nil {
		t.Fatal(err)
	}
	entries := builder.Index().Entries
	entry := entries[len(entries)-1]
	if !reverse {
		if err := builder.AddSymlink("link", "file"); err != nil {
			t.Fatal(err)
		}
	}
	root, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return root, blocks, entry
}

func TestAutoFileRoundTripAndDeterminism(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		want    Compression
	}{
		{"empty", nil, Uncompressed}, {"small", make([]byte, 1023), Uncompressed},
		{"minimum", make([]byte, 1024), Lzip}, {"random-small", autoRandomBytes(1024), Uncompressed},
		{"short-sample", make([]byte, 32767), Lzip}, {"full-sample", make([]byte, 32768), Lzip},
		{"full-random", autoRandomBytes(32768), Uncompressed},
		{"compressed-tail", append(make([]byte, 32768), autoRandomBytes(65537)...), Lzip},
		{"raw-tail", append(autoRandomBytes(32768), make([]byte, 65537)...), Uncompressed},
	} {
		t.Run(test.name, func(t *testing.T) {
			cap, blocks, entry := buildAutoImage(t, bytes.NewReader(test.content), true, false)
			if entry.Compression != test.want || entry.Size != int64(len(test.content)) || entry.Type != Executable {
				t.Fatalf("metadata: %#v", entry)
			}
			counted := &countReader{reader: smallChunkReader{Reader: iotest.DataErrReader(bytes.NewReader(test.content)), max: 37}}
			cap2, blocks2, entry2 := buildAutoImage(t, counted, true, true)
			if counted.size != int64(len(test.content)) {
				t.Fatalf("read %d bytes from original source", counted.size)
			}
			if cap != cap2 || !reflect.DeepEqual(blocks, blocks2) || !reflect.DeepEqual(entry, entry2) {
				t.Fatal("chunk size or insertion order changed image")
			}
			image, err := OpenFS(cap, blocks)
			if err != nil {
				t.Fatal(err)
			}
			content, err := stdfs.ReadFile(image, "link")
			if err != nil || !bytes.Equal(content, test.content) {
				t.Fatalf("round trip: %v", err)
			}
			file, err := image.OpenFile("file")
			if test.want == Lzip {
				if !errors.Is(err, ErrNotSeekable) {
					t.Fatalf("compressed OpenFile: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				offset := len(test.content) / 2
				buffer := make([]byte, min(17, len(test.content)-offset))
				if _, err := file.ReadAt(buffer, int64(offset)); err != nil || !bytes.Equal(buffer, test.content[offset:offset+len(buffer)]) {
					t.Fatalf("random read: %v", err)
				}
			}
			// Exact block-set equality also detects stored trial blocks and verifies
			// that replay neither omits nor duplicates the sampled prefix.
			forcedBlocks := make(eris.BlockSet)
			forced := NewBuilder(forcedBlocks, eris.Secret{1, 2, 3})
			if test.want == Lzip {
				err = forced.AddCompressedFile("file", bytes.NewReader(test.content), true)
			} else {
				err = forced.AddFile("file", bytes.NewReader(test.content), true)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := forced.AddSymlink("link", "file"); err != nil {
				t.Fatal(err)
			}
			forcedCap, err := forced.Finish()
			if err != nil || forcedCap != cap || !reflect.DeepEqual(forcedBlocks, blocks) {
				t.Fatalf("auto differs from selected explicit mode: %v", err)
			}
		})
	}
}

func TestAutoFileErrors(t *testing.T) {
	sentinel := errors.New("source failure")
	var absent *Builder
	for _, builder := range []*Builder{absent, NewBuilder(nil, eris.Secret{})} {
		if err := builder.AddAutoFile("file", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidEntry) {
			t.Fatal(err)
		}
	}
	builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
	if err := builder.AddAutoFile("file", nil, false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	if err := builder.AddAutoFile(".", errorReader{sentinel}, false); !errors.Is(err, ErrInvalidPath) {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("file", "target"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddAutoFile("file", errorReader{sentinel}, false); !errors.Is(err, ErrPathConflict) {
		t.Fatal(err)
	}
	for _, prefix := range [][]byte{nil, make([]byte, 100), make([]byte, AutoCompressionSampleSize), autoRandomBytes(AutoCompressionSampleSize)} {
		builder = NewBuilder(make(eris.BlockSet), eris.Secret{})
		input := io.MultiReader(bytes.NewReader(prefix), errorReader{sentinel})
		if err := builder.AddAutoFile("file", input, false); !errors.Is(err, sentinel) {
			t.Fatalf("failure after %d bytes: %v", len(prefix), err)
		}
		if len(builder.Index().Entries) != 0 {
			t.Fatal("source failure added an entry")
		}
	}
	for _, data := range [][]byte{make([]byte, 1024), autoRandomBytes(32769)} {
		builder = NewBuilder(errorSink{sentinel}, eris.Secret{})
		if err := builder.AddAutoFile("file", bytes.NewReader(data), false); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if len(builder.Index().Entries) != 0 {
			t.Fatal("sink failure added an entry")
		}
	}
}

func BenchmarkChooseCompression(b *testing.B) {
	for _, size := range []int{1024, AutoCompressionSampleSize} {
		for _, repetitive := range []bool{false, true} {
			data := autoRandomBytes(size)
			if repetitive {
				clear(data)
			}
			b.Run(fmt.Sprintf("%d/repetitive=%t", size, repetitive), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					ChooseCompression(data)
				}
			})
		}
	}
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

func TestAutoFilePreservesReadErrors(t *testing.T) {
	sentinel := errors.New("read failed with data")
	for _, size := range []int{100, AutoCompressionSampleSize, AutoCompressionSampleSize + 1} {
		for _, cause := range []error{sentinel, io.ErrUnexpectedEOF} {
			builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
			input := &finalErrorReader{data: make([]byte, size), err: cause}
			if err := builder.AddAutoFile("file", input, false); !errors.Is(err, cause) {
				t.Fatalf("%d bytes with %v: %v", size, cause, err)
			}
			if len(builder.Index().Entries) != 0 {
				t.Fatal("read failure added an entry")
			}
		}
	}
	builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
	if err := builder.AddAutoFile("file", noProgressReader{}, false); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("empty reads: %v", err)
	}
}
