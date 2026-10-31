package fs

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/eris-tools/eris"
	"github.com/asciimoth/eris-tools/eris/internal/cborwire"
)

func TestIndexCanonicalRoundTrip(t *testing.T) {
	t.Parallel()
	fileCap, _, err := eris.Encode([]byte("file"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	index := Index{Entries: []Entry{
		{Path: []string{"z"}, Type: Symlink, Target: "../outside"},
		{Path: []string{"bin", "tool"}, Type: Executable, Capability: fileCap},
		{Path: []string{"a"}, Type: Regular, Capability: fileCap},
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
	const expectedHex = "da657269748201a181616182026162"
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
		append(bytes.Clone(valid[:5]), 0x82, 0x02, 0xa0),
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
	malformedEntry := append(append([]byte(nil), indexTagHeader[:]...), 0x82, 0x01, 0xa1, 0x00)
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
			if _, err := decoder.entry(); err == nil {
				t.Fatalf("accepted %x", data)
			}
		})
	}
	for _, data := range [][]byte{validFile, validLink} {
		decoder := cborDecoder{data: data}
		if _, err := decoder.entry(); err != nil || decoder.offset != len(data) {
			t.Fatalf("rejected valid entry: %v", err)
		}
	}
}

func TestEncodeContentMatchesCoreEncoder(t *testing.T) {
	t.Parallel()
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		for _, length := range []int{0, 1, int(size), int(size) + 17, int(size)*17 + 3} {
			content := deterministicBytes(length)
			wantCap, wantBlocks, err := eris.Encode(content, eris.Secret{1, 2, 3}, size)
			if err != nil {
				t.Fatal(err)
			}
			blocks := make(eris.BlockSet)
			gotCap, err := EncodeContent(bytes.NewReader(content), eris.Secret{1, 2, 3}, size, blocks)
			if err != nil || gotCap != wantCap || !reflect.DeepEqual(blocks, wantBlocks) {
				t.Fatalf("size %d length %d mismatch: %v", size, length, err)
			}
		}
	}
}

