package fs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"math"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

func TestIndexCanonicalRoundTrip(t *testing.T) {
	t.Parallel()
	fileCap, _, err := eris.Encode([]byte("file"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{Entries: []Entry{
		{Path: []string{"z"}, Type: Symlink, Target: "../outside"},
		{Path: []string{"bin", "tool"}, Type: Executable, Capability: fileCap, Size: 4},
		{Path: []string{"a"}, Type: Regular, Capability: fileCap, Size: 4},
	}}
	encoded, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !HasIndexHeader(encoded) || HasIndexHeader(encoded[:4]) || HasIndexHeader(append([]byte{0}, encoded[1:]...)) {
		t.Fatalf("tag = %x", encoded[:5])
	}
	decoded, err := ParseIndex(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Entries) != 3 {
		t.Fatalf("entry count = %d", len(decoded.Entries))
	}
	reencoded, err := decoded.MarshalBinary()
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("canonical round trip failed: %v", err)
	}
	// Canonical map ordering puts the shortest encoded key first.
	if decoded.Entries[0].Path[0] != "a" {
		t.Fatalf("first canonical path = %v", decoded.Entries[0].Path)
	}
}

func TestIndexWireVector(t *testing.T) {
	t.Parallel()
	index := Index{Entries: []Entry{{Path: []string{"a"}, Type: Symlink, Target: "b"}}}
	encoded, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	const expectedHex = "da657269748202a18161618202816162"
	if hex.EncodeToString(encoded) != expectedHex {
		t.Fatalf("encoded index = %x, want %s", encoded, expectedHex)
	}
	parsed, err := ParseIndex(encoded)
	if err != nil || !reflect.DeepEqual(parsed, index) {
		t.Fatalf("ParseIndex() = (%v, %v)", parsed, err)
	}
}

func TestIndexPreservesBackslashComponent(t *testing.T) {
	t.Parallel()
	index := Index{Entries: []Entry{{Path: []string{`back\slash`}, Type: Symlink, Target: "target"}}}
	encoded, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseIndex(encoded)
	if err != nil || !reflect.DeepEqual(decoded, index) {
		t.Fatalf("backslash round trip = (%v, %v)", decoded, err)
	}
}

func TestIndexValidation(t *testing.T) {
	t.Parallel()
	validCap, _, _ := eris.Encode(nil, eris.Secret{}, eris.BlockSize1KiB)
	tests := []struct {
		name  string
		entry Entry
		err   error
	}{
		{"empty path", Entry{Type: Symlink}, ErrInvalidPath},
		{"empty component", Entry{Path: []string{""}, Type: Symlink}, ErrInvalidPath},
		{"dot", Entry{Path: []string{"."}, Type: Symlink}, ErrInvalidPath},
		{"dot dot", Entry{Path: []string{".."}, Type: Symlink}, ErrInvalidPath},
		{"slash", Entry{Path: []string{"a/b"}, Type: Symlink}, ErrInvalidPath},
		{"invalid utf8 path", Entry{Path: []string{string([]byte{0xff})}, Type: Symlink}, ErrInvalidPath},
		{"bad kind", Entry{Path: []string{"a"}, Type: 9}, ErrInvalidEntry},
		{"file without cap", Entry{Path: []string{"a"}, Type: Regular}, ErrInvalidEntry},
		{"file target", Entry{Path: []string{"a"}, Type: Regular, Capability: validCap, Target: "x"}, ErrInvalidEntry},
		{"link cap", Entry{Path: []string{"a"}, Type: Symlink, Capability: validCap}, ErrInvalidEntry},
		{"link utf8", Entry{Path: []string{"a"}, Type: Symlink, Target: string([]byte{0xff})}, ErrInvalidEntry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (Index{Entries: []Entry{test.entry}}).Validate()
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
		})
	}
	for name, entries := range map[string][]Entry{
		"duplicate": {
			{Path: []string{"a"}, Type: Symlink}, {Path: []string{"a"}, Type: Symlink},
		},
		"prefix": {
			{Path: []string{"a"}, Type: Symlink}, {Path: []string{"a", "b"}, Type: Symlink},
		},
		"interleaved prefix": {
			{Path: []string{"a"}, Type: Symlink},
			{Path: []string{"a-b"}, Type: Symlink},
			{Path: []string{"a", "b"}, Type: Symlink},
		},
		"child before parent": {
			{Path: []string{"a", "b"}, Type: Symlink},
			{Path: []string{"a-b"}, Type: Symlink},
			{Path: []string{"a"}, Type: Symlink},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (Index{Entries: entries}).Validate(); !errors.Is(err, ErrPathConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestIndexRejectsInvalidCBOR(t *testing.T) {
	t.Parallel()
	var nilIndex *Index
	if !errors.Is(nilIndex.UnmarshalBinary(nil), ErrInvalidIndex) {
		t.Fatal("nil receiver accepted")
	}
	valid, err := (Index{}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	mutations := [][]byte{
		nil,
		valid[:4],
		append([]byte{0}, valid[1:]...),
		append(bytes.Clone(valid), 0),
		append(bytes.Clone(valid[:5]), 0x81, 0x01),
		append(bytes.Clone(valid[:5]), 0x82, 0x03, 0xa0),
		append(bytes.Clone(valid[:5]), 0x82, 0x01, 0xbf, 0xff),
	}
	for _, data := range mutations {
		var index Index
		if err := index.UnmarshalBinary(data); !errors.Is(err, ErrInvalidIndex) {
			t.Errorf("UnmarshalBinary(%x) = %v", data, err)
		}
	}
}

func TestCBORPrimitives(t *testing.T) {
	t.Parallel()
	values := []uint64{0, 23, 24, 255, 256, 65535, 65536, 1 << 32, 1 << 48}
	for _, value := range values {
		encoded := cborwire.AppendHead(nil, 6, value)
		decoder := cborDecoder{data: encoded}
		decoded, err := decoder.head(6)
		if err != nil || decoded != value || decoder.offset != len(encoded) {
			t.Errorf("head(%d) = (%d, %v)", value, decoded, err)
		}
	}
	invalid := [][]byte{
		{}, {0x00}, {0xd8}, {0xd8, 23}, {0xd9, 0, 0xff},
		{0xda, 0, 0, 0xff, 0xff}, {0xdb, 0, 0, 0, 0, 0, 0, 0xff, 0xff}, {0xdc},
	}
	for _, data := range invalid {
		decoder := cborDecoder{data: data}
		if _, err := decoder.head(6); err == nil {
			t.Errorf("accepted invalid head %x", data)
		}
	}
	decoder := cborDecoder{data: make([]byte, 3)}
	if _, err := decoder.fixed(3, 0); err == nil {
		t.Fatal("accepted invalid fixed integer size")
	}
	malformedEntry := append(append([]byte(nil), []byte(MagicPrefix)...), 0x82, 0x01, 0xa1, 0x00)
	if _, err := ParseIndex(malformedEntry); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("malformed entry error = %v", err)
	}
}

func TestCBORRejectsMalformedEntries(t *testing.T) {
	t.Parallel()
	capability, _, _ := eris.Encode(nil, eris.Secret{}, eris.BlockSize1KiB)
	binaryCap, _ := capability.MarshalBinary()
	validFile := append([]byte{0x81, 0x61, 'a', 0x82, 0x00, 0xd9, 0x01, 0x14, 0x58, 0x42}, binaryCap...)
	validLink := []byte{0x81, 0x61, 'a', 0x82, 0x02, 0x61, 'x'}
	for name, data := range map[string][]byte{
		"path major":       {0x01},
		"empty path":       {0x80},
		"huge path":        {0x9a, 0xff, 0xff, 0xff, 0xff},
		"text major":       {0x81, 0x01},
		"invalid utf8":     {0x81, 0x61, 0xff},
		"entry array":      {0x81, 0x61, 'a', 0x81},
		"kind major":       {0x81, 0x61, 'a', 0x82, 0x61, 'x'},
		"kind too large":   {0x81, 0x61, 'a', 0x82, 0x19, 0x01, 0x00},
		"unknown kind":     {0x81, 0x61, 'a', 0x82, 0x09},
		"link target":      {0x81, 0x61, 'a', 0x82, 0x02, 0x01},
		"capability tag":   {0x81, 0x61, 'a', 0x82, 0x00, 0xd8, 0x01},
		"capability bytes": {0x81, 0x61, 'a', 0x82, 0x00, 0xd9, 0x01, 0x14, 0x01},
		"bad capability":   {0x81, 0x61, 'a', 0x82, 0x00, 0xd9, 0x01, 0x14, 0x41, 0x00},
	} {
		t.Run(name, func(t *testing.T) {
			decoder := cborDecoder{data: data}
			if _, _, err := decoder.entry(1); err == nil {
				t.Fatalf("accepted %x", data)
			}
		})
	}
	for _, data := range [][]byte{validFile, validLink} {
		decoder := cborDecoder{data: data}
		if _, _, err := decoder.entry(1); err != nil || decoder.offset != len(data) {
			t.Fatalf("rejected valid entry: %v", err)
		}
	}
}

func TestIndexDecodeLimits(t *testing.T) {
	t.Parallel()
	index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink, Target: "target"}}}
	data, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	blocks := make(eris.BlockSet)
	capability, err := EncodeIndex(context.Background(), index, eris.Secret{}, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeIndexWithLimit(context.Background(), capability, blocks, int64(len(data)-1), eris.BlockOptions{}); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("small limit error = %v", err)
	}
	decoded, err := DecodeIndexWithLimit(context.Background(), capability, blocks, int64(len(data)), eris.BlockOptions{})
	if err != nil || !reflect.DeepEqual(decoded, index) {
		t.Fatalf("exact limit decode = (%v, %v)", decoded, err)
	}
	if _, err = DecodeIndexWithLimit(context.Background(), capability, blocks, 0, eris.BlockOptions{}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("invalid limit error = %v", err)
	}
	if _, err = OpenFSWithLimit(context.Background(), capability, blocks, int64(len(data)-1), eris.BlockOptions{}); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("OpenFSWithLimit error = %v", err)
	}
}

