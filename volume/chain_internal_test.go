package volume

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestOpenAvailableFallbackPaths(t *testing.T) {
	base := t.TempDir()
	defaults := []string{
		filepath.Join(base, "system", "eris", "block"),
		filepath.Join(base, "user; with spaces", "eris", "block"),
	}
	chain, err := openAvailableChain("", defaults)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	if got := chain.Len(); got != len(defaults) {
		t.Fatalf("opened %d volumes, want %d", got, len(defaults))
	}
	block := bytes.Repeat([]byte{0x2a}, int(eris.BlockSize1KiB))
	ref, err := eris.PutBlock(t.Context(), chain, block, eris.NoTTL, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(defaults[0], BlockPath(ref))); err != nil || !bytes.Equal(got, block) {
		t.Fatalf("first default did not accept write: %v", err)
	}
	if info, err := os.Stat(defaults[1]); err != nil || !info.IsDir() {
		t.Fatalf("second default directory was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaults[1], BlockPath(ref))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("write also reached the second default: %v", err)
	}
}

func TestOpenAvailableChainErrorBeforeCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eris", "block")
	if chain, err := openAvailableChain(path+";", []string{path}); chain != nil || !errors.Is(err, ErrInvalidChain) {
		t.Fatalf("malformed path list: %v, %v", chain, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("default path was created: %v", err)
	}
}

func TestOpenAvailableChainKeepsOtherErrors(t *testing.T) {
	path := t.TempDir()
	bad := filepath.Join(path, "invalid\x00name")
	chain, err := OpenAvailableChain(path + ";" + bad)
	var pathErr *fs.PathError
	if chain != nil || !errors.As(err, &pathErr) || pathErr.Path != bad {
		t.Fatalf("open invalid path: %v, %v", chain, err)
	}
}

func TestChainCloseOwnsAllHandles(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("first.cbor", []byte{0x80}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("second", 0o755); err != nil {
		t.Fatal(err)
	}
	chain, err := OpenChain("first.cbor;second")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	// Closing the first handle early forces Close to encounter an error.
	if err := chain.volumes[0].Close(); err != nil {
		t.Fatal(err)
	}
	if err := chain.Close(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("close error not retained: %v", err)
	}
	for index, v := range chain.volumes {
		if _, err, _ := eris.GetBlock(context.Background(), v, eris.Reference{}, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("volume %d left open: %v", index, err)
		}
	}
}
