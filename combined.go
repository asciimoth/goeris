package eris

import (
	"context"
	"errors"
)

// ErrNoBlockStorage means that no entry in a CombinedBlockStore supports the
// requested operation. This includes an empty store list.
var ErrNoBlockStorage = errors.New("eris: no compatible block storage")

// CombinedBlockStore reads and writes blocks through an ordered list of stores.
// Each operation tries compatible entries in order until one succeeds. A write
// stops at the first success; it does not copy the block to later stores.
// An options update continues until a sink returns true, nil.
// Use NewCombinedBlockStore to resolve the methods for each operation once.
//
// If all compatible entries fail, the operation returns the last error unchanged
// and a zero result. Earlier errors go to OnError, if set. If no entry supports the
// operation, it returns ErrNoBlockStorage. The zero value has no stores.
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
// Each attempt receives a copy of options. Reads currently discard the returned
// options and return BlockOptions{}.
//
// Concurrent calls are safe if the wrapped stores and OnError support concurrent
// calls and no caller changes OnError during use.
type CombinedBlockStore struct {
	update []combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) (bool, error)]
	get    []combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)]
	put    []combinedBlockMethod[func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)]

	// OnError receives errors that an operation discards. The index is the
	// zero-based position in the constructor's input, including skipped entries.
	// Calls are synchronous and follow attempt order within each operation.
	// Concurrent operations can call OnError at the same time.
	// An error is reported just before the next compatible attempt, or before
	// returning ctx.Err() if cancellation prevents that attempt.
	// The final failed attempt is not reported if its error is returned to the
	// caller. Separate attempts can return the same error value. A nil callback
	// discards earlier errors.
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
		if source, ok := entry.(BlockSource); ok {
			combined.get = append(combined.get, combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)]{index, source.GetBlock})
		}
		if sink, ok := entry.(BlockSink); ok {
			combined.update = append(combined.update, combinedBlockMethod[func(context.Context, Reference, TTL, BlockOptions) (bool, error)]{index, sink.UpdateBlockOptions})
			combined.put = append(combined.put, combinedBlockMethod[func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)]{index, sink.PutBlock})
		}
	}
	return combined
}

// GetBlock tries each source until a read succeeds. It passes ctx unchanged.
//
//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (store *CombinedBlockStore) GetBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	block, err := tryBlockStores(ctx, store.get, store.OnError, func(get func(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions)) ([]byte, error) {
		block, err, _ := get(ctx, ref, ttl, options)
		return block, err
	})
	return block, err, BlockOptions{}
}

// PutBlock tries each sink until a write succeeds. It passes ctx unchanged.
func (store *CombinedBlockStore) PutBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	return tryBlockStores(ctx, store.put, store.OnError, func(put func(context.Context, []byte, TTL, BlockOptions, ...Reference) (Reference, error)) (Reference, error) {
		return put(ctx, block, ttl, options, expected...)
	})
}

// UpdateBlockOptions tries each sink until a call returns true, nil.
// A false, nil result continues to the next sink. If no update succeeds,
// it returns the final attempt's error, or nil if that attempt had no error.
// With no compatible sinks, it returns ErrNoBlockStorage.
func (store *CombinedBlockStore) UpdateBlockOptions(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) (bool, error) {
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
