package eris

import "context"

// BlockSource retrieves complete encrypted blocks by reference.
// Sources used by concurrent consumers must support concurrent calls.
type BlockSource interface {
	GetBlock(ref Reference) ([]byte, error)
}

// ContextBlockSource retrieves blocks with cancellation and deadline support.
// Handlers use it when a BlockSource also implements this interface.
type ContextBlockSource interface {
	GetBlockContext(ctx context.Context, ref Reference) ([]byte, error)
}

// BlockSink stores encrypted blocks by reference. A sink must copy block if it
// keeps the data after PutBlock returns. Sinks used by concurrent consumers must
// support concurrent calls.
type BlockSink interface {
	PutBlock(ref Reference, block []byte) error
}

// ContextBlockSink stores blocks with cancellation and deadline support. A sink
// must copy block if it keeps the data after PutBlockContext returns. Handlers
// use this interface when a BlockSource also implements it.
type ContextBlockSink interface {
	PutBlockContext(ctx context.Context, ref Reference, block []byte) error
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}
