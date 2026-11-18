package fs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	stdfs "io/fs"
	"maps"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

// These bytes and capabilities are the independent v1 compatibility vector
// from specs/eris-go-fuse-layout.md. Do not replace the index with our encoder.
const mountIndexHex = "da657269748201a36968656c6c6f2e7478748200d9011458420a008b56bb1db84d96170d75751443a7a1e0f27b25ff672ccaf51bee6d0c1acc9d52d4114d1b9fc1e2220b2713aa1e996ad7f6472cdfc1a3ed7a6a5be32f05435f626a6c61746573742e74787482026968656c6c6f2e7478746f646f63732f726561646d652e7478748200d9011458420a00efa4a97b1c111d320ad9ea0214f982fe85c6497a8d4057fdb04c7e105f315d20e1e18bee02a1367e23a93febc276cfee3b207840513ee1f9549f58210358475c"
const mountHello = "urn:eris:BIAIWVV3DW4E3FQXBV2XKFCDU6Q6B4T3EX7WOLGK6UN643IMDLGJ2UWUCFGRXH6B4IRAWJYTVIPJS2WX6ZDSZX6BUPWXU2S34MXQKQ27MI"
const mountReadme = "urn:eris:BIAO7JFJPMOBCHJSBLM6UAQU7GBP5BOGJF5I2QCX7WYEY7QQL4YV2IHB4GF64AVBGZ7CHKJ75PBHNT7OHMQHQQCRH3Q7SVE7LAQQGWCHLQ"
const mountIndex = "urn:eris:BIACGBUGVO7DG6RBLO75M3WY6JUXSVOKCD2C3NRSPKJKVHFZO6XBARY5ZN56UT4GIKHWGFKTUYQO7WFCNSRII5HBF42XLXZCUQSPQXTYCU"

func mountFixture(t testing.TB) (eris.BlockSet, []byte) {
	t.Helper()
	index, err := hex.DecodeString(mountIndexHex)
	if err != nil {
		t.Fatal(err)
	}
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	for urn, data := range map[string][]byte{mountHello: []byte("Hello, ERIS!\n"), mountReadme: []byte("ERIS-FS example\n"), mountIndex: index} {
		cap, stored, err := eris.Encode(data, eris.Secret{}, eris.BlockSize1KiB)
		if err != nil || cap.String() != urn {
			t.Fatalf("vector capability = %v, %v; want %s", cap, err, urn)
		}
		maps.Copy(blocks.Blocks, stored.Blocks)
	}
	return blocks, index
}

func mountAliases(t testing.TB, urn string) []string {
	t.Helper()
	cap, err := eris.ParseURN(urn)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cap.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return []string{urn, strings.TrimPrefix(urn, eris.CapabilityURNPrefix), hex.EncodeToString(data), strings.ToUpper(hex.EncodeToString(data))}
}

