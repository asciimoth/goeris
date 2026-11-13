package eris

import (
	"encoding/base32"
	"errors"
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
// Encode owns the slices in the set. Decode does not modify them. Concurrent
// reads are safe only while no caller changes the map or its blocks.
// BlockSet is a terminal store and ignores TTL for reads and writes.
type BlockSet map[Reference][]byte

// GetBlock verifies and returns a copy of the encrypted block for ref. The map
// can be changed directly, so each read checks the reference.
func (s BlockSet) GetBlock(ref Reference, ttl TTL) ([]byte, error) {
	block, ok := s[ref]
	if !ok {
		return nil, ErrMissingBlock
	}
	if err := VerifyBlock(block, ref); err != nil {
		return nil, err
	}
	return append([]byte(nil), block...), nil
}

// PutBlock stores a copy of an encrypted block and returns its reference. It
// rejects a block that does not match an optional expected reference.
func (s BlockSet) PutBlock(block []byte, ttl TTL, expected ...Reference) (Reference, error) {
	if s == nil {
		return Reference{}, ErrMissingBlock
	}
	ref, err := ReferenceOf(block)
	if err != nil {
		return Reference{}, errors.Join(ErrInvalidBlock, err)
	}
	if len(expected) > 1 || len(expected) == 1 && expected[0] != ref {
		return Reference{}, ErrInvalidBlock
	}
	s[ref] = append([]byte(nil), block...)
	return ref, nil
}

var _ BlockStore = BlockSet(nil)

var base32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// String returns the unpadded Base32 encoding of r.
func (r Reference) String() string {
	return base32Encoding.EncodeToString(r[:])
}

// URN returns the canonical URN for r.
func (r Reference) URN() string {
	return ReferenceURNPrefix + r.String()
}

// ParseReference parses a canonical unpadded Base32 reference.
func ParseReference(text string) (Reference, error) {
	var ref Reference
	if len(text) != ReferenceTextSize {
		return ref, ErrInvalidBlock
	}
	b, err := base32Encoding.DecodeString(text)
	if err != nil || len(b) != len(ref) || base32Encoding.EncodeToString(b) != text {
		return ref, ErrInvalidBlock
	}
	copy(ref[:], b)
	return ref, nil
}

// ParseReferenceURN parses a canonical ERIS block URN.
func ParseReferenceURN(urn string) (Reference, error) {
	if len(urn) != len(ReferenceURNPrefix)+ReferenceTextSize || urn[:len(ReferenceURNPrefix)] != ReferenceURNPrefix {
		return Reference{}, ErrInvalidBlock
	}
	return ParseReference(urn[len(ReferenceURNPrefix):])
}

// ChooseBlockSize returns the block size recommended by the specification.
func ChooseBlockSize(contentLength int) BlockSize {
	if contentLength < 1<<14 {
		return BlockSize1KiB
	}
	return BlockSize32KiB
}
