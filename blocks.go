package eris

import (
	"context"
	"errors"

	"github.com/asciimoth/goeris/internal/batch"
)

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

	// Root marks an entry block for an object. It is an advisory hint for
	// indexing and block exchange between nodes. It does not prove that a
	// block is a root. Encoders always set it on the capability's root block,
	// including single-block content and ERIS-FS indexes.
	// Repeated puts should keep true if either value is true. Use
	// UpdateBlockOptions to explicitly set or clear the stored flag.
	Root bool
}

// ErrBatchTooLarge means that a batch exceeds the store's BatchSize limit.
// Stores must reject such a batch before processing any request.
var ErrBatchTooLarge = batch.ErrTooLarge

// ErrInvalidBatchResult means that storage returned an invalid result count.
// A successful call must return one result per request. A failed call must
// never return more results than requests.
var ErrInvalidBatchResult = errors.New("eris: invalid batch result count")

// BlockRequest identifies one read or options update. Each request has its own
// TTL and options. Read options do not change the stored options.
type BlockRequest struct {
	Reference Reference
	TTL       TTL
	Options   BlockOptions
}

// PutBlockRequest holds one encrypted block and its storage options.
// Expected is optional. If set, it must match the block's reference. A mismatch
// returns ErrInvalidBlock without storing that block. The sink must copy Data
// if it retains the bytes after PutBlock returns.
type PutBlockRequest struct {
	Data     []byte
	TTL      TTL
	Options  BlockOptions
	Expected *Reference
}

// BlockSource retrieves complete encrypted blocks. It must verify that each
// block matches its requested reference before returning it. Returned options
// are the stored options; unsupported options have their zero values.
//
// Results correspond to requests in order. A missing block has a zero Block
// result (nil Data and zero Options). Reads must continue after a missing block
// to return all available blocks. If any blocks are missing, the returned error
// must match ErrMissingBlock through errors.Is. The first missing error is
// preserved, including any wrapped storage error.
//
// Cancellation and other errors stop processing before the failed request.
// Results then contain only the processed prefix, including any missing slots.
// If a missing block precedes a stopping error, the returned error matches both
// through errors.Is. If only blocks are missing, the result length equals the
// request length. On success, every result contains a block.
// There is no whole-batch atomicity or rollback guarantee.
// Empty batches are no-ops, including with a canceled context.
// Request slices remain caller-owned. Implementations must not change them
// or retain them after return. Copy any request data that must be kept.
//
// Callers must supply a non-nil context. Middleware passes context, TTL, and
// options unchanged. Network clients consume one TTL forward. Implementations
// that process requests one at a time must check cancellation before each one.
// Stores used concurrently must support concurrent calls.
type BlockSource interface {
	GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error)

	// BatchSize is the maximum number of requests per call, for both reads
	// and writes. Zero permits any size. The value must not be negative.
	// If the limit is unknown (for example, a remote API), return 1 as a
	// safe default. Callers can use smaller batches to bound memory use.
	BatchSize() int
}

// BlockSink stores blocks and updates their options. Results correspond to
// requests in order. Each operation stops at its first error and returns only
// the completed prefix. A failed operation can have side effects.
// The cancellation, empty-batch, request ownership, and non-atomicity rules of
// BlockSource also apply.
// Terminal stores may ignore TTL and unsupported options. Middleware preserves
// them. Repeated puts retain true flags; explicit updates can clear them.
type BlockSink interface {
	PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error)

	// UpdateBlockOptions replaces each block's options. A result is true only
	// when options changed. Absent blocks, unsupported options, and unchanged
	// options return false without an error.
	UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error)

	// BatchSize has the same meaning as BlockSource.BatchSize.
	BatchSize() int
}

// BlockStore can retrieve and store encrypted blocks.
type BlockStore interface {
	BlockSource
	BlockSink
}

// GetBlock reads one block. The caller does not need to construct a request slice.
// Options are zero on error.
//
//nolint:staticcheck // ST1008: preserve the single-block API return order.
func GetBlock(ctx context.Context, source BlockSource, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	results, err := source.GetBlock(ctx, []BlockRequest{{Reference: ref, TTL: ttl, Options: options}})
	if err != nil {
		return nil, err, BlockOptions{}
	}
	if len(results) != 1 {
		return nil, ErrInvalidBatchResult, BlockOptions{}
	}
	return results[0].Data, nil, results[0].Options
}

// PutBlock writes one block. At most one expected reference may be supplied.
func PutBlock(ctx context.Context, sink BlockSink, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	if len(expected) > 1 {
		return Reference{}, ErrInvalidBlock
	}
	request := PutBlockRequest{Data: block, TTL: ttl, Options: options}
	if len(expected) == 1 {
		request.Expected = &expected[0]
	}
	results, err := sink.PutBlock(ctx, []PutBlockRequest{request})
	if err != nil {
		return Reference{}, err
	}
	if len(results) != 1 {
		return Reference{}, ErrInvalidBatchResult
	}
	return results[0], nil
}

// UpdateBlockOptions updates one block. The caller does not need a request slice.
func UpdateBlockOptions(ctx context.Context, sink BlockSink, ref Reference, ttl TTL, options BlockOptions) (bool, error) {
	results, err := sink.UpdateBlockOptions(ctx, []BlockRequest{{Reference: ref, TTL: ttl, Options: options}})
	if err != nil {
		return false, err
	}
	if len(results) != 1 {
		return false, ErrInvalidBatchResult
	}
	return results[0], nil
}