func TestMountLayout(t *testing.T) {
	t.Parallel()
	blocks, index := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	for urn, data := range map[string][]byte{mountHello: []byte("Hello, ERIS!\n"), mountReadme: []byte("ERIS-FS example\n"), mountIndex: index} {
		for _, name := range mountAliases(t, urn) {
			got, err := stdfs.ReadFile(m, name)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("ReadFile(%q) = %x, %v", name, got, err)
			}
			info, err := m.Stat(name)
			if err != nil || info.Name() != name || info.Size() != int64(len(data)) || info.Mode() != 0444 || !info.ModTime().IsZero() || info.Sys() != nil {
				t.Fatalf("Stat(%q) = %v, %v", name, info, err)
			}
			linkInfo, err := m.Lstat(name)
			if err != nil || linkInfo.Mode() != info.Mode() {
				t.Fatalf("Lstat = %v, %v", linkInfo, err)
			}
		}
	}
	for _, alias := range mountAliases(t, mountIndex) {
		prefix := "fs/" + alias
		for name, want := range map[string]string{"hello.txt": "Hello, ERIS!\n", "docs/readme.txt": "ERIS-FS example\n", "latest.txt": "Hello, ERIS!\n"} {
			got, err := m.ReadFile(prefix + "/" + name)
			if err != nil || string(got) != want {
				t.Fatalf("ReadFile(%s) = %q, %v", name, got, err)
			}
		}
		info, err := m.Stat(prefix)
		if err != nil || info.Name() != alias || info.Mode() != stdfs.ModeDir|0555 {
			t.Fatalf("root info = %v, %v", info, err)
		}
		target, err := stdfs.ReadLink(m, prefix+"/latest.txt")
		if err != nil || target != "hello.txt" {
			t.Fatalf("ReadLink = %q, %v", target, err)
		}
		info, err = stdfs.Lstat(m, prefix+"/latest.txt")
		if err != nil || info.Mode() != stdfs.ModeSymlink|0555 || info.Size() != 9 {
			t.Fatalf("Lstat = %v, %v", info, err)
		}
		entries, err := stdfs.ReadDir(m, prefix)
		if err != nil || len(entries) != 3 {
			t.Fatalf("ReadDir = %v, %v", entries, err)
		}
		for i, want := range []string{"docs", "hello.txt", "latest.txt"} {
			entry := entries[i]
			info, err := entry.Info()
			if err != nil || entry.Name() != want || info.Name() != want || info.Mode().Type() != entry.Type() || info.IsDir() != entry.IsDir() {
				t.Fatalf("entry %d = %v, %v", i, entry, err)
			}
		}
	}
	// Looking up capabilities must not change the virtual directory listings.
	if err := fstest.TestFS(m, "fs"); err != nil {
		t.Fatal(err)
	}
}

func TestMountSub(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	same, err := m.Sub(".")
	if err != nil || same != m {
		t.Fatalf("Sub(.) = %v, %v", same, err)
	}
	namespace, err := stdfs.Sub(m, "fs")
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(namespace); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []struct {
		fs   stdfs.FS
		path string
	}{{m, "fs/" + mountIndex}, {namespace, mountIndex}} {
		image, err := stdfs.Sub(parent.fs, parent.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := fstest.TestFS(image, "hello.txt", "latest.txt", "docs/readme.txt"); err != nil {
			t.Fatal(err)
		}
		dir, err := stdfs.Sub(parent.fs, parent.path+"/docs")
		if err != nil {
			t.Fatal(err)
		}
		if err := fstest.TestFS(dir, "readme.txt"); err != nil {
			t.Fatal(err)
		}
	}
	// The namespace itself has no entry called fs.
	if _, err := namespace.Open("fs"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("namespace fs: %v", err)
	}
}

func TestMountHandles(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	for _, name := range []string{mountHello, "fs/" + mountIndex + "/hello.txt"} {
		f, err := m.OpenFile(name)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if n, err := f.ReadAt(buf, 7); err != nil || n != 4 || string(buf) != "ERIS" {
			t.Fatalf("ReadAt = %q, %v", buf, err)
		}
		if _, err := f.Seek(-2, io.SeekEnd); err != nil {
			t.Fatal(err)
		}
		if n, err := f.Read(buf); (err != nil && err != io.EOF) || n != 2 || string(buf[:n]) != "!\n" {
			t.Fatalf("Read = %q, %v", buf[:n], err)
		}
		if info, err := f.Stat(); err != nil || info.Size() != 13 {
			t.Fatalf("Stat = %v, %v", info, err)
		}
		other, err := m.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Read(buf); err != nil || string(buf) != "Hell" {
			t.Fatalf("independent position = %q, %v", buf, err)
		}
		_ = other.Close()
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Stat(); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed Stat: %v", err)
		}
		if _, err := f.Read(buf); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed Read: %v", err)
		}
	}
	for _, name := range []string{".", "fs", "fs/" + mountIndex} {
		file, err := m.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		dir := file.(stdfs.ReadDirFile)
		if _, err := dir.Read(nil); !errors.Is(err, stdfs.ErrInvalid) {
			t.Fatalf("directory Read: %v", err)
		}
		for {
			entries, err := dir.ReadDir(1)
			if err == io.EOF {
				break
			}
			if err != nil || len(entries) != 1 {
				t.Fatalf("ReadDir(1) = %v, %v", entries, err)
			}
		}
		if entries, err := dir.ReadDir(0); err != nil || len(entries) != 0 {
			t.Fatalf("exhausted = %v, %v", entries, err)
		}
		_ = dir.Close()
		if _, err := dir.ReadDir(1); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed ReadDir: %v", err)
		}
	}
}

