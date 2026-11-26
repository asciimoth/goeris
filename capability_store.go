package eris

import (
	"context"
	"errors"
	"io"
	"math"
)

var (
	// ErrReadOnly means that the store cannot encode content.
	ErrReadOnly = errors.New("eris: read-only capability storage")
	// ErrNoCapabilityStorage means that a combined store has no entries.
	ErrNoCapabilityStorage = errors.New("eris: no capability storage")
)

// CapabilityReader provides sequential and random access to decoded content.
// Size returns the decoded length in bytes. Reads stay within that length.
// A zero-length read at a valid offset succeeds even at or beyond the end.
// The context used to open the reader also controls reads. The caller must
// close the reader. Close is idempotent.
type CapabilityReader interface {
	io.ReadSeekCloser
	io.ReaderAt
	Size() int64
}

// EncodeOptions controls [CapabilityStore.EncodeContentWithOptions]. The zero
// value selects the recommended block size and marks the capability's root block.
type EncodeOptions struct {
	// BlockOptions supplies the options for each block write. By default,
	// the encoder sets Root on the capability's root block.
	BlockOptions BlockOptions

	// BlockSize selects [BlockSize1KiB] or [BlockSize32KiB]. Zero selects
	// the recommended size, as in [EncodeContentRecommended].
	BlockSize BlockSize

	// PreserveRoot passes the supplied BlockOptions.Root unchanged on every
	// block write, including the capability's root block.
	// The store's rules for repeated writes still apply.
	PreserveRoot bool
}

// CapabilityStore reads and writes decoded content. The encode methods return
// the capability after a successful write. Read-only stores return ErrReadOnly
// without reading the input. Write-only stores can return errors.ErrUnsupported
// for read methods. Options and TTL apply to block operations.
//
// On block-backed stores, EncodeContentWithOptions uses [EncodeContent] rules
// for explicit sizes and [EncodeContentRecommended] rules for a zero size.
// The PreserveRoot option controls the Root flag on writes. The other encode
// methods always mark the capability's root block. Root marking affects only
// storage metadata.
//
// DecodeWithLimit rejects negative limits with ErrInvalidLimit. For
// NewReaderWithLimit a negative limit disables the check. Both methods return
// ErrDecodedTooLarge when the decoded size exceeds the limit.
// NewReaderWithSize can defer size checks until the caller reads content.
// Prefetch sends a best-effort hint. ResolvePath returns the absolute path of
// a regular file, or errors.ErrUnsupported if the store cannot provide paths.
// Missing content matches ErrMissingBlock when the store detects its absence.
type CapabilityStore interface {
	Encode(ctx context.Context, content []byte, secret Secret, size BlockSize, options BlockOptions) (Capability, error)
	EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error)
	EncodeContent(ctx context.Context, content io.Reader, secret Secret, size BlockSize, options BlockOptions) (Capability, error)
	EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, options BlockOptions) (Capability, error)
	EncodeContentWithOptions(ctx context.Context, content io.Reader, secret Secret, options EncodeOptions) (Capability, error)
	Decode(ctx context.Context, capability Capability, options BlockOptions) ([]byte, error)
	DecodeWithLimit(ctx context.Context, capability Capability, limit int64, options BlockOptions) ([]byte, error)
	NewReader(ctx context.Context, capability Capability, options BlockOptions) (CapabilityReader, error)
	NewReaderWithSize(ctx context.Context, capability Capability, size int64, options BlockOptions) (CapabilityReader, error)
	NewReaderWithLimit(ctx context.Context, capability Capability, limit int64, ttl TTL, options BlockOptions) (CapabilityReader, error)
	Prefetch(ctx context.Context, capability Capability, ttl TTL, options BlockOptions) error
	ResolvePath(ctx context.Context, capability Capability) (string, error)
}

func decodeCapability(ctx context.Context, store CapabilityStore, capability Capability, limit int64, options BlockOptions) ([]byte, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, ErrInvalidLimit
	}
	reader, err := store.NewReaderWithLimit(ctx, capability, min(limit, int64(math.MaxInt)), NoTTL, options)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	// The reader enforces the decoded size and reports truncated content.
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return data, nil
}
