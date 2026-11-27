package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func TestDumpPrependedAndAppendedStores(t *testing.T) {
	first, second, defaults, last := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", defaults)
	t.Setenv("ERIS_MNT", "default capabilities")
	t.Setenv("ERIS_FS_MNT", "default images")
	want := fmt.Sprintf(`  Block volumes %q
  Capabilities
    Mount roots ["cap 1" "cap 2" "cap 3"]
    Block store
    Mount roots ["default capabilities" "cap 4" "cap 5"]
  ERIS-FS images
    Mount roots ["--help"]
    Capability store
    Mount roots ["default images" "image 2" "image 3"]
`, []string{first, second, defaults, last})
	assertDumpSetup(t, want,
		"--prepend-blocks", first+";"+second, "--prepend-capabilities=cap 1;cap 2", "--append-capabilities=cap 4",
		"dump", "--prepend-capabilities", "cap 3", "--append-blocks="+last, "--append-capabilities=cap 5",
		"--prepend-fs", "--help", "--append-fs=image 2;image 3",
	)
}

func TestDumpReplacedStores(t *testing.T) {
	before, first, second, after := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("ERIS_BLOCK_PATH", ";")
	t.Setenv("ERIS_MNT", "default capabilities")
	t.Setenv("ERIS_FS_MNT", "default images")
	want := fmt.Sprintf(`  Block volumes %q
  Capabilities
    Mount roots ["cap 1" "cap 2" "cap 3" "cap 4"]
  ERIS-FS images
    Mount roots ["image 1" "image 2" "image 3" "image 4"]
`, []string{before, first, second, after})
	assertDumpSetup(t, want,
		"--replace-blocks="+missing, "--append-blocks="+after, "--replace-capabilities=discarded",
		"dump", "--prepend-blocks="+before, "--replace-blocks="+first+";"+second,
		"--append-capabilities=cap 4", "--replace-capabilities=cap 2;cap 3", "--prepend-capabilities=cap 1",
		"--append-fs=image 4", "--replace-fs=image 2;image 3", "--prepend-fs=image 1",
	)
}

func TestDumpDisabledDefaults(t *testing.T) {
	blocks := t.TempDir()
	t.Setenv("ERIS_MNT", "default capabilities")
	t.Setenv("ERIS_FS_MNT", "default images")
	for _, tc := range []struct {
		name   string
		args   []string
		blocks []string
		caps   string
		images string
	}{
		{"blocks", []string{"--replace-blocks="}, nil,
			"    Mount roots [\"default capabilities\"]\n",
			"    Capability store\n    Mount roots [\"default images\"]\n"},
		{"capabilities", []string{"--replace-capabilities="}, []string{blocks},
			"    Mount roots []\n", "    Mount roots [\"default images\"]\n"},
		{"fs", []string{"--replace-fs", ""}, []string{blocks},
			"    Block store\n    Mount roots [\"default capabilities\"]\n", "    Mount roots []\n"},
		{"all", []string{"--replace-blocks=", "--replace-capabilities=", "--replace-fs="}, nil,
			"    Mount roots []\n", "    Mount roots []\n"},
		{"keep explicit stores", []string{"--replace-blocks=", "--append-blocks=" + blocks,
			"--prepend-capabilities=before", "--replace-capabilities=discarded", "--replace-capabilities=",
			"--append-capabilities=after", "--replace-fs=discarded", "--replace-fs=", "--append-fs=after"}, []string{blocks},
			"    Mount roots [\"before\" \"after\"]\n", "    Mount roots [\"after\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blockPath := blocks
			if slices.Contains(tc.args, "--replace-blocks=") {
				// Invalid defaults must not prevent opening the configured stores.
				blockPath = ";"
			}
			t.Setenv("ERIS_BLOCK_PATH", blockPath)
			want := fmt.Sprintf("  Block volumes %q\n  Capabilities\n%s  ERIS-FS images\n%s", tc.blocks, tc.caps, tc.images)
			assertDumpSetup(t, want, append([]string{"dump"}, tc.args...)...)
		})
	}
}

