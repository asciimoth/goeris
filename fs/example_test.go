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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddFile(context.Background(), "docs/readme", strings.NewReader("hello"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(context.Background()) // Always writes an ERIS-FS v2 index.
	if err != nil {
		panic(err)
	}
	image, err := images.OpenFS(context.Background(), capability) // Also accepts v1 images.
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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddFile(context.Background(), "file", strings.NewReader("known length"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		panic(err)
	}
	image, err := images.OpenFS(context.Background(), capability)
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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	builder := images.NewBuilder(eris.Secret{})
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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddFile(ctx, "hello", strings.NewReader("hello"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		panic(err)
	}
	image, err := images.OpenFS(ctx, capability)
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

func ExampleNewMountWithStore() {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddFile(ctx, "hello.txt", strings.NewReader("Hello, ERIS!\n"), false); err != nil {
		panic(err)
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		panic(err)
	}

	// Pass mount to an adapter that accepts io/fs.FS. Capability names resolve
	// on demand; they do not appear in the mount's directory listings.
	mount := erisfs.NewMountWithStore(ctx, images, blocks, eris.BlockOptions{})
	content, err := fs.ReadFile(mount, "fs/"+capability.String()+"/hello.txt")
	if err != nil {
		panic(err)
	}
	fmt.Print(string(content))

	// The same capability at the root returns the decoded index bytes.
	index, err := fs.ReadFile(mount, capability.String())
	if err != nil {
		panic(err)
	}
	fmt.Println("index:", erisfs.HasIndexHeader(index))

	// A hash path returns one complete encrypted block, including its padding.
	hash := capability.Root.Reference.String()
	block, err := fs.ReadFile(mount, hash[:3]+"/"+hash[3:6]+"/"+hash[6:9]+"/"+hash[9:])
	if err != nil {
		panic(err)
	}
	fmt.Println("encrypted block bytes:", len(block))
	// Output:
	// Hello, ERIS!
	// index: true
	// encrypted block bytes: 1024
}

func ExampleMount_WithListingCache() {
	ctx := context.Background()
	capability, blocks, err := eris.Encode([]byte("hello"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		panic(err)
	}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	base := erisfs.NewMountWithStore(ctx, images, blocks, eris.BlockOptions{})
	// Enable independent LRU limits for 128 capabilities and 256 raw blocks.
	// The original mount keeps its default listings.
	mount, err := base.WithListingCache(128, 256)
	if err != nil {
		panic(err)
	}
	if _, err := mount.ReadFile(capability.String()); err != nil {
		panic(err)
	}
	if _, err := mount.ReadFile(capability.Root.Reference.String()); err != nil {
		panic(err)
	}
	entries, err := fs.ReadDir(mount, ".")
	if err != nil {
		panic(err)
	}
	// fs, one capability, one flat hash, and its first volume prefix.
	fmt.Println("remembered root entries:", len(entries))
	entries, err = fs.ReadDir(base, ".")
	if err != nil {
		panic(err)
	}
	fmt.Println("default root entries:", len(entries))
	// Output:
	// remembered root entries: 4
	// default root entries: 1
}
