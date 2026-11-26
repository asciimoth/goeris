package eris

import (
	"errors"
	"io/fs"
	"os"

	"github.com/asciimoth/goeris/internal/streamio"
)

// ResolvePath returns the absolute path to decoded content on an ERIS mount.
// The mount must use the layout in specs/eris-go-fuse-layout.md: content is a
// regular file at <mount>/<canonical capability URN>. For an ERIS-FS capability,
// this is the index file, not the directory tree under <mount>/fs/.
//
// Supply at most one mount path. If it is omitted or empty, ResolvePath uses
// ERIS_MNT. If ERIS_MNT is also empty or unset, it tries /n/eris, then /eris.
// An explicit path or a nonempty ERIS_MNT limits the search to that mount.
// Relative mount paths are relative to the current working directory.
//
// ResolvePath follows symbolic links and checks file metadata. It trusts the
// mount to decode and verify content; it does not read the file. The returned
// path retains symbolic link components. File access can still fail later.
// It returns "" for an invalid capability, more than one mount path, or when
// no candidate can be resolved to a regular file. File-system errors are ignored.
func ResolvePath(capability Capability, mountPath ...string) string {
	return resolvePath(capability, mountPath, os.Stat)
}

// resolvePath accepts a metadata lookup so tests can check the default mount
// paths without creating or changing system directories.
func resolvePath(capability Capability, mountPath []string, stat func(string) (fs.FileInfo, error)) string {
	if len(mountPath) > 1 {
		return ""
	}
	var mount string
	if len(mountPath) == 1 {
		mount = mountPath[0]
	}
	path, _ := resolveMountPath(capability, mount, stat)
	return path
}

func resolveMountPath(capability Capability, mount string, stat func(string) (fs.FileInfo, error)) (string, error) {
	return findMountEntry(capability, mountRoots(mount), func(path string) (string, error) {
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

func mountRoots(mount string) []string {
	if mount == "" {
		mount = os.Getenv("ERIS_MNT")
	}
	if mount != "" {
		return []string{mount}
	}
	return []string{"/n/eris", "/eris"}
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
