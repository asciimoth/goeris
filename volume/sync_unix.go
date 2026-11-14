//go:build unix

package volume

import (
	"errors"
	"os"
	"syscall"
)

// syncDirectory persists directory entries on file systems that support it.
// Unsupported directory sync is permitted; actual I/O failures are not hidden.
func syncDirectory(root *os.Root, name string) error {
	dir, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return directorySyncError(dir.Sync())
}

func directorySyncError(err error) error {
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
		return nil
	}
	return err
}
