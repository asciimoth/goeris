package fs_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

func ExampleBuilder() {
	blocks := make(eris.BlockSet)
	builder := erisfs.NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile(context.Background(), "docs/readme", strings.NewReader("hello"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(context.Background()) // Always writes an ERIS-FS v2 index.
	if err != nil {
		panic(err)
	}
	image, err := erisfs.OpenFS(context.Background(), capability, blocks) // Also accepts v1 images.
	if err != nil {
		panic(err)
	}
	file, err := image.OpenFile(context.Background(), "docs/readme")
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
	if err := builder.AddFile(context.Background(), "file", strings.NewReader("known length"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		panic(err)
	}
	image, err := erisfs.OpenFS(context.Background(), capability, blocks)
	if err != nil {
		panic(err)
	}
	// The v2 index supplies the length. Opening needs no file content reads.
	reader, err := image.OpenFile(context.Background(), "file")
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
	if err := builder.AddAutoFile(context.Background(), "notes", strings.NewReader(content), false); err != nil {
		panic(err)
	}
	entry := builder.Index().Entries[0]
	fmt.Println(entry.Size, entry.Compression == erisfs.Lzip)
	// Output: 6144 true
}

func ExampleNewIOFS() {
	ctx := context.Background()
	blocks := make(eris.BlockSet)
	builder := erisfs.NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile(ctx, "hello", strings.NewReader("hello"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		panic(err)
	}
	image, err := erisfs.OpenFS(ctx, capability, blocks)
	if err != nil {
		panic(err)
	}
	compatible := erisfs.NewIOFS(ctx, image)
	content, err := fs.ReadFile(compatible, "hello")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(content))
	// Output: hello
}
