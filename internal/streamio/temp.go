package streamio

import (
	"os"
	"path/filepath"
)

// CreateTemp creates a private file with an absolute name so that the caller
// can remove it after a change of working directory. An empty directory uses
// the system temporary directory.
func CreateTemp(dir, pattern string) (*os.File, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	// Resolve links before joining a relative path to the working directory.
	// Cleaning ".." first could select the parent of the link itself.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dir) {
		dir, err = AbsolutePath(dir, "")
		if err != nil {
			return nil, err
		}
		// The working directory can also contain a link from PWD.
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
	}
	return os.CreateTemp(dir, pattern)
}
