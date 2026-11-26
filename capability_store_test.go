package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"testing"
)

func TestCapabilityStores(t *testing.T) {
	ctx := t.Context()
	opts := BlockOptions{HighPriority: true}
	data := []byte("Hello, ERIS!\n")
	capability, blocks, err := EncodeRecommended(data, Secret{})
	if err != nil {
		t.Fatal(err)
	}
	mount := t.TempDir()
	path := writePathTestContent(t, mount, capability)
	t.Setenv("ERIS_MNT", mount)
	source := NewBlockCapabilityStore(struct{ BlockSource }{blocks})
	mounted := NewMountCapabilityStore("")
	chain := NewCombinedCapabilityStore(NewMountCapabilityStore(t.TempDir()), source, mounted)
	for _, store := range []CapabilityStore{source, mounted, chain} {
		got, err := store.Decode(ctx, capability, opts)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("decode = %q, %v", got, err)
		}
		for _, limit := range []int64{-1, int64(len(data) - 1)} {
			want := ErrDecodedTooLarge
			if limit < 0 {
				want = ErrInvalidLimit
			}
			if got, err := store.DecodeWithLimit(ctx, capability, limit, opts); got != nil || !errors.Is(err, want) {
				t.Fatalf("limit %d: %q, %v", limit, got, err)
			}
		}
		reader, err := store.NewReaderWithSize(ctx, capability, int64(len(data)), opts)
		if err != nil {
			t.Fatal(err)
		}
		if reader.Size() != int64(len(data)) {
			t.Fatalf("size = %d", reader.Size())
		}
		if _, err = reader.Seek(2, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 3)
		if _, err = reader.ReadAt(buf, 0); err != nil || !bytes.Equal(buf, data[:3]) {
			t.Fatalf("ReadAt = %q, %v", buf, err)
		}
		if _, err = reader.Read(buf); err != nil || !bytes.Equal(buf, data[2:5]) {
			t.Fatalf("Read = %q, %v", buf, err)
		}
		if err = reader.Close(); err != nil {
			t.Fatal(err)
		}
		if err = store.Prefetch(ctx, capability, NoTTL, opts); err != nil {
			t.Fatal(err)
		}
		_, e1 := store.Encode(ctx, data, Secret{}, BlockSize1KiB, opts)
		_, e2 := store.EncodeRecommended(ctx, data, Secret{}, opts)
		_, e3 := store.EncodeContent(ctx, nil, Secret{}, BlockSize1KiB, opts)
		_, e4 := store.EncodeContentRecommended(ctx, nil, Secret{}, opts)
		for _, err := range []error{e1, e2, e3, e4} {
			if !errors.Is(err, ErrReadOnly) {
				t.Errorf("read-only write = %v", err)
			}
		}
	}

	if _, err := source.ResolvePath(ctx, capability); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("block path: %v", err)
	}
	if got, err := chain.ResolvePath(ctx, capability); err != nil || got != path {
		t.Fatalf("mount path = %q, %v", got, err)
	}
	if _, err := mounted.NewReaderWithSize(ctx, capability, 0, opts); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	if _, err := mounted.ResolvePath(ctx, Capability{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatal(err)
	}
	absent := NewMountCapabilityStore(filepath.Join(mount, "absent"))
	if r, err := absent.NewReader(ctx, capability, opts); r != nil || !errors.Is(err, fs.ErrNotExist) || !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing = %v, %v", r, err)
	}
}

func TestCapabilityStoreEncodeContentWithOptionsReadOnly(t *testing.T) {
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	for _, store := range []CapabilityStore{
		NewBlockCapabilityStore(struct{ BlockSource }{blocks}),
		NewMountCapabilityStore(t.TempDir()),
	} {
		for _, size := range []BlockSize{0, BlockSize1KiB} {
			// Any read, even a zero-byte read, sets done on an empty input.
			input := &terminalEOFReader{}
			options := EncodeOptions{BlockSize: size, PreserveRoot: true}
			got, err := store.EncodeContentWithOptions(t.Context(), input, Secret{}, options)
			if got != (Capability{}) || !errors.Is(err, ErrReadOnly) || input.done {
				t.Errorf("%T, size %d: encode = %v, %v; input read = %t", store, size, got, err, input.done)
			}
		}
	}
}

