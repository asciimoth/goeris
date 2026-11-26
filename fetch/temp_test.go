package fetch

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestTempDataReadersHaveIndependentOffsets(t *testing.T) {
	for name, storage := range map[string]TempStorage{"memory": nil, "file": TempDir(t.TempDir())} {
		t.Run(name, func(t *testing.T) {
			data, err := NewTempData(t.Context(), storage)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := data.Close(); err != nil {
					t.Error(err)
				}
			})
			const content = "download"
			if _, err := io.WriteString(data, content); err != nil {
				t.Fatal(err)
			}
			first, err := data.Reader()
			if err != nil {
				t.Fatal(err)
			}
			second, err := data.Reader()
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range []io.Reader{first, second} {
				got, err := io.ReadAll(r)
				if err != nil || string(got) != content {
					t.Fatalf("read %q, want %q, error %v", got, content, err)
				}
			}
		})
	}
}

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

func TestTempDirCreateErrors(t *testing.T) {
	dir := TempDir(filepath.Join(t.TempDir(), "missing"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		want error
	}{
		{ctx, context.Canceled},
		{t.Context(), fs.ErrNotExist},
	} {
		if data, err := dir.Create(tc.ctx); data != nil || !errors.Is(err, tc.want) {
			t.Errorf("Create returned %v, %v, want %v", data, err, tc.want)
		}
	}
}

func TestTempDirCleanupAfterSymlinkChange(t *testing.T) {
	for _, useWorkingDir := range []bool{false, true} {
		name := "absolute path"
		if useWorkingDir {
			name = "working directory"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Skipf("cannot create a directory symlink: %v", err)
			}
			path := link
			if useWorkingDir {
				t.Chdir(link)
				t.Setenv("PWD", link)
				path = "."
			}
			data, err := TempDir(path).Create(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(link); err != nil {
				_ = data.Close()
				t.Fatal(err)
			}
			if err := data.Close(); err != nil {
				t.Fatal(err)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("temporary directory after cleanup: %v, %v", entries, err)
			}
		})
	}
}