// Run all public operations so their validation and PathError contracts stay
// consistent. Successful handles are closed even in fuzz tests.
func mountOperations(m *Mount) map[string]func(string) error {
	return map[string]func(string) error{
		"open": func(n string) error {
			f, e := m.Open(n)
			if e == nil {
				_ = f.Close()
			}
			return e
		},
		"openfile": func(n string) error {
			f, e := m.OpenFile(n)
			if e == nil {
				_ = f.Close()
			}
			return e
		},
		"readfile": func(n string) error { _, e := m.ReadFile(n); return e },
		"readdir":  func(n string) error { _, e := m.ReadDir(n); return e },
		"stat":     func(n string) error { _, e := m.Stat(n); return e },
		"lstat":    func(n string) error { _, e := m.Lstat(n); return e },
		"readlink": func(n string) error { _, e := m.ReadLink(n); return e },
		"sub":      func(n string) error { _, e := m.Sub(n); return e },
	}
}

func checkMountError(t testing.TB, op, name string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s(%q): %v; want %v", op, name, err, want)
	}
	if op == "openfile" {
		op = "open"
	}
	var pe *stdfs.PathError
	if !errors.As(err, &pe) || pe.Op != op || pe.Path != name {
		t.Fatalf("error path/operation: %v; want %s %q", err, op, name)
	}
}

func TestMountErrors(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	invalid := []string{"", "/", "/fs", "fs/", "fs//x", "./fs", "fs/../x", "fs/./x", "\xff"}
	missing := []string{"absent", "fs/nope", "fs/" + mountIndex + "/absent", mountHello + "/child", strings.Repeat("A", 52), "AAA/AAA/AAA/" + strings.Repeat("A", 43), strings.Repeat("z", 132), strings.Repeat("0", 132), strings.Repeat("A", 106), strings.Repeat("x", eris.CapabilityTextSize)}
	for op, call := range mountOperations(m) {
		for _, name := range invalid {
			checkMountError(t, op, name, call(name), stdfs.ErrInvalid)
		}
		for _, name := range missing {
			checkMountError(t, op, name, call(name), stdfs.ErrNotExist)
		}
		checkMountError(t, op, "fs/"+mountHello, call("fs/"+mountHello), ErrInvalidIndex)
	}
	for op, call := range mountOperations(NewMount(context.Background(), eris.BlockSet{}, eris.BlockOptions{})) {
		for _, name := range []string{mountHello, "fs/" + mountIndex + "/hello.txt"} {
			// Operations that need no content (ReadLink, ReadDir, Sub of a raw
			// file) can reject its type before fetching a block.
			err := call(name)
			if name == mountHello && (op == "readlink" || op == "readdir" || op == "sub") {
				checkMountError(t, op, name, err, stdfs.ErrInvalid)
				continue
			}
			checkMountError(t, op, name, err, eris.ErrMissingBlock)
			if !errors.Is(err, stdfs.ErrNotExist) {
				t.Fatalf("missing block lacks ErrNotExist: %v", err)
			}
		}
	}
	for _, bad := range []*Mount{nil, {}, {source: blocks}, NewMount(context.Background(), nil, eris.BlockOptions{})} {
		for op, call := range mountOperations(bad) {
			checkMountError(t, op, ".", call("."), stdfs.ErrInvalid)
		}
	}
	for _, name := range []string{".", "fs", "fs/" + mountIndex} {
		_, err := m.OpenFile(name)
		checkMountError(t, "open", name, err, stdfs.ErrInvalid)
		_, err = m.ReadFile(name)
		checkMountError(t, "readfile", name, err, stdfs.ErrInvalid)
		_, err = m.ReadLink(name)
		checkMountError(t, "readlink", name, err, stdfs.ErrInvalid)
	}
	for op, call := range mountOperations(m) {
		if op == "readdir" || op == "readlink" || op == "sub" {
			name := "fs/" + mountIndex + "/hello.txt"
			checkMountError(t, op, name, call(name), stdfs.ErrInvalid)
		}
	}
}

