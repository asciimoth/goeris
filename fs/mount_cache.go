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
// at C, or after opening an image at fs/C. A raw block is remembered after a
// successful load and verification at either hash path. A later read or path
// lookup can still fail. Blocks fetched internally to decode content and file
// capabilities found inside an image are not added to these listings.
//
// With a capability-backed *Store, the root lists remembered capabilities
// as canonical URNs. With a block source, it lists flat uppercase hashes and
// their first volume prefixes. The fs directory lists opened images. Volume prefix
// directories list the next component of remembered raw-block paths. Aliases
// share one entry. Successful access refreshes recency. Listing virtual
// directories (the root, fs, and volume prefixes) or reading DirEntry.Info does
// not. Reading a directory inside fs/C opens the image and refreshes C. Each
// category evicts its least recently used entry independently. Failed loads do
// not add entries or change recency.
// Capability URNs contain decryption keys. Anyone who can list these virtual
// directories can read the remembered keys. Restrict access when they are private.
//
// The cache retains identifiers and metadata, never file content or indexes.
// It does not serve reads or guarantee continued source availability. Virtual
// directory listings need no block I/O. DirEntry.Info can read storage for a
// capability whose decoded size is not yet known, or ask an image backend for
// its root metadata. Open directories retain a snapshot of their entries, even
// after eviction. Cache limits exclude retained handles and returned listings.
//
// Virtual-directory Sub views share this cache and its limits. A separate call
// to WithListingCache creates a separate cache, including when called on a Sub
// view. WithListingCache(0, 0) returns a view with listing caches disabled. It
// does not disable the cache in other views. Concurrent operations are safe.
func (m *Mount) WithListingCache(capabilityLimit, blockLimit int) (*Mount, error) {
	if m == nil || m.ctx == nil || capabilityLimit < 0 || blockLimit < 0 {
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

type mountListingKey interface {
	comparable
	String() string
}

// mountRecent is protected by mountListingCache.mu. Values have fixed size;
// allocation depends on actual entries, never on the caller's limit alone.
type mountRecent[K mountListingKey, V any] struct {
	limit   int
	entries map[K]*list.Element
	order   list.List // Most recently used first.
}

type mountRecentEntry[K mountListingKey, V any] struct {
	key         K
	value       V
	listingName string
}

// Format names only when listed. Refreshing a remembered entry keeps its name.
func (e *mountRecentEntry[K, V]) name() string {
	if e.listingName == "" {
		e.listingName = e.key.String()
	}
	return e.listingName
}

func (c *mountRecent[K, V]) put(key K, value V) {
	if c.limit == 0 {
		return
	}
	if element := c.entries[key]; element != nil {
		element.Value.(*mountRecentEntry[K, V]).value = value
		c.order.MoveToFront(element)
		return
	}
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	if len(c.entries) == c.limit {
		oldest := c.order.Back()
		entry := oldest.Value.(*mountRecentEntry[K, V])
		delete(c.entries, entry.key)
		*entry = mountRecentEntry[K, V]{key: key, value: value}
		c.entries[key] = oldest
		c.order.MoveToFront(oldest)
		return
	}
	c.entries[key] = c.order.PushFront(&mountRecentEntry[K, V]{key: key, value: value})
}

func (m *Mount) rememberCapability(cap eris.Capability, size int64, image bool) {
	if m.listings == nil {
		return
	}
	c := m.listings
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.capabilities.entries[cap]; previous != nil {
		old := previous.Value.(*mountRecentEntry[eris.Capability, mountCapabilityInfo]).value
		image = image || old.image
		if size < 0 {
			size = old.size
		}
	}
	c.capabilities.put(cap, mountCapabilityInfo{size, image})
}

// rememberContent runs only after successful opens or metadata operations.
// The resolved node already has a size, so recording it requires no extra I/O.
func (m *Mount) rememberContent(name string, stat func() (stdfs.FileInfo, error)) {
	if m.listings == nil {
		return
	}
	cap, err := mountCapability(m.fullPath(name))
	if err != nil {
		return
	}
	if info, err := stat(); err == nil {
		m.rememberCapability(cap, info.Size(), false)
	}
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
	addPrefix := func(name string) {
		if dir.children[name] == nil {
			dir.children[name] = &node{name: name, kind: directoryType}
		}
	}
	switch {
	case full == ".":
		if m.content != nil {
			for _, element := range c.capabilities.entries {
				entry := element.Value.(*mountRecentEntry[eris.Capability, mountCapabilityInfo])
				info, name := entry.value, entry.name()
				child := &node{name: name, kind: Regular, capability: entry.key, store: m.content}
				child.initSize(info.size)
				dir.children[name] = child
			}
		}
		for _, element := range c.blocks.entries {
			entry := element.Value.(*mountRecentEntry[eris.Reference, int64])
			name, size := entry.name(), entry.value
			child := &node{name: name, kind: Regular}
			child.initSize(size)
			dir.children[name] = child
			addPrefix(name[:3])
		}
	case isMountFS(full):
		imageSource := m.mountImageSource
		for _, element := range c.capabilities.entries {
			entry := element.Value.(*mountRecentEntry[eris.Capability, mountCapabilityInfo])
			if entry.value.image {
				name := entry.name()
				child := &node{name: name, kind: directoryType}
				// A *Store uses standard ERIS-FS directory metadata. Other
				// backends supply their own when the caller requests Info.
				if m.content == nil {
					child.listedImage = &imageSource
					child.capability = entry.key
				}
				dir.children[name] = child
			}
		}
	default: // A validated volume prefix, with one to three components.
		prefix := strings.ReplaceAll(mountUpper(full), "/", "")
		for _, element := range c.blocks.entries {
			entry := element.Value.(*mountRecentEntry[eris.Reference, int64])
			hash := entry.name()
			if !strings.HasPrefix(hash, prefix) {
				continue
			}
			rest := hash[len(prefix):]
			if len(prefix) < 9 {
				addPrefix(rest[:3])
			} else {
				size := entry.value
				child := &node{name: rest, kind: Regular}
				child.initSize(size)
				dir.children[rest] = child
			}
		}
	}
}
