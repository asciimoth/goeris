package fs

import (
	"context"

	"github.com/asciimoth/goeris"
)

// EncodeIndex writes an index through a capability store.
//
// For repeated operations, use [Store.EncodeIndex].
func EncodeIndex(ctx context.Context, index Index, secret eris.Secret, content eris.CapabilityStore, options eris.BlockOptions) (eris.Capability, error) {
	return NewStore(content, options).EncodeIndex(ctx, index, secret)
}

// EncodeIndex validates the index, encodes canonical CBOR, and marks the index root.
func (s *Store) EncodeIndex(ctx context.Context, index Index, secret eris.Secret) (eris.Capability, error) {
	data, err := index.MarshalBinary()
	if err != nil {
		return eris.Capability{}, err
	}
	if s.content == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	capability, err := s.content.EncodeRecommended(ctx, data, secret, s.options)
	if err != nil {
		return eris.Capability{}, err
	}
	if err := capability.Validate(); err != nil {
		return eris.Capability{}, err
	}
	return capability, nil
}

// DecodeIndex reads an index through a capability store.
//
// For repeated operations, use [Store.DecodeIndex].
func DecodeIndex(ctx context.Context, capability eris.Capability, content eris.CapabilityStore, options eris.BlockOptions) (Index, error) {
	return NewStore(content, options).DecodeIndex(ctx, capability)
}

// DecodeIndex reads an index up to DefaultMaxIndexSize bytes.
func (s *Store) DecodeIndex(ctx context.Context, capability eris.Capability) (Index, error) {
	return s.DecodeIndexWithLimit(ctx, capability, DefaultMaxIndexSize)
}

// DecodeIndexWithLimit reads a bounded index through a capability store.
//
// For repeated operations, use [Store.DecodeIndexWithLimit].
func DecodeIndexWithLimit(ctx context.Context, capability eris.Capability, content eris.CapabilityStore, limit int64, options eris.BlockOptions) (Index, error) {
	return NewStore(content, options).DecodeIndexWithLimit(ctx, capability, limit)
}

// DecodeIndexWithLimit bounds the decoded CBOR bytes before parsing the index.
func (s *Store) DecodeIndexWithLimit(ctx context.Context, capability eris.Capability, limit int64) (Index, error) {
	if limit <= 0 {
		return Index{}, ErrInvalidLimit
	}
	if s.content == nil || capability.Validate() != nil {
		return Index{}, eris.ErrInvalidCapability
	}
	data, err := s.content.DecodeWithLimit(ctx, capability, limit, s.options)
	if err != nil {
		return Index{}, err
	}
	if int64(len(data)) > limit {
		return Index{}, ErrIndexTooLarge
	}
	return ParseIndex(data)
}