func TestIncrementalEncoderMatchesMultiLevelCoreTree(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(int(eris.BlockSize1KiB)*257 + 3)
	wantCap, wantBlocks, err := eris.Encode(content, eris.Secret{9}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	blocks := make(eris.BlockSet)
	gotCap, err := EncodeContent(bytes.NewReader(content), eris.Secret{9}, eris.BlockSize1KiB, blocks)
	if err != nil || gotCap != wantCap || !reflect.DeepEqual(blocks, wantBlocks) {
		t.Fatalf("multi-level encoding mismatch: %v", err)
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
	capability, err := EncodeIndex(index, eris.Secret{}, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeIndexWithLimit(capability, blocks, int64(len(data)-1)); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("small limit error = %v", err)
	}
	decoded, err := DecodeIndexWithLimit(capability, blocks, int64(len(data)))
	if err != nil || !reflect.DeepEqual(decoded, index) {
		t.Fatalf("exact limit decode = (%v, %v)", decoded, err)
	}
	if _, err = DecodeIndexWithLimit(capability, blocks, 0); !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("invalid limit error = %v", err)
	}
	if _, err = OpenFSWithLimit(capability, blocks, int64(len(data)-1)); !errors.Is(err, ErrIndexTooLarge) {
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
	nestedCap, err := EncodeIndex(nested, eris.Secret{}, blocks)
	if err != nil {
		t.Fatal(err)
	}
	top := Index{Entries: []Entry{{Path: []string{"nested"}, Type: Subdirectory, Capability: nestedCap}}}
	topData, _ := top.MarshalBinary()
	if len(topData) >= len(nestedData) {
		t.Fatal("test nested index is not larger than top index")
	}
	topCap, err := EncodeIndex(top, eris.Secret{}, blocks)
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFSWithLimit(topCap, blocks, int64(len(topData)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = image.ReadDir("nested"); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("nested limit error = %v", err)
	}
}

func TestReaderRandomAccess(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(int(eris.BlockSize1KiB)*40 + 123)
	blocks := make(eris.BlockSet)
	capability, err := EncodeContent(bytes.NewReader(content), eris.Secret{8}, eris.BlockSize1KiB, blocks)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Size() != int64(len(content)) {
		t.Fatalf("Size() = %d", reader.Size())
	}
	buffer := make([]byte, 3000)
	n, err := reader.ReadAt(buffer, 900)
	if err != nil || !bytes.Equal(buffer[:n], content[900:3900]) {
		t.Fatalf("ReadAt = (%d, %v)", n, err)
	}
	n, err = reader.ReadAt(buffer, int64(len(content)-100))
	if n != 100 || !errors.Is(err, io.EOF) || !bytes.Equal(buffer[:n], content[len(content)-100:]) {
		t.Fatalf("end ReadAt = (%d, %v)", n, err)
	}
	if position, err := reader.Seek(-10, io.SeekEnd); err != nil || position != int64(len(content)-10) {
		t.Fatalf("Seek = (%d, %v)", position, err)
	}
	buffer = make([]byte, 20)
	n, err = reader.Read(buffer)
	if n != 10 || !errors.Is(err, io.EOF) || !bytes.Equal(buffer[:n], content[len(content)-10:]) {
		t.Fatalf("Read = (%d, %v)", n, err)
	}
	if _, err = reader.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek accepted")
	}
	if _, err = reader.Seek(0, 99); err == nil {
		t.Fatal("invalid seek origin accepted")
	}
	if position, seekErr := reader.Seek(0, io.SeekCurrent); seekErr != nil || position != int64(len(content)) {
		t.Fatalf("current Seek = (%d, %v)", position, seekErr)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if _, err = reader.Read(buffer); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Read error = %v", err)
	}
	if _, err = reader.ReadAt(buffer, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed ReadAt error = %v", err)
	}
	if _, err = reader.Seek(0, io.SeekStart); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Seek error = %v", err)
	}
}

func TestBuilderAndFileSystem(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	secret := eris.Secret{4, 5, 6}
	nested := NewBuilder(blocks, secret)
	if err := nested.AddFile("inside", bytes.NewBufferString("nested data"), false); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish()
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(blocks, secret)
	if err = builder.AddFile("docs/readme", bytes.NewBufferString("hello"), false); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddFile("bin/run", bytes.NewBufferString("execute"), true); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddSymlink("latest", "docs/readme"); err != nil {
		t.Fatal(err)
	}
	if err = builder.AddSubdirectory("vendor", nestedCap); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	var highLevel FileSystem = image
	data, err := stdfs.ReadFile(image, "docs/readme")
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile = (%q, %v)", data, err)
	}
	openedFile, err := image.Open("docs/readme")
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := openedFile.Stat(); statErr != nil || info.Name() != "readme" {
		t.Fatalf("opened file Stat = (%v, %v)", info, statErr)
	}
	_ = openedFile.Close()
	randomAccessFile, err := highLevel.OpenFile("docs/readme")
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
	if _, err = highLevel.OpenFile("docs"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("OpenFile directory error = %v", err)
	}
	if _, err = highLevel.OpenFile("missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("OpenFile missing error = %v", err)
	}
	data, err = stdfs.ReadFile(image, "vendor/inside")
	if err != nil || string(data) != "nested data" {
		t.Fatalf("nested ReadFile = (%q, %v)", data, err)
	}
	target, err := image.ReadLink("latest")
	if err != nil || target != "docs/readme" {
		t.Fatalf("ReadLink = (%q, %v)", target, err)
	}
	info, err := image.Lstat("latest")
	if err != nil || info.Mode()&stdfs.ModeSymlink == 0 || info.Size() != int64(len(target)) {
		t.Fatalf("Lstat link = (%v, %v)", info, err)
	}
	info, err = image.Stat("bin/run")
	if err != nil || info.Mode()&0o111 == 0 || info.Size() != 7 {
		t.Fatalf("Stat executable = (%v, %v)", info, err)
	}
	entries, err := image.ReadDir(".")
	if err != nil || names(entries) != "bin,docs,latest,vendor" {
		t.Fatalf("ReadDir = (%v, %v)", names(entries), err)
	}
	sub, err := image.Sub("docs")
	if err != nil {
		t.Fatal(err)
	}
	data, err = stdfs.ReadFile(sub, "readme")
	if err != nil || string(data) != "hello" {
		t.Fatalf("sub ReadFile = (%q, %v)", data, err)
	}
	data, err = stdfs.ReadFile(image, "latest")
	if err != nil || string(data) != "hello" {
		t.Fatalf("ReadFile through link = (%q, %v)", data, err)
	}
}

