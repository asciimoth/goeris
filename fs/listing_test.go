package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"testing"
)

func TestDirectoryListingContextAndOwnership(t *testing.T) {
	index := Index{Entries: []Entry{
		{Path: []string{"b"}, Type: Symlink, Target: "a"},
		{Path: []string{"a"}, Type: Symlink, Target: "b"},
	}}
	image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	all, err := image.ReadDir(ctx, ".")
	if err != nil || names(all) != "a,b" {
		t.Fatalf("ReadDir = %v, %v", all, err)
	}
	first := all[0]
	all[0] = nil
	file, err := image.Open(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	}()
	page, err := file.(stdfs.ReadDirFile).ReadDir(1)
	if err != nil || len(page) != 1 || page[0] == nil || page[0].Name() != "a" {
		t.Fatalf("ReadDir(1) = %v, %v", page, err)
	}
	cancel()
	if _, err := first.Info(); !errors.Is(err, context.Canceled) {
		t.Fatalf("first.Info() = %v", err)
	}
	if _, err := page[0].Info(); err != nil {
		t.Fatalf("page.Info() = %v", err)
	}
}