// Check a store operation as well as the output reported by dump.
func TestOpenStorageFlagsBlockWritesAndClose(t *testing.T) {
	before, defaults, after := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", defaults)
	_, paths, err := parseCommand([]string{"dump", "--append-blocks=" + after, "--prepend-blocks=" + before})
	if err != nil {
		t.Fatal(err)
	}
	stores, err := paths.open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Error(err)
		}
	})
	data := make([]byte, eris.BlockSize1KiB)
	ref, err := eris.PutBlock(t.Context(), stores.Blocks, data, eris.NoTTL, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(before, volume.BlockPath(ref))); err != nil {
		t.Fatalf("stat block in prepended volume: %v", err)
	}
	for _, root := range []string{defaults, after} {
		if _, err := os.Stat(filepath.Join(root, volume.BlockPath(ref))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat block in %q = %v, want fs.ErrNotExist", root, err)
		}
	}
	explicitVolumes := stores.volumes
	if err := stores.Close(); err != nil {
		t.Fatal(err)
	}
	for _, chain := range explicitVolumes {
		if _, err, _ := eris.GetBlock(t.Context(), chain, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, fs.ErrClosed) {
			t.Errorf("read from closed volume %q = %v, want fs.ErrClosed", chain.Paths(), err)
		}
	}
}

func TestOpenStorageFlagsMountLookups(t *testing.T) {
	common := eris.Capability{BlockSize: eris.BlockSize1KiB}
	defaultOnly, appendOnly := common, common
	defaultOnly.Root.Reference[0] = 1
	appendOnly.Root.Reference[0] = 2
	if _, err := filepath.Localize(common.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
	before, defaults, replacement, after := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, fixture := range []struct {
		mount string
		caps  []eris.Capability
	}{
		{before, []eris.Capability{common}},
		{defaults, []eris.Capability{common, defaultOnly}},
		{replacement, []eris.Capability{common}},
		{after, []eris.Capability{common, appendOnly}},
	} {
		for _, cap := range fixture.caps {
			if err := os.MkdirAll(filepath.Join(fixture.mount, "fs", cap.String()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.mount, cap.String()), []byte("content"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("ERIS_MNT", defaults)
	t.Setenv("ERIS_FS_MNT", "")
	_, paths, err := parseCommand([]string{
		"dump", "--replace-blocks=", "--prepend-capabilities=" + before, "--append-capabilities=" + after,
		"--replace-fs=" + filepath.Join(replacement, "fs"), "--append-fs=" + filepath.Join(after, "fs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	stores, err := paths.open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()
	for _, tc := range []struct {
		cap   eris.Capability
		mount string
	}{
		{common, before}, {defaultOnly, defaults}, {appendOnly, after},
	} {
		want := filepath.Join(tc.mount, tc.cap.String())
		if path, err := stores.Capabilities.ResolvePath(ctx, tc.cap); err != nil || path != want {
			t.Errorf("capability path = %q, %v, want %q", path, err, want)
		}
	}
	for _, tc := range []struct {
		cap   eris.Capability
		mount string
	}{
		{common, replacement}, {appendOnly, after},
	} {
		image, err := stores.Images.OpenFS(ctx, tc.cap)
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(tc.mount, "fs", tc.cap.String())
		if path, err := image.ResolvePath(ctx, "."); err != nil || path != want {
			t.Errorf("image path = %q, %v, want %q", path, err, want)
		}
	}
	if _, err := stores.Images.OpenFS(ctx, defaultOnly); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("open image from disabled default mount = %v, want fs.ErrNotExist", err)
	}
}

func assertDumpSetup(t *testing.T, want string, args ...string) {
	t.Helper()
	stdout, stderr := captureOutput(t)
	code := run(args)
	if errout := readOutput(t, stderr); code != 0 || errout != "" {
		t.Fatalf("dump exit code = %d, stderr = %q", code, errout)
	}
	out := readOutput(t, stdout)
	_, setup, ok := strings.Cut(out, "\nStorage lookup order\n")
	if !ok {
		t.Fatalf("dump output missing storage setup: %q", out)
	}
	if setup != want {
		t.Errorf("storage setup = %q, want %q", setup, want)
	}
}