func TestNestedIndexUsesConfiguredLimit(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	nested := Index{Entries: make([]Entry, 100)}
	for i := range nested.Entries {
		nested.Entries[i] = Entry{
			Path: []string{fmt.Sprintf("link-%03d", i)}, Type: Symlink,
			Target: strings.Repeat("target", 10),
		}
	}
	nestedData, _ := nested.MarshalBinary()
	nestedCap, err := EncodeIndex(context.Background(), nested, eris.Secret{}, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	top := Index{Entries: []Entry{{Path: []string{"nested"}, Type: Subdirectory, Capability: nestedCap}}}
	topData, _ := top.MarshalBinary()
	if len(topData) >= len(nestedData) {
		t.Fatal("test nested index is not larger than top index")
	}
	topCap, err := EncodeIndex(context.Background(), top, eris.Secret{}, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFSWithLimit(context.Background(), topCap, blocks, int64(len(topData)), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = image.ReadDir(context.Background(), "nested"); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("nested limit error = %v", err)
	}
}

func TestBuilderAndFileSystem(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	secret := eris.Secret{4, 5, 6}
	nested := NewBuilder(blocks, secret, eris.BlockOptions{})
	if err := nested.AddFile(context.Background(), "inside", bytes.NewBufferString("nested data"), false); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(blocks, secret, eris.BlockOptions{})
	if err = builder.AddFile(context.Background(), "docs/readme", bytes.NewBufferString("hello"), false); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddFile(context.Background(), "bin/run", bytes.NewBufferString("execute"), true); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddSymlink("latest", "docs/readme"); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddSubdirectory("vendor", nestedCap); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var highLevel FileSystem = image
	data, err := stdfs.ReadFile(NewIOFS(context.Background(), image), "docs/readme")
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile = (%q, %v)", data, err)
	}
	openedFile, err := image.Open(context.Background(), "docs/readme")
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := openedFile.Stat(); statErr != nil || info.Name() != "readme" {
		t.Fatalf("opened file Stat = (%v, %v)", info, statErr)
	}
	_ = openedFile.Close()
	randomAccessFile, err := highLevel.OpenFile(context.Background(), "docs/readme")
	if err != nil {
		t.Fatal(err)
	}
	randomData := make([]byte, 3)
	if count, readErr := randomAccessFile.ReadAt(randomData, 1); readErr != nil || count != 3 || string(randomData) != "ell" {
		t.Fatalf("OpenFile ReadAt = (%d, %q, %v)", count, randomData, readErr)
	}
	if offset, seekErr := randomAccessFile.Seek(-1, io.SeekEnd); seekErr != nil || offset != 4 {
		t.Fatalf("OpenFile Seek = (%d, %v)", offset, seekErr)
	}
	_ = randomAccessFile.Close()
	if _, err = highLevel.OpenFile(context.Background(), "docs"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("OpenFile directory error = %v", err)
	}
	if _, err = highLevel.OpenFile(context.Background(), "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("OpenFile missing error = %v", err)
	}
	data, err = stdfs.ReadFile(NewIOFS(context.Background(), image), "vendor/inside")
	if err != nil || string(data) != "nested data" {
		t.Fatalf("nested ReadFile = (%q, %v)", data, err)
	}
	target, err := image.ReadLink(context.Background(), "latest")
	if err != nil || target != "docs/readme" {
		t.Fatalf("ReadLink = (%q, %v)", target, err)
	}
	info, err := image.Lstat(context.Background(), "latest")
	if err != nil || info.Mode()&stdfs.ModeSymlink == 0 || info.Size() != int64(len(target)) {
		t.Fatalf("Lstat link = (%v, %v)", info, err)
	}
	info, err = image.Stat(context.Background(), "bin/run")
	if err != nil || info.Mode()&0o111 == 0 || info.Size() != 7 {
		t.Fatalf("Stat executable = (%v, %v)", info, err)
	}
	entries, err := image.ReadDir(context.Background(), ".")
	if err != nil || names(entries) != "bin,docs,latest,vendor" {
		t.Fatalf("ReadDir = (%v, %v)", names(entries), err)
	}
	sub, err := image.Sub(context.Background(), "docs")
	if err != nil {
		t.Fatal(err)
	}
	data, err = stdfs.ReadFile(NewIOFS(context.Background(), sub), "readme")
	if err != nil || string(data) != "hello" {
		t.Fatalf("sub ReadFile = (%q, %v)", data, err)
	}
	data, err = stdfs.ReadFile(NewIOFS(context.Background(), image), "latest")
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile through link = (%q, %v)", data, err)
	}
}