func TestCapabilityStoreEncodeContentWithOptionsErrors(t *testing.T) {
	ctx := t.Context()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	readErr := errors.New("read failed")
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	dst := NewBlockCapabilityStore(blocks)
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		store CapabilityStore
		input io.Reader
		size  BlockSize
		err   error
	}{
		{"nil reader with recommended size", ctx, dst, nil, 0, ErrInvalidInput},
		{"nil reader with explicit size", ctx, dst, nil, BlockSize1KiB, ErrInvalidInput},
		{"invalid size", ctx, dst, errorReader{readErr}, 1, ErrInvalidBlockSize},
		{"reader error with recommended size", ctx, dst, errorReader{readErr}, 0, readErr},
		{"reader error with explicit size", ctx, dst, errorReader{readErr}, BlockSize1KiB, readErr},
		{"canceled with recommended size", canceled, dst, errorReader{readErr}, 0, context.Canceled},
		{"canceled with explicit size", canceled, dst, errorReader{readErr}, BlockSize1KiB, context.Canceled},
		{"empty chain", ctx, NewCombinedCapabilityStore(), errorReader{readErr}, 0, ErrNoCapabilityStorage},
		{"canceled chain", canceled, NewCombinedCapabilityStore(NewMountCapabilityStore(""), dst), errorReader{readErr}, 0, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := EncodeOptions{BlockSize: tc.size, PreserveRoot: true}
			got, err := tc.store.EncodeContentWithOptions(tc.ctx, tc.input, Secret{}, options)
			if got != (Capability{}) || !errors.Is(err, tc.err) || len(blocks.Blocks) != 0 {
				t.Fatalf("encode = %v, %v; want %v and no writes, got %d blocks", got, err, tc.err, len(blocks.Blocks))
			}
		})
	}
	got, err := dst.EncodeContent(ctx, errorReader{readErr}, Secret{}, 0, BlockOptions{})
	if got != (Capability{}) || !errors.Is(err, ErrInvalidBlockSize) || len(blocks.Blocks) != 0 {
		t.Fatalf("fixed-size encode with zero size = %v, %v; stored blocks = %d", got, err, len(blocks.Blocks))
	}
}

func TestCapabilityChainErrors(t *testing.T) {
	ctx := t.Context()
	c := pathTestCapability(t)
	empty := NewCombinedCapabilityStore(nil, nil)
	if _, err := empty.NewReader(ctx, c, BlockOptions{}); err != ErrNoCapabilityStorage {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := empty.ResolvePath(ctx, c); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestCapabilityChainByteWrites(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 20<<10)
	first := BlockSet{Blocks: make(map[Reference]Block)}
	last := BlockSet{Blocks: make(map[Reference]Block)}
	chain := NewCombinedCapabilityStore(NewMountCapabilityStore(""), NewBlockCapabilityStore(first), NewBlockCapabilityStore(last))
	opts := BlockOptions{HighPriority: true}
	secret := Secret{1}
	for _, size := range []BlockSize{BlockSize1KiB, ChooseBlockSize(len(data))} {
		var capability Capability
		var err error
		if size == BlockSize1KiB {
			capability, err = chain.Encode(t.Context(), data, secret, size, opts)
		} else {
			capability, err = chain.EncodeRecommended(t.Context(), data, secret, opts)
		}
		if err != nil {
			t.Fatal(err)
		}
		want, _, err := Encode(data, secret, size)
		if err != nil || capability != want {
			t.Fatalf("encode = %v, want %v, %v", capability, want, err)
		}
		root := first.Blocks[capability.Root.Reference]
		if !root.Options.Root || !root.Options.HighPriority {
			t.Fatalf("root options = %+v", root.Options)
		}
		got, err := Decode(capability, first)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("stored %d bytes, %v", len(got), err)
		}
	}
	if len(last.Blocks) != 0 {
		t.Fatal("encode continued after a successful write")
	}
}

// Opening a block reader checks the final leaf. An earlier leaf can still
// be missing when the caller starts reading.
func TestCapabilityChainMissingContentAfterOpen(t *testing.T) {
	content := bytes.Repeat([]byte("x"), int(BlockSize1KiB))
	c, complete, err := Encode(content, Secret{}, BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	_, leaf, err := EncryptNode(content, Secret{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	incomplete := BlockSet{Blocks: maps.Clone(complete.Blocks)}
	delete(incomplete.Blocks, leaf.Reference)
	chain := NewCombinedCapabilityStore(NewBlockCapabilityStore(incomplete), NewBlockCapabilityStore(complete))
	ctx := t.Context()
	opts := BlockOptions{}
	r, err := chain.NewReader(ctx, c, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := io.ReadAll(r); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing leaf on open reader: %v", err)
	}
	got, err := chain.DecodeWithLimit(ctx, c, int64(len(content)), opts)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("decode with a missing leaf returned %d bytes, %v", len(got), err)
	}
}
