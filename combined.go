package eris

import (
	"context"
	"errors"

	"github.com/asciimoth/goeris/internal/batch"
)

// ErrNoBlockStorage means that no entry in a CombinedBlockStore supports the
// requested operation. This includes an empty store list.
var ErrNoBlockStorage = errors.New("eris: no compatible block storage")

// CombinedBlockStore reads and writes blocks through an ordered list of stores.
// Each request tries compatible entries in order until one succeeds. A write
// stops at the first success; it does not copy the block to later stores.
// An options update continues until a sink returns true, nil.
// Use NewCombinedBlockStore to resolve the methods for each operation once.
//
// For a single request, if all compatible entries fail, the operation returns
// the last error unchanged and a zero result. Earlier errors go to OnError, if
// set. If no entry supports the operation, it returns ErrNoBlockStorage.
// The zero value has no stores.
//
// Each call passes ctx unchanged and checks cancellation before each attempt.
// A successful attempt returns its result. If the final entry fails, its error
// is returned unchanged. An already canceled context takes priority over
// ErrNoBlockStorage.
//
// Wrapped stores must meet the contracts of the interfaces they implement,
// including block verification and copying data that they retain. The combined
// store passes arguments through and does not verify blocks again.
// Every attempt receives the original TTL unchanged, including NoTTL and
// LocalTTL. Only a downstream network client consumes a forward.
// Each attempt receives the original options. Reads return the options from
// the successful source, or zero options if all attempts fail.
//
// BatchSize is the smallest nonzero limit of the wrapped sources and sinks,
// or zero if all accept any size. Each child call respects its batch limit.
// Results stay in request order, but a child can process later requests before
// an earlier request falls back. Option updates can therefore have effects
// beyond an error's returned prefix. Single-request calls keep their original
// fallback and callback order.
//
// Concurrent calls are safe if the wrapped stores and OnError support concurrent
// calls and no caller changes OnError during use.
type CombinedBlockStore struct {
	update      []combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) (bool, error)]
	get         []combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)]
	put         []combinedBlockMethod[func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)]
	getBatch    []combinedBatchMethod[BlockRequest, Block]
	putBatch    []combinedBatchMethod[PutBlockRequest, Reference]
	updateBatch []combinedBatchMethod[BlockRequest, bool]
	batchLimits []func() int

	// OnError receives errors that an operation discards. The index is the
	// zero-based position in the constructor's input, including skipped entries.
	// Calls are synchronous and follow attempt order within each operation.
	// Concurrent operations can call OnError at the same time.
	// An error is reported just before the next compatible attempt, or before
	// returning ctx.Err() if cancellation prevents that attempt.
	// The final failed attempt is not reported if its error is returned to the
	// caller. Separate attempts can return the same error value. A nil callback
	// discards earlier errors.
	// For multi-request calls, a callback precedes each fallback group. Separate
	// groups can report the same child batch error. Child calls and callbacks
	// can occur in a different order than separate single-request calls.
	OnError func(index int, err error)
}

var _ BlockStore = (*CombinedBlockStore)(nil)

// combinedBlockMethod keeps a resolved method and its original input index.
// Each operation has its own list, so calls need no interface checks.
type combinedBlockMethod[Method any] struct {
	index int
	call  Method
}

// NewCombinedBlockStore selects read and write methods in input order.
// Entries can implement BlockSource, BlockSink, or both. Other entries are
// skipped. The input slice is not retained. Typed nil entries must support
// calls on a nil receiver. Set OnError before concurrent use.
// Wrap a store in struct{ BlockSource }{store} to expose only reads.
// Assigning a store to a BlockSource variable does not hide its write methods.
func NewCombinedBlockStore(stores ...any) *CombinedBlockStore {
	combined := &CombinedBlockStore{}
	for index, entry := range stores {
		source, reads := entry.(BlockSource)
		if reads {
			combined.batchLimits = append(combined.batchLimits, source.BatchSize)
			combined.getBatch = append(combined.getBatch, combinedBatchMethod[BlockRequest, Block]{index, source.BatchSize, source.GetBlock})
			//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
			combined.get = append(combined.get, combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)]{index, func(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
				return GetBlock(ctx, source, ref, ttl, options)
			}})
		}
		if sink, ok := entry.(BlockSink); ok {
			if !reads {
				combined.batchLimits = append(combined.batchLimits, sink.BatchSize)
			}
			combined.putBatch = append(combined.putBatch, combinedBatchMethod[PutBlockRequest, Reference]{index, sink.BatchSize, sink.PutBlock})
			combined.updateBatch = append(combined.updateBatch, combinedBatchMethod[BlockRequest, bool]{index, sink.BatchSize, sink.UpdateBlockOptions})
			combined.update = append(combined.update, combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) (bool, error)]{index, func(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) (bool, error) {
				return UpdateBlockOptions(ctx, sink, ref, ttl, options)
			}})
			combined.put = append(combined.put, combinedBlockMethod[func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)]{index, func(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
				return PutBlock(ctx, sink, block, ttl, options, expected...)
			}})
		}
	}
	return combined
}

