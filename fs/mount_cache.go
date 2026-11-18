package fs

import (
	"container/list"
	stdfs "io/fs"
	"strings"
	"sync"

	"github.com/asciimoth/goeris"
)

// WithListingCache returns a new Mount with an empty, bounded listing cache.
// The original Mount is unchanged. Both limits count entries, not bytes. Zero
// disables that category; negative limits return fs.ErrInvalid. The default
// constructors disable both categories. This method performs no I/O.
//
// A capability is remembered after a successful Open, OpenFile, Stat, or Lstat
// at C, or after loading an index at fs/C. A raw block is remembered after a
// successful load and verification at either hash path. A later read or path
// lookup can still fail. Blocks fetched internally to decode content and file
// capabilities found inside an image are not added to these listings.
//
// The root lists remembered capabilities as canonical URNs, raw blocks as flat
// uppercase hashes, and their first volume prefixes. The fs directory lists
// only capabilities that have been loaded as valid indexes. Volume prefix
// directories list the next component of remembered raw-block paths. Aliases
// share one entry. Successful access refreshes recency. Listing virtual
// directories (the root, fs, and volume prefixes) or reading DirEntry.Info does
// not. Reading a directory inside fs/C loads its index and refreshes C. Each
// category evicts its least recently used entry independently. Failed loads do
// not add entries or change recency.
// Capability URNs contain decryption keys. Anyone who can list these virtual
// directories can read the remembered keys. Restrict access when they are private.
//
// The cache retains identifiers and metadata, never file content or indexes.
// It does not serve reads or guarantee continued source availability. Virtual
// directory listings need no block I/O. DirEntry.Info can read storage for a
// capability whose decoded size is not yet known. Open directories retain a snapshot of
// their entries, even after eviction. Retained handles and returned listings
// are not counted in the cache limits.
//
// Virtual-directory Sub views share this cache and its limits. A separate call
// to WithListingCache creates a separate cache, including when called on a Sub
// view. WithListingCache(0, 0) returns a view with listing caches disabled. It
// does not disable the cache in other views. Concurrent operations are safe.
func (m *Mount) WithListingCache(capabilityLimit, blockLimit int) (*Mount, error) {
	if m == nil || m.ctx == nil || m.source == nil || capabilityLimit < 0 || blockLimit < 0 {
		return nil, stdfs.ErrInvalid
	}
	result := *m
	result.listings = nil
	if capabilityLimit != 0 || blockLimit != 0 {
		result.listings = &mountListingCache{
			capabilities: mountRecent[eris.Capability, mountCapabilityInfo]{limit: capabilityLimit},
			blocks:       mountRecent[eris.Reference, int64]{limit: blockLimit},
		}
	}
	return &result, nil
}

type mountCapabilityInfo struct {
	size  int64 // -1 until a content open or stat supplies the decoded size.
	image bool
}

type mountListingCache struct {
	mu           sync.Mutex
	capabilities mountRecent[eris.Capability, mountCapabilityInfo]
	blocks       mountRecent[eris.Reference, int64]
}

// mountRecent is protected by mountListingCache.mu. Values have fixed size;
// allocation depends on actual entries, never on the caller's limit alone.
type mountRecent[K comparable, V any] struct {
	limit   int
	entries map[K]*list.Element
	order   list.List // Most recently used first.
}

type mountRecentEntry[K comparable, V any] struct {
	key   K
	value V
}

func (c *mountRecent[K, V]) put(key K, value V) {
	if c.limit == 0 {
		return
	}
	if element := c.entries[key]; element != nil {
		element.Value = mountRecentEntry[K, V]{key, value}
		c.order.MoveToFront(element)
		return
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	if len(c.entries) == c.limit {
		oldest := c.order.Back()
		delete(c.entries, oldest.Value.(mountRecentEntry[K, V]).key)
		c.order.Remove(oldest)
	}
	c.entries[key] = c.order.PushFront(mountRecentEntry[K, V]{key, value})
}

func (m *Mount) rememberCapability(cap eris.Capability, size int64, image bool) {
	if m.listings == nil {
		return
	}
	c := m.listings
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.capabilities.entries[cap]; previous != nil {
		old := previous.Value.(mountRecentEntry[eris.Capability, mountCapabilityInfo]).value
		image = image || old.image
		if size < 0 {
			size = old.size
		}
	}
	c.capabilities.put(cap, mountCapabilityInfo{size, image})
}

// rememberContent runs only after successful opens or metadata operations.
// The resolved node already has a size, so recording it requires no extra I/O.
func (m *Mount) rememberContent(name string, image *FS, inner string) {
	if m.listings == nil {
		return
	}
	cap, err := mountCapability(m.fullPath(name))
	if err != nil {
		return
	}
	m.rememberCapability(cap, image.root.children[inner].Size(), false)
}

func (m *Mount) rememberBlock(ref eris.Reference, size int64) {
	if m.listings == nil {
		return
	}
	m.listings.mu.Lock()
	defer m.listings.mu.Unlock()
	m.listings.blocks.put(ref, size)
}

// addListings builds metadata nodes under a virtual directory. It performs no
// I/O and does not change recency. The nodes belong to this directory snapshot;
// later cache updates cannot change an open handle or an earlier DirEntry.
func (m *Mount) addListings(dir *node, full string) {
	if m.listings == nil {
		return
	}
	c := m.listings
	c.mu.Lock()
	defer c.mu.Unlock()
	directory := func(name string) *node {
		return buildTree(Index{}, m.source, name, m.indexLimit, m.options)
	}
	switch {
	case full == ".":
		for cap, element := range c.capabilities.entries {
			info := element.Value.(mountRecentEntry[eris.Capability, mountCapabilityInfo]).value
			name := cap.String()
			dir.children[name] = &node{name: name, kind: Regular, capability: cap,
				source: m.source, options: m.options, size: info.size, sizeKnown: info.size >= 0}
		}
		for ref, element := range c.blocks.entries {
			name := ref.String()
			size := element.Value.(mountRecentEntry[eris.Reference, int64]).value
			dir.children[name] = &node{name: name, kind: Regular, size: size, sizeKnown: true}
			dir.children[name[:3]] = directory(name[:3])
		}
	case mountUpper(full) == "FS":
		for cap, element := range c.capabilities.entries {
			if element.Value.(mountRecentEntry[eris.Capability, mountCapabilityInfo]).value.image {
				name := cap.String()
				dir.children[name] = directory(name)
			}
		}
	default: // A validated volume prefix, with one to three components.
		prefix := strings.ReplaceAll(mountUpper(full), "/", "")
		for ref, element := range c.blocks.entries {
			hash := ref.String()
			if !strings.HasPrefix(hash, prefix) {
				continue
			}
			rest := hash[len(prefix):]
			if len(prefix) < 9 {
				dir.children[rest[:3]] = directory(rest[:3])
			} else {
				size := element.Value.(mountRecentEntry[eris.Reference, int64]).value
				dir.children[rest] = &node{name: rest, kind: Regular, size: size, sizeKnown: true}
			}
		}
	}
}
