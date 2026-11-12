package fs

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
)

func FuzzIndexDecode(f *testing.F) {
	valid, _ := (Index{}).MarshalBinary()
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 1, 0xa0})
	f.Add([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 2, 0xa1, 0x81, 0x61, 'a', 0x82, 2, 0x80})
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
	for _, compression := range []Compression{Uncompressed, Lzip} {
		data, err := (Index{Entries: []Entry{{Path: []string{"file"}, Capability: capability, Size: 42, Compression: compression}}}).MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		if compression == Uncompressed {
			legacy := bytes.Clone(data[:len(data)-2]) // Remove the two-byte size.
			legacy[6], legacy[14] = 1, 0x82
			f.Add(legacy)
		}
	}
	// Counts fit in the input length, but the declared items are absent.
	f.Add(append([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 0x01, 0xb9, 0x01, 0x00}, make([]byte, 256)...))
	f.Add(append([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 0x01, 0xa1, 0x99, 0x01, 0x00}, make([]byte, 256)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		index, err := ParseIndex(data)
		if err != nil {
			return
		}
		encoded, err := index.MarshalBinary()
		unknown := false
		for _, entry := range index.Entries {
			unknown = unknown || entry.Size == -1
		}
		if unknown {
			if !errors.Is(err, ErrUnknownSize) {
				t.Fatalf("v1 file rewrite: %v", err)
			}
			return
		}
		if err != nil || encoded[6] != 2 {
			t.Fatalf("accepted index cannot be written as v2: %v", err)
		}
		decoded, err := ParseIndex(encoded)
		less := func(a, b Entry) int { return strings.Compare(strings.Join(a.Path, "/"), strings.Join(b.Path, "/")) }
		slices.SortFunc(decoded.Entries, less)
		slices.SortFunc(index.Entries, less)
		if err != nil || !reflect.DeepEqual(decoded, index) {
			t.Fatalf("normalized index changed: %v", err)
		}
	})
}

func FuzzIndexRoundTrip(f *testing.F) {
	f.Add("file", "target", uint8(2))
	f.Add("dir/file", "", uint8(0))
	f.Fuzz(func(t *testing.T, path, target string, kindByte uint8) {
		components, err := splitPath(path)
		if err != nil {
			return
		}
		kind := EntryType(kindByte % 4)
		entry := Entry{Path: components, Type: kind}
		if kind == Symlink {
			entry.Target = target
		} else {
			entry.Capability, _, _ = eris.Encode([]byte("seed"), eris.Secret{}, eris.BlockSize1KiB)
			if kind != Subdirectory {
				entry.Size = 4
			}
		}
		index := Index{Entries: []Entry{entry}}
		encoded, err := index.MarshalBinary()
		if err != nil {
			return
		}
		decoded, err := ParseIndex(encoded)
		if err != nil || !reflect.DeepEqual(decoded, index) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}

func FuzzLzipFile(f *testing.F) {
	f.Add(lzipMember(f, []byte("hello")), uint16(5))
	f.Add(lzipMember(f, nil), uint16(0))
	f.Add(append(lzipMember(f, []byte("one")), lzipMember(f, []byte("two"))...), uint16(6))
	f.Add([]byte("LZIP\x01\x0c"), uint16(0))
	f.Fuzz(func(t *testing.T, data []byte, size uint16) {
		if len(data) > 1<<18 {
			t.Skip()
		}
		image := compressedImage(t, data, int64(size))
		file, err := image.Open("file")
		if err != nil {
			return
		}
		defer func() { _ = file.Close() }()
		// Read in small buffers, without trusting the declared size to allocate.
		got, err := io.ReadAll(io.LimitReader(file, int64(size)+1))
		if len(got) > int(size) {
			t.Fatal("returned more than declared size")
		}
		if err == nil && len(got) != int(size) {
			t.Fatal("accepted wrong length")
		}
	})
}

func FuzzCompressedFile(f *testing.F) {
	f.Add([]byte{}, uint16(1))
	f.Add([]byte("hello"), uint16(3))
	f.Add(bytes.Repeat([]byte("abc"), 1024), uint16(37))
	f.Add(autoRandomBytes(32769), uint16(4095))
	f.Fuzz(func(t *testing.T, content []byte, chunk uint16) {
		if len(content) > 1<<18 {
			t.Skip()
		}
		var first, second bytes.Buffer
		if _, err := writeLzip(&first, bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		input := iotest.DataErrReader(smallChunkReader{Reader: bytes.NewReader(content), max: int(chunk) + 1})
		if _, err := writeLzip(&second, input); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("compression depends on read chunk size")
		}
		image := compressedImage(t, first.Bytes(), int64(len(content)))
		file, err := image.Open("file")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("compressed round trip: %v", err)
		}
	})
}

func FuzzAutoCompressionProbe(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("abcdabcd"))
	f.Add(make([]byte, 1024))
	f.Add(autoRandomBytes(2048))
	f.Add(append(autoRandomBytes(856), make([]byte, 168)...))
	f.Fuzz(func(t *testing.T, sample []byte) {
		// The backward-search oracle is quadratic. Full-size samples have
		// separate golden vectors and are also used by FuzzAutoFile.
		if len(sample) > 2048 {
			t.Skip()
		}
		encoded := referenceAutoProbe(sample)
		if got := autoProbeSize(sample); got != len(encoded) {
			t.Fatalf("probe size %d; reference size %d", got, len(encoded))
		}
		if !bytes.Equal(decodeAutoProbe(t, encoded), sample) {
			t.Fatal("probe token round trip failed")
		}
		want := Uncompressed
		if len(sample) >= 1024 && 8*(len(encoded)+26) <= 7*len(sample) {
			want = Lzip
		}
		if ChooseCompression(sample) != want {
			t.Fatal("decision differs from reference")
		}
	})
}

func FuzzAutoFile(f *testing.F) {
	f.Add([]byte{}, uint16(1), false)
	f.Add(make([]byte, 1023), uint16(37), false)
	f.Add(make([]byte, 1024), uint16(1), true)
	f.Add(autoRandomBytes(1024), uint16(13), true)
	f.Add(autoRandomBytes(AutoCompressionSampleSize), uint16(7), false)
	f.Add(make([]byte, AutoCompressionSampleSize+1), uint16(1023), true)
	f.Fuzz(func(t *testing.T, content []byte, chunk uint16, executable bool) {
		if len(content) > 2*AutoCompressionSampleSize+17 {
			t.Skip()
		}
		cap, blocks, entry := buildAutoImage(t, bytes.NewReader(content), executable, false)
		input := smallChunkReader{Reader: iotest.DataErrReader(bytes.NewReader(content)), max: int(chunk) + 1}
		otherCap, otherBlocks, otherEntry := buildAutoImage(t, input, executable, true)
		if cap != otherCap || !reflect.DeepEqual(blocks, otherBlocks) || !reflect.DeepEqual(entry, otherEntry) {
			t.Fatal("chunk boundaries or entry order changed auto image")
		}
		if entry.Size != int64(len(content)) || entry.Compression != ChooseCompression(content) {
			t.Fatal("incorrect auto metadata")
		}
		image, err := OpenFS(cap, blocks)
		if err != nil {
			t.Fatal(err)
		}
		file, err := image.Open("file")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		decoded, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("auto round trip: %v", err)
		}
	})
}