func TestBuilderBlockSizeBoundary(t *testing.T) {
	t.Parallel()
	for _, length := range []int{(1 << 14) - 1, 1 << 14, (1 << 14) + 1} {
		content := deterministicBytes(length)
		blocks := make(eris.BlockSet)
		builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
		if err := builder.AddFile(context.Background(), "file", bytes.NewReader(content), false); err != nil {
			t.Fatal(err)
		}
		entry := builder.Index().Entries[0]
		if entry.Size != int64(length) {
			t.Fatalf("entry size = %d; want %d", entry.Size, length)
		}
		if entry.Capability.BlockSize != eris.ChooseBlockSize(length) {
			t.Errorf("length %d block size = %d", length, entry.Capability.BlockSize)
		}
		reader, err := eris.NewReader(context.Background(), entry.Capability, blocks, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("length %d round trip failed: %v", length, err)
		}
	}
}

func TestFileSystemConformance(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{7}, eris.BlockOptions{})
	if err := builder.AddFile(context.Background(), "a/file", bytes.NewBufferString("content"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "tool", bytes.NewBufferString("run"), true); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("a/link", "../tool"); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = fstest.TestFS(NewIOFS(context.Background(), image), "a", "a/file", "a/link", "tool"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryReadDirLargeCountAfterOffset(t *testing.T) {
	t.Parallel()
	file := &directoryFile{node: &node{children: map[string]*node{
		"a": {name: "a"}, "b": {name: "b"}, "c": {name: "c"},
	}}}
	first, err := file.ReadDir(1)
	if err != nil || names(first) != "a" {
		t.Fatalf("first ReadDir = (%v, %v)", names(first), err)
	}
	rest, err := file.ReadDir(math.MaxInt)
	if err != nil || names(rest) != "b,c" {
		t.Fatalf("large ReadDir = (%v, %v)", names(rest), err)
	}
}

func TestDirectoryReadDirReturnedSliceIsIndependent(t *testing.T) {
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	for _, name := range []string{"c", "a", "b"} {
		if err := builder.AddSymlink(name, "target"); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := image.Open(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	directory := file.(stdfs.ReadDirFile)
	first, err := directory.ReadDir(1)
	if err != nil || names(first) != "a" {
		t.Fatalf("first entries: %v, %v", first, err)
	}
	// Callers own the returned slice and can append or change its elements.
	first = append(first, first[0])
	first[0] = nil
	rest, err := directory.ReadDir(0)
	if err != nil || names(rest) != "b,c" {
		t.Fatalf("remaining entries: %v, %v", rest, err)
	}
	again, err := image.ReadDir(context.Background(), ".")
	if err != nil || names(again) != "a,b,c" {
		t.Fatalf("independent listing: %v, %v", again, err)
	}
}

func TestOpenReturnsNilOnContentError(t *testing.T) {
	blocks := make(eris.BlockSet)
	// A leaf capability cannot hold the advertised 1024 bytes plus padding.
	index := Index{Entries: []Entry{{Path: []string{"file"}, Size: 1024,
		Capability: eris.Capability{BlockSize: eris.BlockSize1KiB}}}}
	capability, err := EncodeIndex(context.Background(), index, eris.Secret{}, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if file, err := image.Open(context.Background(), "file"); file != nil || !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("Open: %v, %v", file, err)
	}
	if file, err := image.OpenFile(context.Background(), "file"); file != nil || !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("OpenFile: %v, %v", file, err)
	}
}

func TestFileSystemLinkResolutionStaysVirtual(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	for _, link := range []struct{ name, target string }{
		{"directory-link", "directory"},
		{"absolute", "/host/path"},
		{"directory/escape", "../../host/path"},
		{"loop-a", "loop-b"},
		{"loop-b", "loop-a"},
	} {
		if err := builder.AddSymlink(link.name, link.target); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.AddFile(context.Background(), "directory/file", bytes.NewBufferString("virtual"), false); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := stdfs.ReadFile(NewIOFS(context.Background(), image), "directory-link/file")
	if err != nil || string(data) != "virtual" {
		t.Fatalf("internal directory link = (%q, %v)", data, err)
	}
	for _, name := range []string{"absolute", "directory/escape"} {
		if _, err = image.Open(context.Background(), name); !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("Open(%q) error = %v", name, err)
		}
	}
	if _, err = image.Open(context.Background(), "loop-a"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("link loop error = %v", err)
	}
}

func TestDirectoryFile(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	_ = builder.AddSymlink("b", "x")
	_ = builder.AddSymlink("a", "x")
	capability, _ := builder.Finish(context.Background())
	image, _ := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	opened, err := image.Open(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	directory := opened.(stdfs.ReadDirFile)
	entries, err := directory.ReadDir(1)
	if err != nil || len(entries) != 1 || entries[0].Name() != "a" {
		t.Fatalf("first ReadDir = (%v, %v)", entries, err)
	}
	entries, err = directory.ReadDir(2)
	if err != nil || len(entries) != 1 || entries[0].Name() != "b" {
		t.Fatalf("second ReadDir = (%v, %v)", entries, err)
	}
	if entries, err = directory.ReadDir(1); len(entries) != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("final ReadDir = (%v, %v)", entries, err)
	}
	if _, err = opened.Read(nil); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("directory Read error = %v", err)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = opened.Stat(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Stat error = %v", err)
	}
	if _, err = opened.Read(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Read error = %v", err)
	}
	if _, err = directory.ReadDir(0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed ReadDir error = %v", err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type flakySource struct {
	eris.BlockSource
	ref       eris.Reference
	remaining int
	err       error
}

type failAfterSource struct {
	eris.BlockSource
	calls  int
	failAt int
	err    error
}

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (source *failAfterSource) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	source.calls++
	if source.calls == source.failAt {
		return nil, source.err, eris.BlockOptions{}
	}
	return source.BlockSource.GetBlock(ctx, ref, 0, eris.BlockOptions{})
}

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (source *flakySource) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	if ref == source.ref && source.remaining > 0 {
		source.remaining--
		return nil, source.err, eris.BlockOptions{}
	}
	return source.BlockSource.GetBlock(ctx, ref, 0, eris.BlockOptions{})
}

type errorSink struct{ err error }

func (sink errorSink) PutBlock(context.Context, []byte, eris.TTL, eris.BlockOptions, ...eris.Reference) (eris.Reference, error) {
	return eris.Reference{}, sink.err
}

func TestCodecErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sentinel")
	if _, err := DecodeIndex(context.Background(), eris.Capability{}, make(eris.BlockSet), eris.BlockOptions{}); err == nil {
		t.Fatal("invalid index capability accepted")
	}
	index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink, Target: "target"}}}
	blocks := make(eris.BlockSet)
	indexCap, encodeErr := EncodeIndex(context.Background(), index, eris.Secret{}, blocks, eris.BlockOptions{})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	failingSource := &failAfterSource{BlockSource: blocks, failAt: 2, err: sentinel}
	if _, err := DecodeIndex(context.Background(), indexCap, failingSource, eris.BlockOptions{}); !errors.Is(err, sentinel) {
		t.Fatalf("DecodeIndex source error = %v", err)
	}
	if _, err := EncodeIndex(context.Background(), Index{Entries: []Entry{{Type: Symlink}}}, eris.Secret{}, make(eris.BlockSet), eris.BlockOptions{}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid index error = %v", err)
	}
	var builder *Builder
	if err := builder.AddFile(context.Background(), "a", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil builder AddFile error = %v", err)
	}
	if err := builder.AddSymlink(".", "x"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid link path error = %v", err)
	}
	if err := builder.AddSubdirectory(".", eris.Capability{}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid tree path error = %v", err)
	}
	if _, err := builder.Finish(context.Background()); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil builder Finish error = %v", err)
	}
}