func TestBuilderBlockSizeBoundary(t *testing.T) {
	t.Parallel()
	for _, length := range []int{(1 << 14) - 1, 1 << 14, (1 << 14) + 1} {
		content := deterministicBytes(length)
		blocks := make(eris.BlockSet)
		builder := NewBuilder(blocks, eris.Secret{})
		if err := builder.AddFile("file", bytes.NewReader(content), false); err != nil {
			t.Fatal(err)
		}
		entry := builder.Index().Entries[0]
		if entry.Capability.BlockSize != eris.ChooseBlockSize(length) {
			t.Errorf("length %d block size = %d", length, entry.Capability.BlockSize)
		}
		reader, err := NewReader(entry.Capability, blocks)
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
	builder := NewBuilder(blocks, eris.Secret{7})
	if err := builder.AddFile("a/file", bytes.NewBufferString("content"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile("tool", bytes.NewBufferString("run"), true); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("a/link", "../tool"); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if err = fstest.TestFS(image, "a", "a/file", "a/link", "tool"); err != nil {
		t.Fatal(err)
	}
}

func TestFileSystemLinkResolutionStaysVirtual(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
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
	if err := builder.AddFile("directory/file", bytes.NewBufferString("virtual"), false); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	data, err := stdfs.ReadFile(image, "directory-link/file")
	if err != nil || string(data) != "virtual" {
		t.Fatalf("internal directory link = (%q, %v)", data, err)
	}
	for _, name := range []string{"absolute", "directory/escape"} {
		if _, err = image.Open(name); !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("Open(%q) error = %v", name, err)
		}
	}
	if _, err = image.Open("loop-a"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("link loop error = %v", err)
	}
}

func TestDirectoryFile(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	_ = builder.AddSymlink("b", "x")
	_ = builder.AddSymlink("a", "x")
	capability, _ := builder.Finish()
	image, _ := OpenFS(capability, blocks)
	opened, err := image.Open(".")
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

func (source *failAfterSource) GetBlock(ref eris.Reference) ([]byte, error) {
	source.calls++
	if source.calls == source.failAt {
		return nil, source.err
	}
	return source.BlockSource.GetBlock(ref)
}

func (source *flakySource) GetBlock(ref eris.Reference) ([]byte, error) {
	if ref == source.ref && source.remaining > 0 {
		source.remaining--
		return nil, source.err
	}
	return source.BlockSource.GetBlock(ref)
}

type errorSink struct{ err error }

type malformedSource []byte

func (source malformedSource) GetBlock(eris.Reference) ([]byte, error) {
	return source, nil
}

func (sink errorSink) PutBlock([]byte, ...eris.Reference) (eris.Reference, error) {
	return eris.Reference{}, sink.err
}

type countingSink struct {
	calls  int
	failAt int
	err    error
}

func (sink *countingSink) PutBlock(block []byte, _ ...eris.Reference) (eris.Reference, error) {
	sink.calls++
	if sink.calls == sink.failAt {
		return eris.Reference{}, sink.err
	}
	return eris.ReferenceOf(block)
}

func TestCodecErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("sentinel")
	if _, err := EncodeContent(nil, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet)); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(nil), eris.Secret{}, 1, make(eris.BlockSet)); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("invalid size error = %v", err)
	}
	if _, err := EncodeContent(errorReader{sentinel}, eris.Secret{}, eris.BlockSize1KiB, make(eris.BlockSet)); !errors.Is(err, sentinel) {
		t.Fatalf("reader error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(nil), eris.Secret{}, eris.BlockSize1KiB, errorSink{sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("sink error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(make([]byte, eris.BlockSize1KiB)), eris.Secret{}, eris.BlockSize1KiB, &countingSink{failAt: 2, err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("second leaf sink error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(make([]byte, eris.BlockSize1KiB*2)), eris.Secret{}, eris.BlockSize1KiB, &countingSink{failAt: 4, err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("parent sink error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(make([]byte, eris.BlockSize1KiB*16)), eris.Secret{}, eris.BlockSize1KiB, &countingSink{failAt: 17, err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("full parent sink error = %v", err)
	}
	if _, err := EncodeContent(bytes.NewReader(make([]byte, eris.BlockSize1KiB*15)), eris.Secret{}, eris.BlockSize1KiB, &countingSink{failAt: 17, err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("final leaf parent sink error = %v", err)
	}
	if _, err := putNode(nil, eris.Secret{}, 0, make(eris.BlockSet)); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("invalid node error = %v", err)
	}
	encoder := treeEncoder{blockSize: eris.BlockSize1KiB, sink: make(eris.BlockSet)}
	if err := encoder.add(256, eris.Pair{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("excessive encoder level error = %v", err)
	}
	if _, err := encoder.parent(255); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("excessive parent level error = %v", err)
	}
	if _, err := NewReader(eris.Capability{}, make(eris.BlockSet)); !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatalf("invalid capability error = %v", err)
	}
	if _, err := DecodeIndex(eris.Capability{}, make(eris.BlockSet)); err == nil {
		t.Fatal("invalid index capability accepted")
	}
	index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink, Target: "target"}}}
	blocks := make(eris.BlockSet)
	indexCap, encodeErr := EncodeIndex(index, eris.Secret{}, blocks)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	failingSource := &failAfterSource{BlockSource: blocks, failAt: 2, err: sentinel}
	if _, err := DecodeIndex(indexCap, failingSource); !errors.Is(err, sentinel) {
		t.Fatalf("DecodeIndex source error = %v", err)
	}
	if _, err := EncodeIndex(Index{Entries: []Entry{{Type: Symlink}}}, eris.Secret{}, make(eris.BlockSet)); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid index error = %v", err)
	}
	var builder *Builder
	if err := builder.AddFile("a", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil builder AddFile error = %v", err)
	}
	if err := builder.AddSymlink(".", "x"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid link path error = %v", err)
	}
	if err := builder.AddSubdirectory(".", eris.Capability{}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid tree path error = %v", err)
	}
	if _, err := builder.Finish(); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("nil builder Finish error = %v", err)
	}
}

