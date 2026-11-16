package volume_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cbor"
	"github.com/asciimoth/goeris/volume"
)

func openChain(t *testing.T, paths string) *volume.Chain {
	t.Helper()
	chain, err := volume.OpenChain(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	return chain
}

func chainDirectory(t *testing.T, name string) {
	t.Helper()
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestChainReads(t *testing.T) {
	t.Chdir(t.TempDir())
	chainDirectory(t, "first")
	chainDirectory(t, "last")
	elements, blocks := cborBlocks(t)
	data, err := cbor.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, "backup.cbor", data)
	chain := openChain(t, "first;backup.cbor;last")
	var indexes []int
	var failures []error
	chain.OnError = func(index int, err error) {
		indexes = append(indexes, index)
		failures = append(failures, err)
	}
	ref, _ := elements[0].Reference()
	writeFile(t, filepath.Join("first", volume.BlockPath(ref)), []byte("corrupt"))
	got, err, _ := chain.GetBlock(context.Background(), ref, 1, eris.BlockOptions{})
	if err != nil || !bytes.Equal(got, blocks[ref].Data) {
		t.Fatalf("CBOR fallback: length %d, %v", len(got), err)
	}
	if !reflect.DeepEqual(indexes, []int{0}) || !errors.Is(failures[0], eris.ErrInvalidBlock) {
		t.Fatalf("discarded errors: %v, %v", indexes, failures)
	}
	assertFile(t, filepath.Join("first", volume.BlockPath(ref)), []byte("corrupt"))

	block, lastRef := testBlock(t, eris.BlockSize1KiB, 75)
	writeFile(t, filepath.Join("last", lastRef.String()), block)
	indexes, failures = nil, nil
	got, err, _ = chain.GetBlock(context.Background(), lastRef, eris.NoTTL, eris.BlockOptions{})
	if err != nil || !bytes.Equal(got, block) || !reflect.DeepEqual(indexes, []int{0, 1}) {
		t.Fatalf("last-volume fallback: length %d, errors %v, %v", len(got), indexes, err)
	}

	// A success in the first volume stops before errors in later entries.
	writeFile(t, filepath.Join("first", volume.BlockPath(lastRef)), block)
	writeFile(t, "backup.cbor", []byte("invalid"))
	indexes = nil
	if _, err, _ := chain.GetBlock(context.Background(), lastRef, eris.NoTTL, eris.BlockOptions{}); err != nil || len(indexes) != 0 {
		t.Fatalf("first-volume success: %v, %v", indexes, err)
	}
}

func TestChainWrites(t *testing.T) {
	t.Chdir(t.TempDir())
	chainDirectory(t, "first")
	chainDirectory(t, "last")
	archive := []byte{0x80} // An empty CBOR array.
	writeFile(t, "backup.cbor", archive)
	chain := openChain(t, "backup.cbor;first;last")
	var indexes []int
	var failures []error
	chain.OnError = func(index int, err error) {
		indexes = append(indexes, index)
		failures = append(failures, err)
	}
	block, ref := testBlock(t, eris.BlockSize1KiB, 76)
	got, err := chain.PutBlock(context.Background(), block, 1, eris.BlockOptions{}, ref)
	if err != nil || got != ref || !reflect.DeepEqual(indexes, []int{0}) || !errors.Is(failures[0], volume.ErrNotDirectory) {
		t.Fatalf("write past CBOR root: %s, %v, %v", got, indexes, err)
	}
	assertFile(t, filepath.Join("first", volume.BlockPath(ref)), block)
	assertEmpty(t, "last")
	assertFile(t, "backup.cbor", archive)

	// A blocked destination causes a write to continue to the last directory.
	if err := os.Remove(filepath.Join("first", volume.BlockPath(ref))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join("first", volume.BlockPath(ref)), 0o755); err != nil {
		t.Fatal(err)
	}
	indexes, failures = nil, nil
	got, err = chain.PutBlock(context.Background(), block, eris.NoTTL, eris.BlockOptions{}, ref)
	if err != nil || got != ref || !reflect.DeepEqual(indexes, []int{0, 1}) {
		t.Fatalf("write after failed destination: %s, %v, %v", got, indexes, err)
	}
	assertFile(t, filepath.Join("last", volume.BlockPath(ref)), block)
	assertFile(t, "backup.cbor", archive)
}

func TestChainErrorsAndCancellation(t *testing.T) {
	t.Chdir(t.TempDir())
	chainDirectory(t, "first")
	writeFile(t, "invalid.cbor", []byte("invalid"))
	chain := openChain(t, "first;invalid.cbor")
	var indexes []int
	chain.OnError = func(index int, _ error) { indexes = append(indexes, index) }
	if _, err, _ := chain.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); err != cbor.ErrInvalidEncoding || !reflect.DeepEqual(indexes, []int{0}) {
		t.Fatalf("last error not preserved: %v, %v", indexes, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	indexes = nil
	if _, err, _ := chain.GetBlock(ctx, eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	block, _ := testBlock(t, eris.BlockSize1KiB, 77)
	if _, err := chain.PutBlock(ctx, block, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	if len(indexes) != 0 {
		t.Fatalf("attempted storage after cancellation: %v", indexes)
	}
	assertEmpty(t, "first")
	readOnly := openChain(t, "invalid.cbor")
	if _, err := readOnly.PutBlock(context.Background(), block, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, volume.ErrNotDirectory) {
		t.Fatalf("file-only chain write: %v", err)
	}
}

func TestChainPathList(t *testing.T) {
	t.Chdir(t.TempDir())
	chainDirectory(t, "first")
	chainDirectory(t, " with spaces ")
	chain := openChain(t, "first; with spaces ;first")
	var indexes []int
	chain.OnError = func(index int, _ error) { indexes = append(indexes, index) }
	if _, err, _ := chain.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) || !reflect.DeepEqual(indexes, []int{0, 1}) {
		t.Fatalf("literal paths or duplicates not preserved: %v, %v", indexes, err)
	}
	for _, paths := range []string{";", ";first", "first;", "first;;first"} {
		if chain, err := volume.OpenChain(paths); chain != nil || !errors.Is(err, volume.ErrInvalidChain) {
			t.Fatalf("invalid list %q: %v, %v", paths, chain, err)
		}
	}
	if chain, err := volume.OpenChain("first;missing"); chain != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial chain returned: %v, %v", chain, err)
	}
	if _, err := os.Stat("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("created a missing root: %v", err)
	}
	// Unix allows a colon in a file name; the chain must keep it in one entry.
	// Windows absolute paths contain a colon in their drive letter.
	if runtime.GOOS != "windows" {
		chainDirectory(t, "with:colon")
		colonChain := openChain(t, "with:colon;first")
		if _, err, _ := colonChain.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
			t.Fatalf("colon in path: %v", err)
		}
	}
	absolute := t.TempDir()
	absoluteChain := openChain(t, absolute+";"+absolute)
	if _, err, _ := absoluteChain.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("absolute paths: %v", err)
	}
}

func TestEmptyChain(t *testing.T) {
	chain := &volume.Chain{}
	if _, err, _ := chain.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); err != eris.ErrNoBlockStorage {
		t.Fatalf("empty read: %v", err)
	}
	if _, err := chain.PutBlock(context.Background(), nil, eris.NoTTL, eris.BlockOptions{}); err != eris.ErrNoBlockStorage {
		t.Fatalf("empty write: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err, _ := chain.GetBlock(ctx, eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); err != context.Canceled {
		t.Fatalf("empty canceled read: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatal(err)
	}
}