func TestEncodeIndexNilSink(t *testing.T) {
	for _, test := range []struct {
		name  string
		index Index
		want  error
	}{
		{"valid index", Index{}, ErrInvalidEntry},
		{"invalid index", Index{Entries: []Entry{{Type: Symlink}}}, ErrInvalidPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			capability, err := EncodeIndex(context.Background(), test.index, eris.Secret{}, nil, eris.BlockOptions{})
			if !errors.Is(err, test.want) || capability != (eris.Capability{}) {
				t.Fatalf("EncodeIndex with nil sink = %v, %v; want zero capability, %v", capability, err, test.want)
			}
		})
	}
}

func TestFileSystemErrorsAndMetadata(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	_ = builder.AddFile(context.Background(), "dir/file", bytes.NewBufferString("data"), false)
	_ = builder.AddSymlink("link", "/outside")
	capability, _ := builder.Finish(context.Background())
	image, _ := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	for _, path := range []string{"missing", "dir/file/child", "../bad"} {
		if _, err := image.Open(context.Background(), path); err == nil {
			t.Errorf("Open(%q) succeeded", path)
		}
	}
	if _, err := image.Stat(context.Background(), "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Stat missing error = %v", err)
	}
	if _, err := image.ReadDir(context.Background(), "dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("ReadDir file error = %v", err)
	}
	if _, err := image.ReadLink(context.Background(), "dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("ReadLink file error = %v", err)
	}
	if _, err := image.Sub(context.Background(), "dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("Sub file error = %v", err)
	}
	if _, err := image.Sub(context.Background(), "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Sub missing error = %v", err)
	}
	entries, _ := image.ReadDir(context.Background(), ".")
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.Type() != info.Mode().Type() || entry.IsDir() != info.IsDir() || !info.ModTime().IsZero() || info.Sys() != nil {
			t.Errorf("bad metadata for %q", entry.Name())
		}
	}
	var nilFS *FS
	if _, err := nilFS.Open(context.Background(), "."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("nil FS error = %v", err)
	}
	if _, err := OpenFS(context.Background(), eris.Capability{}, blocks, eris.BlockOptions{}); err == nil {
		t.Fatal("OpenFS accepted invalid capability")
	}
	invalidIndexCap, _ := eris.EncodeContent(context.Background(), bytes.NewBufferString("not an index"), eris.Secret{}, eris.BlockSize1KiB, blocks, eris.BlockOptions{})
	if _, err := OpenFS(context.Background(), invalidIndexCap, blocks, eris.BlockOptions{}); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("OpenFS invalid index error = %v", err)
	}

	nestedBuilder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	_ = nestedBuilder.AddSubdirectory("bad", invalidIndexCap)
	nestedCap, _ := nestedBuilder.Finish(context.Background())
	nestedImage, _ := OpenFS(context.Background(), nestedCap, blocks, eris.BlockOptions{})
	for range 2 {
		if _, err := nestedImage.ReadDir(context.Background(), "bad"); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("bad nested index error = %v", err)
		}
	}
	if _, err := nestedImage.Open(context.Background(), "bad"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("open bad nested index error = %v", err)
	}
	if _, err := nestedImage.Open(context.Background(), "bad/child"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("walk bad nested index error = %v", err)
	}
	if _, err := nestedImage.Sub(context.Background(), "bad"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("sub bad nested index error = %v", err)
	}
	if _, err := nestedImage.ReadDir(context.Background(), "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadDir missing error = %v", err)
	}
	if _, err := nestedImage.ReadLink(context.Background(), "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadLink missing error = %v", err)
	}

	directoryNode := &node{kind: directoryType}
	if directoryNode.Size() != 0 || directoryNode.Mode()&stdfs.ModeDir == 0 {
		t.Fatal("directory metadata is invalid")
	}
	unknownNode := &node{kind: 99}
	if unknownNode.Mode() != 0o444 {
		t.Fatal("unknown node mode is invalid")
	}
	missingFileNode := &node{kind: Regular, capability: capability, source: make(eris.BlockSet)}
	if missingFileNode.Size() != -1 {
		t.Fatal("unreadable file size must be unknown")
	}
	missingContentCap, _, _ := eris.Encode([]byte("missing"), eris.Secret{}, eris.BlockSize1KiB)
	missingIndex := Index{Entries: []Entry{{Path: []string{"file"}, Type: Regular, Capability: missingContentCap}}}
	missingStore := make(eris.BlockSet)
	missingIndexCap := storeV1Index(t, missingIndex, missingStore)
	missingImage, err := OpenFS(context.Background(), missingIndexCap, missingStore, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missingImage.Stat(context.Background(), "file"); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("Stat missing content error = %v", err)
	}
	missingEntries, err := missingImage.ReadDir(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missingEntries[0].Info(); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("DirEntry.Info missing content error = %v", err)
	}
}

