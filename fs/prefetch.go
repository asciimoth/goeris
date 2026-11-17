package fs

import (
	"context"

	"github.com/asciimoth/goeris"
)

// Prefetch requests all content below name. Use "." for this file system,
// including a subtree selected with Sub. The named path follows links as Open
// does. Links found inside a directory are skipped, so they cannot cause loops
// or pull content from outside that directory.
//
// The call reads nested ERIS-FS indexes and file internal nodes to discover
// references. It does not read file leaves or decompress file content. Repeated
// capabilities are visited at most once as a file and once as a directory.
// The call can block on index reads and returns their errors. Success gives no cache or availability
// guarantee. All hints use NoTTL and the options supplied to OpenFS.
func (fileSystem *FS) Prefetch(ctx context.Context, name string) error {
	item, err := fileSystem.find(ctx, name, true)
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
		item = pending[len(pending)-1]
		pending = pending[:len(pending)-1]
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
			if err := item.load(ctx); err != nil {
				return pathError("prefetch", name, err)
			}
			item.mu.Lock()
			for _, child := range item.children {
				pending = append(pending, child)
			}
			item.mu.Unlock()
		} else if err := eris.Prefetch(ctx, item.capability, fileSystem.source, eris.NoTTL, item.options); err != nil {
			return pathError("prefetch", name, err)
		}
	}
	return nil
}
