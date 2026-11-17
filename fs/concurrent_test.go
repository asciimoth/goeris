package fs

import (
	"bytes"
	"context"
	stdfs "io/fs"
	"sync"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestConcurrentImageReads(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	content := bytes.Repeat([]byte("concurrent image reads\n"), 2048)
	nested := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
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
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
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
	image, err := OpenFS(context.Background(), capability, blocks, eris.BlockOptions{})
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
