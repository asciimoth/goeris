package eris

import (
	"context"
	"io"
	"sync/atomic"
)

// CombinedCapabilityStore tries stores in order. Read and write operations
// return the first successful result and stop on cancellation. Prefetch calls
// every store. If all attempts fail, the chain returns the last error unless
// cancellation stopped the attempts.
//
// Retry a failed write only while the caller's input is intact. Return the
// failing store's error if it changes the input bytes or reads the stream.
// Keep buffering, rewinding, and temporary files out of this layer.
type CombinedCapabilityStore struct{ stores []CapabilityStore }

// NewCombinedCapabilityStore copies the list and skips nil entries.
func NewCombinedCapabilityStore(stores ...CapabilityStore) *CombinedCapabilityStore {
	s := &CombinedCapabilityStore{}
	for _, store := range stores {
		if store != nil {
			s.stores = append(s.stores, store)
		}
	}
	return s
}

var _ CapabilityStore = (*CombinedCapabilityStore)(nil)

func tryCapabilityStores[T any](ctx context.Context, stores []CapabilityStore, inputChanged func() bool, call func(CapabilityStore) (T, error)) (T, error) {
	var zero T
	for i, store := range stores {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		value, err := call(store)
		if err == nil {
			return value, nil
		}
		if i == len(stores)-1 || (inputChanged != nil && inputChanged()) {
			return zero, err
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return zero, ErrNoCapabilityStorage
}

// Limit the slice capacity so append cannot change bytes outside the input.
func (s *CombinedCapabilityStore) encodeBytes(ctx context.Context, content []byte, call func(CapabilityStore, []byte) (Capability, error)) (Capability, error) {
	content = content[:len(content):len(content)]
	var changed func() bool
	if len(s.stores) > 1 && ctx.Err() == nil {
		initial := blake2bSum(content)
		changed = func() bool { return blake2bSum(content) != initial }
	}
	return tryCapabilityStores(ctx, s.stores, changed, func(store CapabilityStore) (Capability, error) {
		return call(store, content)
	})
}

type capabilityInput struct {
	source io.Reader
	read   atomic.Bool
}

func (r *capabilityInput) Read(p []byte) (int, error) {
	// Even a read that returns zero bytes can change an opaque reader's state.
	r.read.Store(true)
	return r.source.Read(p)
}

func (s *CombinedCapabilityStore) encodeStream(ctx context.Context, content io.Reader, call func(CapabilityStore, io.Reader) (Capability, error)) (Capability, error) {
	var changed func() bool
	if content != nil && len(s.stores) > 1 {
		input := &capabilityInput{source: content}
		content = input
		changed = input.read.Load
	}
	return tryCapabilityStores(ctx, s.stores, changed, func(store CapabilityStore) (Capability, error) {
		return call(store, content)
	})
}

func (s *CombinedCapabilityStore) Encode(ctx context.Context, content []byte, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	return s.encodeBytes(ctx, content, func(store CapabilityStore, content []byte) (Capability, error) {
		return store.Encode(ctx, content, secret, size, options)
	})
}

func (s *CombinedCapabilityStore) EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error) {
	return s.encodeBytes(ctx, content, func(store CapabilityStore, content []byte) (Capability, error) {
		return store.EncodeRecommended(ctx, content, secret, options)
	})
}

func (s *CombinedCapabilityStore) EncodeContent(ctx context.Context, content io.Reader, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	return s.encodeStream(ctx, content, func(store CapabilityStore, content io.Reader) (Capability, error) {
		return store.EncodeContent(ctx, content, secret, size, options)
	})
}

func (s *CombinedCapabilityStore) EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, options BlockOptions) (Capability, error) {
	return s.encodeStream(ctx, content, func(store CapabilityStore, content io.Reader) (Capability, error) {
		return store.EncodeContentRecommended(ctx, content, secret, options)
	})
}

// EncodeContentWithOptions tries stores in order with the same options. A
// failed attempt that reads from content stops the search.
func (s *CombinedCapabilityStore) EncodeContentWithOptions(ctx context.Context, content io.Reader, secret Secret, options EncodeOptions) (Capability, error) {
	return s.encodeStream(ctx, content, func(store CapabilityStore, content io.Reader) (Capability, error) {
		return store.EncodeContentWithOptions(ctx, content, secret, options)
	})
}

func (s *CombinedCapabilityStore) Decode(ctx context.Context, capability Capability, options BlockOptions) ([]byte, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) ([]byte, error) {
		return store.Decode(ctx, capability, options)
	})
}

func (s *CombinedCapabilityStore) DecodeWithLimit(ctx context.Context, capability Capability, limit int64, options BlockOptions) ([]byte, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) ([]byte, error) {
		return store.DecodeWithLimit(ctx, capability, limit, options)
	})
}

func (s *CombinedCapabilityStore) NewReader(ctx context.Context, capability Capability, options BlockOptions) (CapabilityReader, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) (CapabilityReader, error) {
		return store.NewReader(ctx, capability, options)
	})
}

func (s *CombinedCapabilityStore) NewReaderWithSize(ctx context.Context, capability Capability, size int64, options BlockOptions) (CapabilityReader, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) (CapabilityReader, error) {
		return store.NewReaderWithSize(ctx, capability, size, options)
	})
}

func (s *CombinedCapabilityStore) NewReaderWithLimit(ctx context.Context, capability Capability, limit int64, ttl TTL, options BlockOptions) (CapabilityReader, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) (CapabilityReader, error) {
		return store.NewReaderWithLimit(ctx, capability, limit, ttl, options)
	})
}

// Prefetch calls every store in order with the supplied context. It returns
// nil if any call succeeds and returns the last error if all calls fail.
func (s *CombinedCapabilityStore) Prefetch(ctx context.Context, capability Capability, ttl TTL, options BlockOptions) error {
	if len(s.stores) == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrNoCapabilityStorage
	}
	var lastErr error
	var succeeded bool
	for _, store := range s.stores {
		lastErr = store.Prefetch(ctx, capability, ttl, options)
		if lastErr == nil {
			succeeded = true
		}
	}
	if succeeded {
		return nil
	}
	return lastErr
}

func (s *CombinedCapabilityStore) ResolvePath(ctx context.Context, capability Capability) (string, error) {
	return tryCapabilityStores(ctx, s.stores, nil, func(store CapabilityStore) (string, error) {
		return store.ResolvePath(ctx, capability)
	})
}
