package eris

// ParseInternalNode reads the reference-key pairs in one ERIS internal node.
// The node must have a valid block size and at least one pair. Empty slots
// can follow the pairs, but no nonzero slot can follow an empty slot.
// It returns ErrInvalidNode if the node is not valid.
func ParseInternalNode(node []byte) ([]Pair, error) {
	if !validBlockLength(len(node)) {
		return nil, ErrInvalidNode
	}
	pairs := make([]Pair, 0, len(node)/PairSize)
	foundEnd := false
	for offset := 0; offset < len(node); offset += PairSize {
		slot := node[offset : offset+PairSize]
		zero := true
		for _, value := range slot {
			zero = zero && value == 0
		}
		if zero {
			foundEnd = true
			continue
		}
		if foundEnd {
			return nil, ErrInvalidNode
		}
		var pair Pair
		copy(pair.Reference[:], slot[:32])
		copy(pair.Key[:], slot[32:])
		pairs = append(pairs, pair)
	}
	if len(pairs) == 0 {
		return nil, ErrInvalidNode
	}
	return pairs, nil
}
