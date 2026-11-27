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
	for name, target := range map[string]string{
		"dir/safe":       "file",
		"dir/up":         "..",
		"dir/escape":     "../..",
		"root-alias":     ".",
		"deep-alias":     "dir/child",
		"safe-via-alias": "deep-alias/../../dir/file",
		"through-alias":  "root-alias/../outside",
		"chain":          "through-alias",
		"root-escape":    "../outside",
		"absolute":       "/outside",
		"backslash":      `..\outside`,
		"drive":          "C:/outside",
		"nul":            "dir/\x00outside",
		"missing":        "absent",
		"cycle":          "cycle",
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
	for _, name := range []string{"dir/safe", "dir/up", "missing", "root-alias", "safe-via-alias"} {
		for label, read := range map[string]func(string) (string, error){
			"image": imageReadLink(ctx, image),
			"mount": func(path string) (string, error) { return mount.ReadLink(root + path) },
			"iofs":  func(path string) (string, error) { return stdfs.ReadLink(view, path) },
		} {
			if got, err := read(name); err != nil || got == "" {
				t.Errorf("%s ReadLink(%q) = %q, %v", label, name, got, err)
			}
		}
	}
	for _, name := range []string{"dir/escape", "through-alias", "chain", "root-escape", "absolute"} {
		if info, err := mount.Lstat(root + name); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
			t.Fatalf("Lstat(%q) = %v, %v", name, info, err)
		}
		for label, read := range map[string]func(string) (string, error){
			"image": imageReadLink(ctx, image),
			"mount": func(path string) (string, error) { return mount.ReadLink(root + path) },
			"iofs":  func(path string) (string, error) { return stdfs.ReadLink(view, path) },
		} {
			if got, err := read(name); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("%s ReadLink(%q) = %q, %v", label, name, got, err)
			}
		}
	}
	for _, name := range []string{"backslash", "drive", "nul"} {
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
	if got, err := stdfs.ReadLink(sub, "up"); err != nil || got != ".." {
		t.Errorf("sub safe link = %q, %v", got, err)
	}
	if got, err := stdfs.ReadLink(sub, "escape"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("sub escaping link = %q, %v", got, err)
	}
}

func imageReadLink(ctx context.Context, image *FS) func(string) (string, error) {
	return func(path string) (string, error) { return image.ReadLink(ctx, path) }
}

// An unavailable nested index cannot be assumed safe. ReadLink must return the
// storage error before a mount adapter receives the target.
func TestReadLinkChecksNestedTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	nested, err := EncodeIndex(ctx, Index{Entries: []Entry{{Path: []string{"file"}, Type: Symlink, Target: "../../outside"}}}, eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
	if err := builder.AddSubdirectory("nested", nested); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddSymlink("link", "nested/file"); err != nil {
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
	if got, err := image.ReadLink(ctx, "link"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Fatalf("link to escaping nested link = %q, %v", got, err)
	}
	delete(blocks.Blocks, nested.Root.Reference)
	image, err = OpenFS(ctx, outer, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := image.ReadLink(ctx, "link"); got != "" || !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("link to unavailable nested index = %q, %v", got, err)
	}
}
