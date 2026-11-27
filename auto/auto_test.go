package auto_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/auto"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/volume"
)

func TestOpenVolumeChain(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("ERIS_BLOCK_PATH", strings.Join([]string{missing, first, missing, second, missing}, ";"))
	setMissingMounts(t)
	stores := openStores(t)
	secondVolume := openVolume(t, second)
	existing := encode(t, eris.NewBlockCapabilityStore(secondVolume), "second volume")
	assertContent(t, stores.Capabilities, existing, "second volume")

	const payload = "an ERIS-FS file"
	root := buildImage(t, stores.Images, payload)
	index, err := stores.Images.DecodeIndex(t.Context(), root)
	if err != nil || len(index.Entries) != 1 {
		t.Fatalf("DecodeIndex() = %+v, %v", index, err)
	}
	file := index.Entries[0].Capability
	for _, cap := range []eris.Capability{root, file} {
		if _, err := os.Stat(filepath.Join(first, volume.BlockPath(cap.Root.Reference))); err != nil {
			t.Fatalf("first volume is missing block %s: %v", cap.Root.Reference, err)
		}
		if _, err := os.Stat(filepath.Join(second, volume.BlockPath(cap.Root.Reference))); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("second volume unexpectedly has block %s: %v", cap.Root.Reference, err)
		}
	}

	if got := stores.Blocks.BatchSize(); got != secondVolume.BatchSize() {
		t.Fatalf("block chain BatchSize() = %d, want %d", got, secondVolume.BatchSize())
	}
	ref := existing.Root.Reference
	block, err, _ := eris.GetBlock(t.Context(), secondVolume, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertBlock(t, stores.Blocks, ref, block)
	assertImage(t, stores.Images, root, payload)
	if err := stores.Close(); err != nil {
		t.Fatal(err)
	}
	requests := []eris.BlockRequest{{Reference: root.Root.Reference}}
	if _, err := stores.Blocks.GetBlock(t.Context(), requests); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("GetBlock() after Close = %v, want fs.ErrClosed", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	reopened := openStores(t)
	assertImage(t, reopened.Images, root, payload)
	assertAbsent(t, missing)
}

func TestOpenExplicitDefaultVolumePath(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("XDG_CACHE_HOME", base)
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("ProgramData", "")
	// A relative entry can point to the platform default.
	path := filepath.Join("eris", "block")
	t.Setenv("ERIS_BLOCK_PATH", path)
	assertAbsent(t, path)
	stores := openStores(t)
	cap := encode(t, stores.Capabilities, "default user volume")
	if _, err := os.Stat(filepath.Join(path, volume.BlockPath(cap.Root.Reference))); err != nil {
		t.Fatalf("block in created volume: %v", err)
	}
}

func TestOpenSkipsDefaultCreationFailure(t *testing.T) {
	base := t.TempDir()
	blocked := filepath.Join(base, "file")
	writeFile(t, blocked, "regular file")
	t.Setenv("XDG_CACHE_HOME", blocked)
	t.Setenv("LOCALAPPDATA", blocked)
	t.Setenv("ProgramData", base)
	paths, err := volume.DefaultChainPaths()
	if err != nil {
		t.Fatal(err)
	}
	fallback := t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", paths[1]+";"+fallback)
	stores := openStores(t)
	cap := encode(t, stores.Capabilities, "fallback after creation failure")
	if _, err := os.Stat(filepath.Join(fallback, volume.BlockPath(cap.Root.Reference))); err != nil {
		t.Fatalf("block in fallback volume: %v", err)
	}
}

func TestOpenWithoutVolumesOrMounts(t *testing.T) {
	base := t.TempDir()
	block := filepath.Join(base, "block")
	mount := filepath.Join(base, "mount")
	images := filepath.Join(base, "images")
	t.Setenv("ERIS_BLOCK_PATH", block)
	t.Setenv("ERIS_MNT", mount)
	t.Setenv("ERIS_FS_MNT", images)
	stores := openStores(t)
	if _, err := eris.PutBlock(t.Context(), stores.Blocks, make([]byte, eris.BlockSize1KiB), eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrNoBlockStorage) {
		t.Fatalf("PutBlock() = %v, want no block storage", err)
	}
	if _, err := stores.Capabilities.EncodeRecommended(t.Context(), []byte("write"), eris.Secret{}, eris.BlockOptions{}); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatalf("EncodeRecommended() = %v, want read-only", err)
	}
	for _, path := range []string{block, mount, images} {
		assertAbsent(t, path)
	}
}

