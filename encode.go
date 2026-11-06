package eris

// Encode transforms content into an ERIS capability and an in-memory set of
// encrypted blocks. It is deterministic for a given content, secret, and size.
func Encode(content []byte, secret Secret, blockSize BlockSize) (Capability, BlockSet, error) {
	if !blockSize.Valid() {
		return Capability{}, nil, ErrInvalidBlockSize
	}
	padded, _ := Pad(content, blockSize)

	blocks := make(BlockSet)
	pairs := make([]Pair, 0, len(padded)/int(blockSize))
	for offset := 0; offset < len(padded); offset += int(blockSize) {
		block, pair, _ := EncryptNode(padded[offset:offset+int(blockSize)], secret, 0)
		blocks[pair.Reference] = block
		pairs = append(pairs, pair)
	}

	level := Level(0)
	for len(pairs) > 1 {
		level++
		pairs = encodeLevel(pairs, secret, blockSize, level, blocks)
	}

	return Capability{BlockSize: blockSize, Level: level, Root: pairs[0]}, blocks, nil
}

// EncodeRecommended encodes content with the recommended block size.
func EncodeRecommended(content []byte, secret Secret) (Capability, BlockSet, error) {
	return Encode(content, secret, ChooseBlockSize(len(content)))
}

func encodeLevel(pairs []Pair, secret Secret, blockSize BlockSize, level Level, blocks BlockSet) []Pair {
	arity, _ := blockSize.Arity()
	parents := make([]Pair, 0, (len(pairs)+arity-1)/arity)
	for offset := 0; offset < len(pairs); offset += arity {
		end := min(offset+arity, len(pairs))
		node := make([]byte, blockSize)
		for index, pair := range pairs[offset:end] {
			pairOffset := index * PairSize
			copy(node[pairOffset:pairOffset+32], pair.Reference[:])
			copy(node[pairOffset+32:pairOffset+PairSize], pair.Key[:])
		}
		block, pair, _ := EncryptNode(node, secret, level)
		blocks[pair.Reference] = block
		parents = append(parents, pair)
	}
	return parents
}
