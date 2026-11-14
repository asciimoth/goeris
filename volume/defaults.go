package volume

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultChainPaths returns the paths used by OpenChain when its argument is
// empty. It does not open or create roots, or read ERIS_BLOCK_PATH.
//
// On Windows, the paths are %ProgramData%\eris\block followed by
// %LOCALAPPDATA%\eris\block. Both environment variables must be absolute paths.
// On other platforms, the paths are /eris/block followed by eris/block under
// XDG_CACHE_HOME. If XDG_CACHE_HOME is empty or relative, HOME/.cache is used;
// HOME must then be absolute. Missing or invalid required environment values
// return an error that matches fs.ErrInvalid.
func DefaultChainPaths() ([]string, error) {
	if runtime.GOOS == "windows" {
		paths := make([]string, 0, 2)
		for _, key := range []string{"ProgramData", "LOCALAPPDATA"} {
			base, err := absoluteEnvPath(key)
			if err != nil {
				return nil, err
			}
			paths = append(paths, filepath.Join(base, "eris", "block"))
		}
		return paths, nil
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(cache) {
		home, err := absoluteEnvPath("HOME")
		if err != nil {
			return nil, err
		}
		cache = filepath.Join(home, ".cache")
	}
	return []string{"/eris/block", filepath.Join(cache, "eris", "block")}, nil
}

func absoluteEnvPath(key string) (string, error) {
	value := os.Getenv(key)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("eris-volume: default chain requires an absolute %s: %w", key, fs.ErrInvalid)
	}
	return value, nil
}
