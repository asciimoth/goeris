package streamio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateTemp(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, name := range []string{dir, ".", ""} {
		f, err := CreateTemp(name, "stream-*")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.Name()); err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(f.Name()) {
			t.Errorf("CreateTemp(%q) returned relative name %q", name, f.Name())
		}
	}
	if _, err := CreateTemp("missing", "stream-*"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing directory: %v", err)
	}
}

func TestCreateTempAfterWorkingDirectoryRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Skipf("cannot remove the working directory: %v", err)
	}
	if f, err := CreateTemp(".", "stream-*"); f != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("CreateTemp = %v, %v", f, err)
	}
}

func TestCreateTempParentOfLinkedWorkingDirectory(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("requires Unix parent directory lookup")
	}
	target := t.TempDir()
	child := filepath.Join(target, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(child, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Chdir(link)
	t.Setenv("PWD", link)
	f, err := CreateTemp("..", "stream-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, filepath.Base(f.Name()))); err != nil {
		t.Fatalf("temporary file missing from the parent of the working directory: %v", err)
	}
}
