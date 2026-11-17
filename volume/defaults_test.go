package volume_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func TestDefaultChainPaths(t *testing.T) {
	base := t.TempDir()
	// Defaults use environment paths as single entries, even with semicolons.
	cache := filepath.Join(base, "cache;with spaces")
	t.Setenv("ERIS_BLOCK_PATH", "ignored")
	if runtime.GOOS == "windows" {
		t.Setenv("ProgramData", base)
		t.Setenv("LOCALAPPDATA", cache)
		assertDefaultPaths(t, []string{filepath.Join(base, "eris", "block"), filepath.Join(cache, "eris", "block")})
		for _, key := range []string{"ProgramData", "LOCALAPPDATA"} {
			for _, value := range []string{"", "relative", `C:relative`, `\rooted`} {
				t.Run(key+value, func(t *testing.T) {
					t.Setenv(key, value)
					assertInvalidDefaults(t)
				})
			}
		}
		return
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", cache)
	assertDefaultPaths(t, []string{"/eris/block", filepath.Join(cache, "eris", "block")})
	t.Setenv("HOME", base)
	for _, value := range []string{"", "relative/cache"} {
		t.Setenv("XDG_CACHE_HOME", value)
		assertDefaultPaths(t, []string{"/eris/block", filepath.Join(base, ".cache", "eris", "block")})
	}
	for _, value := range []string{"", "relative/home"} {
		t.Setenv("HOME", value)
		assertInvalidDefaults(t)
	}
}

func assertDefaultPaths(t *testing.T, want []string) {
	t.Helper()
	got, err := volume.DefaultChainPaths()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("default paths: %v, %v; want %v", got, err, want)
	}
}

func assertInvalidDefaults(t *testing.T) {
	t.Helper()
	if paths, err := volume.DefaultChainPaths(); paths != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("invalid default paths: %v, %v", paths, err)
	}
	if chain, err := volume.OpenChain(""); chain != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("invalid default chain: %v, %v", chain, err)
	}
	// An explicit chain needs no default environment values.
	openChain(t, t.TempDir())
}

func TestOpenDefaultChain(t *testing.T) {
	base := t.TempDir()
	cache := filepath.Join(base, "cache;with spaces")
	t.Setenv("ProgramData", base)
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("ERIS_BLOCK_PATH", "ignored")
	paths, err := volume.DefaultChainPaths()
	if err != nil {
		t.Fatal(err)
	}
	// Missing default roots must cause an error and must not be created.
	chain, err := volume.OpenChain("")
	if chain != nil || err == nil {
		t.Fatalf("missing default roots: %v, %v", chain, err)
	}
	if _, err := os.Stat(paths[1]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("default cache root was created: %v", err)
	}
	if err := os.MkdirAll(paths[1], 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.MkdirAll(paths[0], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Do not create or change the system volume in Unix tests. If it cannot be
	// opened, check that default construction reports that same failure.
	system, systemErr := volume.Open(paths[0])
	if systemErr != nil {
		chain, err := volume.OpenChain("")
		if chain != nil || err == nil || !strings.Contains(err.Error(), paths[0]) {
			t.Fatalf("system default open failure: %v, %v", chain, err)
		}
		return
	}
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
	chain = openChain(t, "")
	block, ref := testBlock(t, eris.BlockSize1KiB, 92)
	writeFile(t, filepath.Join(paths[1], ref.String()), block)
	got, err, _ := eris.GetBlock(context.Background(), chain, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("read from default cache: length %d, %v", len(got), err)
	}
}
