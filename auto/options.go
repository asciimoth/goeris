package auto

import (
	"slices"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fs"
)

// Option changes one store chain. Prepend and append calls keep their order
// within each group. The last replacement sets the middle of the chain.
// The same option can be used in multiple calls to [Open].
type Option func(*config)

type config struct {
	wrapBlocks   func(eris.BlockStore) eris.BlockStore
	blocks       chainOptions[any]
	capabilities chainOptions[eris.CapabilityStore]
	images       chainOptions[fs.ImageStore]
}

// WrapBlocks installs middleware around the assembled block chain before Open
// constructs the capability and image stores. The last wrapper wins. Pass nil
// to clear it. The wrapper must return a non-nil store and forward calls that
// it does not handle. Open retains ownership of the default volumes.
func WrapBlocks(wrap func(eris.BlockStore) eris.BlockStore) Option {
	return func(c *config) { c.wrapBlocks = wrap }
}

type chainOptions[T any] struct {
	before, replacement, after []T
	replaced                   bool
}

func (c *chainOptions[T]) entries(defaults ...T) []T {
	if c.replaced {
		defaults = c.replacement
	}
	return slices.Concat(c.before, defaults, c.after)
}

// PrependBlocks adds backends before the default or replacement block
// backends. Each backend can implement [eris.BlockSource], [eris.BlockSink],
// or both.
func PrependBlocks(stores ...any) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.blocks.before = append(c.blocks.before, stores...) }
}

// AppendBlocks adds backends after the default or replacement block backends.
// It accepts the same types as [PrependBlocks].
func AppendBlocks(stores ...any) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.blocks.after = append(c.blocks.after, stores...) }
}

// ReplaceBlocks uses stores in place of the default volume chain.
// With no stores, it removes the default entries. It accepts the same types
// as [PrependBlocks].
func ReplaceBlocks(stores ...any) Option {
	stores = slices.Clone(stores)
	return func(c *config) {
		c.blocks.replaced = true
		c.blocks.replacement = stores
	}
}

// PrependCapabilities adds backends before the default or replacement
// capability backends.
func PrependCapabilities(stores ...eris.CapabilityStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.capabilities.before = append(c.capabilities.before, stores...) }
}

// AppendCapabilities adds backends after the default or replacement
// capability backends.
func AppendCapabilities(stores ...eris.CapabilityStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.capabilities.after = append(c.capabilities.after, stores...) }
}

// ReplaceCapabilities uses stores in place of both default capability
// backends. With no stores, it removes the default entries.
func ReplaceCapabilities(stores ...eris.CapabilityStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) {
		c.capabilities.replaced = true
		c.capabilities.replacement = stores
	}
}

// PrependImages adds backends before the default or replacement image
// backends.
func PrependImages(stores ...fs.ImageStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.images.before = append(c.images.before, stores...) }
}

// AppendImages adds backends after the default or replacement image
// backends.
func AppendImages(stores ...fs.ImageStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) { c.images.after = append(c.images.after, stores...) }
}

// ReplaceImages uses stores in place of both default image backends. With
// no stores, it removes the default entries.
func ReplaceImages(stores ...fs.ImageStore) Option {
	stores = slices.Clone(stores)
	return func(c *config) {
		c.images.replaced = true
		c.images.replacement = stores
	}
}
