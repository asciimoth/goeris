package fs

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

// storeV1Index builds the old wire format without using the production writer.
// These fixtures have one entry, so key order does not affect their encoding.
func storeV1Index(t testing.TB, index Index, blocks eris.BlockSet) eris.Capability {
	t.Helper()
	data := append(bytes.Clone([]byte(MagicPrefix)), 0x82, 1)
	data = cborwire.AppendHead(data, 5, uint64(len(index.Entries)))
	for _, entry := range index.Entries {
		data = cborwire.AppendHead(data, 4, uint64(len(entry.Path)))
		for _, part := range entry.Path {
			data = appendText(data, part)
		}
		data = append(data, 0x82, byte(entry.Type))
		if entry.Type == Symlink {
			data = appendText(data, entry.Target)
		} else {
			cap, err := entry.Capability.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			data = cborwire.AppendHead(data, 6, eris.CapabilityCBORTag)
			data = appendBytes(data, cap)
		}
	}
	cap, err := EncodeContent(bytes.NewReader(data), eris.Secret{}, eris.BlockSize1KiB, blocks)
	if err != nil {
		t.Fatal(err)
	}
	return cap
}

func TestV2WireVectors(t *testing.T) {
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
	capHex := "0a00" + strings.Repeat("00", 64)
	for _, test := range []struct {
		name  string
		entry Entry
		wire  string
	}{
		{"file", Entry{Path: []string{"f"}, Capability: capability, Size: 24}, "da657269748202a18161668300d901145842" + capHex + "1818"},
		{"executable", Entry{Path: []string{"f"}, Type: Executable, Capability: capability, Size: 65536}, "da657269748202a18161668301d901145842" + capHex + "1a00010000"},
		{"lzip", Entry{Path: []string{"f"}, Capability: capability, Size: 256, Compression: Lzip}, "da657269748202a18161668300d901145842" + capHex + "da4c5a4950190100"},
		{"directory", Entry{Path: []string{"f"}, Type: Subdirectory, Capability: capability}, "da657269748202a18161668203d901145842" + capHex},
		{"absolute link", Entry{Path: []string{"f"}, Type: Symlink, Target: "/a/../b"}, "da657269748202a1816166820284606161622e2e6162"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want, err := hex.DecodeString(test.wire)
			if err != nil {
				t.Fatal(err)
			}
			index := Index{Entries: []Entry{test.entry}}
			got, err := index.MarshalBinary()
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("wire = %x, %v; want %x", got, err, want)
			}
			parsed, err := ParseIndex(want)
			if err != nil || !reflect.DeepEqual(parsed, index) {
				t.Fatalf("parse = %#v, %v", parsed, err)
			}
			for i := range want {
				original := index
				if err := original.UnmarshalBinary(want[:i]); !errors.Is(err, ErrInvalidIndex) || !reflect.DeepEqual(original, index) {
					t.Fatalf("truncation %d: %#v, %v", i, original, err)
				}
			}
		})
	}
}

func TestV2LengthValidation(t *testing.T) {
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
	base := Entry{Path: []string{"f"}, Capability: capability}
	for _, size := range []int64{0, 23, 24, 255, 256, 65535, 65536, 1 << 32, math.MaxInt64} {
		for _, compression := range []Compression{Uncompressed, Lzip} {
			entry := base
			entry.Size, entry.Compression = size, compression
			data, err := (Index{Entries: []Entry{entry}}).MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseIndex(data)
			if err != nil || !reflect.DeepEqual(parsed.Entries[0], entry) {
				t.Fatalf("size %d: %v, %v", size, parsed, err)
			}
		}
	}
	for _, entry := range []Entry{
		{Path: []string{"f"}, Capability: capability, Size: -2},
		{Path: []string{"f"}, Capability: capability, Size: -1, Compression: Lzip},
		{Path: []string{"f"}, Capability: capability, Compression: 2},
		{Path: []string{"f"}, Type: Subdirectory, Capability: capability, Size: 1},
		{Path: []string{"f"}, Type: Symlink, Size: 1},
		{Path: []string{"f"}, Type: Symlink, Compression: Lzip},
	} {
		if _, err := (Index{Entries: []Entry{entry}}).MarshalBinary(); !errors.Is(err, ErrInvalidEntry) {
			t.Fatalf("accepted %#v: %v", entry, err)
		}
	}
	base.Size = -1
	if _, err := (Index{Entries: []Entry{base}}).MarshalBinary(); !errors.Is(err, ErrUnknownSize) {
		t.Fatal(err)
	}
	base.Size = 0
	data, _ := (Index{Entries: []Entry{base}}).MarshalBinary()
	prefix := data[:len(data)-1]
	for _, suffix := range [][]byte{
		{0x20}, {0x3b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x1b, 0x80, 0, 0, 0, 0, 0, 0, 0}, {0x18, 0},
		{0xd8, 1, 0}, {0xda, 0x4c, 0x5a, 0x49, 0x51, 0}, {0xda, 0x4c},
		{0xda, 0x4c, 0x5a, 0x49, 0x50, 0x20},
		{0xda, 0x4c, 0x5a, 0x49, 0x50, 0xda, 0x4c, 0x5a, 0x49, 0x50, 0},
	} {
		if _, err := ParseIndex(append(bytes.Clone(prefix), suffix...)); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("accepted length %x: %v", suffix, err)
		}
	}
}

