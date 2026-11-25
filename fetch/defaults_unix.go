//go:build !windows

package fetch

import (
	"os"
	"runtime"
	"strings"
)

// Leave the native base intact. A symlink before ".." affects which
// directory the OS selects. Cleaning the path can select another file.
func underBase(base, suffix string) string {
	if strings.HasSuffix(base, "/") {
		return base + suffix
	}
	return base + "/" + suffix
}

func defaultFiles() []string { return unixFiles(runtime.GOOS) }
func unixFiles(platform string) []string {
	home, _ := os.UserHomeDir()
	var paths []string
	if platform == "darwin" {
		if absolute(home) {
			paths = append(paths, underBase(home, "Library/Application Support/ERIS/fetch.conf"))
		}
		return append(paths, "/Library/Application Support/ERIS/fetch.conf")
	}
	user := os.Getenv("XDG_CONFIG_HOME")
	if !absolute(user) && absolute(home) {
		user = underBase(home, ".config")
	}
	if absolute(user) {
		paths = append(paths, underBase(user, "eris/fetch.conf"))
	}
	dirs := os.Getenv("XDG_CONFIG_DIRS")
	if dirs == "" {
		dirs = "/etc/xdg"
	}
	for d := range strings.SplitSeq(dirs, ":") {
		if absolute(d) {
			paths = append(paths, underBase(d, "eris/fetch.conf"))
		}
	}
	return append(paths, "/etc/eris/fetch.conf")
}
