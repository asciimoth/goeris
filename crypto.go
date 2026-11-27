package eris

import (
	"crypto/subtle"
	"fmt"
	"hash"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20"
)

// ReferenceOf computes the reference for an encrypted ERIS block.
func ReferenceOf(block []byte) (Reference, error) {
	if !ValidBlockLength(len(block)) {
		return Reference{}, ErrInvalidBlockSize
	}
	return Reference(blake2b.Sum256(block)), nil
}

// VerifyBlock checks the size and reference of an encrypted block.
func VerifyBlock(block []byte, expected Reference) error {
	actual, err := ReferenceOf(block)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBlock, err)
	}
	if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		return ErrInvalidBlock
	}
	return nil
}

// EncryptNode encrypts one full node and returns its reference-key pair.
// The secret is used only when level is zero, as required by ERIS.
func EncryptNode(node []byte, secret Secret, level Level) ([]byte, Pair, error) {
	block, key, err := EncryptNodeForStorage(node, secret, level)
	if err != nil {
		return nil, Pair{}, err
	}
	ref, _ := ReferenceOf(block)
	return block, Pair{Reference: ref, Key: key}, nil
}

// EncryptNodeForStorage encrypts one node without hashing the block. The sink
// that stores the block computes its reference.
func EncryptNodeForStorage(node []byte, secret Secret, level Level) ([]byte, Key, error) {
	if !ValidBlockLength(len(node)) {
		return nil, Key{}, ErrInvalidBlockSize
	}
	var leafHash *leafHasher
	if level == 0 {
		leafHash = newLeafHasher(secret)
	}
	block := make([]byte, len(node))
	return block, encryptNodeInto(block, node, leafHash, level), nil
}

// encryptNodeInto requires a valid node length and an equally large output.
func encryptNodeInto(block, node []byte, leafHash *leafHasher, level Level) Key {
	var key Key
	if level == 0 {
		key = leafHash.sum(node)
	} else {
		key = Key(blake2b.Sum256(node))
	}
	xorNode(block, node, key, level)
	return key
}

// DecryptNode verifies and decrypts one full block.
func DecryptNode(block []byte, pair Pair, level Level) ([]byte, error) {
	if err := VerifyBlock(block, pair.Reference); err != nil {
		return nil, err
	}
	return DecryptNodeFromSource(block, pair.Key, level)
}

// DecryptNodeFromSource decrypts a block returned by a trusted BlockSource.
// It checks the block size but does not compute its reference again.
func DecryptNodeFromSource(block []byte, key Key, level Level) ([]byte, error) {
	if !ValidBlockLength(len(block)) {
		return nil, ErrInvalidBlockSize
	}
	node := make([]byte, len(block))
	xorNode(node, block, key, level)
	return node, nil
}

// leafHasher reuses the keyed digest and its output space within one encode.
type leafHasher struct {
	digest hash.Hash
	key    Key
}

func newLeafHasher(secret Secret) *leafHasher {
	digest, _ := blake2b.New256(secret[:])
	return &leafHasher{digest: digest}
}

func (h *leafHasher) sum(node []byte) Key {
	_, _ = h.digest.Write(node)
	_ = h.digest.Sum(h.key[:0])
	h.digest.Reset()
	return h.key
}

func xorNode(dst, src []byte, key Key, level Level) {
	var nonce [chacha20.NonceSize]byte
	nonce[0] = byte(level)
	cipher, _ := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	cipher.XORKeyStream(dst, src)
}