func TestFileSystemErrorsAndMetadata(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	_ = builder.AddFile("dir/file", bytes.NewBufferString("data"), false)
	_ = builder.AddSymlink("link", "/outside")
	capability, _ := builder.Finish()
	image, _ := OpenFS(capability, blocks)
	for _, path := range []string{"missing", "dir/file/child", "../bad"} {
		if _, err := image.Open(path); err == nil {
			t.Errorf("Open(%q) succeeded", path)
		}
	}
	if _, err := image.Stat("missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Stat missing error = %v", err)
	}
	if _, err := image.ReadDir("dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("ReadDir file error = %v", err)
	}
	if _, err := image.ReadLink("dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("ReadLink file error = %v", err)
	}
	if _, err := image.Sub("dir/file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("Sub file error = %v", err)
	}
	if _, err := image.Sub("missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Sub missing error = %v", err)
	}
	entries, _ := image.ReadDir(".")
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || entry.Type() != info.Mode().Type() || entry.IsDir() != info.IsDir() || !info.ModTime().IsZero() || info.Sys() != nil {
			t.Errorf("bad metadata for %q", entry.Name())
		}
	}
	var nilFS *FS
	if _, err := nilFS.Open("."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("nil FS error = %v", err)
	}
	if _, err := OpenFS(eris.Capability{}, blocks); err == nil {
		t.Fatal("OpenFS accepted invalid capability")
	}
	invalidIndexCap, _ := EncodeContent(bytes.NewBufferString("not an index"), eris.Secret{}, eris.BlockSize1KiB, blocks)
	if _, err := OpenFS(invalidIndexCap, blocks); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("OpenFS invalid index error = %v", err)
	}

	nestedBuilder := NewBuilder(blocks, eris.Secret{})
	_ = nestedBuilder.AddSubdirectory("bad", invalidIndexCap)
	nestedCap, _ := nestedBuilder.Finish()
	nestedImage, _ := OpenFS(nestedCap, blocks)
	for range 2 {
		if _, err := nestedImage.ReadDir("bad"); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("bad nested index error = %v", err)
		}
	}
	if _, err := nestedImage.Open("bad"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("open bad nested index error = %v", err)
	}
	if _, err := nestedImage.Open("bad/child"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("walk bad nested index error = %v", err)
	}
	if _, err := nestedImage.Sub("bad"); !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("sub bad nested index error = %v", err)
	}
	if _, err := nestedImage.ReadDir("missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadDir missing error = %v", err)
	}
	if _, err := nestedImage.ReadLink("missing"); !errors.Is(err, stdfs.ErrNotExist) {
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
	missingIndexCap, _ := EncodeIndex(missingIndex, eris.Secret{}, missingStore)
	missingImage, err := OpenFS(missingIndexCap, missingStore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missingImage.Stat("file"); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("Stat missing content error = %v", err)
	}
	missingEntries, err := missingImage.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missingEntries[0].Info(); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("DirEntry.Info missing content error = %v", err)
	}
}

