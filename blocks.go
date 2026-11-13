package eris

import "context"

// BlockSource retrieves complete encrypted blocks by reference. It must return
// only blocks that match the requested reference. A source that can read
// incorrect data must verify it before returning it.
// Sources used by concurrent consumers must support concurrent calls.
// The ttl argument follows TTL: terminal stores may ignore it, middleware
// must preserve it, and network clients must consume one forward.
type BlockSource interface {
	GetBlock(ref Reference, ttl TTL) ([]byte, error)
}

// ContextBlockSource retrieves blocks with cancellation and deadline support.
// It must return only blocks that match the requested reference. Handlers use
// it when a BlockSource also implements this interface.
// The ttl argument has the same contract as in BlockSource.
type ContextBlockSource interface {
	GetBlockContext(ctx context.Context, ref Reference, ttl TTL) ([]byte, error)
}

// BlockSink stores encrypted blocks and returns their computed reference.
// At most one expected reference can be supplied. If it does not match the
// block, the sink must return ErrInvalidBlock without storing the block. A sink
// must copy block if it keeps the data after PutBlock returns. Sinks used by
// concurrent consumers must support concurrent calls.
// The ttl argument follows the same rules as in BlockSource, including for
// writes copied to other nodes. Use NoTTL when no limit is specified.
type BlockSink interface {
	PutBlock(block []byte, ttl TTL, expected ...Reference) (Reference, error)
}

// ContextBlockSink stores blocks with cancellation and deadline support and
// returns their computed reference. At most one expected reference can be
// supplied. If it does not match the block, the sink must return ErrInvalidBlock
// without storing the block. A sink must copy block if it keeps the data after
// PutBlockContext returns. Handlers use this interface when a BlockSource also
// implements it.
// The ttl argument has the same contract as in BlockSink.
type ContextBlockSink interface {
	PutBlockContext(ctx context.Context, block []byte, ttl TTL, expected ...Reference) (Reference, error)
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}
