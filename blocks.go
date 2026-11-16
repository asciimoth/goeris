package eris

import "context"

// BlockOptions holds options for block storage operations.
// Pass and return it by value. Middleware must pass options unchanged by default.
// The zero value selects the default options.
type BlockOptions struct {
	// HighPriority is an advisory flag. False is the default. Terminal stores
	// define its meaning: for example, cache eviction, read/write scheduling,
	// or no special action. It does not promise retention or faster service.
	// Repeated puts should keep the highest priority (true wins). Use
	// UpdateBlockOptions to explicitly change an existing block's priority.
	HighPriority bool
}

// BlockSource retrieves complete encrypted blocks by reference. It must return
// only blocks that match the requested reference. A source that can read
// incorrect data must verify it before returning it.
// Sources must honor cancellation where possible and document any limits.
// Callers must supply a non-nil context. Middleware must pass it unchanged.
// Sources used by concurrent consumers must support concurrent calls.
// The ttl argument follows TTL: terminal stores may ignore it, middleware
// must preserve it, and network clients must consume one forward.
// Sources return the stored options as the third value. Unsupported options
// and options returned with an error have their zero values. Read request
// options do not change stored options. Middleware preserves returned options.
type BlockSource interface {
	GetBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions)
}

// BlockSink stores encrypted blocks and updates their storage options.
// Sinks must honor cancellation where possible and document any limits.
// Callers must supply a non-nil context. Middleware must pass it unchanged.
// At most one expected reference can be supplied. If it does not match the
// block, the sink must return ErrInvalidBlock without storing the block. A sink
// must copy block if it keeps the data after PutBlock returns. Sinks used by
// concurrent consumers must support concurrent calls.
// The ttl argument follows the same rules as in BlockSource, including for
// writes copied to other nodes. Use NoTTL when no limit is specified.
// Terminal sinks may ignore options. Middleware passes them unchanged by default.
type BlockSink interface {
	PutBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error)

	// UpdateBlockOptions updates options for the block identified by ref.
	// It returns true only when the options were updated.
	// It returns false, nil if the block is absent or options are unsupported.
	// Unlike PutBlock, this operation can lower an existing priority.
	// It returns false, nil when the options already match.
	// Middleware passes context, TTL, and options unchanged, as with PutBlock.
	UpdateBlockOptions(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) (bool, error)
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}
