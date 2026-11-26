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

func storeFromSource(source eris.BlockSource, options eris.BlockOptions) *Store {
	return NewStore(eris.NewBlockCapabilityStore(source), options)
}

func storeFromSink(sink eris.BlockSink, options eris.BlockOptions) *Store {
	if sink == nil {
		return NewStore(nil, options)
	}
	if source, ok := sink.(eris.BlockSource); ok {
		return storeFromSource(source, options)
	}
	// The compatibility API also accepts write-only sinks.
	return storeFromSource(eris.NewCombinedBlockStore(sink), options)
}
