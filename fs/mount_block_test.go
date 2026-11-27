package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func mountCaseVariants(name string) []string {
	mixed := []byte(strings.ToUpper(name))
	for i, ch := range mixed {
		if i%2 == 0 && ch >= 'A' && ch <= 'Z' {
			mixed[i] = ch + ('a' - 'A')
		}
	}
	return []string{name, strings.ToLower(name), strings.ToUpper(name), string(mixed)}
}

func TestMountBlockLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), []byte("decoded content"), eris.Secret{}, size, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ref := cap.Root.Reference
		stored := blocks.Blocks[ref].Data
		m := NewMount(ctx, blocks, eris.BlockOptions{})
		nested := filepath.ToSlash(volume.BlockPath(ref))
		for _, layout := range []string{nested, ref.String()} {
			for _, name := range mountCaseVariants(layout) {
				got, err := m.ReadFile(name)
				if err != nil || !bytes.Equal(got, stored) {
					t.Fatalf("block %q: %v", name, err)
				}
				for _, stat := range []func(string) (stdfs.FileInfo, error){m.Stat, m.Lstat} {
					info, err := stat(name)
					if err != nil || info.Name() != path.Base(name) || info.Size() != int64(size) || info.Mode() != 0444 || !info.ModTime().IsZero() || info.Sys() != nil {
						t.Fatalf("block metadata: %v, %v", info, err)
					}
				}
				for op, call := range mountOperations(m) {
					if op == "readdir" || op == "readlink" || op == "sub" {
						checkMountError(t, op, name, call(name), stdfs.ErrInvalid)
					}
				}
			}
		}
		decoded, err := m.ReadFile(cap.String())
		if err != nil || string(decoded) != "decoded content" {
			t.Fatalf("capability changed: %q, %v", decoded, err)
		}
		// Export the bytes at the exact paths that volume.BlockPath supplies.
		// Reading them through volume checks the path and complete encrypted data.
		for _, layout := range []string{nested, ref.String()} {
			root := t.TempDir()
			destination := filepath.Join(root, filepath.FromSlash(layout))
			if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				t.Fatal(err)
			}
			data, err := m.ReadFile(layout)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, data, 0444); err != nil {
				t.Fatal(err)
			}
			v, err := volume.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			got, err, _ := eris.GetBlock(ctx, v, ref, eris.NoTTL, eris.BlockOptions{})
			_ = v.Close()
			if err != nil || !bytes.Equal(got, stored) {
				t.Fatalf("volume round trip: %v", err)
			}
		}
	}
}

func TestMountBlockDirectories(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	ctx := context.Background()
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, fail: errors.New("unexpected read")}
	m := NewMount(ctx, source, eris.BlockOptions{})
	cap, _ := eris.ParseURN(mountHello)
	parts := strings.Split(filepath.ToSlash(volume.BlockPath(cap.Root.Reference)), "/")
	for depth := 1; depth <= 3; depth++ {
		for _, prefix := range mountCaseVariants(strings.Join(parts[:depth], "/")) {
			entries, err := m.ReadDir(prefix)
			if err != nil || len(entries) != 0 {
				t.Fatalf("prefix listing: %v, %v", entries, err)
			}
			info, err := m.Stat(prefix)
			if err != nil || info.Name() != path.Base(prefix) || info.Mode() != stdfs.ModeDir|0555 {
				t.Fatalf("prefix metadata: %v, %v", info, err)
			}
			sub, err := m.Sub(prefix)
			if err != nil {
				t.Fatal(err)
			}
			if err := fstest.TestFS(sub); err != nil {
				t.Fatal(err)
			}
			source.fail = nil
			data, err := stdfs.ReadFile(sub, strings.Join(parts[depth:], "/"))
			if err != nil || !bytes.Equal(data, blocks.Blocks[cap.Root.Reference].Data) {
				t.Fatalf("prefix Sub: %v", err)
			}
			source.fail = errors.New("unexpected read")
		}
	}
	// Chained Sub calls must keep the on-demand namespace at every level.
	sub := stdfs.FS(m)
	for _, part := range parts[:3] {
		var err error
		sub, err = stdfs.Sub(sub, strings.ToLower(part))
		if err != nil {
			t.Fatal(err)
		}
	}
	source.fail = nil
	if _, err := stdfs.ReadFile(sub, parts[3]); err != nil {
		t.Fatal(err)
	}
	reads := source.reads
	for _, name := range []string{"zzz", "zzz/222", "zzz/222/abc"} {
		if _, err := m.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
	if source.reads != reads {
		t.Fatal("virtual prefixes read storage")
	}
	if err := fstest.TestFS(m, "fs"); err != nil {
		t.Fatal(err)
	}
}