func TestVersionAndMapOrder(t *testing.T) {
	// Core deterministic order compares encoded bytes. A one-component path
	// sorts before a two-component path, even if its encoded key is longer.
	index := Index{Entries: []Entry{
		{Path: []string{"a", "b"}, Type: Symlink, Target: "x"},
		{Path: []string{"long-name"}, Type: Symlink, Target: "x"},
	}}
	data, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseIndex(data)
	if err != nil || parsed.Entries[0].Path[0] != "long-name" {
		t.Fatalf("order = %v: %v", parsed, err)
	}
	decoder := cborDecoder{data: data, offset: 8}
	_, _, err = decoder.entry(2)
	if err != nil {
		t.Fatal(err)
	}
	boundary := decoder.offset
	reversed := append(bytes.Clone(data[:8]), data[boundary:]...)
	reversed = append(reversed, data[8:boundary]...)
	if _, err := ParseIndex(reversed); !errors.Is(err, ErrInvalidIndex) {
		t.Fatal("accepted v2 length-first order")
	}
	reversed[6] = 1
	if _, err := ParseIndex(reversed); err != nil {
		t.Fatalf("v1 length-first order: %v", err)
	}
	data[6] = 1
	if _, err := ParseIndex(data); err != nil {
		t.Fatalf("v1 core order: %v", err)
	}
	for _, version := range []byte{0, 3, 23} {
		data[6] = version
		if _, err := ParseIndex(data); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("accepted version %d", version)
		}
	}
	// Both historical and specified symlink forms remain readable in v1.
	for _, wire := range []string{"da657269748201a181616182026162", "da657269748201a18161618202816162"} {
		data, _ := hex.DecodeString(wire)
		parsed, err := ParseIndex(data)
		if err != nil || parsed.Entries[0].Target != "b" {
			t.Fatalf("v1 link: %v, %v", parsed, err)
		}
		encoded, err := parsed.MarshalBinary()
		if err != nil || encoded[6] != 2 {
			t.Fatalf("rewrite: %x, %v", encoded, err)
		}
	}
	for _, target := range [][]byte{{0x61, 'b'}, {0x81, 0x61, 0xff}, {0x81, 0x61, '/'}, {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		data := append([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 2, 0xa1, 0x81, 0x61, 'a', 0x82, 2}, target...)
		if _, err := ParseIndex(data); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("accepted target %x", target)
		}
	}
	for _, target := range [][]byte{{0x80}, {0x81, 0x60}} {
		data := append([]byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 2, 0xa1, 0x81, 0x61, 'a', 0x82, 2}, target...)
		parsed, err := ParseIndex(data)
		if err != nil || parsed.Entries[0].Target != "" {
			t.Fatalf("empty target: %v", err)
		}
	}
}

func TestV1FilesAndMixedNestedImages(t *testing.T) {
	for _, kind := range []EntryType{Regular, Executable} {
		blocks := make(eris.BlockSet)
		content := bytes.Repeat([]byte("old file\n"), 3000)
		capability, err := EncodeContent(bytes.NewReader(content), eris.Secret{}, eris.BlockSize1KiB, blocks)
		if err != nil {
			t.Fatal(err)
		}
		old := storeV1Index(t, Index{Entries: []Entry{{Path: []string{"file"}, Type: kind, Capability: capability}}}, blocks)
		parsed, err := DecodeIndex(old, blocks)
		if err != nil || parsed.Entries[0].Size != -1 {
			t.Fatalf("v1 size: %v, %v", parsed, err)
		}
		if _, err := EncodeIndex(parsed, eris.Secret{}, blocks); !errors.Is(err, ErrUnknownSize) {
			t.Fatal(err)
		}
		image, err := OpenFS(old, blocks)
		if err != nil {
			t.Fatal(err)
		}
		info, err := image.Stat("file")
		if err != nil || info.Size() != int64(len(content)) {
			t.Fatalf("stat: %v, %v", info, err)
		}
		for range 2 {
			file, err := image.OpenFile("file")
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("v1 read: %v", err)
			}
			_ = file.Close()
		}
		parsed.Entries[0].Size = int64(len(content))
		modern, err := EncodeIndex(parsed, eris.Secret{}, blocks)
		if err != nil {
			t.Fatal(err)
		}
		// A v1 parent can contain v2, and a v2 parent can contain v1.
		oldParent := storeV1Index(t, Index{Entries: []Entry{{Path: []string{"new"}, Type: Subdirectory, Capability: modern}}}, blocks)
		builder := NewBuilder(blocks, eris.Secret{})
		if err := builder.AddSubdirectory("old", oldParent); err != nil {
			t.Fatal(err)
		}
		root, err := builder.Finish()
		if err != nil {
			t.Fatal(err)
		}
		mixed, err := OpenFS(root, blocks)
		if err != nil {
			t.Fatal(err)
		}
		got, err := stdfs.ReadFile(mixed, "old/new/file")
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("nested read: %v", err)
		}
	}
}