func TestBuilderConflictsAndReadError(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddFile(context.Background(), "file", errorReader{errors.New("read failure")}, false); err == nil {
		t.Fatal("AddFile ignored read error")
	}
	if err := builder.AddFile(context.Background(), ".", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid file path error = %v", err)
	}
	if err := builder.AddFile(context.Background(), "\xff", errorReader{errors.New("must not read")}, false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid UTF-8 path error = %v", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("invalid file path wrote %d blocks", len(blocks))
	}
	if err := builder.AddSymlink("file", "a"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(context.Background(), "file", errorReader{errors.New("must not read")}, false); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("preflight conflict error = %v", err)
	}
	if err := builder.AddSymlink("file", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("duplicate error = %v", err)
	}
	if err := builder.AddSymlink("file/child", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("prefix error = %v", err)
	}
	second := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := second.AddSymlink("dir/child", "a"); err != nil {
		t.Fatal(err)
	}
	if err := second.AddSymlink("dir", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("ancestor error = %v", err)
	}
	if err := second.AddSubdirectory("invalid", eris.Capability{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("invalid capability error = %v", err)
	}
	bad := NewBuilder(nil, eris.Secret{}, eris.BlockOptions{})
	if err := bad.AddSymlink("a", "b"); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil sink error = %v", err)
	}
	var nilBuilder *Builder
	if len(nilBuilder.Index().Entries) != 0 {
		t.Fatal("nil builder returned a non-empty index")
	}
}

