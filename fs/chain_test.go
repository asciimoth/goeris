package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

// Build a real compressed image. A host mount presents the decoded content.
// Only that backend can give the chain a host path or a seekable file.
func TestCombinedStore(t *testing.T) {
	ctx := t.Context()
	secret := eris.Secret{7}
	content := NewStore(newTestStore(), eris.BlockOptions{})
	mountDir := t.TempDir()
	host := NewMountStore(mountDir)
	stores := []ImageStore{nil, host, content}
	chain := NewCombinedStore(stores...)
	clear(stores)
	builder, err := chain.NewBuilder(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := builder.(*Builder); !ok {
		t.Fatalf("builder wrapped in %T", builder)
	}
	const payload = "decoded content from either backend"
	if err := builder.AddCompressedFile(ctx, "dir/file", strings.NewReader(payload), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("link", "dir/file"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("dir/parent", ".."); err != nil {
		t.Fatal(err)
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := chain.EncodeIndex(ctx, builder.Index(), secret); err != nil || got != cap {
		t.Fatal(got, err)
	}
	if index, err := chain.DecodeIndex(ctx, cap); err != nil || len(index.Entries) != 3 {
		t.Fatal(index, err)
	}
	if _, err := chain.DecodeIndexWithLimit(ctx, cap, 1); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatal(err)
	}
	limited, err := chain.OpenFSWithLimit(ctx, cap, DefaultMaxIndexSize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.ResolvePath(ctx, "dir/file"); !errors.Is(err, ErrNoHostPath) {
		t.Fatal(err)
	}
	if err := limited.Prefetch(ctx, "."); err != nil {
		t.Fatal(err)
	}

	root := makePathTestTree(t, mountDir, cap)
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dir", "file")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	// Put the capability store first. All operations that it supports stop there.
	view, err := NewCombinedStore(content, host).OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".", "dir", "dir/file"} {
		got, err := view.ResolvePath(ctx, name)
		if err != nil || got != filepath.Join(root, name) {
			t.Fatal(got, err)
		}
	}
	file, err := view.OpenFile(ctx, "dir/file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != payload {
		t.Fatal(string(got), err)
	}
	raw, err := view.Open(ctx, "dir/file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, ok := raw.(io.Seeker); ok {
		t.Fatal("Open skipped the first backend")
	}
	if got, err := view.ReadFile(ctx, "dir/file"); err != nil || string(got) != payload {
		t.Fatal(string(got), err)
	}
	if target, err := view.ReadLink(ctx, "link"); err != nil || target != "dir/file" {
		t.Fatal(target, err)
	}
	// The mount has no index boundary markers for dir.
	if target, err := view.ReadLink(ctx, "dir/parent"); target != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("ReadLink across a mounted directory = %q, %v", target, err)
	}
	if info, err := view.Lstat(ctx, "link"); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatal(info, err)
	}
	if info, err := view.Stat(ctx, "link"); err != nil || info.Size() != int64(len(payload)) {
		t.Fatal(info, err)
	}
	if entries, err := view.ReadDir(ctx, "."); err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	sub, err := view.Sub(ctx, "dir")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.ResolvePath(ctx, "file"); !errors.Is(err, ErrNoHostPath) {
		t.Fatal(err)
	}
	if _, err := NewCombinedStore(host, content).OpenFS(ctx, eris.Capability{}); !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatal(err)
	}
	if _, err := NewCombinedStore(host).EncodeIndex(ctx, Index{}, secret); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := NewCombinedStore().NewBuilder(secret); !errors.Is(err, ErrNoBackend) {
		t.Fatal(err)
	}
	if _, err := NewStore(nil, eris.BlockOptions{}).NewBuilder(secret); !errors.Is(err, ErrInvalidEntry) {
		t.Fatal(err)
	}
}