func TestV2MetadataWithoutContent(t *testing.T) {
	blocks := make(eris.BlockSet)
	index := Index{Entries: []Entry{{Path: []string{"file"}, Capability: eris.Capability{BlockSize: eris.BlockSize1KiB}, Size: 123}}}
	root, err := EncodeIndex(index, eris.Secret{}, blocks)
	if err != nil {
		t.Fatal(err)
	}
	source := &failAfterSource{BlockSource: blocks}
	image, err := OpenFS(root, source)
	if err != nil {
		t.Fatal(err)
	}
	source.calls = 0
	for _, stat := range []func(string) (stdfs.FileInfo, error){image.Stat, image.Lstat} {
		info, err := stat("file")
		if err != nil || info.Size() != 123 {
			t.Fatalf("metadata: %v, %v", info, err)
		}
	}
	entries, err := image.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := entries[0].Info(); err != nil || info.Size() != 123 {
		t.Fatalf("dir metadata: %v, %v", info, err)
	}
	file, err := image.OpenFile("file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if source.calls != 0 {
		t.Fatalf("metadata/open fetched %d blocks", source.calls)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
}

func TestV2AdditionalErrorPaths(t *testing.T) {
	sentinel := errors.New("storage failed")
	if _, err := EncodeContent(bytes.NewReader(make([]byte, 1024)), eris.Secret{}, eris.BlockSize1KiB, errorSink{sentinel}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	builder := NewBuilder(errorSink{sentinel}, eris.Secret{})
	if err := builder.AddFile("file", strings.NewReader("data"), false); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if len(builder.Index().Entries) != 0 {
		t.Fatal("failed write added a file")
	}
	// Completing the last partial subtree can fill and flush its parent.
	if _, err := EncodeContent(bytes.NewReader(make([]byte, 240*1024)), eris.Secret{}, eris.BlockSize1KiB, &countingSink{failAt: 258, err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("final parent: %v", err)
	}
	for _, wire := range []string{
		"da657269748202a18161668200", // v2 regular file with only two fields
		"da657269748202a18161668302", // symlink with three fields
	} {
		data, _ := hex.DecodeString(wire)
		if _, err := ParseIndex(data); !errors.Is(err, ErrInvalidIndex) {
			t.Fatal(err)
		}
	}
	blocks := make(eris.BlockSet)
	cap, _, _ := eris.Encode([]byte("missing"), eris.Secret{}, eris.BlockSize1KiB)
	old := storeV1Index(t, Index{Entries: []Entry{{Path: []string{"file"}, Capability: cap}}}, blocks)
	image, err := OpenFS(old, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := image.Open("file"); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
	if _, err := image.Lstat("absent"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := image.Sub("file"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestMixedSubdirectoryReuse(t *testing.T) {
	blocks := make(eris.BlockSet)
	nested := NewBuilder(blocks, eris.Secret{})
	if err := nested.AddFile("dir/file", strings.NewReader("content"), false); err != nil {
		t.Fatal(err)
	}
	inner, err := nested.Finish()
	if err != nil {
		t.Fatal(err)
	}
	outer := NewBuilder(blocks, eris.Secret{})
	if err := outer.AddSubdirectory("nested", inner); err != nil {
		t.Fatal(err)
	}
	root, err := outer.Finish()
	if err != nil {
		t.Fatal(err)
	}
	source := &failAfterSource{BlockSource: blocks}
	image, err := OpenFS(root, source)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub("nested")
	if err != nil {
		t.Fatal(err)
	}
	before := source.calls
	for range 2 {
		if _, err := image.ReadDir("nested"); err != nil {
			t.Fatal(err)
		}
	}
	if source.calls != before {
		t.Fatal("nested index was fetched again")
	}
	deeper, err := stdfs.Sub(sub, "dir")
	if err != nil {
		t.Fatal(err)
	}
	content, err := stdfs.ReadFile(deeper, "file")
	if err != nil || string(content) != "content" {
		t.Fatalf("nested Sub: %q, %v", content, err)
	}
}
