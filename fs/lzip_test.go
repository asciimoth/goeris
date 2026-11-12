package fs

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	stdfs "io/fs"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/ulikunitz/xz/lzma"
)

// lzipMember constructs a test stream with the independently maintained LZMA
// encoder. The container fields follow the Lzip format, not the FS codec.
func lzipMember(t testing.TB, content []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := (lzma.WriterConfig{DictCap: 4096}).NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := append([]byte{'L', 'Z', 'I', 'P', 1, 12}, compressed.Bytes()[lzma.HeaderLen:]...)
	var trailer [20]byte
	binary.LittleEndian.PutUint32(trailer[:4], crc32.ChecksumIEEE(content))
	binary.LittleEndian.PutUint64(trailer[4:12], uint64(len(content)))
	binary.LittleEndian.PutUint64(trailer[12:], uint64(len(data)+20))
	return append(data, trailer[:]...)
}

func compressedImage(t testing.TB, data []byte, size int64) *FS {
	t.Helper()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	if err := builder.AddLzipFile("file", bytes.NewReader(data), size, true); err != nil {
		t.Fatal(err)
	}
	cap, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(cap, blocks)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func TestLzipFile(t *testing.T) {
	for _, content := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte("streaming Lzip file\n"), 20000)} {
		data := lzipMember(t, content)
		for _, stream := range [][]byte{data, append(append(lzipMember(t, nil), data...), lzipMember(t, nil)...)} {
			image := compressedImage(t, stream, int64(len(content)))
			info, err := image.Stat("file")
			if err != nil || info.Size() != int64(len(content)) || info.Mode() != 0o555 {
				t.Fatalf("stat: %v, %v", info, err)
			}
			if _, err := image.OpenFile("file"); !errors.Is(err, ErrNotSeekable) {
				t.Fatalf("seekable compressed file: %v", err)
			}
			file, err := image.Open("file")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := file.(io.Seeker); ok {
				t.Fatal("Lzip claims seek support")
			}
			if _, err := file.Stat(); err != nil {
				t.Fatal(err)
			}
			if n, err := file.Read(nil); n != 0 || err != nil {
				t.Fatalf("empty read %d, %v", n, err)
			}
			var got bytes.Buffer
			buffer := make([]byte, 37)
			if _, err := io.CopyBuffer(&got, file, buffer); err != nil || !bytes.Equal(got.Bytes(), content) {
				t.Fatalf("read mismatch: %v", err)
			}
			if n, err := file.Read(buffer); n != 0 || err != io.EOF {
				t.Fatalf("EOF: %d, %v", n, err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Read(buffer); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
			if _, err := file.Stat(); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Each member has an independent CRC, size, and dictionary.
	left, right := []byte("first member\n"), []byte("second member\n")
	image := compressedImage(t, append(lzipMember(t, left), lzipMember(t, right)...), int64(len(left)+len(right)))
	got, err := stdfs.ReadFile(image, "file")
	if err != nil || !bytes.Equal(got, append(left, right...)) {
		t.Fatalf("members: %q, %v", got, err)
	}
}

func TestLzipRejectsMalformedStreams(t *testing.T) {
	content := []byte("hello Lzip\n")
	valid := lzipMember(t, content)
	check := func(t *testing.T, data []byte, size int64, want error) {
		t.Helper()
		image := compressedImage(t, data, size)
		file, err := image.Open("file")
		if err == nil {
			_, err = io.ReadAll(file)
			_ = file.Close()
		}
		if err == nil || want != nil && !errors.Is(err, want) {
			t.Fatalf("accepted malformed stream %x: %v; want %v", data, err, want)
		}
	}
	for i := range valid {
		t.Run("truncated-"+string(rune('A'+i)), func(t *testing.T) { check(t, valid[:i], int64(len(content)), nil) })
	}
	for _, offset := range []int{0, 4, len(valid) - 20, len(valid) - 16, len(valid) - 8} {
		data := bytes.Clone(valid)
		data[offset] ^= 1
		check(t, data, int64(len(content)), ErrInvalidCompression)
	}
	for _, dict := range []byte{0, 11, 27, 29, 31, 0xff} {
		data := bytes.Clone(valid)
		data[5] = dict
		check(t, data, int64(len(content)), ErrInvalidCompression)
	}
	data := bytes.Clone(valid)
	data[6] = 0xff
	check(t, data, int64(len(content)), nil)
	check(t, valid, int64(len(content)-1), ErrSizeMismatch)
	check(t, valid, int64(len(content)+1), ErrSizeMismatch)
	check(t, valid, 0, ErrSizeMismatch)
	check(t, lzipMember(t, nil), 1, ErrSizeMismatch)
	check(t, append(bytes.Clone(valid), 0), int64(len(content)), io.ErrUnexpectedEOF)
	check(t, append(bytes.Clone(valid), 'L', 'Z', 'I', 'P', 1, 12), int64(len(content)), nil)
	// CRC must be checked even when the caller reads exactly the declared size.
	data = bytes.Clone(valid)
	data[len(data)-20] ^= 1
	image := compressedImage(t, data, int64(len(content)))
	file, err := image.Open("file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Read(make([]byte, len(content))); !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("exact-size read skipped CRC: %v", err)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, ErrInvalidCompression) {
		t.Fatalf("error not retained: %v", err)
	}
}

func TestLzipStorageErrors(t *testing.T) {
	cap, _, _ := eris.Encode([]byte("missing"), eris.Secret{}, eris.BlockSize1KiB)
	if _, err := openLzipFile(&node{capability: cap, source: make(eris.BlockSet)}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	if err := builder.AddLzipFile("file", strings.NewReader(""), -1, false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
	if err := builder.AddLzipFile(".", strings.NewReader(""), 0, false); !errors.Is(err, ErrInvalidPath) {
		t.Fatal(err)
	}
	sentinel := errors.New("input error")
	if err := builder.AddLzipFile("file", errorReader{sentinel}, 0, false); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("failed call added entry")
	}
	counted := countReader{reader: strings.NewReader("a"), size: math.MaxInt64}
	if _, err := counted.Read(make([]byte, 1)); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
}

func TestLzipIndependentVector(t *testing.T) {
	// Generated with Python's liblzma binding and verified with xz --format=lzip.
	// This fixed vector keeps the test independent of the Go LZMA encoder.
	const wire = "4c5a495001170022948535bb270d39c4eb845c044ff5bfffe80d8000c811d22d0b000000000000003000000000000000"
	data, err := hex.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	image := compressedImage(t, data, 11)
	got, err := stdfs.ReadFile(image, "file")
	if err != nil || string(got) != "ERIS-FS v2\n" {
		t.Fatalf("independent vector: %q, %v", got, err)
	}
}

func TestOpenFileRejectsCompressionWithoutContentReads(t *testing.T) {
	image := compressedImage(t, lzipMember(t, []byte("content")), 7)
	// Metadata alone must establish that this file has no random access.
	clear(image.source.(eris.BlockSet))
	if _, err := image.OpenFile("file"); !errors.Is(err, ErrNotSeekable) {
		t.Fatalf("OpenFile with unavailable compressed content: %v", err)
	}
}

func TestLzipSmallOutputMemory(t *testing.T) {
	// A valid empty member can advertise a large dictionary. Repeating it
	// must not allocate that dictionary once per member for a zero-byte file.
	member := lzipMember(t, nil)
	member[5] = 26 // 64 MiB; no matches depend on the dictionary size.
	image := compressedImage(t, bytes.Repeat(member, 4), 0)
	// Keep this test sequential so parallel package tests do not contribute
	// to the allocation count. The allowance includes decoder and ERIS work.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := image.ReadFile("file")
	runtime.ReadMemStats(&after)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty members: %d bytes, %v", len(got), err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("empty members allocated %d bytes; maximum is 8 MiB", allocated)
	}
}

func TestLzipDictionaryOutputBoundaries(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097, 16384, 16385} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			content := bytes.Repeat([]byte("abcd"), (size+3)/4)[:size]
			member := lzipMember(t, content)
			member[5] = 26 // The larger declared dictionary is valid too.
			// A second member starts with a smaller remaining output bound.
			stream := append(member, lzipMember(t, []byte("tail"))...)
			image := compressedImage(t, stream, int64(size+4))
			file, err := image.Open("file")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			var got bytes.Buffer
			// Hide bytes.Buffer.ReadFrom so CopyBuffer uses small Read calls.
			_, err = io.CopyBuffer(struct{ io.Writer }{&got}, file, make([]byte, 37))
			if err != nil || !bytes.Equal(got.Bytes(), append(content, "tail"...)) {
				t.Fatalf("dictionary boundary: %v", err)
			}
		})
	}
}