func TestCombinedStoreReadOrder(t *testing.T) {
	ctx := t.Context()
	memory := NewStore(newTestStore(), eris.BlockOptions{})
	mountDir := t.TempDir()
	host := NewMountStore(mountDir)
	mountFirst := NewCombinedStore(host, memory)
	builder, err := mountFirst.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	// Distinct bytes at the same path show which backend supplied the file.
	memoryData := strings.Repeat("0123456789", 2000)
	const mountedData = "Hello, ERIS!\n"
	if err := builder.AddFile(ctx, "hello.txt", strings.NewReader(memoryData), true); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(ctx, "empty", strings.NewReader(""), false); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	makePathTestTree(t, mountDir, capability)

	for _, tc := range []struct {
		name    string
		chain   *CombinedStore
		data    string
		entries []string
	}{
		{"mount first", mountFirst, mountedData, []string{"docs", "hello.txt"}},
		{"memory first", NewCombinedStore(memory, host), memoryData, []string{"empty", "hello.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			image, err := tc.chain.OpenFS(ctx, capability)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range []struct{ name, data string }{
				{"hello.txt", tc.data},
				{"docs/readme.txt", "ERIS-FS example\n"},
				{"empty", ""},
			} {
				got, err := image.ReadFile(ctx, file.name)
				if err != nil || string(got) != file.data {
					t.Errorf("ReadFile(%q) returned %d bytes and %v, want %d matching bytes", file.name, len(got), err, len(file.data))
				}
			}
			if got, err := image.ReadFile(ctx, "missing"); got != nil || !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("ReadFile(missing) = %q, %v", got, err)
			}
			info, err := image.Stat(ctx, "hello.txt")
			if err != nil || info.Size() != int64(len(tc.data)) {
				t.Errorf("Stat(hello.txt) = %v, %v, want size %d", info, err, len(tc.data))
			}
			entries, err := image.ReadDir(ctx, ".")
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			if err != nil || !slices.Equal(names, tc.entries) {
				t.Errorf("ReadDir(.) = %v, %v, want %v", names, err, tc.entries)
			}

			file, err := image.OpenFile(ctx, "hello.txt")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if pos, err := file.Seek(-4, io.SeekEnd); err != nil || pos != int64(len(tc.data)-4) {
				t.Fatalf("Seek(-4, SeekEnd) = %d, %v", pos, err)
			}
			var buf [4]byte
			if n, err := file.ReadAt(buf[:], 0); err != nil || n != len(buf) || string(buf[:]) != tc.data[:4] {
				t.Errorf("ReadAt at start = %q, %d, %v", buf, n, err)
			}
			if got, err := io.ReadAll(file); err != nil || string(got) != tc.data[len(tc.data)-4:] {
				t.Errorf("Read after seek = %q, %v", got, err)
			}
		})
	}

	t.Run("mount only image", func(t *testing.T) {
		ctx := t.Context()
		mountedOnly := pathTestCapability(t)
		makePathTestTree(t, mountDir, mountedOnly)
		if _, err := memory.OpenFS(ctx, mountedOnly); !errors.Is(err, eris.ErrMissingBlock) {
			t.Fatalf("memory OpenFS = %v, want missing block", err)
		}
		image, err := NewCombinedStore(memory, host).OpenFS(ctx, mountedOnly)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := image.ReadFile(ctx, "hello.txt"); err != nil || string(got) != mountedData {
			t.Errorf("ReadFile(hello.txt) = %q, %v, want %q", got, err, mountedData)
		}
	})
}

type chainIndexFunc func(Index) (eris.Capability, error)
type chainIndexStore struct {
	ImageStore
	write chainIndexFunc
}

func (s chainIndexStore) EncodeIndex(_ context.Context, i Index, _ eris.Secret) (eris.Capability, error) {
	return s.write(i)
}

func FuzzCombinedIndexMutation(f *testing.F) {
	for _, kind := range []byte{0, 1, 2, 3, 4} {
		f.Add("dir", "child", kind)
	}
	f.Fuzz(checkIndexMutation)
}

func checkIndexMutation(t *testing.T, dir, name string, kind byte) {
	index := Index{Entries: []Entry{{Path: []string{dir, name}}}}
	sentinel := errors.New("first")
	last := errors.New("last")
	calls := 0
	first := chainIndexStore{write: func(i Index) (eris.Capability, error) {
		switch kind % 5 {
		case 1:
			i.Entries[0].Path[1] += "x"
		case 2:
			i.Entries[0].Path = nil
		case 3:
			i.Entries[0].Size++
		case 4:
			i.Entries[0].Target = "x"
		}
		return eris.Capability{}, sentinel
	}}
	second := chainIndexStore{write: func(Index) (eris.Capability, error) { calls++; return eris.Capability{}, last }}
	_, err := NewCombinedStore(first, second).EncodeIndex(t.Context(), index, eris.Secret{})
	if kind%5 == 0 {
		if err != last || calls != 1 {
			t.Fatal(err, calls)
		}
	} else if err != sentinel || calls != 0 {
		t.Fatal(err, calls)
	}
	if !slices.Equal(index.Entries[0].Path, []string{dir, name}) || index.Entries[0].Size != 0 || index.Entries[0].Target != "" {
		t.Fatal(index)
	}
}

type probeImage struct {
	FileSystem
	run func(context.Context, string) ([]byte, error)
}

func (p probeImage) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return p.run(ctx, path)
}
func (p probeImage) Prefetch(ctx context.Context, path string) error {
	_, err := p.run(ctx, path)
	return err
}

