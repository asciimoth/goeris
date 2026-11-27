package eris

import "math"

type decodeNodeID struct {
	pair  Pair
	level Level
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
