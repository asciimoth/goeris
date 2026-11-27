package fs

import (
	"errors"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestMountedIndexSymlinkBoundaries(t *testing.T) {
	ctx := t.Context()
	options := eris.BlockOptions{}
	blocks := fixtureBlocks()
	content := eris.NewBlockCapabilityStore(blocks)
	store := NewStore(content, options)
	base := t.TempDir()
	writeFile := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeIndex := func(cap eris.Capability) {
		t.Helper()
		if _, err := filepath.Localize(cap.String()); err != nil {
			t.Skipf("host cannot represent capability names: %v", err)
		}
		data, err := content.Decode(ctx, cap, options)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(filepath.Join(base, cap.String()), data)
	}

	file, err := content.Encode(ctx, []byte("local"), eris.Secret{}, eris.BlockSize1KiB, options)
	if err != nil {
		t.Fatal(err)
	}
	nested := storeV1Index(t, Index{Entries: []Entry{
		{Path: []string{"file"}, Type: Regular, Capability: file},
		{Path: []string{"local", "back"}, Type: Symlink, Target: "../file"},
		{Path: []string{"local", "up"}, Type: Symlink, Target: "../../file"},
		{Path: []string{"up"}, Type: Symlink, Target: "../file"},
	}}, blocks)
	absolute := filepath.Join(t.TempDir(), "file")
	writeFile(absolute, []byte("host"))
	links := map[string]string{
		"docs/back": "../file", "alias": "docs", "nested/local/back": "../file",
		"nested/local/up": "../../file", "nested/up": "../file",
		"host": absolute, "other": "../" + nested.String() + "/file", "chain": "nested/up",
	}
	index := Index{Entries: []Entry{
		{Path: []string{"file"}, Type: Regular, Capability: file, Size: 5},
		{Path: []string{"nested"}, Type: Subdirectory, Capability: nested},
	}}
	for _, name := range []string{"docs/back", "alias", "host", "other", "chain"} {
		index.Entries = append(index.Entries, Entry{Path: strings.Split(name, "/"), Type: Symlink, Target: links[name]})
	}
	cap, err := store.EncodeIndex(ctx, index, eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	writeIndex(cap)
	writeIndex(nested)
	trees := filepath.Join(base, "fs")
	root := filepath.Join(trees, cap.String())
	writeFile(filepath.Join(root, "file"), []byte("local"))
	writeFile(filepath.Join(root, "nested", "file"), []byte("local"))
	writeFile(filepath.Join(trees, nested.String(), "file"), []byte("other image"))
	for name, target := range links {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("cannot create a symbolic link: %v", err)
		}
	}

	mounted := NewMountStore(trees)
	treeAlias := filepath.Join(base, "trees")
	if err := os.Symlink(trees, treeAlias); err != nil {
		t.Fatal(err)
	}
	checkLinks := func(t *testing.T, view stdfs.FS, prefix string) {
		t.Helper()
		for _, name := range []string{"docs/back", "alias/back", "nested/local/back"} {
			if got, err := stdfs.ReadFile(view, prefix+name); err != nil || string(got) != "local" {
				t.Errorf("ReadFile(%q) = %q, %v", name, got, err)
			}
			if got, err := stdfs.ReadLink(view, prefix+name); err != nil || got != "../file" {
				t.Errorf("ReadLink(%q) = %q, %v", name, got, err)
			}
		}
		for _, name := range []string{"nested/up", "nested/local/up", "host", "other", "chain"} {
			if got, err := stdfs.ReadFile(view, prefix+name); got != nil || !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("ReadFile(%q) = %q, %v", name, got, err)
			}
			if got, err := stdfs.ReadLink(view, prefix+name); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
				t.Errorf("ReadLink(%q) = %q, %v", name, got, err)
			}
		}
	}
	missingTrees := filepath.Join(t.TempDir(), "fs")
	missingMount := NewMountStore(missingTrees)
	for name, backend := range map[string]ImageStore{
		"mounted":                     mounted,
		"mount path alias":            NewMountStore(treeAlias),
		"decoded then mounted":        NewCombinedStore(store, mounted),
		"mounted then decoded":        NewCombinedStore(mounted, store),
		"absent mount first":          NewCombinedStore(missingMount, mounted),
		"absent mount last":           NewCombinedStore(mounted, missingMount),
		"all alternate mounts absent": NewCombinedStore(NewCombinedStore(missingMount, missingMount), mounted),
	} {
		t.Run(name, func(t *testing.T) {
			checkLinks(t, NewMountWithStore(ctx, backend, nil, options), "fs/"+cap.String()+"/")
		})
	}
	// The default mount searches two directories. Either capability tree
	// can be absent while the other still supplies a valid link.
	for _, roots := range [][]string{{missingTrees, trees}, {trees, missingTrees}} {
		view, err := openMountedTrees(ctx, cap, roots).result()
		if err != nil {
			t.Fatal(err)
		}
		checkLinks(t, NewIOFS(ctx, view), "")
	}
	unindexedTrees := filepath.Join(t.TempDir(), "fs")
	unindexedDocs := filepath.Join(unindexedTrees, cap.String(), "docs")
	if err := os.MkdirAll(unindexedDocs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../file", filepath.Join(unindexedDocs, "back")); err != nil {
		t.Fatal(err)
	}
	view, err := openMountedTrees(ctx, cap, []string{trees, unindexedTrees}).result()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.ReadLink(ctx, "docs/back"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink with an alternate tree without index metadata = %q, %v", got, err)
	}

	// The last open error of an alternate chain can report a missing
	// capability even when an earlier mount had a different error.
	invalidTrees := t.TempDir()
	writeFile(filepath.Join(invalidTrees, cap.String()), nil)
	alternate := NewCombinedStore(NewMountStore(invalidTrees), missingMount)
	view, err = NewCombinedStore(mounted, alternate).OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := view.ReadLink(ctx, "docs/back"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink with an invalid alternate tree = %q, %v", got, err)
	}

	image, err := mounted.OpenFS(ctx, cap)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := image.Sub(ctx, "alias")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sub.ResolvePath(ctx, "back"); err != nil || got != filepath.Join(root, "file") {
		t.Errorf("Sub ResolvePath(back) = %q, %v", got, err)
	}
	if got, err := sub.ReadLink(ctx, "back"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("Sub ReadLink(back) = %q, %v", got, err)
	}

	// An unavailable nested index leaves its parent image usable, but
	// cannot establish which directories inside the nested image are implicit.
	nestedIndex := filepath.Join(base, nested.String())
	saved := filepath.Join(t.TempDir(), "index")
	if err := os.Rename(nestedIndex, saved); err != nil {
		t.Fatal(err)
	}
	if got, err := image.ReadLink(ctx, "docs/back"); err != nil || got != "../file" {
		t.Errorf("ReadLink without unrelated nested index = %q, %v", got, err)
	}
	if got, err := image.ReadLink(ctx, "nested/local/back"); got != "" || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadLink without nested index = %q, %v", got, err)
	}
	if err := os.Symlink(saved, nestedIndex); err != nil {
		t.Fatal(err)
	}
	if got, err := image.ReadFile(ctx, "nested/local/back"); got != nil || !errors.Is(err, stdfs.ErrNotExist) {
		t.Errorf("ReadFile with linked index metadata = %q, %v", got, err)
	}
}