func FuzzCombinedDispatch(f *testing.F) {
	for _, seed := range [][]byte{nil, {0}, {1}, {0, 0, 1}, {1, 0, 1}, {0, 0, 0}} {
		f.Add(seed, "path")
	}
	f.Fuzz(checkCombinedDispatch)
}

func checkCombinedDispatch(t *testing.T, states []byte, name string) {
	if len(states) > 64 {
		t.Skip()
	}
	ctx := t.Context()
	var images []FileSystem
	var calls []int
	firstSuccess := -1
	last := ErrNoBackend
	for i, state := range states {
		err := fmt.Errorf("failure %d", i)
		last = err
		if state&1 != 0 && firstSuccess < 0 {
			firstSuccess = i
		}
		images = append(images, probeImage{run: func(got context.Context, path string) ([]byte, error) {
			if ctx != got || path != name {
				t.Fatal("argument changed")
			}
			calls = append(calls, i)
			if state&1 != 0 {
				return []byte{byte(i)}, nil
			}
			return []byte("partial"), err
		}})
	}
	chain := NewCombinedFileSystem(append(images, nil)...)
	clear(images)
	got, err := chain.ReadFile(ctx, name)
	if firstSuccess >= 0 {
		if err != nil || !bytes.Equal(got, []byte{byte(firstSuccess)}) || len(calls) != firstSuccess+1 {
			t.Fatal(got, err, calls)
		}
	} else if err != last || got != nil || len(calls) != len(states) {
		t.Fatal(got, err, calls)
	}
	for i, call := range calls {
		if i != call {
			t.Fatal(calls)
		}
	}
	calls = nil
	err = chain.Prefetch(ctx, name)
	if (firstSuccess >= 0 && err != nil) || (firstSuccess < 0 && err != last) || len(calls) != len(states) {
		t.Fatal(err, calls)
	}
	for i, call := range calls {
		if i != call {
			t.Fatal(calls)
		}
	}
}

func TestChainFailureBoundaries(t *testing.T) {
	for kind := byte(0); kind < 5; kind++ {
		checkIndexMutation(t, "dir", "file", kind)
	}
	// Every ordering of success and failure in a three-backend chain.
	for mask := byte(0); mask < 8; mask++ {
		checkCombinedDispatch(t, []byte{mask, mask >> 1, mask >> 2}, "file")
	}
	checkCombinedDispatch(t, nil, "file")
}

// The reader fails after delivering a prefix. The builder returned by the
// chain must report that error without starting a builder on the next store.
type builderProbe struct {
	ImageStore
	calls int
}

func (s *builderProbe) NewBuilder(secret eris.Secret) (ImageBuilder, error) {
	s.calls++
	return s.ImageStore.NewBuilder(secret)
}

func TestCombinedBuilderKeepsFailedStream(t *testing.T) {
	ctx := t.Context()
	secret := eris.Secret{5}
	first := &builderProbe{ImageStore: NewStore(newTestStore(), eris.BlockOptions{})}
	second := &builderProbe{ImageStore: NewStore(newTestStore(), eris.BlockOptions{})}
	chain := NewCombinedStore(NewMountStore(t.TempDir()), first, second)
	builder, err := chain.NewBuilder(secret)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("interrupted input")
	prefix := strings.NewReader("partial payload")
	reader := io.MultiReader(prefix, errorReader{failure})
	if err := builder.AddFile(ctx, "file", reader, false); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if prefix.Len() != 0 || first.calls != 1 || second.calls != 0 || len(builder.Index().Entries) != 0 {
		t.Fatal(prefix.Len(), first.calls, second.calls, builder.Index())
	}

	const payload = "complete payload"
	if err := builder.AddFile(ctx, "file", strings.NewReader(payload), false); err != nil {
		t.Fatalf("retry AddFile: %v", err)
	}
	original, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	index := builder.Index()
	index.Entries[0].Path = []string{"renamed"}
	updated, err := chain.EncodeIndex(ctx, index, secret)
	if err != nil || updated == original {
		t.Fatalf("EncodeIndex returned %v, %v, want a new capability", updated, err)
	}

	for _, tc := range []struct {
		capability eris.Capability
		name       string
	}{
		{original, "file"},
		{updated, "renamed"},
	} {
		if _, err := second.OpenFS(ctx, tc.capability); !errors.Is(err, eris.ErrMissingBlock) {
			t.Errorf("second writable store OpenFS(%s) = %v, want missing block", tc.name, err)
		}
		image, err := first.OpenFS(ctx, tc.capability)
		if err != nil {
			t.Fatalf("first writable store OpenFS(%s): %v", tc.name, err)
		}
		if got, err := image.ReadFile(ctx, tc.name); err != nil || string(got) != payload {
			t.Errorf("ReadFile(%q) = %q, %v, want %q", tc.name, got, err, payload)
		}
	}
}

