package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

func TestStoreFromMountedContent(t *testing.T) {
	_, index := mountFixture(t)
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		mountIndex: index, mountHello: []byte("Hello, ERIS!\n"), mountReadme: []byte("ERIS-FS example\n"),
	} {
		if _, err := filepath.Localize(name); err != nil {
			t.Skipf("host cannot represent capability names: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	images := NewStore(eris.NewMountCapabilityStore(dir), eris.BlockOptions{})
	root := pathTestCapability(t)
	if _, err := images.OpenFSWithLimit(ctx, root, int64(len(index)-1)); !errors.Is(err, ErrIndexTooLarge) {
		t.Fatal(err)
	}
	image, err := images.OpenFS(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := image.Prefetch(ctx, "."); err != nil {
		t.Fatal(err)
	}
	view := NewIOFS(ctx, image)
	if _, err := view.Sub("missing"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatal(err)
	}
	info, err := view.Stat("hello.txt") // The v1 index omits file sizes.
	if err != nil || info.Size() != 13 {
		t.Fatal(info, err)
	}
	file, err := view.OpenFile("latest.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if n := file.(interface{ CacheSize() int }).CacheSize(); n != 0 {
		t.Fatal(n)
	}
	var got [4]byte
	if _, err := file.ReadAt(got[:], 7); err != nil || string(got[:]) != "ERIS" {
		t.Fatal(string(got[:]), err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddFile(ctx, "x", strings.NewReader("content"), false); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := builder.Finish(ctx); !errors.Is(err, eris.ErrReadOnly) {
		t.Fatal(err)
	}
}

// Track readers that can own a resource, such as a mounted file descriptor.
type trackedReaderStore struct {
	eris.CapabilityStore
	opened, closed int
}

func (s *trackedReaderStore) NewReader(ctx context.Context, cap eris.Capability, options eris.BlockOptions) (eris.CapabilityReader, error) {
	reader, err := s.CapabilityStore.NewReader(ctx, cap, options)
	if err != nil {
		return nil, err
	}
	s.opened++
	return &trackedCapabilityReader{reader, s}, nil
}

type trackedCapabilityReader struct {
	eris.CapabilityReader
	store *trackedReaderStore
}

func (r *trackedCapabilityReader) Close() error {
	if r.store != nil {
		r.store.closed++
		r.store = nil
	}
	return r.CapabilityReader.Close()
}

func TestStoreReaderLifetime(t *testing.T) {
	ctx := t.Context()
	blocks, _ := mountFixture(t)
	backend := &trackedReaderStore{CapabilityStore: eris.NewBlockCapabilityStore(blocks)}
	images := NewStore(backend, eris.BlockOptions{})
	legacy, err := images.OpenFS(ctx, pathTestCapability(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := legacy.Stat(ctx, "hello.txt"); err != nil {
			t.Fatal(err)
		}
	}
	if backend.opened != 1 || backend.closed != 1 {
		t.Fatalf("legacy size lookup opened %d readers and closed %d", backend.opened, backend.closed)
	}
	builder := images.NewBuilder(eris.Secret{})
	if err := builder.AddCompressedFile(ctx, "packed", strings.NewReader("payload"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddLzipFile(ctx, "invalid", strings.NewReader("broken header"), 0, false); err != nil {
		t.Fatal(err)
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	image, err := images.OpenFS(ctx, capability)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := image.Open(ctx, "invalid"); !errors.Is(err, ErrInvalidCompression) {
		t.Fatal(err)
	}
	if backend.opened != 2 || backend.closed != 2 {
		t.Fatalf("bad header left a reader open: opened %d, closed %d", backend.opened, backend.closed)
	}
	got, err := image.ReadFile(ctx, "packed")
	if err != nil || string(got) != "payload" {
		t.Fatal(string(got), err)
	}
	if backend.opened != 3 || backend.closed != 3 {
		t.Fatalf("compressed read opened %d readers and closed %d", backend.opened, backend.closed)
	}

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	file, err := image.Open(readCtx, "packed")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, readErr := file.Read(make([]byte, 1))
	closeErr := file.Close()
	if !errors.Is(readErr, context.Canceled) || closeErr != nil || backend.opened != backend.closed {
		t.Fatalf("canceled compressed read: read %v, close %v, opened %d, closed %d", readErr, closeErr, backend.opened, backend.closed)
	}
}

func TestStoreErrors(t *testing.T) {
	ctx := t.Context()
	empty := NewStore(nil, eris.BlockOptions{})
	if _, err := empty.DecodeIndex(ctx, pathTestCapability(t)); !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatal(err)
	}
	checkMountListing(t, NewMountWithStore(ctx, empty, nil, eris.BlockOptions{}), ".")
	if _, err := NewMountWithStoreAndLimit(ctx, nil, nil, 0, eris.BlockOptions{}); !errors.Is(err, ErrInvalidLimit) {
		t.Fatal(err)
	}
	m, err := NewMountWithLimit(ctx, nil, 1, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat("."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	cancel()
	builder := NewStore(eris.NewBlockCapabilityStore(eris.BlockSet{}), eris.BlockOptions{}).NewBuilder(eris.Secret{})
	for _, add := range []func(context.Context, string, io.Reader, bool) error{builder.AddAutoFile, builder.AddCompressedFile} {
		if err := add(ctx, "file", strings.NewReader(""), false); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if err := (&node{}).load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	//nolint:staticcheck // Exercise the public nil-context error.
	if _, err := (&FS{}).Stat(nil, "."); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatal(err)
	}
}

// Read both namespaces from independent backends. The raw source has no image
// blocks, and its standalone capability is absent from the image store.
func TestMountIndependentStores(t *testing.T) {
	ctx := t.Context()
	imageBlocks, _ := mountFixture(t)
	imageOptions := eris.BlockOptions{HighPriority: true}
	imageSource := &mountObservedSource{BlockSource: imageBlocks, t: t, ctx: ctx, options: imageOptions}
	images := NewStore(eris.NewBlockCapabilityStore(imageSource), imageOptions)
	rawCap, raw, err := eris.Encode([]byte("raw only"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	hash := rawCap.Root.Reference.String()
	rawOptions := eris.BlockOptions{Root: true}
	rawSource := &mountObservedSource{BlockSource: raw, t: t, ctx: ctx, options: rawOptions}
	m := withMountCache(t, NewMountWithStore(ctx, images, rawSource, rawOptions), 2, 2)
	file, err := m.OpenFile(mountHello)
	if err != nil {
		t.Fatal(err)
	}
	if file.(interface{ CacheSize() int }).CacheSize() != 0 {
		t.Fatal("single-block file cached internal nodes")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := m.ReadFile(hash)
	if err != nil || !bytes.Equal(got, raw.Blocks[rawCap.Root.Reference].Data) {
		t.Fatalf("raw block: %x, %v", got, err)
	}
	got, err = m.ReadFile("fs/" + mountIndex + "/hello.txt")
	if err != nil || string(got) != "Hello, ERIS!\n" {
		t.Fatalf("image file: %q, %v", got, err)
	}
	if _, err := m.Stat(rawCap.String()); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("decoded raw-only content: %v", err)
	}
	if _, err := m.Stat(pathTestCapability(t).Root.Reference.String()); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("raw image block: %v", err)
	}
	checkMountListing(t, m, ".", "fs", hash, hash[:3], mountHello, mountIndex)
	checkMountListing(t, m, "fs", mountIndex)

	// Open another root through the same mount, including a nested v1 index.
	index := Index{Entries: []Entry{{Path: []string{"docs"}, Type: Subdirectory, Capability: pathTestCapability(t)}}}
	writer := NewStore(eris.NewBlockCapabilityStore(imageBlocks), imageOptions)
	second, err := writer.EncodeIndex(ctx, index, eris.Secret{1})
	if err != nil {
		t.Fatal(err)
	}
	got, err = m.ReadFile("fs/" + second.String() + "/docs/hello.txt")
	if err != nil || string(got) != "Hello, ERIS!\n" {
		t.Fatalf("second image: %q, %v", got, err)
	}
	checkMountListing(t, m, "fs", mountIndex, second.String())
}

// Test partial mounts through io/fs, including listing-cache entries and Sub views.
func TestMountOptionalComponents(t *testing.T) {
	blocks, _ := mountFixture(t)
	images := NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	hash := pathTestCapability(t).Root.Reference.String()
	for _, tc := range []struct {
		name   string
		images *Store
		raw    eris.BlockSource
	}{
		{"empty", nil, nil},
		{"images only", images, nil},
		{"blocks only", nil, blocks},
		{"images and blocks", images, blocks},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := withMountCache(t, NewMountWithStore(t.Context(), tc.images, tc.raw, eris.BlockOptions{}), 2, 2)
			var expected []string
			if tc.images != nil {
				view, err := stdfs.Sub(m, "fS")
				if err != nil {
					t.Fatal(err)
				}
				got, err := stdfs.ReadFile(view, mountIndex+"/latest.txt")
				if err != nil || string(got) != "Hello, ERIS!\n" {
					t.Fatal(string(got), err)
				}
				expected = append(expected, "fs/"+mountIndex+"/hello.txt", mountIndex)
			} else {
				for _, path := range []string{"fs", "fs/" + mountIndex, mountHello} {
					for op, call := range mountOperations(m) {
						checkMountError(t, op, path, call(path), stdfs.ErrNotExist)
					}
				}
			}
			if tc.raw != nil {
				view, err := stdfs.Sub(m, hash[:3])
				if err != nil {
					t.Fatal(err)
				}
				rest := hash[3:6] + "/" + hash[6:9] + "/" + hash[9:]
				got, err := stdfs.ReadFile(view, rest)
				if err != nil || !bytes.Equal(got, blocks.Blocks[pathTestCapability(t).Root.Reference].Data) {
					t.Fatal("raw block", err)
				}
				expected = append(expected, hash, hash[:3]+"/"+rest)
			} else {
				for _, path := range []string{hash, hash[:3], hash[:3] + "/" + hash[3:6] + "/" + hash[6:9] + "/" + hash[9:]} {
					for op, call := range mountOperations(m) {
						checkMountError(t, op, path, call(path), stdfs.ErrNotExist)
					}
				}
			}
			if err := fstest.TestFS(m, expected...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// FuzzMountComponents compares each partial mount with a full mount. The
// image store and raw source hold different data to expose accidental routing.
func FuzzMountComponents(f *testing.F) {
	blocks, _ := mountFixture(f)
	images := NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{})
	rawCap, raw, err := eris.Encode([]byte("independent raw source"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		f.Fatal(err)
	}
	hash := rawCap.Root.Reference.String()
	for _, name := range []string{".", "fS", mountHello, "fs/" + mountIndex + "/latest.txt", hash, hash[:3], hash[:3] + "/" + hash[3:6] + "/" + hash[6:9] + "/" + hash[9:], rawCap.String(), "../invalid"} {
		for mask := byte(0); mask < 4; mask++ {
			f.Add(name, mask)
		}
	}
	f.Fuzz(func(t *testing.T, name string, mask byte) {
		if len(name) > 4096 {
			t.Skip()
		}
		var s *Store
		var source eris.BlockSource
		var listing []string
		if mask&1 != 0 {
			s = images
			listing = []string{"fs"}
		}
		if mask&2 != 0 {
			source = raw
		}
		mount := withMountCache(t, NewMountWithStore(t.Context(), s, source, eris.BlockOptions{}), 2, 2)
		checkMountListing(t, mount, ".", listing...)
		full := NewMountWithStore(t.Context(), images, raw, eris.BlockOptions{})
		_, _, blockPath := mountBlockPath(name)
		disabled := stdfs.ValidPath(name) && name != "." && ((blockPath && source == nil) || (!blockPath && s == nil))
		if disabled {
			for op, call := range mountOperations(mount) {
				checkMountError(t, op, name, call(name), stdfs.ErrNotExist)
			}
			checkMountListing(t, mount, ".", listing...)
			return
		}
		got, err := mount.ReadFile(name)
		want, wantErr := full.ReadFile(name)
		if !bytes.Equal(got, want) || (err == nil) != (wantErr == nil) {
			t.Fatalf("%q: got %q, %v; want %q, %v", name, got, err, want, wantErr)
		}
		for _, target := range []error{stdfs.ErrNotExist, stdfs.ErrInvalid, eris.ErrMissingBlock} {
			if errors.Is(err, target) != errors.Is(wantErr, target) {
				t.Fatalf("%q: %v, want %v", name, err, wantErr)
			}
		}
	})
}
