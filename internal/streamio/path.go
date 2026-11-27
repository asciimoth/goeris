package streamio

import (
	"os"
	"path/filepath"
	"strings"
)

// MountRoots returns the capability mount roots in lookup order.
func MountRoots(mount string) []string {
	if mount == "" {
		mount = os.Getenv("ERIS_MNT")
	}
	if mount != "" {
		return []string{mount}
	}
	return DefaultMountRoots()
}

// ImageMountRoots returns the ERIS-FS mount roots in lookup order.
func ImageMountRoots(mount string) []string {
	if mount == "" {
		mount = os.Getenv("ERIS_FS_MNT")
	}
	if mount != "" {
		return []string{mount}
	}
	roots := MountRoots("")
	for i, root := range roots {
		roots[i] = root + string(filepath.Separator) + "fs"
	}
	return roots
}

// DefaultMountRoots returns the platform's ERIS mount roots in lookup order.
func DefaultMountRoots() []string {
	if filepath.Separator != '\\' {
		return []string{"/n/eris", "/eris"}
	}
	var roots []string
	for _, key := range []string{"ProgramData", "LOCALAPPDATA"} {
		if base := os.Getenv(key); filepath.IsAbs(base) {
			roots = append(roots, filepath.Join(base, "eris"))
		}
	}
	return roots
}

// AbsolutePath returns the absolute path to name in dir. An empty name
// selects the directory itself.
func AbsolutePath(dir, name string) (string, error) {
	if filepath.Separator != '/' {
		return filepath.Abs(filepath.Join(dir, name))
	}
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// On macOS, Getwd can return the path of a removed directory.
		if _, err := os.Stat(cwd); err != nil {
			return "", err
		}
		dir = strings.TrimSuffix(cwd, "/") + "/" + dir
	}
	path := strings.TrimSuffix(dir, "/") + "/" + name
	// Either dir or PWD can contain a symlink before "..". The filesystem
	// must resolve those components in order.
	if strings.Contains(path, "/../") {
		return path, nil
	}
	return filepath.Clean(path), nil
}
