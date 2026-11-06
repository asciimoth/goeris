package eris

import "context"

// BlockSource retrieves complete encrypted blocks by reference. It must return
// only blocks that match the requested reference. A source that can read
// incorrect data must verify it before returning it.
// Sources used by concurrent consumers must support concurrent calls.
type BlockSource interface {
	GetBlock(ref Reference) ([]byte, error)
}

// ContextBlockSource retrieves blocks with cancellation and deadline support.
// It must return only blocks that match the requested reference. Handlers use
// it when a BlockSource also implements this interface.
type ContextBlockSource interface {
	GetBlockContext(ctx context.Context, ref Reference) ([]byte, error)
}

// BlockSink stores encrypted blocks and returns their computed reference.
// At most one expected reference can be supplied. If it does not match the
// block, the sink must return ErrInvalidBlock without storing the block. A sink
// must copy block if it keeps the data after PutBlock returns. Sinks used by
// concurrent consumers must support concurrent calls.
type BlockSink interface {
	PutBlock(block []byte, expected ...Reference) (Reference, error)
}

// ContextBlockSink stores blocks with cancellation and deadline support and
// returns their computed reference. At most one expected reference can be
// supplied. If it does not match the block, the sink must return ErrInvalidBlock
// without storing the block. A sink must copy block if it keeps the data after
// PutBlockContext returns. Handlers use this interface when a BlockSource also
// implements it.
type ContextBlockSink interface {
	PutBlockContext(ctx context.Context, block []byte, expected ...Reference) (Reference, error)
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}
