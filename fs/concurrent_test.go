package fs

import (
	"bytes"
	"context"
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestEmptyNestedIndexStaysLoaded(t *testing.T) {
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	empty, err := store.EncodeIndex(ctx, Index{}, eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	index := Index{Entries: []Entry{{Path: []string{"empty"}, Type: Subdirectory, Capability: empty}}}
	root, err := store.EncodeIndex(ctx, index, eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := store.OpenFS(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		entries, err := image.ReadDir(ctx, "empty")
		if err != nil || len(entries) != 0 {
			t.Fatalf("ReadDir(empty) returned %v, %v", entries, err)
		}
		delete(blocks.Blocks, empty.Root.Reference)
	}
}

func TestConcurrentImageReads(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	content := bytes.Repeat([]byte("concurrent image reads\n"), 2048)
	nested := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddFile(context.Background(), "plain", bytes.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	legacy := storeV1Index(t, nested.Index(), blocks)
	if err := nested.AddCompressedFile(context.Background(), "packed", bytes.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	modern, err := nested.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	for name, capability := range map[string]eris.Capability{"v1": legacy, "v2": modern} {
		if err := builder.AddSubdirectory(name, capability); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.AddSymlink("link", "v1/plain"); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(context.Background(), capability, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// No writes occur after construction. BlockSet supports concurrent reads.
	// Start together while both nested indexes and the v1 size are uncached.
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			<-start
			for _, name := range []string{"link", "v1/plain", "v2/plain", "v2/packed"} {
				info, err := image.Stat(context.Background(), name)
				if err != nil || info.Size() != int64(len(content)) {
					t.Errorf("Stat(%s): %v, %v", name, info, err)
					return
				}
				got, err := stdfs.ReadFile(NewIOFS(context.Background(), image), name)
				if err != nil || !bytes.Equal(got, content) {
					t.Errorf("ReadFile(%s): equal=%t, %v", name, bytes.Equal(got, content), err)
					return
				}
			}
			for _, name := range []string{"v1", "v2"} {
				sub, err := image.Sub(context.Background(), name)
				if err != nil {
					t.Error(err)
					return
				}
				entries, err := stdfs.ReadDir(NewIOFS(context.Background(), sub), ".")
				want := "plain"
				if name == "v2" {
					want = "packed,plain"
				}
				if err != nil || names(entries) != want {
					t.Errorf("ReadDir(%s): %s, %v", name, names(entries), err)
					return
				}
				file, err := image.OpenFile(context.Background(), name+"/plain")
				if err != nil {
					t.Error(err)
					return
				}
				var sample [31]byte
				offset := worker * 4096
				_, err = file.ReadAt(sample[:], int64(offset))
				closeErr := file.Close()
				if err != nil || closeErr != nil || !bytes.Equal(sample[:], content[offset:offset+len(sample)]) {
					t.Errorf("random read %s at %d: read=%v, close=%v", name, offset, err, closeErr)
					return
				}
			}
		})
	}
	close(start)
	workers.Wait()
}

func TestReadSeekCloseConcurrently(t *testing.T) {
	blocks, _ := mountFixture(t)
	image, err := OpenFS(t.Context(), pathTestCapability(t), eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("Hello, ERIS!\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		image FileSystem
	}{
		{"decoded", image},
		{"mounted", &mountedImage{root: root, prefix: "."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := test.image.OpenFile(t.Context(), "hello.txt")
			if err != nil {
				t.Fatal(err)
			}
			var workers sync.WaitGroup
			for range 8 {
				workers.Go(func() {
					var p [1]byte
					if _, err := file.ReadAt(p[:], 0); err != nil && !errors.Is(err, ErrClosed) {
						t.Errorf("ReadAt during Close: %v", err)
					}
					if _, err := file.Seek(0, 0); err != nil && !errors.Is(err, ErrClosed) {
						t.Errorf("Seek during Close: %v", err)
					}
					if _, err := file.Stat(); err != nil && !errors.Is(err, ErrClosed) {
						t.Errorf("Stat during Close: %v", err)
					}
				})
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
			workers.Wait()
			if _, err := file.Read(make([]byte, 1)); !errors.Is(err, ErrClosed) {
				t.Errorf("Read after Close: %v", err)
			}
		})
	}
}
