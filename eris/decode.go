package eris

import (
	"crypto/subtle"
	"errors"
	"math"
)

// Decode verifies and decodes content from an in-memory block set. It has no
// application size limit; use DecodeWithLimit for untrusted block sets.
func Decode(capability Capability, blocks BlockSet) ([]byte, error) {
	return DecodeWithLimit(capability, blocks, math.MaxInt)
}

// DecodeWithLimit decodes content only if its unpadded size is at most limit.
// It checks the tree size before allocating the output. Use this function for
// block sets supplied by untrusted sources. A negative limit is invalid.
func DecodeWithLimit(capability Capability, blocks BlockSet, limit int64) ([]byte, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, ErrInvalidLimit
	}
	inspector := decodeInspector{blocks: blocks, blockSize: capability.BlockSize, summaries: make(map[decodeNodeID]int64)}
	leaves, err := inspector.inspect(capability.Root, capability.Level, true)
	if err != nil {
		return nil, err
	}
	last, err := inspector.lastLeaf(capability.Root, capability.Level)
	if err != nil {
		return nil, err
	}
	lastNode, err := inspector.node(last, 0)
	if err != nil {
		return nil, err
	}
	lastLength, err := paddedContentLength(lastNode)
	if err != nil {
		return nil, err
	}
	blockSize := int64(capability.BlockSize)
	if leaves-1 > (math.MaxInt64-int64(lastLength))/blockSize {
		return nil, ErrDecodedTooLarge
	}
	size := (leaves-1)*blockSize + int64(lastLength)
	if size > limit || size > int64(math.MaxInt) {
		return nil, ErrDecodedTooLarge
	}
	padded, _, err := decodeNode(capability.Root, capability.Level, capability.BlockSize, blocks, true)
	if err != nil {
		return nil, err
	}
	return Unpad(padded, capability.BlockSize)
}

type decodeNodeID struct {
	pair  Pair
	level Level
}

type decodeInspector struct {
	blocks    BlockSet
	blockSize BlockSize
	summaries map[decodeNodeID]int64
}

func (inspector *decodeInspector) node(pair Pair, level Level) ([]byte, error) {
	block, ok := inspector.blocks[pair.Reference]
	if !ok {
		return nil, ErrMissingBlock
	}
	if len(block) != int(inspector.blockSize) {
		return nil, errors.Join(ErrInvalidBlock, ErrInvalidBlockSize)
	}
	return DecryptNode(block, pair, level)
}

func (inspector *decodeInspector) pairs(pair Pair, level Level, root bool) ([]Pair, error) {
	node, err := inspector.node(pair, level)
	if err != nil {
		return nil, err
	}
	if root {
		digest := blake2bKey(node)
		if subtle.ConstantTimeCompare(digest[:], pair.Key[:]) != 1 {
			return nil, ErrInvalidRootKey
		}
	}
	return ParseInternalNode(node)
}

func (inspector *decodeInspector) inspect(pair Pair, level Level, root bool) (int64, error) {
	if level == 0 {
		return 1, nil
	}
	id := decodeNodeID{pair: pair, level: level}
	if count, ok := inspector.summaries[id]; ok {
		return count, nil
	}
	pairs, err := inspector.pairs(pair, level, root)
	if err != nil {
		return 0, err
	}
	if root && len(pairs) < 2 {
		return 0, ErrInvalidNode
	}
	var total int64
	for i, child := range pairs {
		count, err := inspector.inspect(child, level-1, false)
		if err != nil {
			return 0, err
		}
		if i < len(pairs)-1 && count != subtreeLeafCapacity(inspector.blockSize, level-1) ||
			count > math.MaxInt64-total {
			return 0, ErrInvalidNode
		}
		total += count
	}
	inspector.summaries[id] = total
	return total, nil
}

func (inspector *decodeInspector) lastLeaf(pair Pair, level Level) (Pair, error) {
	for level > 0 {
		pairs, err := inspector.pairs(pair, level, false)
		if err != nil {
			return Pair{}, err
		}
		pair = pairs[len(pairs)-1]
		level--
	}
	return pair, nil
}

func paddedContentLength(node []byte) (int, error) {
	for i := len(node) - 1; i >= 0; i-- {
		switch node[i] {
		case 0:
			continue
		case 0x80:
			return i, nil
		default:
			return 0, ErrInvalidPadding
		}
	}
	return 0, ErrInvalidPadding
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
