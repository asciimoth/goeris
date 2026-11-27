package fs

import (
	"context"
	"errors"
	"fmt"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

// A mount adapter can send ReadLink's result to a host kernel. These cases
// check the returned text as well as the image's own link lookup.
func TestReadLinkCannotEscapeImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddFile(ctx, "dir/file", strings.NewReader("content"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(ctx, "dir/child/file", strings.NewReader("child"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(ctx, "dir/cOm¹ .txt", strings.NewReader("image file"), false); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"dir/safe":         "file",
		"dir/up":           "..",
		"dir/escape":       "../..",
		"root-alias":       ".",
		"deep-alias":       "dir/child",
		"dir/child/top":    "../..",
		"via-deep-alias":   "deep-alias/top",
		"ambiguous-parent": "deep-alias/../file",
		"clean-escape":     "deep-alias/../../dir/file",
		"through-alias":    "root-alias/../outside",
		"chain":            "through-alias",
		"root-escape":      "../outside",
		"absolute":         "/outside",
		"backslash":        `..\outside`,
		"drive":            "C:/outside",
		"nul":              "dir/\x00outside",
		"device":           "dir/cOm¹ .txt",
		"device-alias":     "device",
		"trimmed-parent":   ".. /outside",
		"plan9-device":     "#c/cons",
		"dir/plan9-device": "../#c/cons",
		`dir\../up`:        "..",
		"missing":          "absent",
		"missing-safe":     "dir/../absent",
		"missing-escape":   "absent/../../outside",
		"cycle":            "cycle",
	} {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A client with a higher link limit could reach the escape at the end.
	for i := 0; i <= 40; i++ {
		target := "../outside"
		if i < 40 {
			target = fmt.Sprintf("step%02d", i+1)
		}
		name := fmt.Sprintf("step%02d", i)
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(ctx, capability, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mount := NewMount(ctx, blocks, eris.BlockOptions{})
	view := NewIOFS(ctx, image)
	root := "fs/" + capability.String() + "/"
	readers := map[string]func(string) (string, error){
		"image": func(name string) (string, error) { return image.ReadLink(ctx, name) },
		"mount": func(name string) (string, error) { return mount.ReadLink(root + name) },
		"iofs":  func(name string) (string, error) { return stdfs.ReadLink(view, name) },
	}
	for name, want := range map[string]string{
		"dir/safe": "file", "dir/up": "..", "missing": "absent", "missing-safe": "dir/../absent",
		"root-alias": ".", "deep-alias": "dir/child", "dir/child/top": "../..",
	} {
		for label, read := range readers {
			if got, err := read(name); err != nil || got != want {
				t.Errorf("%s ReadLink(%q) = %q, %v, want %q", label, name, got, err, want)
			}
		}
	}
	for _, name := range []string{"dir/escape", "through-alias", "chain", "root-escape", "absolute", "missing-escape", "clean-escape", "ambiguous-parent", "deep-alias/top", "via-deep-alias"} {
		if info, err := mount.Lstat(root + name); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
			t.Fatalf("Lstat(%q) = %v, %v", name, info, err)
		}
		for label, read := range readers {
			if got, err := read(name); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("%s ReadLink(%q) = %q, %v", label, name, got, err)
			}
		}
	}
	// Image lookup expands deep-alias before processing the parent steps.
	if got, err := image.ReadFile(ctx, "clean-escape"); err != nil || string(got) != "content" {
		t.Errorf("in-image read through directory alias = %q, %v", got, err)
	}
	if got, err := image.ReadFile(ctx, "via-deep-alias/dir/file"); err != nil || string(got) != "content" {
		t.Errorf("in-image read through aliased parent link = %q, %v", got, err)
	}
	if got, err := image.ReadFile(ctx, "device-alias"); err != nil || string(got) != "image file" {
		t.Errorf("in-image read of literal file name = %q, %v", got, err)
	}
	if info, err := image.Stat(ctx, `dir\../up`); err != nil || !info.IsDir() {
		t.Errorf("in-image lookup from literal directory name = %v, %v", info, err)
	}
	for _, name := range []string{"backslash", "drive", "nul", "device", "device-alias", "trimmed-parent", "plan9-device", "dir/plan9-device", `dir\../up`} {
		if got, err := mount.ReadLink(root + name); got != "" || !errors.Is(err, stdfs.ErrInvalid) {
			t.Errorf("ReadLink(%q) = %q, %v", name, got, err)
		}
	}
	if got, err := mount.ReadLink(root + "cycle"); got != "" || !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("cyclic ReadLink = %q, %v", got, err)
	}
	if got, err := mount.ReadLink(root + "step00"); got != "" || !errors.Is(err, stdfs.ErrInvalid) {
		t.Errorf("long escaping chain = %q, %v", got, err)
	}
	sub, err := mount.Sub(root + "dir")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := stdfs.ReadLink(sub, "up"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("link above exported subdirectory = %q, %v", got, err)
	}
	if got, err := stdfs.ReadLink(sub, "escape"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("sub escaping link = %q, %v", got, err)
	}
}

// A client that cleans alias/.. and a client that follows the alias must
// both reach the directory that contains the link.
func TestReadLinkThroughSiblingDirectoryAlias(t *testing.T) {
	ctx := t.Context()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	content := eris.NewBlockCapabilityStore(blocks)
	payload := []byte("manual")
	file, err := content.Encode(ctx, payload, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const target = "./alias//../file"
	// Keep the keys in wire order for the legacy fixture.
	index := Index{Entries: []Entry{
		{Path: []string{"file"}, Type: Regular, Capability: file, Size: int64(len(payload))},
		{Path: []string{"link"}, Type: Symlink, Target: target},
		{Path: []string{"alias"}, Type: Symlink, Target: "docs"},
		{Path: []string{"docs", "marker"}, Type: Regular, Capability: file, Size: int64(len(payload))},
	}}
	v2, err := EncodeIndex(ctx, index, eris.Secret{}, content, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mount := NewMount(ctx, blocks, eris.BlockOptions{})
	for _, cap := range []eris.Capability{storeV1Index(t, index, blocks), v2} {
		parent, err := EncodeIndex(ctx, Index{Entries: []Entry{
			{Path: []string{"book"}, Type: Subdirectory, Capability: cap},
		}}, eris.Secret{}, content, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{"fs/" + cap.String(), "fs/" + parent.String() + "/book"} {
			view, err := mount.Sub(root)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := stdfs.ReadLink(view, "link"); err != nil || got != target {
				t.Errorf("ReadLink = %q, %v", got, err)
			}
			if got, err := stdfs.ReadFile(view, "link"); err != nil || string(got) != string(payload) {
				t.Errorf("ReadFile = %q, %v", got, err)
			}
		}
	}
}

func TestLegacyLinkConfinedToItsNestedImage(t *testing.T) {
	ctx := t.Context()
	sibling, blocks := contextImage(t)
	source := storeV1Index(t, Index{Entries: []Entry{
		{Path: []string{"link"}, Type: Symlink, Target: "../sibling/nested/file"},
	}}, blocks)
	store := eris.NewBlockCapabilityStore(blocks)
	parent, err := EncodeIndex(ctx, Index{Entries: []Entry{
		{Path: []string{"source"}, Type: Subdirectory, Capability: source},
		{Path: []string{"sibling"}, Type: Subdirectory, Capability: sibling},
	}}, eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(ctx, parent, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := image.ReadFile(ctx, "sibling/nested/file"); err != nil || string(data) != "payload" {
		t.Fatalf("direct read of sibling = %q, %v", data, err)
	}
	const link = "source/link"
	if info, err := image.Lstat(ctx, link); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat = %v, %v", info, err)
	}
	if data, err := image.ReadFile(ctx, link); data != nil || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if target, err := image.ReadLink(ctx, link); target != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink = %q, %v", target, err)
	}
	if name, err := image.ResolvePath(ctx, link); name != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ResolvePath = %q, %v", name, err)
	}
	if err := image.Prefetch(ctx, link); !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Prefetch = %v", err)
	}
}

type missingIndexStore struct{ eris.CapabilityStore }

func (s missingIndexStore) DecodeWithLimit(ctx context.Context, cap eris.Capability, limit int64, options eris.BlockOptions) ([]byte, error) {
	data, err := s.CapabilityStore.DecodeWithLimit(ctx, cap, limit, options)
	if errors.Is(err, eris.ErrMissingBlock) {
		err = errors.Join(err, stdfs.ErrNotExist)
	}
	return data, err
}

// An unavailable nested index cannot be assumed safe. ReadLink must return the
// storage error before a mount adapter receives the target.
func TestReadLinkChecksNestedTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	nested, err := EncodeIndex(ctx, Index{Entries: []Entry{
		{Path: []string{"file"}, Type: Symlink, Target: "../outside"},
		{Path: []string{"dir", "up"}, Type: Symlink, Target: ".."},
		{Path: []string{"indirect"}, Type: Symlink, Target: "dir/up/../outside"},
	}}, eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddFile(ctx, "outside", strings.NewReader("parent image"), false); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSubdirectory("nested", nested); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("alias", "nested/dir"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("link", "nested/file"); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("local", "nested/dir/up/../outside"); err != nil {
		t.Fatal(err)
	}
	outer, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(ctx, outer, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Cleaning ".." against "alias" would select the outer image root.
	if got, err := image.ReadLink(ctx, "alias/up"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink through alias into nested image = %q, %v", got, err)
	}
	if got, err := image.ReadLink(ctx, "nested/dir/up"); got != ".." || err != nil {
		t.Errorf("ReadLink at original nested path = %q, %v", got, err)
	}
	for _, name := range []string{"link", "nested/file", "nested/indirect"} {
		if got, err := image.ReadLink(ctx, name); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("ReadLink(%q) = %q, %v", name, got, err)
		}
		if got, err := image.ReadFile(ctx, name); got != nil || !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("ReadFile(%q) = %q, %v", name, got, err)
		}
	}
	if got, err := image.ReadFile(ctx, "local"); string(got) != "parent image" || err != nil {
		t.Errorf("link in parent image = %q, %v", got, err)
	}
	delete(blocks.Blocks, nested.Root.Reference)
	image, err = OpenFS(ctx, outer, missingIndexStore{store}, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := image.ReadLink(ctx, "link"); got != "" || !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("link to unavailable nested index = %q, %v", got, err)
	}
}

func linkTestImage(entries ...Entry) *FS {
	return &FS{root: buildTree(Index{Entries: entries}, nil, ".", DefaultMaxIndexSize), prefix: "."}
}

func TestCombinedReadLinkThroughOrdinaryDirectories(t *testing.T) {
	ctx := t.Context()
	store := NewStore(newTestStore(), eris.BlockOptions{})
	builder, err := store.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddFile(ctx, "dir/sub/file", strings.NewReader("ok"), false); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"link":         "dir/sub/../../dir/sub/file",
		"dir/link":     "sub/../../dir/sub/file",
		"dir/sub/link": "../sub/./../sub/file",
	}
	for name, target := range links {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	cap, err := builder.Finish(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view, err := NewCombinedStore(store, store).OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range links {
		if got, err := view.ReadLink(ctx, name); err != nil || got != target {
			t.Errorf("ReadLink(%q) = %q, %v, want %q", name, got, err, target)
		}
		if got, err := view.ReadFile(ctx, name); err != nil || string(got) != "ok" {
			t.Errorf("ReadFile(%q) = %q, %v", name, got, err)
		}
	}
}

func TestCombinedReadLinkWithDifferentDirectoryTargets(t *testing.T) {
	ctx := t.Context()
	first := linkTestImage(
		Entry{Path: []string{"dir", "marker"}, Type: Regular, Capability: pathTestCapability(t)},
		Entry{Path: []string{"link"}, Type: Symlink, Target: "dir/../file"},
		Entry{Path: []string{"sub", "link"}, Type: Symlink, Target: "../file"},
	)
	second := linkTestImage(Entry{Path: []string{"dir"}, Type: Symlink, Target: "."})
	// Stores for the same capability can differ in which entries are available.
	view, err := openImages(ctx, []*FS{first, second}, func(_ context.Context, image *FS) (FileSystem, error) {
		return image, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.ReadLink(ctx, "link"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink across backends = %q, %v", got, err)
	}
	if got, err := view.ReadLink(ctx, "sub/link"); got != "../file" || err != nil {
		t.Errorf("ReadLink within image = %q, %v", got, err)
	}
}

func TestCombinedReadLinkChecksDirectoryAliasesInOtherBackends(t *testing.T) {
	ctx := t.Context()
	dir := linkTestImage(Entry{Path: []string{"dir", "up"}, Type: Symlink, Target: ".."})
	alias := linkTestImage(Entry{Path: []string{"dir"}, Type: Symlink, Target: "."})
	combine := func(images ...FileSystem) FileSystem {
		t.Helper()
		view, err := openImages(ctx, images, func(_ context.Context, image FileSystem) (FileSystem, error) {
			return image, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	for _, view := range []FileSystem{
		combine(alias, dir),
		combine(dir, alias),
		combine(dir, combine(dir, alias)),
	} {
		if got, err := view.ReadLink(ctx, "dir/up"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("ReadLink through a directory alias in another backend = %q, %v", got, err)
		}
	}
}

func TestCombinedReadLinkChecksUnavailableParent(t *testing.T) {
	ctx := t.Context()
	first := linkTestImage(Entry{Path: []string{"nested", "dir", "up"}, Type: Symlink, Target: ".."})
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := NewStore(missingIndexStore{eris.NewBlockCapabilityStore(blocks)}, eris.BlockOptions{})
	second := &FS{root: buildTree(Index{Entries: []Entry{
		{Path: []string{"nested"}, Type: Subdirectory, Capability: pathTestCapability(t)},
	}}, store, ".", DefaultMaxIndexSize), prefix: "."}
	view, err := openImages(ctx, []*FS{first, second}, func(_ context.Context, image *FS) (FileSystem, error) {
		return image, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.ReadLink(ctx, "nested/dir/up"); got != "" || !errors.Is(err, eris.ErrMissingBlock) {
		t.Errorf("ReadLink with unavailable parent index = %q, %v", got, err)
	}
}

func TestCombinedReadLinkRespectsNestedRootsInEveryBackend(t *testing.T) {
	ctx := t.Context()
	store := NewStore(newTestStore(), eris.BlockOptions{})
	child, err := store.EncodeIndex(ctx, Index{Entries: []Entry{
		{Path: []string{"dir", "file"}, Type: Regular, Capability: pathTestCapability(t)},
	}}, eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	nested := &FS{root: buildTree(Index{Entries: []Entry{
		{Path: []string{"child"}, Type: Subdirectory, Capability: child},
	}}, store, ".", DefaultMaxIndexSize), prefix: "."}
	flat := linkTestImage(
		Entry{Path: []string{"child", "dir", "local"}, Type: Symlink, Target: ".."},
		Entry{Path: []string{"child", "dir", "up"}, Type: Symlink, Target: "../.."},
	)
	for _, images := range [][]*FS{{flat, nested}, {nested, flat}} {
		view, err := openImages(ctx, images, func(_ context.Context, image *FS) (FileSystem, error) {
			return image, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := view.ReadLink(ctx, "child/dir/up"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
			t.Errorf("ReadLink across nested image root = %q, %v", got, err)
		}
		if got, err := view.ReadLink(ctx, "child/dir/local"); got != ".." || err != nil {
			t.Errorf("ReadLink within nested image = %q, %v", got, err)
		}
	}
}

func TestCombinedReadLinkConfinesTargetToSelectedImage(t *testing.T) {
	ctx := t.Context()
	first := linkTestImage(Entry{Path: []string{"dir", "link"}, Type: Symlink, Target: "file"})
	second := linkTestImage(Entry{Path: []string{"dir", "file"}, Type: Regular, Capability: pathTestCapability(t)})

	view := NewIOFS(ctx, NewCombinedFileSystem(first, second))
	if _, err := view.Stat("dir/file"); err != nil {
		t.Fatal(err)
	}
	if got, err := view.ReadLink("dir/link"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("combined ReadLink = %q, %v", got, err)
	}
	// Sub selects one backend for all later lookups, so it can export
	// that image's dangling link without making the other file reachable.
	sub, err := view.Sub("dir")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := stdfs.ReadLink(sub, "link"); err != nil || got != "file" {
		t.Fatalf("sub ReadLink = %q, %v", got, err)
	}
	if _, err := stdfs.Stat(sub, "file"); !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("sub target lookup = %v", err)
	}
}
