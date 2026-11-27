package fs

import (
	"context"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/imagepath"
)

// Prefetch requests all content below name. Use "." for this file system,
// including a subtree selected with Sub. The named path follows links as Open
// does. Links found inside a directory are skipped, so they cannot cause loops
// or pull content from outside that directory.
//
// The call reads nested indexes and sends one hint per file capability. A
// block-backed store reads internal nodes to discover leaves. Repeated
// capabilities are visited at most once as a file and once as a directory.
// Indexes decoded for path lookup or traversal are kept out of the live tree.
// Index reads and prefetch calls can return errors. All hints use NoTTL and
// the image store's options.
func (fileSystem *FS) Prefetch(ctx context.Context, name string) error {
	item, _, err := fileSystem.lookupWithChildren(ctx, name, imagepath.Follow, (*node).prefetchChildren)
	if err != nil {
		return pathError("prefetch", name, err)
	}
	type entryID struct {
		capability eris.Capability
		directory  bool
	}
	seen := make(map[entryID]bool)
	pending := []*node{item}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return pathError("prefetch", name, err)
		}
		last := len(pending) - 1
		item = pending[last]
		pending[last] = nil
		pending = pending[:last]
		if item.kind == Symlink {
			continue
		}
		if item.kind != directoryType {
			id := entryID{item.capability, item.isDir()}
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		if item.isDir() {
			children, err := item.prefetchChildren(ctx)
			if err != nil {
				return pathError("prefetch", name, err)
			}
			for _, child := range children {
				pending = append(pending, child)
			}
		} else if err := item.prefetchFile(ctx); err != nil {
			return pathError("prefetch", name, err)
		}
	}
	return nil
}

func (item *node) prefetchChildren(ctx context.Context) (map[string]*node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if item.kind != Subdirectory {
		return item.children, nil
	}
	item.mu.Lock()
	children := item.children
	item.mu.Unlock()
	if children != nil {
		return children, nil
	}
	// Prefetch can visit the entire image. Keep its temporary subtrees out
	// of the live tree used for path lookups.
	return item.decodeChildren(ctx)
}

// Prefetch hints this open file's capability using the supplied traversal
// context. The hint is independent of the reader and its current position.
func (file *regularFile) Prefetch(ctx context.Context) error {
	state := file.state.Load()
	if state == nil {
		return ErrClosed
	}
	return state.node.prefetchFile(ctx)
}

func (item *node) prefetchFile(ctx context.Context) error {
	return item.store.content.Prefetch(ctx, item.capability, eris.NoTTL, item.store.options)
}