func TestMountLimits(t *testing.T) {
	t.Parallel()
	blocks, index := mountFixture(t)
	for _, limit := range []int64{-1, 0} {
		if _, err := NewMountWithLimit(context.Background(), blocks, limit, eris.BlockOptions{}); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	for _, limit := range []int64{int64(len(index) - 1), int64(len(index))} {
		m, err := NewMountWithLimit(context.Background(), blocks, limit, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := m.ReadFile(mountIndex)
		if err != nil || !bytes.Equal(got, index) {
			t.Fatalf("raw index limited: %v", err)
		}
		file, err := m.Open("fs/" + mountIndex)
		if err == nil {
			_ = file.Close()
		}
		if limit < int64(len(index)) {
			if !errors.Is(err, ErrIndexTooLarge) {
				t.Fatalf("limit: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

// mountObservedSource checks propagation at the block boundary, including
// prefetch hints. It can also inject a storage error without a special store.
type mountObservedSource struct {
	eris.BlockSource
	t       *testing.T
	ctx     context.Context
	options eris.BlockOptions
	fail    error
	reads   int
}

func (s *mountObservedSource) check(ctx context.Context, requests []eris.BlockRequest) {
	s.t.Helper()
	if ctx != s.ctx {
		s.t.Fatal("context changed")
	}
	for _, r := range requests {
		if r.Options != s.options || r.TTL != eris.NoTTL {
			s.t.Fatalf("request changed: %+v", r)
		}
	}
}
func (s *mountObservedSource) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	s.check(ctx, requests)
	s.reads++
	if s.fail != nil {
		return nil, s.fail
	}
	return s.BlockSource.GetBlock(ctx, requests)
}
func (s *mountObservedSource) PrefetchBlock(ctx context.Context, requests []eris.BlockRequest) {
	s.check(ctx, requests)
}

func TestMountContextAndOptions(t *testing.T) {
	blocks, _ := mountFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := eris.BlockOptions{HighPriority: true, Root: true}
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, options: options}
	m := NewMount(ctx, source, options)
	options.HighPriority = false
	if source.reads != 0 {
		t.Fatal("constructor did I/O")
	}
	_, _ = m.ReadDir(".")
	_, _ = m.ReadDir("fs")
	if source.reads != 0 {
		t.Fatal("virtual directory did I/O")
	}
	for _, name := range []string{mountHello, "fs/" + mountIndex + "/hello.txt"} {
		if _, err := m.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := m.Sub("fs/" + mountIndex)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := stdfs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := entry.Info(); err != nil {
			t.Fatal(err)
		}
	}
	file, err := sub.Open("docs/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if source.reads == 0 {
		t.Fatal("no storage reads")
	}
	cancel()
	for op, call := range mountOperations(m) {
		for _, name := range []string{".", "fs", mountHello, "fs/" + mountIndex} {
			checkMountError(t, op, name, call(name), context.Canceled)
		}
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("retained context: %v", err)
	}
	if _, err := sub.Open("hello.txt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sub context: %v", err)
	}
}

func TestMountSourceFailureAndRetry(t *testing.T) {
	blocks, _ := mountFixture(t)
	ctx := context.Background()
	failure := errors.New("storage unavailable")
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, fail: failure}
	m := NewMount(ctx, source, eris.BlockOptions{})
	for op, call := range mountOperations(m) {
		name := "fs/" + mountIndex + "/hello.txt"
		checkMountError(t, op, name, call(name), failure)
	}
	source.fail = nil
	if _, err := m.ReadFile("fs/" + mountIndex + "/hello.txt"); err != nil {
		t.Fatal(err)
	}
	// A leaf error after Open must still propagate to the caller.
	file, err := m.Open("fs/" + mountIndex + "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	source.fail = failure
	if _, err := io.ReadAll(file); !errors.Is(err, failure) {
		t.Fatalf("read failure: %v", err)
	}
	_ = file.Close()
}

func TestMountConcurrent(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 10 {
				for _, name := range []string{mountHello, "fs/" + mountIndex + "/latest.txt"} {
					got, err := m.ReadFile(name)
					if err != nil || string(got) != "Hello, ERIS!\n" {
						t.Errorf("concurrent read = %q, %v", got, err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func TestMountNestedImagesAndLinks(t *testing.T) {
	t.Parallel()
	nestedCap, blocks := contextImage(t)
	ctx := context.Background()
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSubdirectory("tree", nestedCap); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(ctx, "bin/tool", strings.NewReader("executable"), true); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"alias": "tree/nested", "bin/up": "../tree/nested/file", "absolute": "/bin/tool",
		"escape": "../" + mountHello, "cycle": "cycle", "dangling": "absent", "image-escape": "../../bin/tool",
	} {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, options: eris.BlockOptions{HighPriority: true}}
	m := NewMount(ctx, source, source.options)
	prefix := "fs/" + cap.String() + "/"
	for name, want := range map[string]string{"tree/nested/file": "payload", "alias/link": "payload", "bin/up": "payload", "tree/nested/compressed": strings.Repeat("data", 1000), "bin/tool": "executable"} {
		got, err := m.ReadFile(prefix + name)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
	info, err := m.Stat(prefix + "bin/tool")
	if err != nil || info.Mode() != 0555 {
		t.Fatalf("executable mode: %v, %v", info, err)
	}
	_, err = m.OpenFile(prefix + "tree/nested/compressed")
	checkMountError(t, "open", prefix+"tree/nested/compressed", err, ErrNotSeekable)
	for _, name := range []string{"absolute", "escape", "dangling", "image-escape", "cycle"} {
		_, err := m.Open(prefix + name)
		want := stdfs.ErrNotExist
		if name == "cycle" {
			want = stdfs.ErrInvalid
		}
		checkMountError(t, "open", prefix+name, err, want)
		_, err = m.ReadLink(prefix + name)
		if name == "absolute" || name == "escape" || name == "image-escape" {
			checkMountError(t, "readlink", prefix+name, err, stdfs.ErrNotExist)
		} else if name == "cycle" {
			checkMountError(t, "readlink", prefix+name, err, stdfs.ErrInvalid)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alias", "bin"} {
		sub, err := m.Sub(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		child := "link"
		if name == "bin" {
			child = "up"
		}
		got, err := stdfs.ReadFile(sub, child)
		if err != nil || string(got) != "payload" {
			t.Fatalf("Sub link: %q, %v", got, err)
		}
		reads := source.reads
		if _, err := stdfs.Stat(sub, "."); err != nil {
			t.Fatal(err)
		}
		if source.reads != reads {
			t.Fatal("Sub reread its index")
		}
	}
}

func TestMountNestedIndexLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	nested := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := nested.AddSymlink("link", strings.Repeat("x", 1024)); err != nil {
		t.Fatal(err)
	}
	child, err := nested.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(blocks, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSubdirectory("nested", child); err != nil {
		t.Fatal(err)
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMountWithLimit(ctx, blocks, 256, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "fs/" + cap.String()
	sub, err := m.Sub(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Open("nested"); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatalf("nested limit via Sub: %v", err)
	}
	_, err = m.Open(prefix + "/nested")
	checkMountError(t, "open", prefix+"/nested", err, ErrIndexTooLarge)
}

func TestIndexLegacyTextPaths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"a", "docs/readme", "日本語/文書", "back\\slash"} {
		wire := appendText([]byte(MagicPrefix+"\x82\x01\xa1"), name)
		wire = append(wire, 0x82, 2, 0x61, 'x')
		index, err := ParseIndex(wire)
		if err != nil || strings.Join(index.Entries[0].Path, "/") != name {
			t.Fatalf("legacy text %q: %v", name, err)
		}
		encoded, err := index.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseIndex(encoded); err != nil {
			t.Fatal(err)
		}
		wire[6] = 2
		if _, err := ParseIndex(wire); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("v2 text path: %v", err)
		}
	}
	for _, name := range []string{"", ".", "..", "a/../b", "/a", "a/", "a//b", "a/./b", "\xff"} {
		wire := appendText([]byte(MagicPrefix+"\x82\x01\xa1"), name)
		wire = append(wire, 0x82, 2, 0x61, 'x')
		if _, err := ParseIndex(wire); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("invalid text path %q: %v", name, err)
		}
	}
	// A truncated text key and the same path encoded once as text and once as
	// an array must both fail, without changing a receiver's previous value.
	for _, suffix := range [][]byte{{0xa1, 0x78, 24}, {0xa2, 0x61, 'a', 0x82, 2, 0x80, 0x81, 0x61, 'a', 0x82, 2, 0x80}} {
		index := Index{Entries: []Entry{{Path: []string{"keep"}, Type: Symlink}}}
		wire := append([]byte(MagicPrefix+"\x82\x01"), suffix...)
		if err := index.UnmarshalBinary(wire); !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("malformed text path: %v", err)
		}
		if index.Entries[0].Path[0] != "keep" {
			t.Fatal("failed decode changed receiver")
		}
	}
}

func TestMountPreservesStoragePathError(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	ctx := context.Background()
	failure := &stdfs.PathError{Op: "read", Path: "volume/block", Err: stdfs.ErrPermission}
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, fail: failure}
	m := NewMount(ctx, source, eris.BlockOptions{})
	cap, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cap.Root.Reference.String(), "fs/" + mountIndex + "/hello.txt"} {
		for op, call := range mountOperations(m) {
			err := call(name)
			checkMountError(t, op, name, err, stdfs.ErrPermission)
			if !errors.Is(err, failure) {
				t.Fatalf("%s lost storage PathError: %v", op, err)
			}
		}
	}
	// Content paths wrap the source error inside FS.Open or FS.Stat before it
	// reaches the mount. That error chain must also retain the original error.
	for op, call := range mountOperations(m) {
		if op == "readdir" || op == "readlink" || op == "sub" {
			continue
		}
		err := call(mountHello)
		checkMountError(t, op, mountHello, err, stdfs.ErrPermission)
		if !errors.Is(err, failure) {
			t.Fatalf("%s lost content source error: %v", op, err)
		}
	}
}

func TestMountMissingErrorWrappedOnce(t *testing.T) {
	t.Parallel()
	m := NewMount(context.Background(), eris.BlockSet{}, eris.BlockOptions{})
	for _, name := range []string{mountHello, "fs/" + mountIndex + "/hello.txt"} {
		_, err := m.ReadFile(name)
		checkMountError(t, "readfile", name, err, eris.ErrMissingBlock)
		if !errors.Is(err, stdfs.ErrNotExist) {
			t.Fatalf("missing fs error: %v", err)
		}
		if strings.Count(err.Error(), stdfs.ErrNotExist.Error()) != 1 {
			t.Fatalf("repeated missing-file message: %v", err)
		}
	}
}
