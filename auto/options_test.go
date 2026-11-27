package auto_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/auto"
	erisfs "github.com/asciimoth/goeris/fs"
)

func TestBlockOptionsWithDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", root)
	before, after := newBlockSet(), newBlockSet()
	data := bytes.Repeat([]byte{1}, int(eris.BlockSize1KiB))
	other := bytes.Repeat([]byte{2}, int(eris.BlockSize1KiB))
	ref := putBlock(t, before, data, eris.BlockOptions{HighPriority: true})
	putBlock(t, after, data, eris.BlockOptions{Root: true})
	otherRef := putBlock(t, after, other, eris.BlockOptions{})
	owned := openVolume(t, root)
	putBlock(t, owned, data, eris.BlockOptions{})

	stores := openStores(t, auto.PrependBlocks(struct{ eris.BlockSource }{before}), auto.AppendBlocks(after))
	_, err, options := eris.GetBlock(t.Context(), stores.Blocks, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil || !options.HighPriority {
		t.Fatalf("read from prepended source = %+v, %v", options, err)
	}
	assertBlock(t, stores.Blocks, otherRef, other)

	defaultFirst := openStores(t, auto.AppendBlocks(after))
	_, err, options = eris.GetBlock(t.Context(), defaultFirst.Blocks, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil || options.Root {
		t.Fatalf("read from default before appended store = %+v, %v", options, err)
	}
	written := encode(t, stores.Capabilities, "write past source-only store")
	assertContent(t, eris.NewBlockCapabilityStore(owned), written, "write past source-only store")
	if _, ok := after.Blocks[written.Root.Reference]; ok {
		t.Fatal("block write also reached appended store")
	}
}

func TestCapabilityAndImageOptionsWithDefaults(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	setMissingMounts(t)
	var visits []string
	missing := errors.New("test backend missed")
	capAfter := eris.NewBlockCapabilityStore(newBlockSet())
	imageAfter := erisfs.NewStore(eris.NewBlockCapabilityStore(newBlockSet()), eris.BlockOptions{})
	stores := openStores(t,
		auto.PrependCapabilities(capabilityProbe{
			CapabilityStore: eris.NewCombinedCapabilityStore(),
			probe:           readProbe{"capability", &visits, missing},
		}),
		auto.AppendCapabilities(capAfter),
		auto.PrependImages(imageProbe{probe: readProbe{"image", &visits, missing}}),
		auto.AppendImages(imageAfter),
	)

	defaultContent := eris.NewBlockCapabilityStore(stores.Blocks)
	defaultCap := encode(t, defaultContent, "default content")
	appendCap := encode(t, capAfter, "appended content")
	defaultImage := buildImage(t, erisfs.NewStore(defaultContent, eris.BlockOptions{}), "default image")
	appendImage := buildImage(t, imageAfter, "appended image")
	assertContent(t, stores.Capabilities, defaultCap, "default content")
	assertContent(t, stores.Capabilities, appendCap, "appended content")
	want := []string{"capability", "capability"}
	if !slices.Equal(visits, want) {
		t.Fatalf("prepended stores visited in order %v, want %v", visits, want)
	}
	for _, image := range []struct {
		root eris.Capability
		text string
	}{
		{defaultImage, "default image"},
		{appendImage, "appended image"},
	} {
		visits = nil
		view, err := stores.Images.OpenFS(t.Context(), image.root)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"image", "capability"}; !slices.Equal(visits, want) {
			t.Fatalf("opening %s visited %v, want %v", image.text, visits, want)
		}
		got, err := view.ReadFile(t.Context(), "hello.txt")
		if err != nil || string(got) != image.text {
			t.Fatalf("ReadFile() = %q, %v; want %q", got, err, image.text)
		}
	}

	preferred := openStores(t, auto.PrependCapabilities(capAfter), auto.PrependImages(imageAfter))
	cap := encode(t, preferred.Capabilities, "preferred content")
	assertContent(t, capAfter, cap, "preferred content")
	root := buildImage(t, preferred.Images, "preferred image")
	assertImage(t, imageAfter, root, "preferred image")
}

func TestReplacementOrder(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", ";")
	for _, layer := range []string{"blocks", "capabilities", "images"} {
		t.Run(layer, func(t *testing.T) {
			var visits []string
			last := errors.New("last store")
			probe := func(name string) readProbe { return readProbe{name, &visits, last} }
			var options []auto.Option
			switch layer {
			case "blocks":
				block := func(name string) blockProbe { return blockProbe{probe: probe(name)} }
				options = []auto.Option{
					auto.ReplaceBlocks(block("removed")),
					auto.AppendBlocks(block("after1")),
					auto.PrependBlocks(block("before1"), block("before2")),
					auto.ReplaceBlocks(block("replacement1"), block("replacement2")),
					auto.AppendBlocks(block("after2")),
					auto.PrependBlocks(block("before3")),
				}
			case "capabilities":
				content := func(name string) capabilityProbe { return capabilityProbe{probe: probe(name)} }
				options = []auto.Option{
					auto.ReplaceBlocks(),
					auto.ReplaceCapabilities(content("removed")),
					auto.AppendCapabilities(content("after1")),
					auto.PrependCapabilities(content("before1"), content("before2")),
					auto.ReplaceCapabilities(content("replacement1"), content("replacement2")),
					auto.AppendCapabilities(content("after2")),
					auto.PrependCapabilities(content("before3")),
				}
			case "images":
				image := func(name string) imageProbe { return imageProbe{probe: probe(name)} }
				options = []auto.Option{
					auto.ReplaceBlocks(),
					auto.ReplaceImages(image("removed")),
					auto.AppendImages(image("after1")),
					auto.PrependImages(image("before1"), image("before2")),
					auto.ReplaceImages(image("replacement1"), image("replacement2")),
					auto.AppendImages(image("after2")),
					auto.PrependImages(image("before3")),
				}
			}
			stores := openStores(t, options...)
			ctx := t.Context()
			var err error
			switch layer {
			case "blocks":
				_, err = stores.Blocks.GetBlock(ctx, []eris.BlockRequest{{}})
			case "capabilities":
				_, err = stores.Capabilities.Decode(ctx, eris.Capability{}, eris.BlockOptions{})
			case "images":
				_, err = stores.Images.OpenFS(ctx, eris.Capability{})
			}
			want := []string{"before1", "before2", "before3", "replacement1", "replacement2", "after1", "after2"}
			if !slices.Equal(visits, want) || !errors.Is(err, last) {
				t.Fatalf("visited %v, error %v; want %v and %v", visits, err, want, last)
			}
		})
	}
}

