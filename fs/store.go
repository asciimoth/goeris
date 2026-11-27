package fs

import "github.com/asciimoth/goeris"

// Store reads and builds ERIS-FS images by capability. Each image operation
// uses its caller's context and the options supplied to NewStore.
type Store struct {
	content eris.CapabilityStore
	options eris.BlockOptions
}

// NewStore binds an ERIS capability store to the options for image reads and
// writes. A read-only capability store can open images and decode indexes.
// A nil capability store disables image operations.
func NewStore(content eris.CapabilityStore, options eris.BlockOptions) *Store {
	return &Store{content: content, options: options}
}
