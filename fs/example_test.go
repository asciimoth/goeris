package fs_test

import (
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

func ExampleBuilder() {
	blocks := make(eris.BlockSet)
	builder := erisfs.NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile("docs/readme", strings.NewReader("hello"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish() // Always writes an ERIS-FS v2 index.
	if err != nil {
		panic(err)
	}
	image, err := erisfs.OpenFS(capability, blocks) // Also accepts v1 images.
	if err != nil {
		panic(err)
	}
	file, err := image.OpenFile("docs/readme")
	if err != nil {
		panic(err)
	}
	defer func() { _ = file.Close() }()
	var tail [3]byte
	if _, err := file.ReadAt(tail[:], 2); err != nil {
		panic(err)
	}
	info, err := file.Stat()
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Size(), string(tail[:]))
	// Output: 5 llo
}

func ExampleFS_OpenFile() {
	blocks := make(eris.BlockSet)
	builder := erisfs.NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile("file", strings.NewReader("known length"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish()
	if err != nil {
		panic(err)
	}
	image, err := erisfs.OpenFS(capability, blocks)
	if err != nil {
		panic(err)
	}
	// The v2 index supplies the length. Opening needs no file content reads.
	reader, err := image.OpenFile("file")
	if err != nil {
		panic(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := reader.Seek(-6, io.SeekEnd); err != nil {
		panic(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(data))
	// Output: length
}

func ExampleBuilder_AddAutoFile() {
	blocks := make(eris.BlockSet)
	builder := erisfs.NewBuilder(blocks, eris.Secret{})
	content := strings.Repeat("hello\n", 1024)
	if err := builder.AddAutoFile("notes", strings.NewReader(content), false); err != nil {
		panic(err)
	}
	entry := builder.Index().Entries[0]
	fmt.Println(entry.Size, entry.Compression == erisfs.Lzip)
	// Output: 6144 true
}
