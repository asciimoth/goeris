package fs_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

func TestBuilderRootOptions(t *testing.T) {
	for _, originalRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("Root=%v", originalRoot), func(t *testing.T) {
			ctx := context.Background()
			options := eris.BlockOptions{HighPriority: true, Root: originalRoot}
			store := &opaqueBlocks{data: make(map[eris.Reference][]byte), wantOptions: options}
			builder := erisfs.NewBuilder(store, eris.Secret{}, options)
			large := bytes.Repeat([]byte("file content "), 4000)
			for _, file := range []struct {
				name string
				data []byte
				add  func(context.Context, string, io.Reader, bool) error
			}{
				{"empty", nil, builder.AddFile},
				{"small", []byte("small file"), builder.AddFile},
				{"large", large, builder.AddFile},
				{"compressed", large, builder.AddCompressedFile},
				{"auto-small", []byte("auto file"), builder.AddAutoFile},
				{"auto-large", large, builder.AddAutoFile},
			} {
				if err := file.add(ctx, file.name, bytes.NewReader(file.data), false); err != nil {
					t.Fatal(err)
				}
				for _, root := range store.roots {
					if root != originalRoot {
						t.Fatalf("%s changed the original root flag", file.name)
					}
				}
			}
			// Make the index itself span several blocks. Its child blocks must also
			// preserve the original flag.
			for i := range 100 {
				if err := builder.AddSymlink(fmt.Sprintf("link-%d", i), strings.Repeat("target/", 15)); err != nil {
					t.Fatal(err)
				}
			}
			capability, err := builder.Finish(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if capability.Level == 0 {
				t.Fatal("test index fits in one block")
			}
			for ref, root := range store.roots {
				if root != (originalRoot || ref == capability.Root.Reference) {
					t.Fatal("root flag does not match original OR index root")
				}
			}
			if store.wrongOptions {
				t.Fatal("builder changed other options")
			}
		})
	}
}

func TestBuilderFileAndStandaloneBlob(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	options := eris.BlockOptions{HighPriority: true}
	builder := erisfs.NewBuilder(blocks, eris.Secret{}, options)
	content := []byte("shared content")
	if err := builder.AddFile(ctx, "file", bytes.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	fileCap := builder.Index().Entries[0].Capability
	if got := blocks.Blocks[fileCap.Root.Reference].Options; got.Root || !got.HighPriority {
		t.Fatalf("file options: %v", got)
	}
	blobCap, err := eris.EncodeContentRecommended(ctx, bytes.NewReader(content), eris.Secret{}, blocks, eris.BlockOptions{})
	if err != nil || blobCap != fileCap {
		t.Fatalf("blob: %v, %v", blobCap, err)
	}
	if !blocks.Blocks[blobCap.Root.Reference].Options.Root {
		t.Fatal("standalone blob root was not marked")
	}
	if err := builder.AddFile(ctx, "same-file", bytes.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	if !blocks.Blocks[blobCap.Root.Reference].Options.Root {
		t.Fatal("file write cleared a stored blob root flag")
	}
}
