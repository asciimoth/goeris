package fetch

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/volume"
)

const largeTreeFileCount = 100_000

func newDefaultTree(t *testing.T, images *erisfs.Store) *Tree {
	t.Helper()
	options, err := source(t, "type archive\nformat tar\nurl https://example.org/tree.tar\n").Validate(true)
	if err != nil {
		t.Fatal(err)
	}
	if options.Compression != "none" {
		t.Fatalf("default compression = %q, want none", options.Compression)
	}
	tree, err := options.NewTree(images)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestWideTreeDefaultEncoding(t *testing.T) {
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	tree := newDefaultTree(t, images)
	const content = "small file\n"
	for i := largeTreeFileCount - 1; i >= 0; i-- {
		name := fmt.Sprintf("file-%06d", i)
		if err := tree.AddFile(ctx, name, strings.NewReader(content), false); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	root, err := tree.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if root.BlockSize != eris.BlockSize32KiB {
		t.Fatalf("index block size = %d", root.BlockSize)
	}
	image, err := images.OpenFS(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := image.ReadDir(ctx, ".")
	if err != nil || len(entries) != largeTreeFileCount {
		t.Fatalf("listed %d files, want %d: %v", len(entries), largeTreeFileCount, err)
	}
	for i, entry := range entries {
		if want := fmt.Sprintf("file-%06d", i); entry.Name() != want {
			t.Fatalf("entry %d = %q, want %q", i, entry.Name(), want)
		}
	}
	got, err := image.ReadFile(ctx, entries[len(entries)-1].Name())
	if err != nil || string(got) != content {
		t.Fatalf("file content = %q, want %q: %v", got, content, err)
	}
}

// Run with GOERIS_LARGE_IMAGE_TEST=1. The volume needs about 6 GiB of free space.
func TestMultiGiBTreeDefaultEncoding(t *testing.T) {
	if os.Getenv("GOERIS_LARGE_IMAGE_TEST") != "1" {
		t.Skip("set GOERIS_LARGE_IMAGE_TEST=1 to run the multi-GiB test")
	}
	ctx := t.Context()
	blocks, err := volume.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := blocks.Close(); err != nil {
			t.Error(err)
		}
	})
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	tree := newDefaultTree(t, images)

	const smallSize = 24 << 10
	const boundary int64 = 2 << 30
	const tail = "file data across the 2 GiB boundary"
	const tailStart = boundary - 5
	const largeSize = tailStart + int64(len(tail))
	generator := rand.NewChaCha8([32]byte{1})
	written := sha256.New()
	small := &io.LimitedReader{R: generator}
	for i := range largeTreeFileCount {
		name := fmt.Sprintf("file-%06d", i)
		small.N = smallSize
		if err := tree.AddFile(ctx, name, io.TeeReader(small, written), false); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	t.Logf("encoded %d files with %d bytes of total content", largeTreeFileCount, largeTreeFileCount*int64(smallSize))
	large := io.MultiReader(io.LimitReader(generator, tailStart), strings.NewReader(tail))
	const largeName = "large-file"
	if err := tree.AddFile(ctx, largeName, io.TeeReader(large, written), true); err != nil {
		t.Fatal(err)
	}
	t.Logf("encoded one file with %d bytes", largeSize)
	root, err := tree.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	image, err := images.OpenFS(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := image.ReadDir(ctx, ".")
	if err != nil || len(entries) != largeTreeFileCount+1 {
		t.Fatalf("listed %d files, want %d: %v", len(entries), largeTreeFileCount+1, err)
	}

	read := sha256.New()
	buffer := make([]byte, 1<<20)
	for _, entry := range entries[:largeTreeFileCount] {
		copyTreeFile(t, image, entry.Name(), read, buffer, smallSize)
	}
	file, err := image.OpenFile(ctx, largeName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != largeSize || info.Mode().Perm() != 0o555 {
		t.Fatalf("large file size = %d, mode = %v", info.Size(), info.Mode())
	}
	got := make([]byte, len(tail))
	if _, err := file.ReadAt(got, tailStart); err != nil || string(got) != tail {
		t.Fatalf("read across 2 GiB = %q, want %q: %v", got, tail, err)
	}
	if offset, err := file.Seek(-int64(len(tail)), io.SeekEnd); offset != tailStart || err != nil {
		t.Fatalf("seek from end = %d, want %d: %v", offset, tailStart, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := io.CopyBuffer(read, file, buffer); n != largeSize || err != nil {
		t.Fatalf("read %d bytes from the large file, want %d: %v", n, largeSize, err)
	}
	if got, want := read.Sum(nil), written.Sum(nil); !bytes.Equal(got, want) {
		t.Fatalf("image content digest = %x, want %x", got, want)
	}
}

func copyTreeFile(t *testing.T, image erisfs.FileSystem, name string, dst io.Writer, buffer []byte, size int64) {
	t.Helper()
	file, err := image.OpenFile(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.CopyBuffer(dst, file, buffer)
	closeErr := file.Close()
	if n != size || err != nil {
		t.Fatalf("read %d bytes from %s, want %d: %v", n, name, size, err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}