// Opening a v2 file can succeed from its index alone. The mount adapter must
// give ReadFile to the chain so a later read error can trigger a whole-file retry.
func TestMountReadFileRetriesWholeFile(t *testing.T) {
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	b, err := store.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	const payload = "complete content"
	if err := b.AddFile(ctx, "file", strings.NewReader(payload), false); err != nil {
		t.Fatal(err)
	}
	cap, err := b.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	delete(blocks.Blocks, b.Index().Entries[0].Capability.Root.Reference)
	host := t.TempDir()
	root := makePathTestTree(t, host, cap)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	chain := NewCombinedStore(store, NewMountStore(host))
	mount := NewMountWithStore(ctx, chain, nil, eris.BlockOptions{})
	name := "fs/" + cap.String() + "/file"
	opened, err := mount.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if _, err := io.ReadAll(opened); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("read on selected handle = %v", err)
	}
	if got, err := mount.ReadFile(name); err != nil || string(got) != payload {
		t.Fatalf("whole-file read = %q, %v", got, err)
	}
	sub, err := stdfs.Sub(mount, "fs/"+cap.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdfs.ReadFile(sub, "file"); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("Sub switched backends: %v", err)
	}
}

func TestCombinedImageUsesOperationContext(t *testing.T) {
	openCtx, expire := context.WithCancel(t.Context())
	defer expire()
	var calls []string
	view, err := openImages(openCtx, []string{"first", "second"}, false, func(ctx context.Context, backend string) (FileSystem, error) {
		calls = append(calls, "open "+backend)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return probeImage{run: func(ctx context.Context, path string) ([]byte, error) {
			calls = append(calls, "read "+backend)
			if ctx != t.Context() || path != "file" {
				t.Fatal("wrong operation arguments")
			}
			if backend == "first" {
				return nil, io.ErrUnexpectedEOF
			}
			return []byte("complete"), nil
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"open first"}) {
		t.Fatalf("opened extra images: %v", calls)
	}
	expire()
	data, err := view.ReadFile(t.Context(), "file")
	if err != nil || string(data) != "complete" {
		t.Fatal(string(data), err)
	}
	if want := []string{"open first", "read first", "open second", "read second"}; !slices.Equal(calls, want) {
		t.Fatalf("backend order = %v, want %v", calls, want)
	}
}

func TestCombinedCancellationStopsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writes := 0
	first := chainIndexStore{write: func(Index) (eris.Capability, error) {
		writes++
		cancel()
		return eris.Capability{}, ctx.Err()
	}}
	second := chainIndexStore{write: func(Index) (eris.Capability, error) {
		t.Error("retried a canceled index write")
		return eris.Capability{}, eris.ErrReadOnly
	}}
	chain := NewCombinedStore(first, second)
	if _, err := chain.EncodeIndex(ctx, Index{}, eris.Secret{}); !errors.Is(err, context.Canceled) {
		t.Errorf("EncodeIndex returned %v", err)
	}
	if _, err := chain.EncodeIndex(ctx, Index{}, eris.Secret{}); !errors.Is(err, context.Canceled) || writes != 1 {
		t.Errorf("EncodeIndex with canceled context returned %v after %d writes", err, writes)
	}

	if err := NewCombinedFileSystem().Prefetch(ctx, "."); !errors.Is(err, context.Canceled) {
		t.Errorf("empty chain Prefetch returned %v", err)
	}

	mounted := NewCombinedStore(NewMountStore(""))
	cap := pathTestCapability(t)
	if _, err := mounted.DecodeIndex(ctx, cap); !errors.Is(err, context.Canceled) {
		t.Errorf("DecodeIndex returned %v", err)
	}
	if _, err := mounted.OpenFSWithLimit(ctx, cap, 1024); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenFSWithLimit returned %v", err)
	}
	readCtx, cancelRead := context.WithCancel(t.Context())
	defer cancelRead()
	reads := 0
	view := NewCombinedFileSystem(probeImage{run: func(context.Context, string) ([]byte, error) {
		reads++
		cancelRead()
		return nil, stdfs.ErrNotExist
	}}, probeImage{run: func(context.Context, string) ([]byte, error) {
		t.Fatal("retried a canceled file read")
		return nil, nil
	}})
	if _, err := view.ReadFile(readCtx, "file"); !errors.Is(err, context.Canceled) || reads != 1 {
		t.Errorf("ReadFile returned %v after %d reads", err, reads)
	}
	//nolint:staticcheck // The file system reports an invalid context before dispatch.
	if err := view.Prefetch(nil, "."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("Prefetch with nil context returned %v", err)
	}
}
