package erisreader

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/asciimoth/goeris"
)

func FuzzReaderRandomAccess(f *testing.F) {
	f.Add([]byte{}, uint16(0), uint16(1))
	f.Add([]byte("content"), uint16(0), uint16(20))
	f.Add(make([]byte, 2049), uint16(1000), uint16(2000))
	f.Add(make([]byte, 16385), uint16(16380), uint16(16))
	f.Fuzz(func(t *testing.T, content []byte, offset, length uint16) {
		if len(content) > 1<<18 {
			t.Skip()
		}
		capability, blocks, err := eris.Encode(content, eris.Secret{1}, eris.BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		for _, sized := range []bool{false, true} {
			var reader *Reader
			if sized {
				reader, err = NewReaderWithSize(capability, blocks, int64(len(content)))
			} else {
				reader, err = NewReader(capability, blocks)
			}
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
			_ = reader.Close()
		}
	})
}

func FuzzTreeShape(f *testing.F) {
	f.Add([]byte{16, 1}, uint16(17))
	f.Add([]byte{1, 16}, uint16(17))
	f.Add([]byte{16, 0}, uint16(17))
	f.Add([]byte{16}, uint16(17))
	f.Add([]byte{16, 16}, uint16(1))
	f.Fuzz(func(t *testing.T, counts []byte, limitBlocks uint16) {
		if len(counts) > 16 {
			t.Skip()
		}
		blocks := make(eris.BlockSet)
		store := func(node []byte, level eris.Level) eris.Pair {
			block, pair, err := eris.EncryptNode(node, eris.Secret{}, level)
			if err != nil {
				t.Fatal(err)
			}
			blocks[pair.Reference] = block
			return pair
		}
		putPair := func(node []byte, index int, pair eris.Pair) {
			copy(node[index*eris.PairSize:], pair.Reference[:])
			copy(node[index*eris.PairSize+32:], pair.Key[:])
		}
		leaf := make([]byte, eris.BlockSize1KiB)
		leaf[0] = 0x80
		leafPair := store(leaf, 0)
		root := make([]byte, eris.BlockSize1KiB)
		valid := len(counts) >= 2
		leaves := 0
		for index, value := range counts {
			count := int(value % 17)
			valid = valid && count > 0 && (index == len(counts)-1 || count == 16)
			leaves += count
			parent := make([]byte, eris.BlockSize1KiB)
			for child := range count {
				putPair(parent, child, leafPair)
			}
			putPair(root, index, store(parent, 1))
		}
		capability := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: store(root, 2)}
		limit := int64(limitBlocks%257) * int64(eris.BlockSize1KiB)
		wantSize := (leaves - 1) * int(eris.BlockSize1KiB)
		// The size-aware reader must reject malformed visited paths too. It
		// has no index-size limit, so compare before applying that limit.
		sized, sizedErr := NewReaderWithSize(capability, blocks, int64(wantSize))
		if sizedErr == nil {
			_, sizedErr = io.Copy(io.Discard, sized)
			_ = sized.Close()
		}
		if (sizedErr == nil) != valid {
			t.Fatalf("counts %v: sized reader = %v; valid = %t", counts, sizedErr, valid)
		}
		valid = valid && int64(wantSize) <= limit
		decoded, coreErr := eris.DecodeWithLimit(capability, blocks, limit)
		reader, readerErr := NewReaderWithLimit(capability, blocks, limit)
		if (coreErr == nil) != valid || (readerErr == nil) != valid {
			t.Fatalf("counts %v, limit %d: core = %v, reader = %v; valid = %t", counts, limit, coreErr, readerErr, valid)
		}
		if !valid {
			return
		}
		defer func() { _ = reader.Close() }()
		got, err := io.ReadAll(reader)
		want := bytes.Repeat(leaf, leaves-1)
		if err != nil || !bytes.Equal(decoded, want) || !bytes.Equal(got, want) || reader.Size() != int64(wantSize) {
			t.Fatalf("tree content mismatch: size = %d, want %d, error = %v", reader.Size(), wantSize, err)
		}
	})
}
