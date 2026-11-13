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
// Context methods prefer the context method of each entry and otherwise use its
// plain method. They check ctx before each attempt; cancellation stops further
// attempts and returns ctx.Err(). A plain method cannot be stopped while it is
// in progress. A successful attempt returns its result. If the final compatible
// entry fails, its error is returned unchanged, even if ctx was canceled during
// that attempt. An already canceled context takes priority over ErrNoBlockStorage.
//
// Wrapped stores must meet the contracts of the interfaces they implement,
// including block verification and copying data that they retain. The combined
// store passes arguments through and does not verify blocks again.
//
// Concurrent calls are safe if the wrapped stores and OnError support concurrent
// calls and no caller changes OnError during use.
type CombinedBlockStore struct {
	get        []combinedBlockMethod[func(Reference) ([]byte, error)]
	put        []combinedBlockMethod[func([]byte, ...Reference) (Reference, error)]
	getContext []combinedBlockMethod[func(context.Context, Reference) ([]byte, error)]
	putContext []combinedBlockMethod[func(context.Context, []byte, ...Reference) (Reference, error)]

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

// combinedBlockMethod keeps a resolved method and its original input index.
// Each operation has its own list, so calls need no interface checks.
type combinedBlockMethod[Method any] struct {
	index int
	call  Method
}

// NewCombinedBlockStore resolves the methods of stores and returns a combined
// store with a fixed attempt order. Each entry should implement at least one of
// BlockSource, BlockSink, ContextBlockSource, or ContextBlockSink. Separate
// lists retain the methods supported by each entry and its original index.
// For context calls, a plain method is used when the entry has no context
// method for that operation.
// Construction does not call storage methods.
//
// The input slice is not retained. Replacing or reordering its entries after
// construction does not change the combined store. The wrapped stores themselves
// are not copied. Set OnError before use to report discarded errors.
//
// Entries without a method for an operation, including nil entries, are skipped.
// An interface that holds a typed nil value still implements its interfaces;
// its methods must handle that receiver.
//
// To expose only reads, wrap a source in struct{ BlockSource }{source}.
// This also hides its context methods. If the source implements both read
// interfaces, use struct{ BlockSource; ContextBlockSource }{source, source}
// to keep context support without exposing writes.
// Assigning a store to a BlockSource variable does not hide its other methods.
func NewCombinedBlockStore(stores ...any) *CombinedBlockStore {
	combined := &CombinedBlockStore{}
	for index, entry := range stores {
		if source, ok := entry.(BlockSource); ok {
			combined.get = append(combined.get, combinedBlockMethod[func(Reference) ([]byte, error)]{index, source.GetBlock})
		}
		if sink, ok := entry.(BlockSink); ok {
			combined.put = append(combined.put, combinedBlockMethod[func([]byte, ...Reference) (Reference, error)]{index, sink.PutBlock})
		}
		if source, ok := entry.(ContextBlockSource); ok {
			combined.getContext = append(combined.getContext, combinedBlockMethod[func(context.Context, Reference) ([]byte, error)]{index, source.GetBlockContext})
		} else if source, ok := entry.(BlockSource); ok {
			combined.getContext = append(combined.getContext, combinedBlockMethod[func(context.Context, Reference) ([]byte, error)]{index, func(_ context.Context, ref Reference) ([]byte, error) {
				return source.GetBlock(ref)
			}})
		}
		if sink, ok := entry.(ContextBlockSink); ok {
			combined.putContext = append(combined.putContext, combinedBlockMethod[func(context.Context, []byte, ...Reference) (Reference, error)]{index, sink.PutBlockContext})
		} else if sink, ok := entry.(BlockSink); ok {
			combined.putContext = append(combined.putContext, combinedBlockMethod[func(context.Context, []byte, ...Reference) (Reference, error)]{index, func(_ context.Context, block []byte, expected ...Reference) (Reference, error) {
				return sink.PutBlock(block, expected...)
			}})
		}
	}
	return combined
}

// GetBlock tries each resolved BlockSource method until one returns a block without
// an error. Entries that implement only ContextBlockSource are skipped.
func (store *CombinedBlockStore) GetBlock(ref Reference) ([]byte, error) {
	return tryBlockStores(context.Background(), store.get, store.OnError, func(get func(Reference) ([]byte, error)) ([]byte, error) {
		return get(ref)
	})
}

// PutBlock tries each resolved BlockSink method until one stores the block without
// an error. Read-only entries and entries that implement only ContextBlockSink
// are skipped. Each attempt receives the same block and expected references.
func (store *CombinedBlockStore) PutBlock(block []byte, expected ...Reference) (Reference, error) {
	return tryBlockStores(context.Background(), store.put, store.OnError, func(put func([]byte, ...Reference) (Reference, error)) (Reference, error) {
		return put(block, expected...)
	})
}

// GetBlockContext tries each resolved source method until one returns a block
// without an error. It uses GetBlockContext when available and otherwise uses
// GetBlock. It passes ctx unchanged to context methods. The caller must supply
// a non-nil context.
func (store *CombinedBlockStore) GetBlockContext(ctx context.Context, ref Reference) ([]byte, error) {
	return tryBlockStores(ctx, store.getContext, store.OnError, func(get func(context.Context, Reference) ([]byte, error)) ([]byte, error) {
		return get(ctx, ref)
	})
}

// PutBlockContext tries each resolved sink method until one stores the block
// without an error. It uses PutBlockContext when available and otherwise uses
// PutBlock. Read-only entries are excluded. It passes ctx unchanged to context
// methods. The caller must supply a non-nil context.
func (store *CombinedBlockStore) PutBlockContext(ctx context.Context, block []byte, expected ...Reference) (Reference, error) {
	return tryBlockStores(ctx, store.putContext, store.OnError, func(put func(context.Context, []byte, ...Reference) (Reference, error)) (Reference, error) {
		return put(ctx, block, expected...)
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

var (
	_ BlockStore         = (*CombinedBlockStore)(nil)
	_ ContextBlockSource = (*CombinedBlockStore)(nil)
	_ ContextBlockSink   = (*CombinedBlockStore)(nil)
)