func TestNestedIndexLoadRetriesSourceErrors(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	nested := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddSymlink("link", "target"); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	top := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err = top.AddSubdirectory("nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	topCap, err := top.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("temporary source failure")
	source := &flakySource{BlockSource: blocks, ref: nestedCap.Root.Reference, remaining: 1, err: sentinel}
	image, err := OpenFS(context.Background(), topCap, source, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = image.ReadDir(context.Background(), "nested"); !errors.Is(err, sentinel) {
		t.Fatalf("first ReadDir error = %v", err)
	}
	entries, err := image.ReadDir(context.Background(), "nested")
	if err != nil || names(entries) != "link" {
		t.Fatalf("retried ReadDir = (%v, %v)", names(entries), err)
	}
}

func TestBlockSetStorageMethods(t *testing.T) {
	t.Parallel()
	node := make([]byte, eris.BlockSize1KiB)
	block, pair, _ := eris.EncryptNode(node, eris.Secret{}, 0)
	blocks := make(eris.BlockSet)
	if _, err := blocks.PutBlock(context.Background(), block, 0, eris.BlockOptions{}, pair.Reference); err != nil {
		t.Fatal(err)
	}
	block[0] ^= 1
	got, err, _ := blocks.GetBlock(context.Background(), pair.Reference, 0, eris.BlockOptions{})
	if err != nil || got[0] == block[0] {
		t.Fatal("store did not copy input")
	}
	got[0] ^= 1
	again, _, _ := blocks.GetBlock(context.Background(), pair.Reference, 0, eris.BlockOptions{})
	if got[0] == again[0] {
		t.Fatal("store did not copy output")
	}
	if _, err, _ = blocks.GetBlock(context.Background(), eris.Reference{9}, 0, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err = (eris.BlockSet(nil)).PutBlock(context.Background(), again, 0, eris.BlockOptions{}, pair.Reference); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("nil put error = %v", err)
	}
	again[0] ^= 1
	if _, err = blocks.PutBlock(context.Background(), again, 0, eris.BlockOptions{}, pair.Reference); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("corrupt put error = %v", err)
	}
}

func deterministicBytes(length int) []byte {
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(i*31 + i/7)
	}
	return data
}

