package fs

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
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

func TestPagedListingRetainedEntries(t *testing.T) {
	image := &FS{root: buildTree(wideIndex(), nil, ".", DefaultMaxIndexSize), prefix: "."}
	file, err := image.Open(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var entries []stdfs.DirEntry
	for {
		page, err := file.(stdfs.ReadDirFile).ReadDir(73)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 || len(page) > 73 {
			t.Fatalf("invalid page size %d", len(page))
		}
		entries = append(entries, page...)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, entry := range wideIndex().Entries {
		want = append(want, entry.Path[0])
	}
	slices.Sort(want)
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Name() != want[i] || entry.Name() != want[i] {
			t.Fatalf("entry %d = %v, %v, want %q", i, entry, err, want[i])
		}
	}
}
