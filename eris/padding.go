package eris

// Pad applies the mandatory ERIS byte padding. The returned slice does not
// share its backing array with content.
func Pad(content []byte, blockSize BlockSize) ([]byte, error) {
	if !blockSize.Valid() {
		return nil, ErrInvalidBlockSize
	}
	size := int(blockSize)
	paddingLength := size - (len(content) % size)
	padded := make([]byte, len(content)+paddingLength)
	copy(padded, content)
	padded[len(content)] = 0x80
	return padded, nil
}

// Unpad validates and removes ERIS byte padding. The returned slice is a copy
// and does not share its backing array with padded.
func Unpad(padded []byte, blockSize BlockSize) ([]byte, error) {
	if !blockSize.Valid() {
		return nil, ErrInvalidBlockSize
	}
	size := int(blockSize)
	if len(padded) < size || len(padded)%size != 0 {
		return nil, ErrInvalidPadding
	}
	for i := len(padded) - 1; i >= len(padded)-size; i-- {
		switch padded[i] {
		case 0x80:
			out := make([]byte, i)
			copy(out, padded[:i])
			return out, nil
		case 0x00:
			continue
		default:
			return nil, ErrInvalidPadding
		}
	}
	return nil, ErrInvalidPadding
}
