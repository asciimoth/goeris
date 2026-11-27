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
	// the recommended size, as in [CapabilityStore.EncodeContentRecommended].
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
// Block-backed stores mark the root block on each encode operation.
// Set [EncodeOptions.PreserveRoot] to pass the supplied Root flag unchanged.
// Root marking affects storage metadata only. Missing content matches
// [ErrMissingBlock] when the store detects its absence.
type CapabilityStore interface {
	// Encode writes content with the specified ERIS block size. The supported
	// sizes are [BlockSize1KiB] and [BlockSize32KiB].
	Encode(ctx context.Context, content []byte, secret Secret, size BlockSize, options BlockOptions) (Capability, error)
	// EncodeRecommended selects the block size with [ChooseBlockSize].
	EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error)
	// EncodeContent reads a stream once and writes its ERIS tree. Block-backed
	// stores write with NoTTL and check ctx between reads and writes. Cancellation
	// cannot interrupt a blocked input Read. Encoding stops at the first EOF,
	// including EOF with data. After 100 consecutive empty reads, encoding
	// returns io.ErrNoProgress. A writable block-backed store returns
	// ErrInvalidBlockSize for an unsupported size and ErrInvalidInput for a nil
	// reader. A failed encode can leave blocks in the store.
	EncodeContent(ctx context.Context, content io.Reader, secret Secret, size BlockSize, options BlockOptions) (Capability, error)
	// EncodeContentRecommended buffers up to 16 KiB to select the block size.
	// It uses 1 KiB blocks for shorter input and 32 KiB blocks otherwise.
	EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, options BlockOptions) (Capability, error)
	// EncodeContentWithOptions selects block size and root marking for a stream.
	EncodeContentWithOptions(ctx context.Context, content io.Reader, secret Secret, options EncodeOptions) (Capability, error)
	// Decode reads all decoded bytes. Use DecodeWithLimit to set a size limit.
	Decode(ctx context.Context, capability Capability, options BlockOptions) ([]byte, error)
	// DecodeWithLimit returns ErrDecodedTooLarge if the decoded size exceeds
	// limit. A negative limit returns ErrInvalidLimit.
	DecodeWithLimit(ctx context.Context, capability Capability, limit int64, options BlockOptions) ([]byte, error)
	// NewReader opens content for sequential and random reads. A block-backed
	// store scans internal nodes and the final leaf to determine the size.
	NewReader(ctx context.Context, capability Capability, options BlockOptions) (CapabilityReader, error)
	// NewReaderWithSize opens content with a known decoded size. A block-backed
	// store defers block I/O until a read and checks the size on visited paths.
	// A negative size returns ErrInvalidSize. A detected mismatch returns
	// ErrSizeMismatch.
	NewReaderWithSize(ctx context.Context, capability Capability, size int64, options BlockOptions) (CapabilityReader, error)
	// NewReaderWithLimit passes ttl to block reads. It returns ErrDecodedTooLarge
	// if the decoded size exceeds limit. A negative limit disables this check.
	NewReaderWithLimit(ctx context.Context, capability Capability, limit int64, ttl TTL, options BlockOptions) (CapabilityReader, error)
	// Prefetch requests content in advance. A block-backed store reads internal
	// nodes and sends best-effort hints for leaves. The call can block or return
	// an index read error. Success does not guarantee that leaves are available.
	Prefetch(ctx context.Context, capability Capability, ttl TTL, options BlockOptions) error
	// ResolvePath returns the absolute path to a regular file with the decoded
	// content. Stores without host paths return errors.ErrUnsupported.
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
