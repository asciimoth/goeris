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