func TestReaderRejectsBadBlocks(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(3000)
	capability, blocks, _ := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	missing := cloneBlockSet(blocks)
	delete(missing, capability.Root.Reference)
	if _, err := NewReader(capability, missing); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing root error = %v", err)
	}
	wrongSize := cloneBlockSet(blocks)
	wrongSize[capability.Root.Reference] = []byte{1}
	if _, err := NewReader(capability, wrongSize); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("short root error = %v", err)
	}
	if _, err := NewReader(capability, malformedSource(make([]byte, eris.BlockSize32KiB))); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("wrong size from source error = %v", err)
	}
	corrupt := cloneBlockSet(blocks)
	corrupt[capability.Root.Reference][0] ^= 1
	if _, err := NewReader(capability, corrupt); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("corrupt root error = %v", err)
	}
	leafCap, leafBlocks, _ := eris.Encode([]byte("x"), eris.Secret{}, eris.BlockSize1KiB)
	leafBlocks[leafCap.Root.Reference] = make([]byte, eris.BlockSize1KiB)
	if _, err := NewReader(leafCap, leafBlocks); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("corrupt leaf error = %v", err)
	}
	reader, _ := NewReader(capability, blocks)
	if _, err := reader.ReadAt(nil, -1); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("negative ReadAt error = %v", err)
	}
	if n, err := reader.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt = (%d, %v)", n, err)
	}
	if n, err := reader.ReadAt(make([]byte, 1), int64(len(content))); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("end ReadAt = (%d, %v)", n, err)
	}
	if _, err := reader.leafPair(-1); !errors.Is(err, io.EOF) {
		t.Fatalf("negative leaf error = %v", err)
	}
	firstPair, _ := reader.leafPair(0)
	delete(blocks, firstPair.Reference)
	if _, err := reader.ReadAt(make([]byte, 1), 0); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing leaf read error = %v", err)
	}
}

