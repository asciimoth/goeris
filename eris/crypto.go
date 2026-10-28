package eris

import (
	"crypto/subtle"
	"fmt"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/chacha20"
)

// ReferenceOf computes the reference for an encrypted ERIS block.
func ReferenceOf(block []byte) (Reference, error) {
	var ref Reference
	if !validBlockLength(len(block)) {
		return ref, ErrInvalidBlockSize
	}
	digest := blake2b.Sum256(block)
	copy(ref[:], digest[:])
	return ref, nil
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
	var pair Pair
	if !validBlockLength(len(node)) {
		return nil, pair, ErrInvalidBlockSize
	}

	if level == 0 {
		pair.Key = keyedHash(node, secret)
	} else {
		digest := blake2b.Sum256(node)
		copy(pair.Key[:], digest[:])
	}

	block := make([]byte, len(node))
	xorNode(block, node, pair.Key, level)
	pair.Reference, _ = ReferenceOf(block)
	return block, pair, nil
}

// DecryptNode verifies and decrypts one full block.
func DecryptNode(block []byte, pair Pair, level Level) ([]byte, error) {
	if err := VerifyBlock(block, pair.Reference); err != nil {
		return nil, err
	}
	node := make([]byte, len(block))
	xorNode(node, block, pair.Key, level)
	return node, nil
}

func keyedHash(data []byte, secret Secret) Key {
	var key Key
	hash, _ := blake2b.New256(secret[:])
	_, _ = hash.Write(data)
	copy(key[:], hash.Sum(nil))
	return key
}

func xorNode(dst, src []byte, key Key, level Level) {
	var nonce [chacha20.NonceSize]byte
	nonce[0] = byte(level)
	cipher, _ := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	cipher.XORKeyStream(dst, src)
}

func validBlockLength(length int) bool {
	return length == int(BlockSize1KiB) || length == int(BlockSize32KiB)
}