func TestReplacementDependencies(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", ";")
	setMissingMounts(t)
	blocks := newBlockSet()
	owned := &closableBlocks{BlockStore: blocks}
	blockReplacement := openStores(t, auto.ReplaceBlocks(owned))
	root := buildImage(t, blockReplacement.Images, "custom blocks")
	assertImage(t, erisfs.NewStore(eris.NewBlockCapabilityStore(blocks), eris.BlockOptions{}), root, "custom blocks")

	content := eris.NewBlockCapabilityStore(newBlockSet())
	capReplacement := openStores(t, auto.ReplaceBlocks(owned), auto.ReplaceCapabilities(content))
	root = buildImage(t, capReplacement.Images, "custom capabilities")
	assertImage(t, erisfs.NewStore(content, eris.BlockOptions{}), root, "custom capabilities")
	if _, err := capReplacement.Blocks.GetBlock(t.Context(), []eris.BlockRequest{{Reference: root.Root.Reference}}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("replacement capability store wrote to block chain: %v", err)
	}

	images := erisfs.NewStore(content, eris.BlockOptions{})
	imageAppend := openStores(t, auto.ReplaceBlocks(), auto.ReplaceCapabilities(), auto.AppendImages(images))
	root = buildImage(t, imageAppend.Images, "appended images with no capabilities")
	assertImage(t, imageAppend.Images, root, "appended images with no capabilities")

	imageReplacement := openStores(t, auto.ReplaceBlocks(), auto.ReplaceCapabilities(), auto.ReplaceImages(images))
	root = buildImage(t, imageReplacement.Images, "custom images")
	assertImage(t, imageReplacement.Images, root, "custom images")
	if _, err := imageReplacement.Capabilities.Decode(t.Context(), root, eris.BlockOptions{}); !errors.Is(err, eris.ErrNoCapabilityStorage) {
		t.Fatalf("empty capability chain returned %v", err)
	}
	empty := openStores(t, auto.ReplaceBlocks(), auto.ReplaceCapabilities(), auto.ReplaceImages())
	if _, err := empty.Images.OpenFS(t.Context(), root); !errors.Is(err, erisfs.ErrNoBackend) {
		t.Fatalf("empty image chain returned %v", err)
	}
	if err := blockReplacement.Close(); err != nil || owned.closed {
		t.Fatalf("Close() = %v, caller store closed = %t", err, owned.closed)
	}
}

func TestReadOnlyBlocksPermitStreamingFallback(t *testing.T) {
	blocks := newBlockSet()
	content := eris.NewBlockCapabilityStore(newBlockSet())
	stores := openStores(t,
		auto.ReplaceBlocks(struct{ eris.BlockSource }{blocks}),
		auto.AppendCapabilities(content),
	)
	cap := encode(t, eris.NewBlockCapabilityStore(blocks), "read-only block source")
	assertContent(t, stores.Capabilities, cap, "read-only block source")
	written := encode(t, stores.Capabilities, "stream to appended capability store")
	assertContent(t, content, written, "stream to appended capability store")
}

func TestOptionsKeepTheirStores(t *testing.T) {
	blocks := newBlockSet()
	members := []any{blocks}
	option := auto.ReplaceBlocks(members...)
	clear(members)
	stores := openStores(t, nil, option)
	cap := encode(t, stores.Capabilities, "reused option")
	reopened := openStores(t, option)
	assertContent(t, reopened.Capabilities, cap, "reused option")
}

func putBlock(t *testing.T, sink eris.BlockSink, data []byte, options eris.BlockOptions) eris.Reference {
	t.Helper()
	ref, err := eris.PutBlock(t.Context(), sink, data, eris.NoTTL, options)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

type readProbe struct {
	name   string
	visits *[]string
	err    error
}

func (p readProbe) read() error {
	*p.visits = append(*p.visits, p.name)
	return fmt.Errorf("%s: %w", p.name, p.err)
}

type blockProbe struct {
	eris.BlockSource
	probe readProbe
}

func (p blockProbe) GetBlock(context.Context, []eris.BlockRequest) ([]eris.Block, error) {
	return nil, p.probe.read()
}

type capabilityProbe struct {
	eris.CapabilityStore
	probe readProbe
}

func (p capabilityProbe) Decode(context.Context, eris.Capability, eris.BlockOptions) ([]byte, error) {
	return nil, p.probe.read()
}

func (p capabilityProbe) DecodeWithLimit(context.Context, eris.Capability, int64, eris.BlockOptions) ([]byte, error) {
	return nil, p.probe.read()
}

type imageProbe struct {
	erisfs.ImageStore
	probe readProbe
}

func (p imageProbe) OpenFS(context.Context, eris.Capability) (erisfs.FileSystem, error) {
	return nil, p.probe.read()
}

type closableBlocks struct {
	eris.BlockStore
	closed bool
}

func (s *closableBlocks) Close() error {
	s.closed = true
	return nil
}