func TestReaderRejectsMalformedTreesAndPadding(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	badLeaf := make([]byte, eris.BlockSize1KiB)
	badLeafBlock, badLeafPair, _ := eris.EncryptNode(badLeaf, eris.Secret{}, 0)
	blocks[badLeafPair.Reference] = badLeafBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Root: badLeafPair}, blocks); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("bad padding error = %v", err)
	}

	validLeaf := make([]byte, eris.BlockSize1KiB)
	validLeaf[0] = 0x80
	leafBlock, leafPair, _ := eris.EncryptNode(validLeaf, eris.Secret{}, 0)
	blocks[leafPair.Reference] = leafBlock
	oneChildNode := makeNode(leafPair)
	oneBlock, onePair, _ := eris.EncryptNode(oneChildNode, eris.Secret{}, 1)
	blocks[onePair.Reference] = oneBlock
	oneCap := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: onePair}
	if _, err := NewReader(oneCap, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("one-child root error = %v", err)
	}
	levelTwoNode := makeNode(onePair, onePair)
	levelTwoBlock, levelTwoPair, _ := eris.EncryptNode(levelTwoNode, eris.Secret{}, 2)
	blocks[levelTwoPair.Reference] = levelTwoBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: levelTwoPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("short non-final subtree error = %v", err)
	}
	fullChildren := make([]eris.Pair, 16)
	for i := range fullChildren {
		fullChildren[i] = leafPair
	}
	fullNode := makeNode(fullChildren...)
	fullBlock, fullPair, _ := eris.EncryptNode(fullNode, eris.Secret{}, 1)
	blocks[fullPair.Reference] = fullBlock
	repeatedNode := makeNode(fullPair, fullPair)
	repeatedBlock, repeatedPair, _ := eris.EncryptNode(repeatedNode, eris.Secret{}, 2)
	blocks[repeatedPair.Reference] = repeatedBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: repeatedPair}, blocks); err != nil {
		t.Fatalf("repeated valid subtree error = %v", err)
	}
	missingInternal := eris.Pair{Reference: eris.Reference{9}, Key: eris.Key{8}}
	missingNode := makeNode(missingInternal, onePair)
	missingBlock, missingPair, _ := eris.EncryptNode(missingNode, eris.Secret{}, 2)
	blocks[missingPair.Reference] = missingBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: missingPair}, blocks); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing internal error = %v", err)
	}

	emptyNode := make([]byte, eris.BlockSize1KiB)
	emptyBlock, emptyPair, _ := eris.EncryptNode(emptyNode, eris.Secret{}, 1)
	blocks[emptyPair.Reference] = emptyBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: emptyPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("empty root error = %v", err)
	}

	twoChildren := makeNode(leafPair, leafPair)
	rootBlock, rootPair, _ := eris.EncryptNode(twoChildren, eris.Secret{}, 1)
	blocks[rootPair.Reference] = rootBlock
	badKeyCap := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: rootPair}
	badKeyCap.Root.Key[0] ^= 1
	if _, err := NewReader(badKeyCap, blocks); !errors.Is(err, eris.ErrInvalidRootKey) {
		t.Fatalf("bad root key error = %v", err)
	}

	brokenSlots := makeNode(leafPair)
	copy(brokenSlots[2*eris.PairSize:], brokenSlots[:eris.PairSize])
	brokenBlock, brokenPair, _ := eris.EncryptNode(brokenSlots, eris.Secret{}, 1)
	blocks[brokenPair.Reference] = brokenBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: brokenPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("non-contiguous pairs error = %v", err)
	}

	if _, err := eris.ParseInternalNode([]byte{1}); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("short pairs error = %v", err)
	}
	if _, err := paddedLength([]byte{1}); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("bad padded byte error = %v", err)
	}
	if _, err := paddedLength(nil); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("empty padded data error = %v", err)
	}
	if subtreeCapacity(eris.BlockSize32KiB, 100) != int64(^uint64(0)>>1) {
		t.Fatal("capacity did not saturate")
	}
}

