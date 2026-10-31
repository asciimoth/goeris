package eris

import (
	"crypto/subtle"
	"errors"
)

// Decode verifies and decodes content from an in-memory block set.
func Decode(capability Capability, blocks BlockSet) ([]byte, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	padded, err := decodeNode(capability.Root, capability.Level, capability.BlockSize, blocks, true)
	if err != nil {
		return nil, err
	}
	return Unpad(padded, capability.BlockSize)
}

func decodeNode(pair Pair, level Level, blockSize BlockSize, blocks BlockSet, root bool) ([]byte, error) {
	block, ok := blocks[pair.Reference]
	if !ok {
		return nil, ErrMissingBlock
	}
	if len(block) != int(blockSize) {
		return nil, errors.Join(ErrInvalidBlock, ErrInvalidBlockSize)
	}
	node, err := DecryptNode(block, pair, level)
	if err != nil {
		return nil, err
	}
	if level == 0 {
		return node, nil
	}
	if root {
		digest := blake2bKey(node)
		if subtle.ConstantTimeCompare(digest[:], pair.Key[:]) != 1 {
			return nil, ErrInvalidRootKey
		}
	}

	pairs, err := ParseInternalNode(node)
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0, len(pairs)*int(blockSize))
	for _, child := range pairs {
		decoded, decodeErr := decodeNode(child, level-1, blockSize, blocks, false)
		if decodeErr != nil {
			return nil, decodeErr
		}
		output = append(output, decoded...)
	}
	return output, nil
}

func blake2bKey(data []byte) Key {
	digest := blake2bSum(data)
	return Key(digest)
}