func TestMountBlockHandles(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	cap, _ := eris.ParseURN(mountHello)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, options: eris.BlockOptions{Root: true, HighPriority: true}}
	m := NewMount(ctx, source, source.options)
	f, err := m.OpenFile(cap.Root.Reference.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	stored := blocks.Blocks[cap.Root.Reference].Data
	buf := make([]byte, 8)
	if n, err := f.ReadAt(buf, 7); err != nil || n != len(buf) || !bytes.Equal(buf, stored[7:15]) {
		t.Fatalf("ReadAt: %v", err)
	}
	if n, err := f.Read(buf); err != nil || n != len(buf) || !bytes.Equal(buf, stored[:8]) {
		t.Fatalf("Read position changed by ReadAt: %v", err)
	}
	if pos, err := f.Seek(-3, io.SeekEnd); err != nil || pos != int64(len(stored)-3) {
		t.Fatalf("Seek: %v", err)
	}
	tail, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(tail, stored[len(stored)-3:]) {
		t.Fatalf("tail: %v", err)
	}
	if _, err := f.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek accepted")
	}
	if _, err := f.Seek(0, 123); err == nil {
		t.Fatal("invalid whence accepted")
	}
	if _, err := f.ReadAt(buf, -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, err := f.ReadAt(buf, int64(len(stored))); err != io.EOF {
		t.Fatalf("EOF: %v", err)
	}
	info, err := f.Stat()
	if err != nil || info.Size() != int64(len(stored)) {
		t.Fatalf("handle metadata: %v", err)
	}
	// Concurrent operations on one handle must remain safe. ReadAt does not
	// change its shared seek position. A second handle has its own position.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			b := make([]byte, 7)
			for range 20 {
				if _, err := f.ReadAt(b, 2); err != nil || !bytes.Equal(b, stored[2:9]) {
					t.Errorf("concurrent ReadAt: %v", err)
				}
				_, _ = f.Stat()
				_, _ = f.Seek(0, io.SeekStart)
			}
		})
	}
	wg.Wait()
	second, err := m.Open(cap.Root.Reference.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Read(buf); err != nil || !bytes.Equal(buf, stored[:8]) {
		t.Fatalf("independent handle: %v", err)
	}
	_ = second.Close()
	// Reads use the snapshot from Open and do not call the source again.
	source.fail = errors.New("must not fetch again")
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	operations := []func() error{
		func() error { _, e := f.Read(buf); return e }, func() error { _, e := f.ReadAt(buf, 0); return e },
		func() error { _, e := f.Seek(0, io.SeekStart); return e }, func() error { _, e := f.Stat(); return e },
	}
	for _, call := range operations {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	}
	for range 2 {
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range operations {
		if err := call(); !errors.Is(err, stdfs.ErrClosed) {
			t.Fatalf("closed: %v", err)
		}
	}
}

// Deliberately bypass the BlockSource contract to check the raw path's boundary
// validation, including successful responses with absent or oversized data.
type mountBadBlockSource struct {
	eris.BlockSource
	data []byte
}

func (s mountBadBlockSource) GetBlock(context.Context, []eris.BlockRequest) ([]eris.Block, error) {
	return []eris.Block{{Data: s.data}}, nil
}

func TestMountBlockErrors(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	cap, _ := eris.ParseURN(mountHello)
	name := filepath.ToSlash(volume.BlockPath(cap.Root.Reference))
	for _, data := range [][]byte{nil, make([]byte, 1023), make([]byte, 32769), make([]byte, 1024)} {
		m := NewMount(context.Background(), mountBadBlockSource{blocks, data}, eris.BlockOptions{})
		for op, call := range mountOperations(m) {
			checkMountError(t, op, name, call(name), eris.ErrInvalidBlock)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx, fail: errors.New("unavailable")}
	m := NewMount(ctx, source, eris.BlockOptions{})
	for op, call := range mountOperations(m) {
		checkMountError(t, op, name, call(name), source.fail)
	}
	source.fail = nil
	if _, err := m.ReadFile(name); err != nil {
		t.Fatalf("retry: %v", err)
	}
	cancel()
	for op, call := range mountOperations(m) {
		checkMountError(t, op, name, call(name), context.Canceled)
	}
	missing := NewMount(context.Background(), eris.BlockSet{}, eris.BlockOptions{})
	for op, call := range mountOperations(missing) {
		err := call(name)
		checkMountError(t, op, name, err, stdfs.ErrNotExist)
		if !errors.Is(err, eris.ErrMissingBlock) {
			t.Fatalf("lost missing block: %v", err)
		}
	}
}

func TestMountBlockNames(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	invalid := []string{
		"AAA/AAA/AAA/AAA/AAA", "AAA/AAA/AAA/" + strings.Repeat("A", 42),
		"AAA/AAA/AAA/" + strings.Repeat("A", 42) + "B", strings.Repeat("A", 51) + "B",
		"AAA/AAA/AA1", "AAA/AAA/AA=", "AAA/AAA/AA_", "AAA/AAA/AAſ", "AAA/AAA/AAK",
		strings.Repeat("A", 52) + "=", "AAAA/AA/AAA/" + strings.Repeat("A", 43),
		"AAA/AAA/AAA/" + strings.Repeat("A", 43) + "/file", "AAA/file", "Fs/AAA",
	}
	for _, name := range invalid {
		for op, call := range mountOperations(m) {
			checkMountError(t, op, name, call(name), stdfs.ErrNotExist)
		}
	}
	// Library parsers retain their canonical input requirements. Only mount
	// lookup accepts case aliases, and unused base32 bits remain checked.
	cap, _ := eris.ParseURN(mountHello)
	if _, err := eris.ParseReference(strings.ToLower(cap.Root.Reference.String())); err == nil {
		t.Fatal("Reference parser became case-insensitive")
	}
	if _, err := eris.ParseURN(strings.ToLower(mountHello)); err == nil {
		t.Fatal("capability parser became case-insensitive")
	}
}

func TestMountCaseAliases(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	for _, alias := range mountAliases(t, mountHello) {
		for _, name := range mountCaseVariants(alias) {
			data, err := m.ReadFile(name)
			if err != nil || string(data) != "Hello, ERIS!\n" {
				t.Fatalf("capability case alias: %v", err)
			}
		}
	}
	for _, fsName := range mountCaseVariants("fs") {
		info, err := m.Stat(fsName)
		if err != nil || info.Name() != fsName || !info.IsDir() {
			t.Fatalf("namespace alias: %v", err)
		}
		namespace, err := m.Sub(fsName)
		if err != nil {
			t.Fatal(err)
		}
		for _, capName := range mountCaseVariants(mountIndex) {
			data, err := stdfs.ReadFile(namespace, capName+"/hello.txt")
			if err != nil || string(data) != "Hello, ERIS!\n" {
				t.Fatalf("Sub alias: %v", err)
			}
			if _, err := m.ReadFile(fsName + "/" + capName + "/HELLO.TXT"); !errors.Is(err, stdfs.ErrNotExist) {
				t.Fatalf("image path case changed: %v", err)
			}
		}
	}
	ctx := context.Background()
	builder := NewBuilder(eris.NewBlockCapabilityStore(blocks), eris.Secret{}, eris.BlockOptions{})
	for name, value := range map[string]string{"File": "upper", "file": "lower", "FS": "image entry", strings.Repeat("A", 52): "image hash name"} {
		if err := builder.AddFile(ctx, name, strings.NewReader(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := builder.AddSymlink("Link", "File"); err != nil {
		t.Fatal(err)
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "FS/" + strings.ToLower(cap.String()) + "/"
	for name, want := range map[string]string{"File": "upper", "file": "lower", "Link": "upper", "FS": "image entry", strings.Repeat("A", 52): "image hash name"} {
		data, err := m.ReadFile(prefix + name)
		if err != nil || string(data) != want {
			t.Fatalf("image collision %s: %q, %v", name, data, err)
		}
	}
	if target, err := m.ReadLink(prefix + "Link"); err != nil || target != "File" {
		t.Fatalf("link target changed: %q, %v", target, err)
	}
}

func TestMountBlockConcurrentClose(t *testing.T) {
	t.Parallel()
	blocks, _ := mountFixture(t)
	cap, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	file, err := m.OpenFile(cap.Root.Reference.String())
	if err != nil {
		t.Fatal(err)
	}
	want := blocks.Blocks[cap.Root.Reference].Data
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			buffer := make([]byte, 16)
			for range 100 {
				n, err := file.ReadAt(buffer, 0)
				if err == nil {
					if n != len(buffer) || !bytes.Equal(buffer, want[:len(buffer)]) {
						t.Error("concurrent close changed content")
						return
					}
				} else if !errors.Is(err, stdfs.ErrClosed) {
					t.Errorf("ReadAt: %v", err)
					return
				}
				if _, err := file.Stat(); err != nil && !errors.Is(err, stdfs.ErrClosed) {
					t.Errorf("Stat: %v", err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	closed := file.(*mountBlockFile)
	if closed.reader != nil || closed.info != nil || closed.ctx != nil {
		t.Fatal("closed block handle retained data or context")
	}
}

func BenchmarkMountBlockPath(b *testing.B) {
	ref := eris.Reference{1, 2, 3}
	nested := filepath.ToSlash(volume.BlockPath(ref))
	for _, test := range []struct{ label, name string }{
		{"flat", ref.String()}, {"nested", nested}, {"prefix", nested[:11]},
		{"lowercase", strings.ToLower(nested)},
	} {
		b.Run(test.label, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, ok := mountBlockPath(test.name); !ok {
					b.Fatal(test.name)
				}
			}
		})
	}
}

func TestMountBlockPathRequiresExactSeparators(t *testing.T) {
	name := filepath.ToSlash(volume.BlockPath(eris.Reference{1, 2, 3}))
	for _, length := range []int{3, 7, 11, eris.ReferenceTextSize, len(name)} {
		valid := name[:length]
		if length == eris.ReferenceTextSize {
			valid = (eris.Reference{1, 2, 3}).String()
		}
		for i := range len(valid) {
			bad := []byte(valid)
			if bad[i] == '/' {
				bad[i] = 'A'
			} else {
				bad[i] = '/'
			}
			if _, _, ok := mountBlockPath(string(bad)); ok {
				t.Fatalf("accepted misplaced separator in %q", bad)
			}
		}
	}
}

func BenchmarkMountCapability(b *testing.B) {
	for _, tc := range []struct{ label, name string }{
		{"canonical", mountHello},
		{"lowercase", strings.ToLower(mountHello)},
		{"image-path", "fs/" + mountIndex + "/" + strings.Repeat("dir/", 128) + "file"},
	} {
		b.Run(tc.label, func(b *testing.B) {
			for b.Loop() {
				_, _ = mountCapability(tc.name)
			}
		})
	}
}

func TestMountRejectsInvalidCapabilityAliases(t *testing.T) {
	blocks, _ := mountFixture(t)
	ctx := t.Context()
	source := &mountObservedSource{BlockSource: blocks, t: t, ctx: ctx}
	m := NewMount(ctx, source, eris.BlockOptions{})
	body := strings.TrimPrefix(mountHello, eris.CapabilityURNPrefix)
	for _, name := range []string{
		"urn:eriſ:" + body, "URN:ERIS:" + body[:12] + "K" + body[15:],
		body[:len(body)-1] + "J", strings.Repeat("0", 2*eris.CapabilitySize),
		"fſ/" + mountIndex,
	} {
		for _, p := range []string{name, "fs/" + name} {
			_, err := m.ReadFile(p)
			checkMountError(t, "readfile", p, err, stdfs.ErrNotExist)
		}
	}
	if source.reads != 0 {
		t.Fatalf("invalid names caused %d block reads", source.reads)
	}
}

func BenchmarkMountReadRawBlock(b *testing.B) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, err := eris.NewBlockCapabilityStore(blocks).Encode(b.Context(), []byte("payload"), eris.Secret{}, eris.BlockSize32KiB, eris.BlockOptions{})
	if err != nil {
		b.Fatal(err)
	}
	m := NewMount(b.Context(), blocks, eris.BlockOptions{})
	name := cap.Root.Reference.String()
	b.Run("ReadFile", func(b *testing.B) {
		for b.Loop() {
			if _, err := m.ReadFile(name); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Stat", func(b *testing.B) {
		for b.Loop() {
			if _, err := m.Stat(name); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestMountReadFileOwnsBlockBytes(t *testing.T) {
	blocks, _ := mountFixture(t)
	cap, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	ref := cap.Root.Reference
	source := mountBadBlockSource{blocks, blocks.Blocks[ref].Data}
	want := bytes.Clone(source.data)
	m := withMountCache(t, NewMount(t.Context(), source, eris.BlockOptions{}), 0, 1)
	got, err := m.ReadFile(ref.String())
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile returned %d bytes, %v", len(got), err)
	}
	got[0] ^= 0xff
	again, err := m.ReadFile(filepath.ToSlash(volume.BlockPath(ref)))
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("second ReadFile returned %d bytes, %v", len(again), err)
	}
}

func TestMountOpenRetainsVerifiedBlock(t *testing.T) {
	blocks, _ := mountFixture(t)
	cap, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	ref := cap.Root.Reference
	want := blocks.Blocks[ref].Data
	source := mountBadBlockSource{blocks, bytes.Clone(want)}
	m := NewMount(t.Context(), source, eris.BlockOptions{})
	name := ref.String()
	nested := filepath.ToSlash(volume.BlockPath(ref))
	file, err := m.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	seekable, err := m.OpenFile(nested)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seekable.Close() }()
	info, err := m.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	source.data[0] ^= 0xff
	_, err = m.Lstat(nested)
	checkMountError(t, "lstat", nested, err, eris.ErrInvalidBlock)
	if info.Name() != name || info.Size() != int64(len(want)) || !info.Mode().IsRegular() {
		t.Fatalf("retained raw block metadata: %v", info)
	}
	for _, opened := range []stdfs.File{file, seekable} {
		data, err := io.ReadAll(opened)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("opened block returned %d bytes, %v", len(data), err)
		}
	}
}

type cancelOnBlockRead struct {
	mountBadBlockSource
	cancel context.CancelFunc
}

func (s cancelOnBlockRead) GetBlock(ctx context.Context, req []eris.BlockRequest) ([]eris.Block, error) {
	s.cancel()
	return s.mountBadBlockSource.GetBlock(ctx, req)
}

func TestMountReadFileCanceledDuringBlockLoad(t *testing.T) {
	blocks, _ := mountFixture(t)
	cap, err := eris.ParseURN(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	ref := cap.Root.Reference
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := cancelOnBlockRead{mountBadBlockSource{blocks, blocks.Blocks[ref].Data}, cancel}
	m := NewMount(ctx, source, eris.BlockOptions{})
	data, err := m.ReadFile(ref.String())
	checkMountError(t, "readfile", ref.String(), err, context.Canceled)
	if data != nil {
		t.Fatal("canceled read returned block data")
	}
}
