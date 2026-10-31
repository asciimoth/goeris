package eris

import (
	"crypto/subtle"
	"errors"
	"math"
)

// Decode verifies and decodes content from an in-memory block set.
func Decode(capability Capability, blocks BlockSet) ([]byte, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	padded, _, err := decodeNode(capability.Root, capability.Level, capability.BlockSize, blocks, true)
	if err != nil {
		return nil, err
	}
	return Unpad(padded, capability.BlockSize)
}

func decodeNode(pair Pair, level Level, blockSize BlockSize, blocks BlockSet, root bool) ([]byte, int64, error) {
	block, ok := blocks[pair.Reference]
	if !ok {
		return nil, 0, ErrMissingBlock
	}
	if len(block) != int(blockSize) {
		return nil, 0, errors.Join(ErrInvalidBlock, ErrInvalidBlockSize)
	}
	node, err := DecryptNode(block, pair, level)
	if err != nil {
		return nil, 0, err
	}
	if level == 0 {
		return node, 1, nil
	}
	if root {
		digest := blake2bKey(node)
		if subtle.ConstantTimeCompare(digest[:], pair.Key[:]) != 1 {
			return nil, 0, ErrInvalidRootKey
		}
	}

	pairs, err := ParseInternalNode(node)
	if err != nil {
		return nil, 0, err
	}
	if root && len(pairs) < 2 {
		return nil, 0, ErrInvalidNode
	}
	output := make([]byte, 0, len(pairs)*int(blockSize))
	var leaves int64
	for i, child := range pairs {
		decoded, count, decodeErr := decodeNode(child, level-1, blockSize, blocks, false)
		if decodeErr != nil {
			return nil, 0, decodeErr
		}
		if i < len(pairs)-1 && count != subtreeLeafCapacity(blockSize, level-1) ||
			count > math.MaxInt64-leaves {
			return nil, 0, ErrInvalidNode
		}
		leaves += count
		output = append(output, decoded...)
	}
	return output, leaves, nil
}

func subtreeLeafCapacity(blockSize BlockSize, level Level) int64 {
	arity, _ := blockSize.Arity()
	capacity := int64(1)
	for range level {
		if capacity > math.MaxInt64/int64(arity) {
			return math.MaxInt64
		}
		capacity *= int64(arity)
	}
	return capacity
}

func blake2bKey(data []byte) Key {
	digest := blake2bSum(data)
	return Key(digest)
}
