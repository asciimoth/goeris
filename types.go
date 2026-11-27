package eris

import (
	"context"
	"encoding/base32"
	"errors"

	"github.com/asciimoth/goeris/internal/batch"
)

const (
	// CapabilitySize is the size of a binary ERIS read capability.
	CapabilitySize = 66
	// PairSize is the size of a reference-key pair.
	PairSize = 64
	// ReferenceSize is the size of a block reference.
	ReferenceSize = 32
	// KeySize is the size of a node key or convergence secret.
	KeySize = 32
	// ReferenceTextSize is the length of an unpadded Base32 block reference.
	ReferenceTextSize = 52
	// ReferenceURNPrefix starts a canonical ERIS block-reference URN.
	ReferenceURNPrefix = "urn:blake2b:"
)

var (
	// ErrInvalidBlockSize means that a size is not an ERIS block size.
	ErrInvalidBlockSize = errors.New("eris: invalid block size")
	// ErrInvalidBlock means that a block has an invalid size or reference.
	ErrInvalidBlock = errors.New("eris: invalid block")
	// ErrMissingBlock means that a required block is not in a block set.
	ErrMissingBlock = errors.New("eris: missing block")
	// ErrInvalidPadding means that content does not have valid ERIS padding.
	ErrInvalidPadding = errors.New("eris: invalid padding")
	// ErrInvalidNode means that an internal node has an invalid structure.
	ErrInvalidNode = errors.New("eris: invalid internal node")
	// ErrInvalidCapability means that a read capability is not valid.
	ErrInvalidCapability = errors.New("eris: invalid read capability")
	// ErrInvalidRootKey means that an internal root key does not match its node.
	ErrInvalidRootKey = errors.New("eris: invalid root key")
	// ErrInvalidLimit means that a decoded content limit is negative.
	ErrInvalidLimit = errors.New("eris: invalid decoded content limit")
	// ErrDecodedTooLarge means that content exceeds the decoded limit or the
	// maximum size supported by the current system.
	ErrDecodedTooLarge = errors.New("eris: decoded content is too large")
	// ErrInvalidInput means that an encoder received a nil input reader.
	ErrInvalidInput = errors.New("eris: invalid encoding input")
	// ErrInvalidSize means that a declared decoded size is negative.
	ErrInvalidSize = errors.New("eris: invalid decoded size")
	// ErrInvalidTree means that a tree exceeds the supported depth or decoded size.
	ErrInvalidTree = errors.New("eris: invalid tree")
	// ErrSizeMismatch means that content does not match its declared decoded size.
	ErrSizeMismatch = errors.New("eris: decoded size mismatch")
)

// BlockSize is one of the two block sizes defined by ERIS.
type BlockSize uint32

const (
	// BlockSize1KiB is suitable for content smaller than 16 KiB.
	BlockSize1KiB BlockSize = 1 << 10
	// BlockSize32KiB is suitable for content of 16 KiB or more.
	BlockSize32KiB BlockSize = 1 << 15
	// BlockSize1KiBCode is the capability byte for 1 KiB blocks.
	BlockSize1KiBCode byte = 0x0a
	// BlockSize32KiBCode is the capability byte for 32 KiB blocks.
	BlockSize32KiBCode byte = 0x0f
)

// Valid reports whether s is an ERIS block size.
func (s BlockSize) Valid() bool {
	return s == BlockSize1KiB || s == BlockSize32KiB
}

// ValidBlockLength reports whether length is an ERIS block length in bytes.
// It checks the full int value without truncating it to BlockSize.
func ValidBlockLength(length int) bool {
	return length == int(BlockSize1KiB) || length == int(BlockSize32KiB)
}

// Arity returns the maximum number of pairs in an internal node.
func (s BlockSize) Arity() (int, error) {
	if !s.Valid() {
		return 0, ErrInvalidBlockSize
	}
	return int(s) / PairSize, nil
}

func (s BlockSize) code() (byte, error) {
	switch s {
	case BlockSize1KiB:
		return BlockSize1KiBCode, nil
	case BlockSize32KiB:
		return BlockSize32KiBCode, nil
	default:
		return 0, ErrInvalidBlockSize
	}
}

func blockSizeFromCode(code byte) (BlockSize, error) {
	switch code {
	case BlockSize1KiBCode:
		return BlockSize1KiB, nil
	case BlockSize32KiBCode:
		return BlockSize32KiB, nil
	default:
		return 0, ErrInvalidCapability
	}
}

// Level is the level of a node. Leaf nodes are at level zero.
type Level uint8

// Reference is the BLAKE2b-256 digest of an encrypted block.
type Reference [ReferenceSize]byte

// Key is a ChaCha20 node encryption key.
type Key [KeySize]byte

// Secret is a convergence secret. The zero value is the null secret.
type Secret [KeySize]byte

// Pair identifies an encrypted block and contains the key that decrypts it.
type Pair struct {
	Reference Reference
	Key       Key
}

// BlockSet is an in-memory set of encrypted blocks, indexed by reference.
// Initialize Blocks before writes. A nil Blocks map is readable but rejects
// writes with ErrMissingBlock. Copies of a BlockSet share the same map.
// PutBlock stores a copy of each block. GetBlock returns a copy. Concurrent
// reads are safe only while no caller changes the map or its blocks.
// BlockSet is a terminal store and ignores TTL. It checks cancellation before
// each request. Puts preserve true HighPriority and Root flags. Explicit
// option updates can set or clear them.
type BlockSet struct {
	// Blocks holds encrypted data and options. Initialize it before writes.
	Blocks map[Reference]Block
	// MaxBatchSize optionally limits batches for tests and debugging.
	// Zero accepts any batch size. Negative values use the safe limit of one.
	MaxBatchSize int
}

