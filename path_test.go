package eris

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

// Use the hello.txt capability from the mount layout compatibility example.
func pathTestCapability(t *testing.T) Capability {
	t.Helper()
	capability, err := ParseURN("urn:eris:BIAIWVV3DW4E3FQXBV2XKFCDU6Q6B4T3EX7WOLGK6UN643IMDLGJ2UWUCFGRXH6B4IRAWJYTVIPJS2WX6ZDSZX6BUPWXU2S34MXQKQ27MI")
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

func writePathTestContent(t *testing.T, mount string, capability Capability) string {
	t.Helper()
	requireCapabilityFileName(t, capability)
	path := filepath.Join(mount, capability.String())
	if err := os.WriteFile(path, []byte("Hello, ERIS!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The mount layout requires literal URNs as file names. For example, Windows
// cannot create these names. Metadata-only tests still run on those hosts.
func requireCapabilityFileName(t *testing.T, capability Capability) {
	t.Helper()
	if _, err := filepath.Localize(capability.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
}

func TestResolvePath(t *testing.T) {
	capability := pathTestCapability(t)
	mount := t.TempDir()
	want := writePathTestContent(t, mount, capability)
	envMount := t.TempDir()
	envWant := writePathTestContent(t, envMount, capability)
	t.Setenv("ERIS_MNT", envMount)

	for _, test := range []struct {
		name   string
		mounts []string
		want   string
	}{
		{"explicit overrides environment", []string{mount}, want},
		{"omitted uses environment", nil, envWant},
		{"empty uses environment", []string{""}, envWant},
		{"missing explicit mount has no fallback", []string{filepath.Join(mount, "missing")}, ""},
		{"invalid mount path", []string{"mount\x00"}, ""},
		{"multiple mounts rejected", []string{mount, envMount}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolvePath(capability, test.mounts...); got != test.want {
				t.Fatalf("ResolvePath() = %q, want %q", got, test.want)
			}
		})
	}
	if got := ResolvePath(Capability{}, mount); got != "" {
		t.Fatalf("invalid capability resolved to %q", got)
	}
	// A root reference alone is not enough: every capability field belongs
	// to the file name used by the mount.
	for field := range 4 {
		other := capability
		switch field {
		case 0:
			other.Root.Reference[0] ^= 1
		case 1:
			other.Root.Key[0] ^= 1
		case 2:
			other.Level++
		case 3:
			other.BlockSize = BlockSize32KiB
		}
		if got := ResolvePath(other, mount); got != "" {
			t.Errorf("different capability field %d resolved to %q", field, got)
		}
	}
}

func TestResolvePathContent(t *testing.T) {
	t.Parallel()
	requireCapabilityFileName(t, pathTestCapability(t))
	mount := t.TempDir()
	for _, size := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
		for _, content := range [][]byte{nil, []byte("Hello, ERIS!\n"), bytes.Repeat([]byte("x"), int(size)+1)} {
			capability, _, err := Encode(content, Secret{}, size)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(mount, capability.String())
			if err := os.WriteFile(want, content, 0o600); err != nil {
				t.Fatal(err)
			}
			got := ResolvePath(capability, mount)
			if got != want {
				t.Fatalf("ResolvePath() = %q, want %q", got, want)
			}
			data, err := os.ReadFile(got)
			if err != nil || !bytes.Equal(data, content) {
				t.Fatalf("resolved content = %q, %v; want %q", data, err, content)
			}
		}
	}
}

func TestResolvePathRelativeMount(t *testing.T) {
	capability := pathTestCapability(t)
	base := t.TempDir()
	// Spaces and semicolons are literal path characters, not list separators.
	mount := filepath.Join(base, "mount; with spaces")
	if err := os.Mkdir(mount, 0o700); err != nil {
		t.Fatal(err)
	}
	want := writePathTestContent(t, mount, capability)
	t.Chdir(base)
	t.Setenv("ERIS_MNT", "mount; with spaces")
	for _, mounts := range [][]string{nil, {"mount; with spaces"}} {
		if got := ResolvePath(capability, mounts...); got != want || !filepath.IsAbs(got) {
			t.Fatalf("ResolvePath(%q) = %q, want absolute path %q", mounts, got, want)
		}
	}
}

func TestResolvePathFileTypes(t *testing.T) {
	capability := pathTestCapability(t)
	requireCapabilityFileName(t, capability)
	for _, kind := range []string{"missing", "directory", "file mount", "symlink", "broken symlink", "directory symlink", "symlink loop", "tree only"} {
		t.Run(kind, func(t *testing.T) {
			mount := t.TempDir()
			path := filepath.Join(mount, capability.String())
			want := ""
			switch kind {
			case "missing":
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file mount":
				mount = writePathTestContent(t, mount, capability)
			case "symlink", "broken symlink", "directory symlink", "symlink loop":
				target := filepath.Join(mount, "target")
				if kind == "symlink" {
					if err := os.WriteFile(target, []byte("Hello, ERIS!\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					want = path
				}
				if kind == "directory symlink" {
					if err := os.Mkdir(target, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "symlink loop" {
					target = path
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symbolic links unavailable: %v", err)
				}
			case "tree only":
				if err := os.MkdirAll(filepath.Join(mount, "fs", capability.String()), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if got := ResolvePath(capability, mount); got != want {
				t.Fatalf("ResolvePath() = %q, want %q", got, want)
			}
		})
	}
}

func TestMountPathThroughSymlinkParent(t *testing.T) {
	c := pathTestCapability(t)
	mount := t.TempDir()
	want := writePathTestContent(t, mount, c)
	child := filepath.Join(mount, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Chdir(work)
	if err := os.Symlink(child, "link"); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"link/..", filepath.Join(work, "link") + "/.."} {
		path, err := NewMountCapabilityStore(root).ResolvePath(t.Context(), c)
		if err != nil || !filepath.IsAbs(path) {
			t.Fatalf("resolve %q = %q, %v", root, path, err)
		}
		info, err := os.Stat(path)
		if err != nil || !os.SameFile(info, wantInfo) {
			t.Fatalf("resolved %q to a different file: %v", root, err)
		}
	}
	// A shell can retain this spelling of the current directory in PWD.
	t.Chdir(mount)
	t.Setenv("PWD", filepath.Join(work, "link")+"/..")
	path := ResolvePath(c, ".")
	info, err := os.Stat(path)
	if err != nil || !os.SameFile(info, wantInfo) {
		t.Fatalf("relative mount resolved to %q: %v", path, err)
	}
}

func TestResolvePathMountSearch(t *testing.T) {
	capability := pathTestCapability(t)
	fixture := t.TempDir()
	// Use portable metadata fixtures so these tests also run on hosts that
	// cannot create literal capability names.
	metadata := fstest.MapFS{
		"file": {Mode: 0o600},
		"dir":  {Mode: fs.ModeDir | 0o700},
		"pipe": {Mode: fs.ModeNamedPipe | 0o600},
	}
	regular, err := fs.Stat(metadata, "file")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := fs.Stat(metadata, "dir")
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := fs.Stat(metadata, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	absolute := func(mount string) string {
		t.Helper()
		path, err := filepath.Abs(filepath.Join(mount, capability.String()))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	first, second := absolute("/n/eris"), absolute("/eris")
	custom := absolute(filepath.Join(fixture, "custom"))

	for _, test := range []struct {
		name      string
		env       string
		unset     bool
		mounts    []string
		found     string
		firstInfo fs.FileInfo
		firstErr  error
		want      string
		calls     []string
	}{
		{name: "unset prefers first", unset: true, found: first, want: first, calls: []string{first}},
		{name: "empty prefers first", found: first, want: first, calls: []string{first}},
		{name: "empty argument uses defaults", mounts: []string{""}, found: first, want: first, calls: []string{first}},
		{name: "missing first", found: second, want: second, calls: []string{first, second}},
		{name: "permission error at first", firstErr: fs.ErrPermission, found: second, want: second, calls: []string{first, second}},
		{name: "directory at first", firstInfo: directory, found: second, want: second, calls: []string{first, second}},
		{name: "special file at first", firstInfo: pipe, found: second, want: second, calls: []string{first, second}},
		{name: "neither found", calls: []string{first, second}},
		{name: "environment has no fallback", env: filepath.Dir(custom), found: second, calls: []string{custom}},
		{name: "explicit has no fallback", mounts: []string{filepath.Dir(custom)}, found: second, calls: []string{custom}},
		{name: "multiple mounts do no lookup", mounts: []string{"/n/eris", "/eris"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ERIS_MNT", test.env)
			if test.unset {
				if err := os.Unsetenv("ERIS_MNT"); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			stat := func(path string) (fs.FileInfo, error) {
				calls = append(calls, path)
				if path == first && (test.firstInfo != nil || test.firstErr != nil) {
					return test.firstInfo, test.firstErr
				}
				if path == test.found {
					return regular, nil
				}
				return nil, fs.ErrNotExist
			}
			if got := resolvePath(capability, test.mounts, stat); got != test.want {
				t.Errorf("resolvePath() = %q, want %q", got, test.want)
			}
			if !slices.Equal(calls, test.calls) {
				t.Errorf("lookup paths = %q, want %q", calls, test.calls)
			}
		})
	}
}

func TestResolvePathMissingWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Skipf("cannot remove the current directory: %v", err)
	}
	path, err := NewMountCapabilityStore(".").ResolvePath(t.Context(), pathTestCapability(t))
	if path != "" || !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrMissingBlock) {
		t.Fatalf("relative mount lookup = %q, %v", path, err)
	}
}

func TestMountSearchReportsMissingOnlyWhenAllPathsAreAbsent(t *testing.T) {
	t.Setenv("ERIS_MNT", "")
	c := pathTestCapability(t)
	for _, tc := range []struct {
		name          string
		first, second error
		wantMissing   bool
	}{
		{"both absent", fs.ErrNotExist, fs.ErrNotExist, true},
		{"first inaccessible", fs.ErrPermission, fs.ErrNotExist, false},
		{"second inaccessible", fs.ErrNotExist, fs.ErrPermission, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures := []error{tc.first, tc.second}
			path, err := resolveMountPath(c, "", func(string) (fs.FileInfo, error) {
				if len(failures) == 0 {
					t.Fatal("unexpected third mount lookup")
				}
				err := failures[0]
				failures = failures[1:]
				return nil, err
			})
			if len(failures) != 0 || path != "" || !errors.Is(err, tc.first) || !errors.Is(err, tc.second) ||
				errors.Is(err, ErrMissingBlock) != tc.wantMissing {
				t.Fatalf("resolve = %q, %v, want missing = %t", path, err, tc.wantMissing)
			}
		})
	}
}
