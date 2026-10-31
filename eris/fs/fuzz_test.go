package fs

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/asciimoth/eris-tools/eris"
)

func FuzzIndexDecode(f *testing.F) {
	valid, _ := (Index{}).MarshalBinary()
	f.Add(valid)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		index, err := ParseIndex(data)
		if err != nil {
			return
		}
		encoded, err := index.MarshalBinary()
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("accepted index is not canonical: %v", err)
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

func FuzzReaderRandomAccess(f *testing.F) {
	f.Add([]byte{}, uint16(0), uint16(1))
	f.Add([]byte("content"), uint16(0), uint16(20))
	f.Add(make([]byte, 2049), uint16(1000), uint16(2000))
	f.Add(make([]byte, 16385), uint16(16380), uint16(16))
	f.Fuzz(func(t *testing.T, content []byte, offset, length uint16) {
		if len(content) > 1<<18 {
			t.Skip()
		}
		blocks := make(eris.BlockSet)
		capability, err := EncodeContent(bytes.NewReader(content), eris.Secret{1}, eris.BlockSize1KiB, blocks)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := NewReader(capability, blocks)
		if err != nil {
			t.Fatal(err)
		}
		position := int(offset)
		if position > len(content) {
			position = len(content)
		}
		buffer := make([]byte, int(length))
		n, readErr := reader.ReadAt(buffer, int64(position))
		end := min(len(content), position+len(buffer))
		if n != end-position || !bytes.Equal(buffer[:n], content[position:end]) {
			t.Fatalf("random read returned %d bytes; want %d matching bytes", n, end-position)
		}
		if len(buffer) > n && !errors.Is(readErr, io.EOF) {
			t.Fatalf("short read error = %v; want EOF", readErr)
		}
		if len(buffer) == n && readErr != nil {
			t.Fatalf("complete read error = %v; want nil", readErr)
		}
	})
}
