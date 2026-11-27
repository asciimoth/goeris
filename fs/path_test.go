package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

// Use the index capability from the mount layout compatibility example.
func pathTestCapability(t *testing.T) eris.Capability {
	t.Helper()
	capability, err := eris.ParseURN("urn:eris:BIACGBUGVO7DG6RBLO75M3WY6JUXSVOKCD2C3NRSPKJKVHFZO6XBARY5ZN56UT4GIKHWGFKTUYQO7WFCNSRII5HBF42XLXZCUQSPQXTYCU")
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

// makePathTestTree represents the decoded tree exposed by a compatible mount.
func makePathTestTree(t *testing.T, mount string, capability eris.Capability) string {
	t.Helper()
	// Windows cannot create literal URN file names. Tests that use only
	// metadata fixtures still run there.
	if _, err := filepath.Localize(capability.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
	root := filepath.Join(mount, capability.String())
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"hello.txt": "Hello, ERIS!\n", "docs/readme.txt": "ERIS-FS example\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type pathLookupStore struct {
	eris.CapabilityStore
	lookup func(context.Context, eris.Capability) (string, error)
}

func (s pathLookupStore) ResolvePath(ctx context.Context, capability eris.Capability) (string, error) {
	return s.lookup(ctx, capability)
}

func TestFSResolvePathStoreLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hostPath := filepath.Join(t.TempDir(), "content")
	blocks, _ := mountFixture(t)
	store := pathLookupStore{
		CapabilityStore: eris.NewBlockCapabilityStore(blocks),
		lookup: func(got context.Context, capability eris.Capability) (string, error) {
			if got != ctx {
				t.Fatal("lookup did not receive the caller's context")
			}
			switch capability.String() {
			case mountHello:
				return hostPath, nil
			case mountReadme:
				return "", eris.ErrMissingBlock
			default:
				t.Fatalf("unexpected lookup for %v", capability)
				return "", nil
			}
		},
	}
	image, err := NewStore(store, eris.BlockOptions{}).OpenFS(t.Context(), pathTestCapability(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := image.ResolvePath(ctx, "latest.txt"); got != hostPath || err != nil {
		t.Fatalf("ResolvePath through link = (%q, %v), want %q", got, err, hostPath)
	}
	const missing = "docs/readme.txt"
	got, err := image.ResolvePath(ctx, missing)
	var pe *stdfs.PathError
	if got != "" || !errors.As(err, &pe) || pe.Op != "resolvepath" || pe.Path != missing ||
		!errors.Is(err, eris.ErrMissingBlock) || errors.Is(err, ErrNoHostPath) {
		t.Fatalf("ResolvePath(%q) = (%q, %v), want missing content", missing, got, err)
	}
}

func TestFSResolvePathErrors(t *testing.T) {
	ctx := t.Context()
	root, blocks := contextImage(t)
	image, err := OpenFS(ctx, root, eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		ctx        context.Context
		name       string
		want       error
		noHostPath bool
	}{
		{ctx, ".", ErrNoHostPath, true},
		{ctx, "nested", ErrNoHostPath, true},
		{ctx, "nested/compressed", ErrNoHostPath, true},
		{ctx, "nested/link", errors.ErrUnsupported, true},
		{ctx, "nested/missing", stdfs.ErrNotExist, false},
		{ctx, "../file", stdfs.ErrInvalid, false},
		{canceled, "nested/file", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := image.ResolvePath(tc.ctx, tc.name)
			var pe *stdfs.PathError
			if got != "" || !errors.As(err, &pe) || pe.Op != "resolvepath" || pe.Path != tc.name {
				t.Fatalf("ResolvePath(%q) = (%q, %v)", tc.name, got, err)
			}
			if !errors.Is(err, tc.want) || errors.Is(err, ErrNoHostPath) != tc.noHostPath {
				t.Fatalf("got %v, want %v (ErrNoHostPath = %t)", err, tc.want, tc.noHostPath)
			}
			if tc.want == ErrNoHostPath && errors.Is(err, errors.ErrUnsupported) {
				t.Fatal("entry was passed to the block store")
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	capability := pathTestCapability(t)
	base := t.TempDir()
	mount := filepath.Join(base, "fs")
	root := makePathTestTree(t, mount, capability)
	if got := ResolveRoot(capability, mount); got != root {
		t.Fatalf("ResolveRoot() = %q, want %q", got, root)
	}
	for _, name := range []string{".", "docs", "hello.txt", "docs/readme.txt"} {
		want := filepath.Join(root, filepath.FromSlash(name))
		if got := ResolvePath(capability, name, mount); got != want || !filepath.IsAbs(got) {
			t.Errorf("ResolvePath(%q) = %q, want %q", name, got, want)
		}
	}
	got := ResolvePath(capability, "docs/readme.txt", mount)
	if data, err := os.ReadFile(got); err != nil || string(data) != "ERIS-FS example\n" {
		t.Fatalf("read resolved file = %q, %v", data, err)
	}
	if got := ResolveRoot(capability, base); got != "" {
		t.Errorf("mount argument must not append /fs: got %q", got)
	}
	for _, name := range []string{"missing", "docs/missing", "hello.txt/child"} {
		if got := ResolvePath(capability, name, mount); got != "" {
			t.Errorf("missing entry %q resolved to %q", name, got)
		}
	}
	other := capability
	other.Root.Key[0] ^= 1
	if got := ResolveRoot(other, mount); got != "" {
		t.Errorf("different capability resolved to %q", got)
	}
	// Decoded index bytes at M/C are not an ERIS-FS root directory.
	if err := os.WriteFile(filepath.Join(base, capability.String()), []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveRoot(capability, base); got != "" {
		t.Errorf("regular file accepted as image root: %q", got)
	}
}

func TestResolvePathEnvironment(t *testing.T) {
	capability := pathTestCapability(t)
	base := t.TempDir()
	fsMount := filepath.Join(base, "trees; with spaces")
	erisMount := filepath.Join(base, "eris")
	explicit := filepath.Join(base, "explicit")
	fsRoot := makePathTestTree(t, fsMount, capability)
	erisRoot := makePathTestTree(t, filepath.Join(erisMount, "fs"), capability)
	explicitRoot := makePathTestTree(t, explicit, capability)
	t.Setenv("ERIS_FS_MNT", fsMount)
	t.Setenv("ERIS_MNT", erisMount)
	for _, mounts := range [][]string{nil, {""}, {explicit}} {
		want := fsRoot
		if len(mounts) == 1 && mounts[0] != "" {
			want = explicitRoot
		}
		if got := ResolveRoot(capability, mounts...); got != want {
			t.Errorf("ResolveRoot(%q) = %q, want %q", mounts, got, want)
		}
	}
	t.Setenv("ERIS_FS_MNT", "")
	if got := ResolveRoot(capability); got != erisRoot {
		t.Errorf("ERIS_MNT root = %q, want %q", got, erisRoot)
	}
	t.Chdir(base)
	t.Setenv("ERIS_MNT", "eris")
	if got := ResolvePath(capability, "docs"); got != filepath.Join(erisRoot, "docs") {
		t.Errorf("relative ERIS_MNT path = %q", got)
	}
	t.Setenv("ERIS_FS_MNT", "trees; with spaces")
	if got := ResolveRoot(capability); got != fsRoot {
		t.Errorf("relative ERIS_FS_MNT root = %q, want %q", got, fsRoot)
	}
	if got := ResolveRoot(capability, "explicit"); got != explicitRoot {
		t.Errorf("relative explicit root = %q, want %q", got, explicitRoot)
	}
}

func TestResolvePathInvalidArguments(t *testing.T) {
	t.Parallel()
	capability := pathTestCapability(t)
	noLookup := func(path string) (stdfs.FileInfo, error) {
		t.Errorf("invalid argument caused lookup of %q", path)
		return nil, stdfs.ErrNotExist
	}
	for _, name := range []string{"", "/", "/file", "..", "../file", "docs/../file", "docs/./file", "./docs", "docs//file", "docs/", "file\x00", "\xff"} {
		if got := resolvePath(capability, name, nil, noLookup); got != "" {
			t.Errorf("invalid name %q resolved to %q", name, got)
		}
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{`..\file`, `docs\file`, `C:/file`, "NUL"} {
			if got := resolvePath(capability, name, nil, noLookup); got != "" {
				t.Errorf("invalid native name %q resolved to %q", name, got)
			}
		}
	}
	if got := resolvePath(eris.Capability{}, ".", nil, noLookup); got != "" {
		t.Errorf("invalid capability resolved to %q", got)
	}
	if got := resolvePath(capability, ".", []string{"one", "two"}, noLookup); got != "" {
		t.Errorf("multiple mounts resolved to %q", got)
	}
}

func TestResolvePathSymlinks(t *testing.T) {
	t.Parallel()
	capability := pathTestCapability(t)
	mount := t.TempDir()
	root := makePathTestTree(t, mount, capability)
	for name, target := range map[string]string{
		"latest.txt": "hello.txt", "alias": "docs", "broken": "missing", "loop": "loop",
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
	}
	for _, name := range []string{"latest.txt", "alias", "alias/readme.txt", "broken", "loop"} {
		want := filepath.Join(root, filepath.FromSlash(name))
		if name == "broken" || name == "loop" {
			want = ""
		}
		if got := ResolvePath(capability, name, mount); got != want {
			t.Errorf("ResolvePath(%q) = %q, want %q", name, got, want)
		}
	}
	aliasMount := filepath.Join(t.TempDir(), "mount")
	if err := os.Symlink(mount, aliasMount); err != nil {
		t.Fatal(err)
	}
	if got := ResolveRoot(capability, aliasMount); got != filepath.Join(aliasMount, capability.String()) {
		t.Errorf("symbolic link mount resolved to %q", got)
	}
}

func TestResolvePathThroughSymlinkParent(t *testing.T) {
	capability := pathTestCapability(t)
	base := t.TempDir()
	makePathTestTree(t, filepath.Join(base, "fs"), capability)
	child := filepath.Join(base, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.Symlink(child, "link"); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	for _, tc := range []struct{ fsMount, erisMount string }{
		{"link/../fs", ""}, {"", "link/.."},
	} {
		t.Setenv("ERIS_FS_MNT", tc.fsMount)
		t.Setenv("ERIS_MNT", tc.erisMount)
		path := ResolvePath(capability, "hello.txt")
		data, err := os.ReadFile(path)
		if err != nil || !filepath.IsAbs(path) || string(data) != "Hello, ERIS!\n" {
			t.Errorf("mounts %q, %q resolved to %q: read %q, %v", tc.fsMount, tc.erisMount, path, data, err)
		}
	}
}

func TestResolvePathDefaultSearch(t *testing.T) {
	capability := pathTestCapability(t)
	// Metadata fixtures include a special file to check that only regular
	// files and directories can be returned. No system paths are modified.
	metadata := fstest.MapFS{
		"file": {Mode: 0o600},
		"dir":  {Mode: stdfs.ModeDir | 0o700},
		"pipe": {Mode: stdfs.ModeNamedPipe | 0o600},
	}
	for _, test := range []struct {
		name      string
		entry     string
		unset     bool
		firstType string
		firstErr  error
		lastType  string
		wantIndex int
	}{
		{name: "unset root first", entry: ".", unset: true, firstType: "dir", wantIndex: 0},
		{name: "empty root first", entry: ".", firstType: "dir", wantIndex: 0},
		{name: "root second", entry: ".", lastType: "dir", wantIndex: 1},
		{name: "index file rejected", entry: ".", firstType: "file", lastType: "dir", wantIndex: 1},
		{name: "file first", entry: "docs/readme.txt", firstType: "file", wantIndex: 0},
		{name: "directory second", entry: "docs", lastType: "dir", wantIndex: 1},
		{name: "file second", entry: "docs/readme.txt", lastType: "file", wantIndex: 1},
		{name: "permission error", entry: ".", firstErr: stdfs.ErrPermission, lastType: "dir", wantIndex: 1},
		{name: "special file rejected", entry: "pipe", firstType: "pipe", wantIndex: -1},
		{name: "both missing", entry: ".", wantIndex: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []string{"ERIS_FS_MNT", "ERIS_MNT"} {
				t.Setenv(key, "")
				if test.unset {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			var paths []string
			for _, mount := range []string{"/n/eris/fs", "/eris/fs"} {
				path, err := filepath.Abs(filepath.Join(mount, capability.String(), filepath.FromSlash(test.entry)))
				if err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			var calls []string
			stat := func(path string) (stdfs.FileInfo, error) {
				calls = append(calls, path)
				kind := test.lastType
				if path == paths[0] {
					kind = test.firstType
					if test.firstErr != nil {
						return nil, test.firstErr
					}
				}
				return stdfs.Stat(metadata, kind)
			}
			want := ""
			wantCalls := paths
			if test.wantIndex >= 0 {
				want = paths[test.wantIndex]
				wantCalls = paths[:test.wantIndex+1]
			}
			if got := resolvePath(capability, test.entry, nil, stat); got != want {
				t.Errorf("resolvePath() = %q, want %q", got, want)
			}
			if !slices.Equal(calls, wantCalls) {
				t.Errorf("lookup paths = %q, want %q", calls, wantCalls)
			}
		})
	}
}

func TestResolvePathConfiguredMountHasNoFallback(t *testing.T) {
	capability := pathTestCapability(t)
	base := t.TempDir()
	fsMount := filepath.Join(base, "trees")
	erisMount := filepath.Join(base, "eris")
	explicit := filepath.Join(base, "explicit")
	info, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		fsEnv   string
		erisEnv string
		mounts  []string
		want    string
	}{
		{"explicit", fsMount, erisMount, []string{explicit}, explicit},
		{"fs environment", fsMount, erisMount, nil, fsMount},
		{"empty argument", fsMount, erisMount, []string{""}, fsMount},
		{"eris environment", "", erisMount, nil, filepath.Join(erisMount, "fs")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ERIS_FS_MNT", test.fsEnv)
			t.Setenv("ERIS_MNT", test.erisEnv)
			for _, name := range []string{".", "docs/readme.txt"} {
				want := filepath.Join(test.want, capability.String(), filepath.FromSlash(name))
				var calls []string
				stat := func(path string) (stdfs.FileInfo, error) {
					calls = append(calls, path)
					if path == want {
						return nil, stdfs.ErrNotExist
					}
					// Other mounts would succeed if incorrectly searched.
					return info, nil
				}
				if got := resolvePath(capability, name, test.mounts, stat); got != "" {
					t.Errorf("missing configured path resolved to %q", got)
				}
				if !slices.Equal(calls, []string{want}) {
					t.Errorf("lookup paths = %q, want only %q", calls, want)
				}
			}
		})
	}
}

func TestResolveRootMissingWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Skipf("cannot remove current directory: %v", err)
	}
	stat := func(path string) (stdfs.FileInfo, error) {
		t.Fatalf("attempted lookup at %q after removal of the working directory", path)
		return nil, stdfs.ErrNotExist
	}
	if got := resolvePath(pathTestCapability(t), ".", []string{"."}, stat); got != "" {
		t.Fatalf("resolved root to %q", got)
	}
}
