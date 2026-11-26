package fs

import (
	stdfs "io/fs"
	"os"
	"path/filepath"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/streamio"
)

// ResolveRoot returns the absolute path to an ERIS-FS root directory on a
// local mount, or "" if it cannot be found. It is equivalent to
// ResolvePath(capability, ".", mountPath...). See ResolvePath for mount selection
// and file-system error handling.
func ResolveRoot(capability eris.Capability, mountPath ...string) string {
	return ResolvePath(capability, ".", mountPath...)
}

// ResolvePath returns the absolute host path to a file or directory in a
// mounted ERIS-FS image. The capability identifies the image's root index.
// The name uses io/fs path rules: "." selects the root, and other names are
// relative paths separated by slashes. Empty names, absolute paths, and dot or
// parent components are invalid. Names must also be valid native path names.
//
// Supply at most one mount path. It must point to the directory that contains
// capability trees: for the eris-go layout, supply <mount>/fs. The function
// appends the full capability URN and name; it does not append another /fs.
//
// If the mount path is omitted or empty, ResolvePath uses ERIS_FS_MNT as is.
// If that variable is empty or unset, it uses ERIS_MNT with /fs appended.
// If both variables are empty or unset, it tries /n/eris/fs, then /eris/fs.
// An explicit path or a nonempty environment variable limits the search to
// that mount. Relative mount paths use the current working directory.
//
// ResolvePath follows host symbolic links and retains them in the returned
// path. It trusts the mount to interpret and verify the ERIS-FS image and to
// handle its symbolic links. It only checks metadata; it does not read content
// or confine host symbolic links to the image. File access can still fail later.
//
// It returns "" for invalid arguments, more than one mount path, or when no
// candidate resolves to a regular file or directory. The image root must be a
// directory. File-system errors are ignored.
func ResolvePath(capability eris.Capability, name string, mountPath ...string) string {
	return resolvePath(capability, name, mountPath, os.Stat)
}

// resolvePath accepts a metadata lookup so tests can check default mount paths
// without creating or changing system directories.
func resolvePath(capability eris.Capability, name string, mountPath []string, stat func(string) (stdfs.FileInfo, error)) string {
	capName := capability.String()
	if capName == "" || len(mountPath) > 1 {
		return ""
	}
	// Localize checks io/fs syntax and rejects names that cannot be represented
	// as local paths. Thus a name cannot add host path traversal components.
	localName, err := filepath.Localize(name)
	if err != nil {
		return ""
	}

	var mount string
	if len(mountPath) == 1 {
		mount = mountPath[0]
	}
	if mount == "" {
		mount = os.Getenv("ERIS_FS_MNT")
	}
	entry := filepath.Join(capName, localName)
	if mount == "" {
		if base := os.Getenv("ERIS_MNT"); base != "" {
			mount = base
			entry = filepath.Join("fs", entry)
		}
	}
	mounts := []string{mount}
	if mount == "" {
		mounts = []string{"/n/eris/fs", "/eris/fs"}
	}
	for _, root := range mounts {
		path, err := streamio.AbsolutePath(root, entry)
		if err != nil {
			continue
		}
		// Compatible mounts can resolve capability paths on demand without
		// including those capabilities in a directory listing.
		info, err := stat(path)
		if err == nil && (info.IsDir() || (name != "." && info.Mode().IsRegular())) {
			return path
		}
	}
	return ""
}
