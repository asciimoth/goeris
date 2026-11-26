package fs

import (
	"context"

	"github.com/asciimoth/goeris"
)

// EncodeIndex writes an index through a block sink.
//
// This compatibility function wraps Store.EncodeIndex.
func EncodeIndex(ctx context.Context, index Index, secret eris.Secret, sink eris.BlockSink, options eris.BlockOptions) (eris.Capability, error) {
	return storeFromSink(sink, options).EncodeIndex(ctx, index, secret)
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
	return s.content.EncodeRecommended(ctx, data, secret, s.options)
}

// DecodeIndex reads an index through a block source.
//
// This compatibility function wraps Store.DecodeIndex.
func DecodeIndex(ctx context.Context, capability eris.Capability, source eris.BlockSource, options eris.BlockOptions) (Index, error) {
	return storeFromSource(source, options).DecodeIndex(ctx, capability)
}

// DecodeIndex reads an index up to DefaultMaxIndexSize bytes.
func (s *Store) DecodeIndex(ctx context.Context, capability eris.Capability) (Index, error) {
	return s.DecodeIndexWithLimit(ctx, capability, DefaultMaxIndexSize)
}

// DecodeIndexWithLimit reads a bounded index through a block source.
//
// This compatibility function wraps Store.DecodeIndexWithLimit.
func DecodeIndexWithLimit(ctx context.Context, capability eris.Capability, source eris.BlockSource, limit int64, options eris.BlockOptions) (Index, error) {
	return storeFromSource(source, options).DecodeIndexWithLimit(ctx, capability, limit)
}

// DecodeIndexWithLimit bounds the decoded CBOR bytes before parsing the index.
func (s *Store) DecodeIndexWithLimit(ctx context.Context, capability eris.Capability, limit int64) (Index, error) {
	if limit <= 0 {
		return Index{}, ErrInvalidLimit
	}
	if s.content == nil {
		return Index{}, eris.ErrInvalidCapability
	}
	data, err := s.content.DecodeWithLimit(ctx, capability, limit, s.options)
	if err != nil {
		return Index{}, err
	}
	return ParseIndex(data)
}
