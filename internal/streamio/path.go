package streamio

import (
	"os"
	"path/filepath"
	"strings"
)

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
