package fs

import (
	"bytes"
	"context"
	"io"

	"github.com/asciimoth/goeris"
)

// EncodeIndex encodes a canonical ERIS-FS index and writes its ERIS blocks.
// It validates the index before checking the sink. A nil sink returns
// ErrInvalidEntry.
func EncodeIndex(ctx context.Context, index Index, secret eris.Secret, sink eris.BlockSink) (eris.Capability, error) {
	data, err := index.MarshalBinary()
	if err != nil {
		return eris.Capability{}, err
	}
	if sink == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	return eris.EncodeContent(ctx, bytes.NewReader(data), secret, eris.ChooseBlockSize(len(data)), sink)
}

// DecodeIndex reads and parses an ERIS-FS index from source.
func DecodeIndex(ctx context.Context, capability eris.Capability, source eris.BlockSource) (Index, error) {
	return DecodeIndexWithLimit(ctx, capability, source, DefaultMaxIndexSize)
}

// DecodeIndexWithLimit reads and parses an index whose decoded representation
// is at most limit bytes. A non-positive limit is invalid.
func DecodeIndexWithLimit(ctx context.Context, capability eris.Capability, source eris.BlockSource, limit int64) (Index, error) {
	if limit <= 0 {
		return Index{}, ErrInvalidLimit
	}
	reader, err := eris.NewReaderWithLimit(ctx, capability, source, limit, eris.NoTTL)
	if err != nil {
		return Index{}, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return Index{}, err
	}
	return ParseIndex(data)
}