// getBlock tries each source until a read succeeds. It passes ctx unchanged.
//
//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (store *CombinedBlockStore) getBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	type result struct {
		block   []byte
		options BlockOptions
	}
	got, err := tryBlockStores(ctx, store.get, store.OnError, func(get func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)) (result, error) {
		block, err, stored := get(ctx, ref, ttl, options)
		return result{block, stored}, err
	})
	return got.block, err, got.options
}

// putBlock tries each sink until a write succeeds. It passes ctx unchanged.
func (store *CombinedBlockStore) putBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	return tryBlockStores(ctx, store.put, store.OnError, func(put func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)) (Reference, error) {
		return put(ctx, block, ttl, options, expected...)
	})
}

// updateBlockOptions tries each sink until a call returns true, nil.
func (store *CombinedBlockStore) updateBlockOptions(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(store.update) == 0 {
		return false, ErrNoBlockStorage
	}
	var lastErr error
	var lastIndex int
	for _, method := range store.update {
		if lastErr != nil && store.OnError != nil {
			store.OnError(lastIndex, lastErr)
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		updated, err := method.call(ctx, ref, ttl, options)
		if updated && err == nil {
			return true, nil
		}
		lastErr, lastIndex = err, method.index
	}
	return false, lastErr
}

// tryBlockStores calls resolved methods in their original order. Keep one
// pending error until another attempt is possible so the final storage error is
// not reported to onError. Results returned with an error are discarded.
func tryBlockStores[Method, Result any](ctx context.Context, methods []combinedBlockMethod[Method], onError func(int, error), attempt func(Method) (Result, error)) (Result, error) {
	var zero Result
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	var lastErr error
	var lastIndex int
	for _, method := range methods {
		if lastErr != nil && onError != nil {
			onError(lastIndex, lastErr)
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := attempt(method.call)
		if err == nil {
			return result, nil
		}
		lastErr, lastIndex = err, method.index
	}
	if lastErr != nil {
		return zero, lastErr
	}
	return zero, ErrNoBlockStorage
}

// BatchSize returns the smallest nonzero limit of the wrapped sources and sinks.
// It returns zero if all accept any size, or one if there are no compatible
// stores. Invalid negative child limits are treated as one. Limits are read on
// each call, so changes to a child's limit also apply to the combined store.
func (s *CombinedBlockStore) BatchSize() int {
	if s == nil || len(s.batchLimits) == 0 {
		return 1
	}
	limit := 0
	for _, childLimit := range s.batchLimits {
		child := childLimit()
		if child < 0 {
			child = 1
		}
		if child > 0 && (limit == 0 || child < limit) {
			limit = child
		}
	}
	return limit
}

// GetBlock tries each source in order until a read succeeds. It accepts up to
// BatchSize requests and passes context, TTL, and options unchanged. A missing
// block has a zero result slot. See BlockSource for the batch error rules.
func (s *CombinedBlockStore) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	// Every valid child limit allows a single request. Keep small calls on
	// the original path without querying any child limits.
	if len(requests) <= 1 {
		return batch.Read(ctx, requests, 1, ErrMissingBlock, func(r BlockRequest) (Block, error) {
			data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
			return Block{Data: data, Options: options}, err
		})
	}
	if limit := s.BatchSize(); limit > 0 && len(requests) > limit {
		return nil, ErrBatchTooLarge
	}
	operation := combinedBatchOperation[BlockRequest, Block]{
		methods: s.getBatch, onError: s.OnError, read: true,
		retry: func(block Block) bool { return block.Data == nil },
	}
	return operation.run(ctx, requests)
}

// PutBlock tries each sink in order until a write succeeds. It accepts up to
// BatchSize requests and passes context, TTL, and options unchanged. A successful
// write is not copied to later sinks. Errors return only the completed prefix.
func (s *CombinedBlockStore) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	if len(requests) <= 1 {
		return batch.Run(ctx, requests, 1, func(r PutBlockRequest) (Reference, error) {
			var expected []Reference
			if r.Expected != nil {
				expected = []Reference{*r.Expected}
			}
			return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
		})
	}
	if limit := s.BatchSize(); limit > 0 && len(requests) > limit {
		return nil, ErrBatchTooLarge
	}
	operation := combinedBatchOperation[PutBlockRequest, Reference]{methods: s.putBatch, onError: s.OnError}
	return operation.run(ctx, requests)
}

// UpdateBlockOptions tries each sink until a call returns true without an error.
// A false result without an error continues to the next sink. If no update
// succeeds, it returns the final attempt's error, or nil if that attempt had no
// error. With no compatible sinks, it returns ErrNoBlockStorage. It accepts up to
// BatchSize requests and returns only the completed prefix on error.
func (s *CombinedBlockStore) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	if len(requests) <= 1 {
		return batch.Run(ctx, requests, 1, func(r BlockRequest) (bool, error) {
			return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
		})
	}
	if limit := s.BatchSize(); limit > 0 && len(requests) > limit {
		return nil, ErrBatchTooLarge
	}
	operation := combinedBatchOperation[BlockRequest, bool]{
		methods: s.updateBatch, onError: s.OnError,
		retry: func(changed bool) bool { return !changed },
	}
	return operation.run(ctx, requests)
}
