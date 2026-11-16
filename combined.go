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
//
// Concurrent calls are safe if the wrapped stores and OnError support concurrent
// calls and no caller changes OnError during use.
type CombinedBlockStore struct {
	get []combinedBlockMethod[func(context.Context, Reference, TTL) ([]byte, error)]
	put []combinedBlockMethod[func(context.Context, []byte, TTL, ...Reference) (Reference, error)]

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
			combined.get = append(combined.get, combinedBlockMethod[func(context.Context, Reference, TTL) ([]byte, error)]{index, source.GetBlock})
		}
		if sink, ok := entry.(BlockSink); ok {
			combined.put = append(combined.put, combinedBlockMethod[func(context.Context, []byte, TTL, ...Reference) (Reference, error)]{index, sink.PutBlock})
		}
	}
	return combined
}

// GetBlock tries each source until a read succeeds. It passes ctx unchanged.
func (store *CombinedBlockStore) GetBlock(ctx context.Context, ref Reference, ttl TTL) ([]byte, error) {
	return tryBlockStores(ctx, store.get, store.OnError, func(get func(context.Context, Reference, TTL) ([]byte, error)) ([]byte, error) {
		return get(ctx, ref, ttl)
	})
}

// PutBlock tries each sink until a write succeeds. It passes ctx unchanged.
func (store *CombinedBlockStore) PutBlock(ctx context.Context, block []byte, ttl TTL, expected ...Reference) (Reference, error) {
	return tryBlockStores(ctx, store.put, store.OnError, func(put func(context.Context, []byte, TTL, ...Reference) (Reference, error)) (Reference, error) {
		return put(ctx, block, ttl, expected...)
	})
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