func TestOpenVolumeErrors(t *testing.T) {
	first := t.TempDir()
	for _, test := range []struct {
		name string
		path string
		err  error
	}{
		{"invalid list", first + ";", volume.ErrInvalidChain},
		{"invalid default paths", "", fs.ErrInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ERIS_BLOCK_PATH", test.path)
			t.Setenv("HOME", "")
			t.Setenv("XDG_CACHE_HOME", "")
			t.Setenv("ProgramData", "")
			t.Setenv("LOCALAPPDATA", "")
			stores, err := auto.Open(auto.AppendBlocks(newBlockSet()))
			if stores != nil || !errors.Is(err, test.err) {
				t.Fatalf("Open() = %v, %v; want %v", stores, err, test.err)
			}
			openStores(t, auto.ReplaceBlocks())
		})
	}
}

func TestOpenMounts(t *testing.T) {
	ctx := t.Context()
	base := t.TempDir()
	t.Chdir(base)
	t.Setenv("ERIS_BLOCK_PATH", filepath.Join(base, "absent"))
	t.Setenv("ERIS_MNT", "mount; with spaces")
	t.Setenv("ERIS_FS_MNT", "trees; with spaces")
	mount := filepath.Join(base, os.Getenv("ERIS_MNT"))
	trees := filepath.Join(base, os.Getenv("ERIS_FS_MNT"))
	local := eris.NewBlockCapabilityStore(newBlockSet())
	localImages := erisfs.NewStore(local, eris.BlockOptions{})
	cap := encode(t, local, "decoded mount file")
	if _, err := filepath.Localize(cap.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
	mkdir(t, mount)
	file := filepath.Join(mount, cap.String())
	writeFile(t, file, "decoded mount file")

	const payload = "expanded tree"
	root := buildImage(t, localImages, payload)
	generalTree := filepath.Join(mount, "fs", root.String())
	overrideTree := filepath.Join(trees, root.String())
	for _, tree := range []string{generalTree, overrideTree} {
		mkdir(t, tree)
		writeFile(t, filepath.Join(tree, "hello.txt"), payload)
	}

	stores := openStores(t)
	assertContent(t, stores.Capabilities, cap, "decoded mount file")
	if path, err := stores.Capabilities.ResolvePath(ctx, cap); err != nil || path != file {
		t.Fatalf("ResolvePath() = %q, %v; want %q", path, err, file)
	}
	if _, err := stores.Blocks.GetBlock(ctx, []eris.BlockRequest{{Reference: cap.Root.Reference}}); !errors.Is(err, eris.ErrNoBlockStorage) {
		t.Fatalf("GetBlock() = %v, want no block storage", err)
	}
	if _, err := stores.Capabilities.EncodeRecommended(ctx, []byte("write"), eris.Secret{}, eris.BlockOptions{}); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatalf("EncodeRecommended() = %v, want read-only", err)
	}

	for _, test := range []struct{ env, want string }{
		{"trees; with spaces", overrideTree},
		{"", generalTree},
	} {
		t.Setenv("ERIS_FS_MNT", test.env)
		image := assertImage(t, stores.Images, root, payload)
		if path, err := image.ResolvePath(ctx, "."); err != nil || path != test.want {
			t.Fatalf("image path = %q, %v; want %q", path, err, test.want)
		}
	}
	t.Setenv("ERIS_FS_MNT", filepath.Join(base, "absent"))
	if _, err := stores.Images.OpenFS(ctx, root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenFS() with absent explicit mount = %v, want fs.ErrNotExist", err)
	}

	// With the block layer disabled, the capability mount can also supply
	// the index and file for an image without an expanded tree.
	index, err := localImages.DecodeIndex(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(mount, root.String()), string(indexBytes))
	writeFile(t, filepath.Join(mount, index.Entries[0].Capability.String()), payload)
	assertImage(t, stores.Images, root, payload)

	// An existing, empty volume must also fall back to the decoded mount.
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	assertImage(t, openStores(t).Images, root, payload)
}

