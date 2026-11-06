package eris

import (
	"errors"
	"testing"
)

func TestParseInternalNode(t *testing.T) {
	t.Parallel()
	first := Pair{Reference: Reference{1}, Key: Key{2}}
	second := Pair{Reference: Reference{3}, Key: Key{4}}
	for _, size := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
		node := make([]byte, size)
		copy(node[:32], first.Reference[:])
		copy(node[32:PairSize], first.Key[:])
		copy(node[PairSize:PairSize+32], second.Reference[:])
		copy(node[PairSize+32:2*PairSize], second.Key[:])
		pairs, err := ParseInternalNode(node)
		if err != nil || len(pairs) != 2 || pairs[0] != first || pairs[1] != second {
			t.Errorf("ParseInternalNode(%d-byte node) = (%v, %v)", size, pairs, err)
		}
		node[2*PairSize] = 1
		if _, err := ParseInternalNode(node); err != nil {
			t.Errorf("ParseInternalNode(%d-byte node with third pair) = %v", size, err)
		}
		node[2*PairSize] = 0
		node[3*PairSize] = 1
		if _, err := ParseInternalNode(node); !errors.Is(err, ErrInvalidNode) {
			t.Errorf("ParseInternalNode(%d-byte node with gap) = %v", size, err)
		}
	}
}
