package fs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
)

type lzipVector struct {
	Name        string
	Recipe      string
	Length      int
	InputSHA256 string `json:"input_sha256"`
	LzipSize    int    `json:"lzip_size"`
	LzipSHA256  string `json:"lzip_sha256"`
	LzipHex     string `json:"lzip_hex"`
	FileURN     string `json:"file_urn"`
	IndexHex    string `json:"index_hex"`
	ImageURN    string `json:"image_urn"`
}

func lzipVectors(t testing.TB) []lzipVector {
	t.Helper()
	data, err := os.ReadFile("testdata/lzip-conformance-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []lzipVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 9 {
		t.Fatal("incomplete Lzip fixture set")
	}
	return vectors
}

func (vector lzipVector) content(t testing.TB) []byte {
	t.Helper()
	data := make([]byte, vector.Length)
	x := uint32(1)
	for i := range data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		switch vector.Recipe {
		case "zeros":
		case "cycle":
			data[i] = byte(i)
		case "xorshift32":
			data[i] = byte(x)
		case "mixed":
			if i%8192 < 4096 {
				data[i] = byte(x)
			} else {
				data[i] = byte(i)
			}
		default:
			t.Fatalf("unknown input recipe %q", vector.Recipe)
		}
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != vector.InputSHA256 {
		t.Fatal("input recipe differs from fixture")
	}
	return data
}

// Fixed bytes and hashes catch changes that a same-process round trip cannot:
// altered compressor defaults, dependency upgrades, and architecture effects.
func TestLzipEncodingVectors(t *testing.T) {
	for _, vector := range lzipVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			content := vector.content(t)
			chunks := []int{1, 37, 4095, 32768}
			if len(content) > 1<<20 {
				chunks = []int{4095, 32768}
			}
			for _, chunk := range chunks {
				var output bytes.Buffer
				input := iotest.DataErrReader(smallChunkReader{bytes.NewReader(content), chunk})
				n, err := writeLzip(&output, input)
				if err != nil || n != int64(len(content)) {
					t.Fatalf("chunk %d: size=%d, error=%v", chunk, n, err)
				}
				stream := output.Bytes()
				if len(stream) != vector.LzipSize || fmt.Sprintf("%x", sha256.Sum256(stream)) != vector.LzipSHA256 {
					t.Fatalf("chunk %d: compressed bytes changed", chunk)
				}
				if vector.LzipHex != "" && hex.EncodeToString(stream) != vector.LzipHex {
					t.Fatalf("chunk %d: compressed bytes differ from literal vector", chunk)
				}
				if !bytes.Equal(stream[:6], []byte{'L', 'Z', 'I', 'P', 1, 23}) || stream[6] != 0 {
					t.Fatal("invalid header, dictionary, or LZMA start byte")
				}
				trailer := stream[len(stream)-20:]
				if binary.LittleEndian.Uint32(trailer) != crc32.ChecksumIEEE(content) ||
					binary.LittleEndian.Uint64(trailer[4:]) != uint64(len(content)) ||
					binary.LittleEndian.Uint64(trailer[12:]) != uint64(len(stream)) {
					t.Fatal("invalid Lzip trailer")
				}
			}
			blocks := make(eris.BlockSet)
			builder := NewBuilder(blocks, eris.Secret{})
			if err := builder.AddCompressedFile("file", bytes.NewReader(content), false); err != nil {
				t.Fatal(err)
			}
			entry := builder.Index().Entries[0]
			if entry.Capability.String() != vector.FileURN || entry.Size != int64(len(content)) || entry.Compression != Lzip {
				t.Fatal("compressed entry differs from fixture")
			}
			index, err := builder.Index().MarshalBinary()
			if err != nil || hex.EncodeToString(index) != vector.IndexHex {
				t.Fatalf("index differs from fixture: %v", err)
			}
			capability, err := builder.Finish()
			if err != nil || capability.String() != vector.ImageURN {
				t.Fatalf("image capability differs from fixture: %v", err)
			}
		})
	}
}

// This additional check uses liblzma through xz, not the Go LZMA decoder.
// All other conformance tests work without an installed external program.
func TestLzipIndependentDecoder(t *testing.T) {
	xz, err := exec.LookPath("xz")
	if err != nil {
		t.Skip("xz is not installed; independent Lzip check is unavailable")
	}
	for _, vector := range lzipVectors(t) {
		t.Run(vector.Name, func(t *testing.T) {
			content := vector.content(t)
			var output bytes.Buffer
			if _, err := writeLzip(&output, bytes.NewReader(content)); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(xz, "--format=lzip", "--decompress", "--stdout")
			command.Stdin = &output
			decoded, err := command.Output()
			if err != nil || !bytes.Equal(decoded, content) {
				t.Fatalf("independent Lzip decode: %v", err)
			}
		})
	}
}
