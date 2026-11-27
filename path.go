package eris

import (
	"errors"
	"io/fs"

	"github.com/asciimoth/goeris/internal/streamio"
)

func resolveMountPath(capability Capability, mount string, stat func(string) (fs.FileInfo, error)) (string, error) {
	return findMountEntry(capability, streamio.MountRoots(mount), func(path string) (string, error) {
		return path, checkMountFile(path, stat)
	})
}

func checkMountFile(path string, stat func(string) (fs.FileInfo, error)) error {
	info, err := stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return &fs.PathError{Op: "stat", Path: path, Err: fs.ErrInvalid}
	}
	return nil
}

// Check each candidate through the requested operation. A file can disappear
// or become inaccessible between the metadata check and open.
func findMountEntry[T any](capability Capability, mounts []string, lookup func(string) (T, error)) (T, error) {
	var zero T
	if err := capability.Validate(); err != nil {
		return zero, err
	}
	name := capability.String()
	var failures []error
	missing := 0
	for _, root := range mounts {
		path, err := streamio.AbsolutePath(root, name)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Look up the capability directly. Compatible mounts can resolve names
		// on demand without including them in a directory listing.
		entry, err := lookup(path)
		if err == nil {
			return entry, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			missing++
		}
		failures = append(failures, err)
	}
	if missing == len(mounts) {
		failures = append(failures, ErrMissingBlock)
	}
	return zero, errors.Join(failures...)
}
