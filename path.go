package eris

import (
	"io/fs"
	"os"
	"path/filepath"
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
	name := capability.String()
	if name == "" || len(mountPath) > 1 {
		return ""
	}

	var mount string
	if len(mountPath) == 1 {
		mount = mountPath[0]
	}
	if mount == "" {
		mount = os.Getenv("ERIS_MNT")
	}
	mounts := []string{mount}
	if mount == "" {
		mounts = []string{"/n/eris", "/eris"}
	}
	for _, root := range mounts {
		path, err := filepath.Abs(filepath.Join(root, name))
		if err != nil {
			continue
		}
		// Look up the capability directly. Compatible mounts can resolve names
		// on demand without including them in a directory listing.
		info, err := stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}
