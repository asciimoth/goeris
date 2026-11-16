package eris

import "context"

// BlockOptions holds options for block storage operations.
// It currently has no fields. Pass and return it by value.
// The zero value selects the default options.
type BlockOptions struct{}

// BlockSource retrieves complete encrypted blocks by reference. It must return
// only blocks that match the requested reference. A source that can read
// incorrect data must verify it before returning it.
// Sources must honor cancellation where possible and document any limits.
// Callers must supply a non-nil context. Middleware must pass it unchanged.
// Sources used by concurrent consumers must support concurrent calls.
// The ttl argument follows TTL: terminal stores may ignore it, middleware
// must preserve it, and network clients must consume one forward.
// Options are passed by value. Sources currently ignore options and return
// BlockOptions{} as the third value, including when an error occurs.
type BlockSource interface {
	GetBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions)
}

// BlockSink stores encrypted blocks and returns their computed reference.
// Sinks must honor cancellation where possible and document any limits.
// Callers must supply a non-nil context. Middleware must pass it unchanged.
// At most one expected reference can be supplied. If it does not match the
// block, the sink must return ErrInvalidBlock without storing the block. A sink
// must copy block if it keeps the data after PutBlock returns. Sinks used by
// concurrent consumers must support concurrent calls.
// The ttl argument follows the same rules as in BlockSource, including for
// writes copied to other nodes. Use NoTTL when no limit is specified.
// Options are passed by value. Sinks currently ignore them.
type BlockSink interface {
	PutBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error)
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}