func TestOpenCanceledReads(t *testing.T) {
	stores := openStores(t, auto.ReplaceBlocks(newBlockSet()))
	cap := encode(t, stores.Capabilities, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := stores.Blocks.GetBlock(ctx, []eris.BlockRequest{{Reference: cap.Root.Reference}}); !errors.Is(err, context.Canceled) {
		t.Errorf("GetBlock() = %v", err)
	}
	if _, err := stores.Capabilities.Decode(ctx, cap, eris.BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Decode() = %v", err)
	}
	if _, err := stores.Images.OpenFS(ctx, cap); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenFS() = %v", err)
	}
}

func TestSizedBlockReaders(t *testing.T) {
	stores := openStores(t, auto.ReplaceBlocks(newBlockSet()))
	for _, content := range []string{"", "x", strings.Repeat("abcd", 1024)} {
		cap := encode(t, stores.Capabilities, content)
		reader, err := stores.Capabilities.NewReaderWithSize(t.Context(), cap, int64(len(content)), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil || string(got) != content {
			t.Fatalf("read %d bytes, want %d: %v", len(got), len(content), err)
		}
		if _, err := stores.Capabilities.NewReaderWithSize(t.Context(), cap, -1, eris.BlockOptions{}); !errors.Is(err, eris.ErrInvalidSize) {
			t.Fatalf("negative size: %v", err)
		}
	}
}

func newBlockSet() eris.BlockSet {
	return eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
}

func openStores(t *testing.T, opts ...auto.Option) *auto.Stores {
	t.Helper()
	stores, err := auto.Open(opts...)
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, stores)
	return stores
}

func openVolume(t *testing.T, path string) *volume.Volume {
	t.Helper()
	v, err := volume.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, v)
	return v
}

func closeStore(t *testing.T, store interface{ Close() error }) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
}

func setMissingMounts(t *testing.T) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("ERIS_MNT", missing)
	t.Setenv("ERIS_FS_MNT", missing)
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %q = %v, want fs.ErrNotExist", path, err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func encode(t *testing.T, store eris.CapabilityStore, content string) eris.Capability {
	t.Helper()
	cap, err := store.EncodeContentRecommended(t.Context(), strings.NewReader(content), eris.Secret{}, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return cap
}

func assertContent(t *testing.T, store eris.CapabilityStore, cap eris.Capability, want string) {
	t.Helper()
	got, err := store.Decode(t.Context(), cap, eris.BlockOptions{})
	if err != nil || string(got) != want {
		t.Fatalf("Decode() = %q, %v; want %q", got, err, want)
	}
}

func buildImage(t *testing.T, store erisfs.ImageStore, content string) eris.Capability {
	t.Helper()
	builder, err := store.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(t.Context(), "hello.txt", strings.NewReader(content), false); err != nil {
		t.Fatal(err)
	}
	root, err := builder.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func assertImage(t *testing.T, store erisfs.ImageStore, cap eris.Capability, want string) erisfs.FileSystem {
	t.Helper()
	image, err := store.OpenFS(t.Context(), cap)
	if err != nil {
		t.Fatal(err)
	}
	got, err := image.ReadFile(t.Context(), "hello.txt")
	if err != nil || string(got) != want {
		t.Fatalf("ReadFile() = %q, %v; want %q", got, err, want)
	}
	return image
}

func assertBlock(t *testing.T, store eris.BlockSource, ref eris.Reference, want []byte) {
	t.Helper()
	got, err, _ := eris.GetBlock(t.Context(), store, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil || len(got) != int(eris.BlockSize1KiB) || !bytes.Equal(got, want) {
		t.Fatalf("GetBlock() = %d bytes, %v; want block %s", len(got), err, ref)
	}
}
