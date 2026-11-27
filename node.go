package eris

// ParseInternalNode reads the reference-key pairs in one ERIS internal node.
// The node must have a valid block size and at least one pair. Empty slots
// can follow the pairs, but no nonzero slot can follow an empty slot.
// It returns ErrInvalidNode if the node is not valid.
func ParseInternalNode(node []byte) ([]Pair, error) {
	if !ValidBlockLength(len(node)) {
		return nil, ErrInvalidNode
	}
	end := len(node)
	var empty [PairSize]byte
	for offset := 0; offset < len(node); offset += PairSize {
		if *(*[PairSize]byte)(node[offset:]) == empty {
			end = min(end, offset)
		} else if offset >= end {
			return nil, ErrInvalidNode
		}
	}
	if end == 0 {
		return nil, ErrInvalidNode
	}
	pairs := make([]Pair, end/PairSize)
	for i := range pairs {
		slot := node[i*PairSize : (i+1)*PairSize]
		copy(pairs[i].Reference[:], slot[:ReferenceSize])
		copy(pairs[i].Key[:], slot[ReferenceSize:])
	}
	return pairs, nil
}
