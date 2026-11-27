package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

func TestMountStoreSelection(t *testing.T) {
	cap := pathTestCapability(t)
	base := t.TempDir()
	explicit := makePathTestTree(t, filepath.Join(base, "explicit"), cap)
	trees := makePathTestTree(t, filepath.Join(base, "trees"), cap)
	general := makePathTestTree(t, filepath.Join(base, "eris", "fs"), cap)
	t.Chdir(base)
	for _, tc := range []struct{ mount, fsEnv, env, want string }{
		{"explicit", "trees", "eris", explicit}, {"", "trees", "eris", trees}, {"", "", "eris", general},
		{"missing", "trees", "eris", ""}, {"", "missing", "eris", ""},
	} {
		t.Setenv("ERIS_FS_MNT", tc.fsEnv)
		t.Setenv("ERIS_MNT", tc.env)
		image, err := NewMountStore(tc.mount).OpenFS(t.Context(), cap)
		if tc.want == "" {
			if !errors.Is(err, stdfs.ErrNotExist) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := image.ResolvePath(t.Context(), ".")
		if got != tc.want || err != nil {
			t.Fatal(got, err)
		}
	}
	t.Setenv("ERIS_FS_MNT", "")
	t.Setenv("ERIS_MNT", "")
	if got := NewMountStore("").roots(); !slices.Equal(got, []string{"/n/eris/fs", "/eris/fs"}) {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(base, cap.String()), nil, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewMountStore(base)
	if _, err := store.OpenFS(t.Context(), cap); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := store.OpenFS(t.Context(), eris.Capability{}); !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.OpenFS(ctx, cap); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMountedOperations(t *testing.T) {
	cap := pathTestCapability(t)
	mount := t.TempDir()
	root := makePathTestTree(t, mount, cap)
	other := cap
	other.Root.Reference[0] ^= 1
	makePathTestTree(t, mount, other)
	host := t.TempDir()
	writePathTestTree(t, host)
	links := map[string]string{
		"alias":         "docs",
		"docs/up":       "../hello.txt",
		"escape":        "../outside",
		"broken":        "missing",
		"broken-child":  "hello.txt/child",
		"broken-safe":   "docs/../missing",
		"broken-escape": "missing/../../outside",
		"unsafe":        "C:/file",
		"unsafe-chain":  "unsafe",
		"device":        "docs/nuL.txt",
		"broken-chain":  "broken-escape",
		"safe-chain":    "broken-safe",
		"sibling":       "../" + other.String() + "/hello.txt",
		"host":          filepath.Join(host, "hello.txt"),
		"indirect":      "alias/../sibling",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Skip(err)
		}
	}
	ctx := t.Context()
	store := NewMountStore(mount)
	image, err := store.OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub(ctx, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := sub.Lstat(ctx, "."); err != nil || !info.IsDir() {
		t.Fatal(info, err)
	}
	if target, err := sub.ReadLink(ctx, "up"); err == nil || target != "" {
		t.Fatalf("Sub exposed link outside the selected directory: %q, %v", target, err)
	}
	if target, err := image.ReadLink(ctx, "alias/up"); err != nil || target != "../hello.txt" {
		t.Fatalf("image ReadLink = %q, %v", target, err)
	}
	if data, err := sub.ReadFile(ctx, "up"); err != nil || string(data) != "Hello, ERIS!\n" {
		t.Fatal(string(data), err)
	}
	for _, name := range []string{".", "alias", "docs/readme.txt", "docs/up"} {
		got, err := image.ResolvePath(ctx, name)
		if err != nil || got != filepath.Join(root, name) {
			t.Fatal(got, err)
		}
	}
	for _, name := range []string{"escape", "broken", "missing", "../hello.txt", "docs//up"} {
		if got, err := image.ResolvePath(ctx, name); err == nil || got != "" {
			t.Fatal(name, got, err)
		}
	}
	for _, name := range []string{"escape", "unsafe", "unsafe-chain", "device", ".", "missing", "broken-escape", "broken-chain"} {
		if target, err := image.ReadLink(ctx, name); err == nil || target != "" {
			t.Errorf("ReadLink(%q) = %q, %v", name, target, err)
		}
	}
	for _, name := range []string{"sibling", "host", "indirect"} {
		if data, err := image.ReadFile(ctx, name); err == nil || data != nil {
			t.Errorf("ReadFile(%q) = %q, %v", name, data, err)
		}
		if target, err := image.ReadLink(ctx, name); err == nil || target != "" {
			t.Errorf("ReadLink(%q) = %q, %v", name, target, err)
		}
		if path, err := image.ResolvePath(ctx, name); err == nil || path != "" {
			t.Errorf("ResolvePath(%q) = %q, %v", name, path, err)
		}
	}
	for _, name := range []string{"broken", "broken-child", "broken-safe", "safe-chain"} {
		want := links[name]
		if target, err := image.ReadLink(ctx, name); err != nil || target != want {
			t.Errorf("ReadLink(%q) = %q, %v, want %q", name, target, err, want)
		}
	}
	for _, name := range []string{"hello.txt", "missing"} {
		if _, err := image.Sub(ctx, name); err == nil {
			t.Fatal(name)
		}
	}
	fileCtx, cancel := context.WithCancel(ctx)
	file, err := image.OpenFile(fileCtx, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, ok := file.(io.Writer); ok {
		t.Fatal("mount exposed write operations")
	}
	if n, err := file.Seek(7, io.SeekStart); err != nil || n != 7 {
		t.Fatal(n, err)
	}
	var buf [4]byte
	if _, err := file.Read(buf[:]); err != nil || string(buf[:]) != "ERIS" {
		t.Fatal(buf, err)
	}
	if _, err := file.ReadAt(buf[:], 0); err != nil || string(buf[:]) != "Hell" {
		t.Fatal(buf, err)
	}
	dir, err := image.Open(fileCtx, "docs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	rd := dir.(stdfs.ReadDirFile)
	if entries, err := rd.ReadDir(-1); err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	cancel()
	if _, err := file.Read(nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := file.ReadAt(nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := rd.ReadDir(1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := image.Stat(fileCtx, "."); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := image.ReadFile(ctx, "."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := image.Open(ctx, "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := image.Prefetch(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	if _, err := image.ReadDir(ctx, "."); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestMountedHandleCancellation(t *testing.T) {
	root := t.TempDir()
	writePathTestTree(t, root)
	image := &mountedImage{root: root, prefix: "."}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	file, err := NewIOFS(ctx, image).OpenFile("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	cancel()
	if _, err := file.Stat(); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat after cancellation = %v", err)
	}
	if _, err := file.Seek(1, io.SeekStart); !errors.Is(err, context.Canceled) {
		t.Errorf("Seek after cancellation = %v", err)
	}
}

func TestMountStoreRejectsSymlinkImageRoot(t *testing.T) {
	ctx := t.Context()
	cap := pathTestCapability(t)
	mount := t.TempDir()
	root := makePathTestTree(t, mount, cap)
	store := NewMountStore(mount)
	image, err := store.OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub(ctx, "docs")
	if err != nil {
		t.Fatal(err)
	}
	other := cap
	other.Root.Reference[0] ^= 1
	makePathTestTree(t, mount, other)
	host := t.TempDir()
	writePathTestTree(t, host)
	if err := os.Rename(root, root+".saved"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{other.String(), host} {
		if err := os.Symlink(target, root); err != nil {
			t.Skipf("cannot create directory symlink: %v", err)
		}
		if _, err := store.OpenFS(ctx, cap); !errors.Is(err, stdfs.ErrInvalid) {
			t.Errorf("OpenFS on linked root = %v", err)
		}
		if data, err := image.ReadFile(ctx, "hello.txt"); err == nil || data != nil {
			t.Errorf("ReadFile on replaced root = %q, %v", data, err)
		}
		if path, err := sub.ResolvePath(ctx, "readme.txt"); err == nil || path != "" {
			t.Errorf("ResolvePath on replaced root = %q, %v", path, err)
		}
		if err := os.Remove(root); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMountedOperationErrors(t *testing.T) {
	root := t.TempDir()
	writePathTestTree(t, root)
	image := &mountedImage{root: root, prefix: "."}
	for _, name := range []string{".", "docs/readme.txt", "../hello.txt", `docs\readme.txt`, "C:/file", "NUL"} {
		checkMountedPath(t, image, root, name)
	}
	//nolint:staticcheck // Check the same nil-context contract as FS.
	if _, err := image.Stat(nil, "."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("Stat with nil context: %v", err)
	}
	//nolint:staticcheck // Check the public mount lookup.
	if _, err := NewMountStore(root).OpenFS(nil, pathTestCapability(t)); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("OpenFS with nil context: %v", err)
	}
	for _, name := range []string{"hello.txt", "missing", "docs//file"} {
		_, err := image.ReadDir(t.Context(), name)
		var pe *stdfs.PathError
		if !errors.As(err, &pe) || pe.Op != "readdir" || pe.Path != name {
			t.Fatalf("ReadDir(%q): %v", name, err)
		}
		if name == "hello.txt" && !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatalf("ReadDir on a regular file: %v", err)
		}
	}
}

func TestMountedIndexOperations(t *testing.T) {
	store := NewMountStore("")
	ctx := t.Context()
	cap := pathTestCapability(t)
	if _, err := store.DecodeIndex(ctx, cap); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
	for _, limit := range []int64{-1, 0, 1, DefaultMaxIndexSize} {
		want := errors.ErrUnsupported
		if limit <= 0 {
			want = ErrInvalidLimit
		}
		if _, err := store.DecodeIndexWithLimit(ctx, cap, limit); !errors.Is(err, want) {
			t.Fatal(err)
		}
		if _, err := store.OpenFSWithLimit(ctx, cap, limit); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	if _, err := store.EncodeIndex(ctx, Index{}, eris.Secret{}); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := store.NewBuilder(eris.Secret{}); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatal(err)
	}
}

func TestMountedAdapter(t *testing.T) {
	cap := pathTestCapability(t)
	base := t.TempDir()
	host := makePathTestTree(t, base, cap)
	image, err := NewMountStore(base).OpenFS(t.Context(), cap)
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(NewIOFS(t.Context(), image), "hello.txt", "docs/readme.txt"); err != nil {
		t.Fatal(err)
	}
	mount := withMountCache(t, NewMountWithStore(t.Context(), NewMountStore(base), nil, eris.BlockOptions{}), 2, 0)
	root := "fs/" + strings.ToUpper(cap.String())
	sub, err := mount.Sub(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(sub, "docs/readme.txt", "hello.txt"); err != nil {
		t.Fatal(err)
	}
	checkMountListing(t, mount, ".", "fs")
	checkMountListing(t, mount, "fs", cap.String())
	if err := fstest.TestFS(mount, "fs/"+cap.String()+"/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(host, host+".old"); err != nil {
		t.Fatal(err)
	}
	entries, err := mount.ReadDir("fs")
	if err != nil || len(entries) != 1 || entries[0].Name() != cap.String() {
		t.Fatalf("cached listing after unmount = %v, %v", entries, err)
	}
	if _, err := entries[0].Info(); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("metadata after unmount = %v", err)
	}
}

func TestMountStoreLookupErrors(t *testing.T) {
	mount := t.TempDir()
	cap := pathTestCapability(t)
	root := makePathTestTree(t, mount, cap)
	ctx := t.Context()
	backend := NewMountStore(mount)
	view, err := backend.OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	// A socket is visible to Stat, but it cannot represent an ERIS-FS file.
	sock := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", sock)
	// Some hosts limit Unix socket paths to fewer bytes than a capability URN.
	if err != nil {
		t.Chdir(root)
		listener, err = net.Listen("unix", "socket")
	}
	if err == nil {
		defer func() { _ = listener.Close() }()
		if _, err := view.ResolvePath(ctx, "socket"); !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := view.ReadDir(ctx, "socket"); !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatal(err)
		}
	}
	adapter := NewMountWithStore(ctx, backend, nil, eris.BlockOptions{})
	sub, err := adapter.Sub("fs/" + cap.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Open("."); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestMountedUnreadableFile(t *testing.T) {
	cap := pathTestCapability(t)
	mount := t.TempDir()
	root := makePathTestTree(t, mount, cap)
	file := filepath.Join(root, "hello.txt")
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(file); err == nil {
		_ = f.Close()
		t.Skip("host can read a mode-zero file")
	}
	image, err := NewMountStore(mount).OpenFS(t.Context(), cap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := image.OpenFile(t.Context(), "hello.txt"); !errors.Is(err, stdfs.ErrPermission) {
		t.Fatal(err)
	}
}

// Compare mounted lookups and reads with the host view across arbitrary
// io/fs names, including malformed bytes, parent components and Sub paths.
func FuzzMountedImage(f *testing.F) {
	// Exercise native path rules even on hosts that cannot name a directory
	// with a capability URN. Mount selection has separate integration tests.
	root := f.TempDir()
	writePathTestTree(f, root)
	image := &mountedImage{root: root, prefix: "."}
	for _, name := range []string{".", "hello.txt", "docs", "docs/readme.txt", "readme.txt", "../hello.txt", "docs/./readme.txt", "hello.txt/child", "", "\x00", "\xff", "/etc/passwd", "docs//readme.txt", `docs\readme.txt`, "NUL", "C:/file"} {
		f.Add(name, false)
		f.Add(name, true)
	}
	f.Fuzz(func(t *testing.T, name string, sub bool) {
		if len(name) > 8192 {
			t.Skip()
		}
		if sub {
			view, err := image.Sub(t.Context(), "docs")
			if err != nil {
				t.Fatal(err)
			}
			checkMountedPath(t, view, filepath.Join(root, "docs"), name)
			return
		}
		checkMountedPath(t, image, root, name)
	})
}

func checkMountedPath(t *testing.T, image FileSystem, dir, name string) {
	t.Helper()
	ctx := t.Context()
	local, localErr := filepath.Localize(name)
	host := os.DirFS(dir)
	want, err := stdfs.Stat(host, name)
	valid := localErr == nil && err == nil
	path, err := image.ResolvePath(ctx, name)
	if valid != (err == nil) {
		t.Fatalf("ResolvePath(%q) = %q, %v", name, path, err)
	}
	if valid && path != filepath.Join(dir, local) {
		t.Fatal(path)
	}
	info, err := image.Lstat(ctx, name)
	if valid != (err == nil) {
		t.Fatal(name, info, err)
	}
	if valid && (info.IsDir() != want.IsDir() || info.Size() != want.Size()) {
		t.Fatal(name, info, want)
	}
	data, err := image.ReadFile(ctx, name)
	if valid && !want.IsDir() {
		expected, readErr := os.ReadFile(path)
		if err != nil || readErr != nil || !bytes.Equal(data, expected) {
			t.Fatal(name, data, err, readErr)
		}
	} else if err == nil {
		t.Fatal("read invalid file", name)
	}
	entries, err := image.ReadDir(ctx, name)
	if (valid && want.IsDir()) != (err == nil) {
		t.Fatal(name, entries, err)
	}
}

func TestMountStoreRelativeRoot(t *testing.T) {
	cap := pathTestCapability(t)
	base := t.TempDir()
	makePathTestTree(t, filepath.Join(base, "fs"), cap)
	child := filepath.Join(base, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.Symlink(child, "link"); err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct{ root, fsEnv, env string }{{"link/../fs", "", ""}, {"", "link/../fs", ""}, {"", "", "link/.."}} {
		t.Setenv("ERIS_FS_MNT", tc.fsEnv)
		t.Setenv("ERIS_MNT", tc.env)
		image, err := NewMountStore(tc.root).OpenFS(t.Context(), cap)
		if err != nil {
			t.Fatal(err)
		}
		path, err := image.ResolvePath(t.Context(), "docs/readme.txt")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "ERIS-FS example\n" {
			t.Fatal(path, string(data), err)
		}
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Skip(err)
	}
	if _, err := NewMountStore(".").OpenFS(t.Context(), cap); err == nil {
		t.Fatal("opened a missing relative root")
	}
}

type openOnlyImage struct{ FileSystem }
type plainFile struct{ stdfs.File }

func (i openOnlyImage) Open(ctx context.Context, name string) (stdfs.File, error) {
	file, err := i.FileSystem.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	return plainFile{file}, nil
}

func TestMountAliasMetadata(t *testing.T) {
	cap := pathTestCapability(t)
	mount := t.TempDir()
	makePathTestTree(t, mount, cap)
	view, err := NewMountStore(mount).OpenFS(t.Context(), cap)
	if err != nil {
		t.Fatal(err)
	}
	for i, base := range []FileSystem{view, openOnlyImage{view}} {
		named := namedImageRoot{FileSystem: base, name: "alias"}
		file, err := named.Open(t.Context(), ".")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.(stdfs.ReadDirFile).ReadDir(-1); (i == 1 && !errors.Is(err, errors.ErrUnsupported)) || (i == 0 && err != nil) {
			t.Fatal(err)
		}
		info, err := file.Stat()
		if err != nil || info.Name() != "alias" {
			t.Fatal(info, err)
		}
		root, err := view.Stat(t.Context(), ".")
		if err != nil || root.Mode() != info.Mode() {
			t.Fatal(root, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Stat(); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatal(err)
		}
	}
}

func TestMountStoreFallbackWithinImage(t *testing.T) {
	ctx := t.Context()
	cap := pathTestCapability(t)
	first := t.TempDir()
	second := t.TempDir()
	a := makePathTestTree(t, first, cap)
	b := makePathTestTree(t, second, cap)
	if err := os.Remove(filepath.Join(a, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	view, err := openMountedTrees(ctx, cap, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{".": a, "hello.txt": filepath.Join(b, "hello.txt"), "docs": filepath.Join(a, "docs")} {
		if got, err := view.ResolvePath(ctx, name); err != nil || got != want {
			t.Fatal(name, got, err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := view.Prefetch(canceled, "."); !errors.Is(err, context.Canceled) {
		t.Fatalf("Prefetch with canceled context returned %v", err)
	}
	view, err = openMountedTrees(ctx, cap, []string{filepath.Join(first, "absent"), second})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.ResolvePath(ctx, "."); err != nil || got != b {
		t.Fatal(got, err)
	}
}

// Record mount metadata requests independently of directory enumeration.
type mountStatTrace struct {
	fstest.MapFS
	paths []string
}

func (s *mountStatTrace) Stat(name string) (stdfs.FileInfo, error) {
	s.paths = append(s.paths, name)
	return s.MapFS.Stat(name)
}

func TestMountedPrefetchTraversal(t *testing.T) {
	tree := &mountStatTrace{MapFS: fstest.MapFS{
		"dir/a/file": {Data: []byte("first")},
		"dir/b":      {Data: []byte("second")},
		"dir/link":   {Mode: stdfs.ModeSymlink, Data: []byte("../outside")},
		"outside":    {Data: []byte("other")},
	}}
	if err := prefetchMountedTree(t.Context(), tree, "dir"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"dir", "dir/a/file", "dir/b"}; !slices.Equal(tree.paths, want) {
		t.Fatalf("stat calls = %q, want %q", tree.paths, want)
	}
	if err := prefetchMountedTree(t.Context(), tree, "missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prefetchMountedTree(ctx, tree, "dir"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
