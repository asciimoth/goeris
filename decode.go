package eris

import (
	"math"
	"math/bits"
)

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
	// Both supported block sizes have arities that are powers of two.
	shift := int(level) * bits.TrailingZeros32(uint32(blockSize/PairSize))
	if shift >= 63 {
		return math.MaxInt64
	}
	return int64(1) << shift
}