// Block holds encrypted block data and its storage options. Direct map writes
// replace the data and options together; omitted options are zero.
// Use BlockSet.PutBlock to copy and verify the data and to preserve true
// flags on repeated writes.
type Block struct {
	Data    []byte
	Options BlockOptions
}

// getBlock verifies and returns a copy of the encrypted block for ref. The map
// can be changed directly, so each read checks the reference. It returns stored
// options and ignores request options.
//
//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (s BlockSet) getBlock(_ context.Context, ref Reference, _ TTL, _ BlockOptions) ([]byte, error, BlockOptions) {
	block, ok := s.Blocks[ref]
	if !ok {
		return nil, ErrMissingBlock, BlockOptions{}
	}
	if err := VerifyBlock(block.Data, ref); err != nil {
		return nil, err, BlockOptions{}
	}
	return append([]byte(nil), block.Data...), nil, block.Options
}

// putBlock stores a copy of an encrypted block and returns its reference. It
// rejects a block that does not match an optional expected reference. Repeated
// puts keep each flag true if either the stored or supplied value is true.
func (s BlockSet) putBlock(_ context.Context, block []byte, _ TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	if s.Blocks == nil {
		return Reference{}, ErrMissingBlock
	}
	ref, err := ReferenceOf(block)
	if err != nil {
		return Reference{}, errors.Join(ErrInvalidBlock, err)
	}
	if len(expected) > 1 || len(expected) == 1 && expected[0] != ref {
		return Reference{}, ErrInvalidBlock
	}
	stored := s.Blocks[ref].Options
	options.HighPriority = options.HighPriority || stored.HighPriority
	options.Root = options.Root || stored.Root
	s.Blocks[ref] = Block{Data: append([]byte(nil), block...), Options: options}
	return ref, nil
}

// updateBlockOptions replaces the options of an existing block. It can set
// or clear either flag. It returns false if the block is absent or unchanged.
// The batch entry point checks cancellation before each request.
func (s BlockSet) updateBlockOptions(_ context.Context, ref Reference, _ TTL, options BlockOptions) (bool, error) {
	block, ok := s.Blocks[ref]
	if !ok || block.Options == options {
		return false, nil
	}
	block.Options = options
	s.Blocks[ref] = block
	return true, nil
}

var _ BlockStore = BlockSet{}

var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// String returns the unpadded Base32 encoding of r.
func (r Reference) String() string {
	var text [ReferenceTextSize]byte
	base32Encoding.Encode(text[:], r[:])
	return string(text[:])
}

// URN returns the canonical URN for r.
func (r Reference) URN() string {
	var text [len(ReferenceURNPrefix) + ReferenceTextSize]byte
	copy(text[:], ReferenceURNPrefix)
	base32Encoding.Encode(text[len(ReferenceURNPrefix):], r[:])
	return string(text[:])
}

// ParseReference parses a canonical unpadded Base32 reference.
func ParseReference(text string) (Reference, error) {
	var ref Reference
	if len(text) != ReferenceTextSize {
		return ref, ErrInvalidBlock
	}
	n, err := base32Encoding.Decode(ref[:], []byte(text))
	if err != nil || n != len(ref) {
		return Reference{}, ErrInvalidBlock
	}
	var canonical [ReferenceTextSize]byte
	base32Encoding.Encode(canonical[:], ref[:])
	if string(canonical[:]) != text {
		return Reference{}, ErrInvalidBlock
	}
	return ref, nil
}

// ParseReferenceURN parses a canonical ERIS block URN.
func ParseReferenceURN(urn string) (Reference, error) {
	if len(urn) != len(ReferenceURNPrefix)+ReferenceTextSize || urn[:len(ReferenceURNPrefix)] != ReferenceURNPrefix {
		return Reference{}, ErrInvalidBlock
	}
	return ParseReference(urn[len(ReferenceURNPrefix):])
}

const recommendedBlockSizeThreshold = 16 << 10

// ChooseBlockSize returns the block size recommended by the specification.
func ChooseBlockSize(contentLength int) BlockSize {
	if contentLength < recommendedBlockSizeThreshold {
		return BlockSize1KiB
	}
	return BlockSize32KiB
}

// BatchSize reports the maximum number of requests per call.
func (s BlockSet) BatchSize() int {
	if s.MaxBatchSize < 0 {
		return 1
	}
	return s.MaxBatchSize
}

// GetBlock verifies each reference and returns a copy of each available block
// with its stored options. It ignores TTL and request options. Missing blocks
// have zero result slots and do not stop later reads.
func (s BlockSet) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

// PutBlock verifies and copies each block. Each Expected reference, when set,
// must match its block. Repeated writes preserve true HighPriority and Root
// flags. It ignores TTL and stops at the first error.
func (s BlockSet) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions replaces stored options, including clearing flags. Each
// result is false for an absent or unchanged block. It ignores TTL.
func (s BlockSet) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// PrefetchBlock ignores prefetch hints. This terminal source does not prefetch.
func (BlockSet) PrefetchBlock(context.Context, []BlockRequest) {}
