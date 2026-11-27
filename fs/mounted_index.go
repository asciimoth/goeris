package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/streamio"
)

// mountedBoundaries reads index metadata only when an operation needs to
// distinguish an implicit directory from a nested image root.
type mountedBoundaries struct {
	image   *mountedImage
	tree    *os.Root
	root    *os.Root
	indexes map[eris.Capability]*node
}

func mountedDepth(dir string) int {
	if dir == "." {
		return 0
	}
	return 1 + strings.Count(filepath.ToSlash(dir), "/")
}

func (b *mountedBoundaries) close() {
	if b.root != nil {
		_ = b.root.Close()
	}
}

// The standard layout provides decoded index bytes beside the fs directory.
// Use them only when this is also the parent of the opened capability tree.
func (b *mountedBoundaries) openIndexRoot() *os.Root {
	if b.image.capability == (eris.Capability{}) {
		return nil
	}
	dir, capName := filepath.Split(b.image.root)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	base, name := filepath.Split(dir)
	if name != "fs" {
		return nil
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil
	}
	if b.sameTree(root, capName) {
		return root
	}
	_ = root.Close()
	return nil
}

func (b *mountedBoundaries) sameTree(root *os.Root, capName string) bool {
	dir, err := root.Lstat("fs")
	if err != nil || !dir.IsDir() {
		return false
	}
	expected, err := root.Lstat(filepath.Join("fs", capName))
	if err != nil || !expected.IsDir() {
		return false
	}
	actual, err := b.tree.Stat(".")
	return err == nil && os.SameFile(expected, actual)
}

func (b *mountedBoundaries) index(ctx context.Context, cap eris.Capability) *node {
	if b.indexes == nil {
		b.indexes = make(map[eris.Capability]*node)
		b.root = b.openIndexRoot()
	}
	if b.root == nil {
		return nil
	}
	if tree, ok := b.indexes[cap]; ok {
		return tree
	}
	var tree *node
	if index, ok := readMountedIndex(ctx, b.root, cap.String()); ok {
		tree = buildTree(index, nil, ".", DefaultMaxIndexSize)
	}
	b.indexes[cap] = tree
	return tree
}

func readMountedIndex(ctx context.Context, root *os.Root, name string) (Index, bool) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > DefaultMaxIndexSize {
		return Index{}, false
	}
	file, err := root.OpenFile(name, streamio.ReadFileFlags, 0)
	if err != nil {
		return Index{}, false
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return Index{}, false
	}
	data := make([]byte, info.Size())
	if _, err := streamio.ReadFull(ctx, file, data); err != nil {
		return Index{}, false
	}
	index, err := ParseIndex(data)
	return index, err == nil
}

// Only a directory identified by the index can be treated as an implicit
// directory. If the metadata is unavailable, confine the link to dir.
func (b *mountedBoundaries) depth(ctx context.Context, dir string) int {
	if dir == "." {
		return 0
	}
	cap := b.image.capability
	var current *node
	depth, boundary := 0, 0
	for component := range strings.SplitSeq(filepath.ToSlash(dir), "/") {
		if current == nil {
			current = b.index(ctx, cap)
			if current == nil {
				return mountedDepth(dir)
			}
		}
		depth++
		current = current.children[component]
		if current == nil {
			return mountedDepth(dir)
		}
		switch current.kind {
		case Subdirectory:
			boundary = depth
			cap, current = current.capability, nil
		case directoryType:
		default:
			return mountedDepth(dir)
		}
	}
	return boundary
}

func (m *mountedImage) isImplicitDirectory(ctx context.Context, name string) (bool, error) {
	return withMountedRoot(ctx, m, "lstat", name, func(r *os.Root, dir string) (bool, error) {
		b := mountedBoundaries{image: m, tree: r}
		defer b.close()
		return b.depth(ctx, dir) < mountedDepth(dir), ctx.Err()
	})
}

func (m *mountedImage) indexedLocation(ctx context.Context) imageLocation {
	location, _ := withMountedRoot(ctx, m, "open", ".", func(r *os.Root, _ string) (imageLocation, error) {
		b := mountedBoundaries{image: m, tree: r}
		defer b.close()
		if b.index(ctx, m.capability) == nil || ctx.Err() != nil {
			return imageLocation{}, nil
		}
		return imageLocation{index: m.capability, dir: m.prefix}, nil
	})
	return location
}
