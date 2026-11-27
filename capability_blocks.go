package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
)

type blockCapabilityStore struct {
	source BlockSource
	sink   BlockSink
}

// NewBlockCapabilityStore wraps source. If its dynamic type also implements
// BlockSink, the store writes encoded blocks to that sink.
func NewBlockCapabilityStore(source BlockSource) CapabilityStore {
	sink, _ := source.(BlockSink)
	return &blockCapabilityStore{source, sink}
}

func (s *blockCapabilityStore) Encode(ctx context.Context, content []byte, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	return s.EncodeContent(ctx, bytes.NewReader(content), secret, size, options)
}

func (s *blockCapabilityStore) EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error) {
	return s.Encode(ctx, content, secret, ChooseBlockSize(len(content)), options)
}

func (s *blockCapabilityStore) EncodeContent(ctx context.Context, content io.Reader, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	if s.sink == nil {
		return Capability{}, ErrReadOnly
	}
	return encodeContent(ctx, content, secret, s.sink, EncodeOptions{BlockSize: size, BlockOptions: options})
}

func (s *blockCapabilityStore) EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, options BlockOptions) (Capability, error) {
	return s.EncodeContentWithOptions(ctx, content, secret, EncodeOptions{BlockOptions: options})
}

func (s *blockCapabilityStore) EncodeContentWithOptions(ctx context.Context, content io.Reader, secret Secret, options EncodeOptions) (Capability, error) {
	if s.sink == nil {
		return Capability{}, ErrReadOnly
	}
	if options.BlockSize == 0 {
		return encodeContentRecommended(ctx, content, secret, s.sink, options)
	}
	return encodeContent(ctx, content, secret, s.sink, options)
}

func (s *blockCapabilityStore) Decode(ctx context.Context, capability Capability, options BlockOptions) ([]byte, error) {
	return s.DecodeWithLimit(ctx, capability, math.MaxInt, options)
}

func (s *blockCapabilityStore) DecodeWithLimit(ctx context.Context, capability Capability, limit int64, options BlockOptions) ([]byte, error) {
	return decodeCapability(ctx, s, capability, limit, options)
}

func (s *blockCapabilityStore) NewReader(ctx context.Context, capability Capability, options BlockOptions) (CapabilityReader, error) {
	return s.NewReaderWithLimit(ctx, capability, -1, NoTTL, options)
}

func (s *blockCapabilityStore) ResolvePath(ctx context.Context, capability Capability) (string, error) {
	return "", errors.ErrUnsupported
}
