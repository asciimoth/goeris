//go:build unix

package volume

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"
)

func TestDirectorySyncErrors(t *testing.T) {
	for _, cause := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.ENOSYS} {
		if err := directorySyncError(&os.PathError{Op: "sync", Path: "dir", Err: cause}); err != nil {
			t.Errorf("unsupported directory sync: %v", err)
		}
	}
	for _, cause := range []error{syscall.EIO, syscall.ENOSPC, syscall.EACCES, fs.ErrClosed} {
		input := &os.PathError{Op: "sync", Path: "dir", Err: cause}
		if err := directorySyncError(input); err != input {
			t.Errorf("lost directory-sync failure %v: %v", input, err)
		}
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := syncDirectory(root, "."); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(root, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory error was hidden: %v", err)
	}
}
