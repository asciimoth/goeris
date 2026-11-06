package link

import (
	"encoding/binary"
	"math"
	"unicode/utf8"

	"github.com/asciimoth/goeris/internal/cborwire"
)

// Value is one well-formed CBOR data item used as an attribute value.
//
// Value keeps the original CBOR representation. This permits all CBOR data
// types without adding a general-purpose CBOR dependency. Use ParseValue to
// create a Value from CBOR data. ParseValue also requires valid UTF-8 in every
// CBOR text string. It does not replace non-preferred CBOR representations
// with preferred representations. A Value does not alias its input or output.
// The zero Value is not valid.
type Value struct {
	data []byte
}

// ParseValue verifies exactly one well-formed CBOR data item and returns an
// immutable copy. It accepts definite and indefinite-length CBOR forms.
func ParseValue(data []byte) (Value, error) {
	decoder := valueDecoder{data: data}
	if err := decoder.item(0, false); err != nil || decoder.offset != len(data) {
		return Value{}, ErrInvalidValue
	}
	return Value{data: append([]byte(nil), data...)}, nil
}

// RawCBOR returns a copy of the encoded CBOR data item.
func (value Value) RawCBOR() []byte {
	return append([]byte(nil), value.data...)
}

// NullValue returns the CBOR null value.
func NullValue() Value {
	return Value{data: []byte{0xf6}}
}

// BoolValue returns a CBOR Boolean value.
func BoolValue(value bool) Value {
	if value {
		return Value{data: []byte{0xf5}}
	}
	return Value{data: []byte{0xf4}}
}

// UintValue returns a CBOR unsigned integer value.
func UintValue(value uint64) Value {
	return Value{data: cborwire.AppendHead(nil, 0, value)}
}

// IntValue returns a CBOR integer value.
func IntValue(value int64) Value {
	if value >= 0 {
		return UintValue(uint64(value))
	}
	return Value{data: cborwire.AppendHead(nil, 1, uint64(-(value + 1)))}
}

// BytesValue returns a CBOR byte string value. It copies data.
func BytesValue(data []byte) Value {
	encoded := cborwire.AppendHead(nil, 2, uint64(len(data)))
	return Value{data: append(encoded, data...)}
}

// TextValue returns a CBOR text string value. It returns ErrInvalidValue if
// text is not valid UTF-8.
func TextValue(text string) (Value, error) {
	if !utf8.ValidString(text) {
		return Value{}, ErrInvalidValue
	}
	encoded := cborwire.AppendHead(nil, 3, uint64(len(text)))
	return Value{data: append(encoded, text...)}, nil
}

const maxCBORDepth = 256

type valueDecoder struct {
	data   []byte
	offset int
}

func (decoder *valueDecoder) item(depth int, allowBreak bool) error {
	if depth > maxCBORDepth || decoder.offset >= len(decoder.data) {
		return ErrInvalidValue
	}
	initial := decoder.data[decoder.offset]
	if initial == 0xff {
		if !allowBreak {
			return ErrInvalidValue
		}
		decoder.offset++
		return nil
	}
	decoder.offset++
	major := initial >> 5
	additional := initial & 0x1f
	value, indefinite, err := decoder.argument(additional)
	if err != nil {
		return err
	}

	switch major {
	case 0, 1:
		if indefinite {
			return ErrInvalidValue
		}
	case 2, 3:
		return decoder.string(major, value, indefinite)
	case 4:
		return decoder.items(depth, value, indefinite, 1)
	case 5:
		return decoder.items(depth, value, indefinite, 2)
	case 6:
		if indefinite {
			return ErrInvalidValue
		}
		return decoder.item(depth+1, false)
	case 7:
		if additional == 24 && value < 32 {
			return ErrInvalidValue
		}
		return nil
	}
	return nil
}

func (decoder *valueDecoder) argument(additional byte) (uint64, bool, error) {
	switch {
	case additional < 24:
		return uint64(additional), false, nil
	case additional == 24:
		return decoder.fixed(1)
	case additional == 25:
		return decoder.fixed(2)
	case additional == 26:
		return decoder.fixed(4)
	case additional == 27:
		return decoder.fixed(8)
	case additional == 31:
		return 0, true, nil
	default:
		return 0, false, ErrInvalidValue
	}
}

func (decoder *valueDecoder) fixed(size int) (uint64, bool, error) {
	if len(decoder.data)-decoder.offset < size {
		return 0, false, ErrInvalidValue
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
	return value, false, nil
}

func (decoder *valueDecoder) string(major byte, length uint64, indefinite bool) error {
	if !indefinite {
		return decoder.stringBytes(major, length)
	}
	for {
		if decoder.offset >= len(decoder.data) {
			return ErrInvalidValue
		}
		if decoder.data[decoder.offset] == 0xff {
			decoder.offset++
			return nil
		}
		initial := decoder.data[decoder.offset]
		decoder.offset++
		if initial>>5 != major {
			return ErrInvalidValue
		}
		length, nested, err := decoder.argument(initial & 0x1f)
		if err != nil || nested {
			return ErrInvalidValue
		}
		if err := decoder.stringBytes(major, length); err != nil {
			return err
		}
	}
}

func (decoder *valueDecoder) stringBytes(major byte, length uint64) error {
	if length > uint64(len(decoder.data)-decoder.offset) || length > uint64(math.MaxInt) {
		return ErrInvalidValue
	}
	end := decoder.offset + int(length)
	if major == 3 && !utf8.Valid(decoder.data[decoder.offset:end]) {
		return ErrInvalidValue
	}
	decoder.offset = end
	return nil
}

func (decoder *valueDecoder) items(depth int, count uint64, indefinite bool, width uint64) error {
	if !indefinite && count > uint64(len(decoder.data))/width {
		return ErrInvalidValue
	}
	if indefinite {
		for {
			start := decoder.offset
			if err := decoder.item(depth+1, true); err != nil {
				return err
			}
			if decoder.data[start] == 0xff {
				return nil
			}
			if width == 2 {
				if err := decoder.item(depth+1, false); err != nil {
					return err
				}
			}
		}
	}
	for range count * width {
		if err := decoder.item(depth+1, false); err != nil {
			return err
		}
	}
	return nil
}
