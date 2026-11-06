package cbor

import (
	"encoding/binary"

	"github.com/asciimoth/goeris"
)

// Unmarshal parses one ERIS CBOR array. It accepts an untagged array, an array
// with the ERIS CBOR tag, or an array with both the self-described and ERIS
// CBOR tags. The returned elements do not alias data.
func Unmarshal(data []byte) ([]Element, error) {
	decoder := decoder{data: data}
	if decoder.consumeTag(SelfDescribedTag) {
		if !decoder.consumeTag(Tag) {
			return nil, ErrInvalidEncoding
		}
	} else {
		decoder.consumeTag(Tag)
	}
	count, indefinite, err := decoder.head(4, true)
	if err != nil {
		return nil, ErrInvalidEncoding
	}
	elements, err := decoder.elements(count, indefinite, true)
	if err != nil || decoder.offset != len(data) {
		return nil, ErrInvalidEncoding
	}
	return elements, nil
}

// UnmarshalSequence parses a tagged or untagged ERIS CBOR sequence. An empty
// input is a valid untagged empty sequence.
func UnmarshalSequence(data []byte) ([]Element, error) {
	decoder := decoder{data: data}
	if decoder.consumeTag(Tag) {
		if !decoder.consumeByte(0xf6) {
			return nil, ErrInvalidEncoding
		}
	}
	elements, err := decoder.elements(0, true, false)
	if err != nil {
		return nil, ErrInvalidEncoding
	}
	return elements, nil
}

type decoder struct {
	data   []byte
	offset int
}

func (decoder *decoder) elements(count uint64, indefinite, array bool) ([]Element, error) {
	var elements []Element
	for index := uint64(0); indefinite || index < count; index++ {
		if decoder.offset == len(decoder.data) {
			if indefinite && !array {
				return elements, nil
			}
			return nil, ErrInvalidEncoding
		}
		if indefinite && array && decoder.consumeByte(0xff) {
			return elements, nil
		}
		element, err := decoder.element()
		if err != nil {
			return nil, err
		}
		elements = append(elements, element)
	}
	return elements, nil
}

func (decoder *decoder) element() (Element, error) {
	if decoder.peekMajor(2) {
		value, err := decoder.byteString()
		if err != nil {
			return Element{}, err
		}
		switch len(value) {
		case len(eris.Reference{}):
			var reference eris.Reference
			copy(reference[:], value)
			return NewReference(reference), nil
		case int(eris.BlockSize1KiB), int(eris.BlockSize32KiB):
			return NewBlock(value)
		default:
			return Element{}, ErrInvalidElement
		}
	}
	if decoder.peekMajor(5) {
		return decoder.blockMap()
	}
	if decoder.consumeTag(eris.CapabilityCBORTag) {
		value, err := decoder.byteString()
		if err != nil {
			return Element{}, err
		}
		capability, err := eris.ParseCapability(value)
		if err != nil {
			return Element{}, ErrInvalidElement
		}
		return NewCapability(capability)
	}
	return Element{}, ErrInvalidElement
}

func (decoder *decoder) blockMap() (Element, error) {
	count, indefinite, err := decoder.head(5, true)
	if err != nil {
		return Element{}, err
	}
	blocks := make(eris.BlockSet)
	for index := uint64(0); indefinite || index < count; index++ {
		if indefinite && decoder.consumeByte(0xff) {
			return Element{kind: BlockMapKind, blockMap: blocks}, nil
		}
		key, keyErr := decoder.byteString()
		if keyErr != nil || len(key) != len(eris.Reference{}) {
			return Element{}, ErrInvalidElement
		}
		var reference eris.Reference
		copy(reference[:], key)
		if _, duplicate := blocks[reference]; duplicate {
			return Element{}, ErrInvalidElement
		}
		block, blockErr := decoder.byteString()
		if blockErr != nil {
			return Element{}, blockErr
		}
		if verifyErr := eris.VerifyBlock(block, reference); verifyErr != nil {
			return Element{}, ErrInvalidElement
		}
		blocks[reference] = cloneBytes(block)
	}
	return Element{kind: BlockMapKind, blockMap: blocks}, nil
}

func (decoder *decoder) byteString() ([]byte, error) {
	length, indefinite, err := decoder.head(2, true)
	if err != nil {
		return nil, err
	}
	if !indefinite {
		if length > uint64(len(decoder.data)-decoder.offset) || length > intMax() {
			return nil, ErrInvalidEncoding
		}
		start := decoder.offset
		decoder.offset += int(length)
		return decoder.data[start:decoder.offset], nil
	}
	var value []byte
	for !decoder.consumeByte(0xff) {
		chunkLength, _, chunkErr := decoder.head(2, false)
		if chunkErr != nil || chunkLength > uint64(len(decoder.data)-decoder.offset) ||
			chunkLength > uint64(int(eris.BlockSize32KiB)-len(value)) {
			return nil, ErrInvalidEncoding
		}
		end := decoder.offset + int(chunkLength)
		value = append(value, decoder.data[decoder.offset:end]...)
		decoder.offset = end
	}
	return value, nil
}

func (decoder *decoder) consumeTag(tag uint64) bool {
	start := decoder.offset
	value, _, err := decoder.head(6, false)
	if err != nil || value != tag {
		decoder.offset = start
		return false
	}
	return true
}

func (decoder *decoder) peekMajor(major byte) bool {
	return decoder.offset < len(decoder.data) && decoder.data[decoder.offset]>>5 == major
}

func (decoder *decoder) consumeByte(value byte) bool {
	if decoder.offset >= len(decoder.data) || decoder.data[decoder.offset] != value {
		return false
	}
	decoder.offset++
	return true
}

func (decoder *decoder) head(major byte, allowIndefinite bool) (uint64, bool, error) {
	if decoder.offset >= len(decoder.data) {
		return 0, false, ErrInvalidEncoding
	}
	initial := decoder.data[decoder.offset]
	decoder.offset++
	if initial>>5 != major {
		return 0, false, ErrInvalidEncoding
	}
	additional := initial & 0x1f
	switch {
	case additional < 24:
		return uint64(additional), false, nil
	case additional == 24:
		value, err := decoder.fixed(1)
		return value, false, err
	case additional == 25:
		value, err := decoder.fixed(2)
		return value, false, err
	case additional == 26:
		value, err := decoder.fixed(4)
		return value, false, err
	case additional == 27:
		value, err := decoder.fixed(8)
		return value, false, err
	case additional == 31 && allowIndefinite:
		return 0, true, nil
	default:
		return 0, false, ErrInvalidEncoding
	}
}

func (decoder *decoder) fixed(size int) (uint64, error) {
	if len(decoder.data)-decoder.offset < size {
		return 0, ErrInvalidEncoding
	}
	var value uint64
	switch size {
	case 1:
		value = uint64(decoder.data[decoder.offset])
	case 2:
		value = uint64(binary.BigEndian.Uint16(decoder.data[decoder.offset:]))
	case 4:
		value = uint64(binary.BigEndian.Uint32(decoder.data[decoder.offset:]))
	case 8:
		value = binary.BigEndian.Uint64(decoder.data[decoder.offset:])
	}
	decoder.offset += size
	return value, nil
}

func intMax() uint64 {
	return uint64(^uint(0) >> 1)
}