func TestBuilderConflictsAndReadError(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile("file", errorReader{errors.New("read failure")}, false); err == nil {
		t.Fatal("AddFile ignored read error")
	}
	if err := builder.AddFile(".", bytes.NewReader(nil), false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid file path error = %v", err)
	}
	if err := builder.AddFile("\xff", errorReader{errors.New("must not read")}, false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid UTF-8 path error = %v", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("invalid file path wrote %d blocks", len(blocks))
	}
	if err := builder.AddSymlink("file", "a"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile("file", errorReader{errors.New("must not read")}, false); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("preflight conflict error = %v", err)
	}
	if err := builder.AddSymlink("file", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("duplicate error = %v", err)
	}
	if err := builder.AddSymlink("file/child", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("prefix error = %v", err)
	}
	second := NewBuilder(blocks, eris.Secret{})
	if err := second.AddSymlink("dir/child", "a"); err != nil {
		t.Fatal(err)
	}
	if err := second.AddSymlink("dir", "b"); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("ancestor error = %v", err)
	}
	if err := second.AddSubdirectory("invalid", eris.Capability{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("invalid capability error = %v", err)
	}
	bad := NewBuilder(nil, eris.Secret{})
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
	nested := NewBuilder(blocks, eris.Secret{})
	if err := nested.AddSymlink("link", "target"); err != nil {
		t.Fatal(err)
	}
	nestedCap, err := nested.Finish()
	if err != nil {
		t.Fatal(err)
	}
	top := NewBuilder(blocks, eris.Secret{})
	if err = top.AddSubdirectory("nested", nestedCap); err != nil {
		t.Fatal(err)
	}
	topCap, err := top.Finish()
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("temporary source failure")
	source := &flakySource{BlockSource: blocks, ref: nestedCap.Root.Reference, remaining: 1, err: sentinel}
	image, err := OpenFS(topCap, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = image.ReadDir("nested"); !errors.Is(err, sentinel) {
		t.Fatalf("first ReadDir error = %v", err)
	}
	entries, err := image.ReadDir("nested")
	if err != nil || names(entries) != "link" {
		t.Fatalf("retried ReadDir = (%v, %v)", names(entries), err)
	}
}

func TestBlockSetStorageMethods(t *testing.T) {
	t.Parallel()
	node := make([]byte, eris.BlockSize1KiB)
	block, pair, _ := eris.EncryptNode(node, eris.Secret{}, 0)
	blocks := make(eris.BlockSet)
	if _, err := blocks.PutBlock(block, pair.Reference); err != nil {
		t.Fatal(err)
	}
	block[0] ^= 1
	got, err := blocks.GetBlock(pair.Reference)
	if err != nil || got[0] == block[0] {
		t.Fatal("store did not copy input")
	}
	got[0] ^= 1
	again, _ := blocks.GetBlock(pair.Reference)
	if got[0] == again[0] {
		t.Fatal("store did not copy output")
	}
	if _, err = blocks.GetBlock(eris.Reference{9}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err = (eris.BlockSet(nil)).PutBlock(again, pair.Reference); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("nil put error = %v", err)
	}
	again[0] ^= 1
	if _, err = blocks.PutBlock(again, pair.Reference); !errors.Is(err, eris.ErrInvalidBlock) {
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

func cloneBlockSet(blocks eris.BlockSet) eris.BlockSet {
	clone := make(eris.BlockSet, len(blocks))
	for ref, block := range blocks {
		clone[ref] = bytes.Clone(block)
	}
	return clone
}

func makeNode(pairs ...eris.Pair) []byte {
	node := make([]byte, eris.BlockSize1KiB)
	for i, pair := range pairs {
		offset := i * eris.PairSize
		copy(node[offset:offset+32], pair.Reference[:])
		copy(node[offset+32:offset+eris.PairSize], pair.Key[:])
	}
	return node
}