func names(entries []stdfs.DirEntry) string {
	values := make([]string, len(entries))
	for i, entry := range entries {
		values[i] = entry.Name()
	}
	return strings.Join(values, ",")
}

func TestLinksToImageRoot(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	for name, target := range map[string]string{"root": ".", "dir/up": "..", "chain": "root"} {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root", "dir/up", "chain"} {
		info, err := image.Stat(context.Background(), name)
		if err != nil {
			t.Errorf("Stat(%q): %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("Stat(%q) is not a directory", name)
		}
		entries, err := image.ReadDir(context.Background(), name)
		if err != nil || len(entries) != 3 {
			t.Errorf("ReadDir(%q) = %d entries, %v", name, len(entries), err)
		}
	}
}

func TestLinkTargetsResolveComponentsInOrder(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	for name, content := range map[string]string{"value": "root", "real/value": "parent", "real/child/file": "child"} {
		if err := builder.AddFile(context.Background(), name, strings.NewReader(content), false); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"alias": "real/child", "via-parent": "alias/../value",
		"missing-parent": "missing/../value", "file-parent": "value/../value",
		"escape-return": "../real/../value", "file-slash": "value/",
	} {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := stdfs.ReadFile(NewIOFS(context.Background(), image), "via-parent")
	if err != nil || string(data) != "parent" {
		t.Errorf("link through directory link and parent = %q, %v", data, err)
	}
	for _, name := range []string{"missing-parent", "file-parent", "escape-return", "file-slash"} {
		if _, err := image.Stat(context.Background(), name); !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("Stat(%q) error = %v", name, err)
		}
	}
}

func TestLinkResolutionLimit(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	for i := 0; i < 41; i++ {
		target := "."
		if i < 40 {
			target = fmt.Sprintf("link%d", i+1)
		}
		if err := builder.AddSymlink(fmt.Sprintf("link%d", i), target); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := image.Stat(context.Background(), "link1"); err != nil || !info.IsDir() {
		t.Fatalf("40 links to root: %v", err)
	}
	if _, err := image.Stat(context.Background(), "link0"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("41 links error = %v", err)
	}
	if info, err := image.Lstat(context.Background(), "link0"); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat must not follow the final link: %v", err)
	}
}

func TestRegularFileStatAfterClose(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddFile(context.Background(), "file", strings.NewReader("content"), false); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := image.Open(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stat after Close = %v; want ErrClosed", err)
	}
}

// Benchmark malformed counts with enough trailing bytes to pass the initial
// length checks. Memory use must depend on parsed items, not declared counts.
func BenchmarkIndexInvalidCounts(b *testing.B) {
	for _, pathCount := range []bool{false, true} {
		b.Run(fmt.Sprintf("path=%t", pathCount), func(b *testing.B) {
			data := append([]byte(nil), []byte(MagicPrefix)...)
			data = append(data, 0x82, 0x01)
			if pathCount {
				data = append(data, 0xa1)
				data = cborwire.AppendHead(data, 4, 1<<16)
			} else {
				data = cborwire.AppendHead(data, 5, 1<<16)
			}
			data = append(data, make([]byte, 1<<16)...)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := ParseIndex(data); !errors.Is(err, ErrInvalidIndex) {
					b.Fatalf("ParseIndex = %v", err)
				}
			}
		})
	}
}

func TestStatSourceErrorHasPath(t *testing.T) {
	t.Parallel()
	capability, _, err := eris.Encode([]byte("missing"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	blocks := make(eris.BlockSet)
	index := Index{Entries: []Entry{{Path: []string{"file"}, Type: Regular, Capability: capability}}}
	root := storeV1Index(t, index, blocks)
	image, err := OpenFS(context.Background(), root, blocks, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for operation, stat := range map[string]func(context.Context, string) (stdfs.FileInfo, error){
		"stat": image.Stat, "lstat": image.Lstat,
	} {
		_, err := stat(context.Background(), "file")
		var pathErr *stdfs.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != operation || pathErr.Path != "file" || !errors.Is(err, eris.ErrMissingBlock) {
			t.Errorf("%s error = %v; want PathError with missing block", operation, err)
		}
	}
}
