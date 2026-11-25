package fetch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTempDirCleanupAfterChdir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data, err := TempDir(".").Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary directory after cleanup: %v, error %v", entries, err)
	}
}

func TestTempDirRelativeSymlinkParent(t *testing.T) {
	working := t.TempDir()
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(working)
	if err := os.Symlink(child, "link"); err != nil {
		t.Skipf("cannot create a directory symlink: %v", err)
	}
	data, err := TempDir("link/..").Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := data.Close(); err != nil {
			t.Error(err)
		}
	}()
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected child and temporary file in the selected directory: %v, error %v", entries, err)
	}
}
