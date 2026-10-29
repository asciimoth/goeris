package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/asciimoth/eris-tools/eris"
)

const (
	// MIMEType is the specified media type for an ERIS link file.
	MIMEType = "application/x-eris-link+cbor"
	// Extension is the recommended file-name extension for an ERIS link file.
	Extension = ".eris"
	// CBORTag is the self-described CBOR tag used by ERIS link files.
	CBORTag = 55799
)

var (
	// ErrInvalidLink means that input is not a valid ERIS link file.
	ErrInvalidLink = errors.New("eris link: invalid link")
	// ErrInvalidValue means that input is not exactly one valid CBOR data item.
	ErrInvalidValue = errors.New("eris link: invalid CBOR value")
)

var header = [...]byte{0xd9, 0xd9, 0xf7, 0x84, 0xd9, 0x01, 0x14, 0x58, 0x42}

// Link identifies ERIS content and describes its decoded representation.
//
// Size is the size of the decoded content in bytes. MIME is a media type, but
// this package only requires valid UTF-8. The Attributes field maps text keys
// to arbitrary CBOR values. MarshalBinary orders attributes by their encoded
// CBOR keys, so map iteration order does not affect its output.
type Link struct {
	Capability eris.Capability
	Size       uint64
	MIME       string
	Attributes map[string]Value
}

// New returns a link with no attributes.
func New(capability eris.Capability, size uint64, mime string) (Link, error) {
	link := Link{Capability: capability, Size: size, MIME: mime}
	if err := link.Validate(); err != nil {
		return Link{}, err
	}
	return link, nil
}

// HasHeader reports whether data starts with the ERIS link magic bytes. It
// detects a possible link file but does not validate the remaining data.
func HasHeader(data []byte) bool {
	return len(data) >= len(header) && bytes.Equal(data[:len(header)], header[:])
}

// Validate checks all fields of link.
func (link Link) Validate() error {
	if link.Capability.Validate() != nil || !utf8.ValidString(link.MIME) {
		return ErrInvalidLink
	}
	for key, value := range link.Attributes {
		if !utf8.ValidString(key) || validateValue(value) != nil {
			return ErrInvalidLink
		}
	}
	return nil
}

// MarshalBinary returns a deterministic CBOR representation of link. It uses
// preferred CBOR representations for the link fields and map keys. Attribute
// values keep the representations supplied to ParseValue.
func (link Link) MarshalBinary() ([]byte, error) {
	if err := link.Validate(); err != nil {
		return nil, err
	}
	out := append([]byte(nil), header[:]...)
	capability, _ := link.Capability.MarshalBinary()
	out = append(out, capability...)
	out = appendHead(out, 0, link.Size)
	out = appendText(out, link.MIME)

	type attribute struct {
		key     string
		encoded []byte
	}
	attributes := make([]attribute, 0, len(link.Attributes))
	for key := range link.Attributes {
		attributes = append(attributes, attribute{key: key, encoded: appendText(nil, key)})
	}
	slices.SortFunc(attributes, func(left, right attribute) int {
		if len(left.encoded) != len(right.encoded) {
			return len(left.encoded) - len(right.encoded)
		}
		return bytes.Compare(left.encoded, right.encoded)
	})
	out = appendHead(out, 5, uint64(len(attributes)))
	for _, attribute := range attributes {
		out = append(out, attribute.encoded...)
		out = append(out, link.Attributes[attribute.key].data...)
	}
	return out, nil
}

// UnmarshalBinary parses an ERIS link file. It does not change link on error.
// Returned values do not alias data.
func (link *Link) UnmarshalBinary(data []byte) error {
	if link == nil || !HasHeader(data) {
		return ErrInvalidLink
	}
	decoder := linkDecoder{data: data, offset: len(header)}
	var next Link
	if len(data)-decoder.offset < eris.CapabilitySize ||
		next.Capability.UnmarshalBinary(data[decoder.offset:decoder.offset+eris.CapabilitySize]) != nil {
		return ErrInvalidLink
	}
	decoder.offset += eris.CapabilitySize
	var err error
	if next.Size, err = decoder.unsigned(); err != nil {
		return ErrInvalidLink
	}
	if next.MIME, err = decoder.text(); err != nil {
		return ErrInvalidLink
	}
	count, indefinite, err := decoder.collection(5)
	if err != nil || (!indefinite && count > uint64(len(data))) {
		return ErrInvalidLink
	}
	next.Attributes = make(map[string]Value)
	for index := uint64(0); indefinite || index < count; index++ {
		if indefinite && decoder.consumeBreak() {
			break
		}
		key, keyErr := decoder.text()
		if keyErr != nil {
			return ErrInvalidLink
		}
		if _, exists := next.Attributes[key]; exists {
			return ErrInvalidLink
		}
		start := decoder.offset
		valueDecoder := valueDecoder{data: data, offset: start}
		if valueDecoder.item(0, false) != nil {
			return ErrInvalidLink
		}
		decoder.offset = valueDecoder.offset
		next.Attributes[key] = Value{data: append([]byte(nil), data[start:decoder.offset]...)}
	}
	if decoder.offset != len(data) || next.Validate() != nil {
		return ErrInvalidLink
	}
	*link = next
	return nil
}

// Parse parses an ERIS link file.
func Parse(data []byte) (Link, error) {
	var link Link
	err := link.UnmarshalBinary(data)
	return link, err
}

func validateValue(value Value) error {
	decoder := valueDecoder{data: value.data}
	if decoder.item(0, false) != nil || decoder.offset != len(value.data) {
		return ErrInvalidValue
	}
	return nil
}

func appendHead(dst []byte, major byte, value uint64) []byte {
	switch {
	case value < 24:
		return append(dst, major<<5|byte(value))
	case value <= math.MaxUint8:
		return append(dst, major<<5|24, byte(value))
	case value <= math.MaxUint16:
		dst = append(dst, major<<5|25, 0, 0)
		binary.BigEndian.PutUint16(dst[len(dst)-2:], uint16(value))
	case value <= math.MaxUint32:
		dst = append(dst, major<<5|26, 0, 0, 0, 0)
		binary.BigEndian.PutUint32(dst[len(dst)-4:], uint32(value))
	default:
		dst = append(dst, major<<5|27, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(dst[len(dst)-8:], value)
	}
	return dst
}

func appendText(dst []byte, text string) []byte {
	dst = appendHead(dst, 3, uint64(len(text)))
	return append(dst, text...)
}

type linkDecoder struct {
	data   []byte
	offset int
}

func (decoder *linkDecoder) unsigned() (uint64, error) {
	value, indefinite, err := decoder.head(0)
	if err != nil || indefinite {
		return 0, ErrInvalidLink
	}
	return value, nil
}

func (decoder *linkDecoder) text() (string, error) {
	valueDecoder := valueDecoder{data: decoder.data, offset: decoder.offset}
	if valueDecoder.offset >= len(valueDecoder.data) || valueDecoder.data[valueDecoder.offset]>>5 != 3 {
		return "", ErrInvalidLink
	}
	initial := valueDecoder.data[valueDecoder.offset]
	valueDecoder.offset++
	length, indefinite, err := valueDecoder.argument(initial & 0x1f)
	if err != nil {
		return "", ErrInvalidLink
	}
	if !indefinite {
		start := valueDecoder.offset
		if valueDecoder.stringBytes(3, length) != nil {
			return "", ErrInvalidLink
		}
		decoder.offset = valueDecoder.offset
		return string(decoder.data[start:decoder.offset]), nil
	}
	var result []byte
	for {
		if valueDecoder.offset >= len(valueDecoder.data) {
			return "", ErrInvalidLink
		}
		if valueDecoder.data[valueDecoder.offset] == 0xff {
			valueDecoder.offset++
			decoder.offset = valueDecoder.offset
			return string(result), nil
		}
		chunk := valueDecoder.data[valueDecoder.offset]
		valueDecoder.offset++
		if chunk>>5 != 3 {
			return "", ErrInvalidLink
		}
		chunkLength, nested, chunkErr := valueDecoder.argument(chunk & 0x1f)
		if chunkErr != nil || nested || chunkLength > uint64(len(valueDecoder.data)-valueDecoder.offset) {
			return "", ErrInvalidLink
		}
		end := valueDecoder.offset + int(chunkLength)
		if !utf8.Valid(valueDecoder.data[valueDecoder.offset:end]) {
			return "", ErrInvalidLink
		}
		result = append(result, valueDecoder.data[valueDecoder.offset:end]...)
		valueDecoder.offset = end
	}
}

func (decoder *linkDecoder) collection(major byte) (uint64, bool, error) {
	return decoder.head(major)
}

func (decoder *linkDecoder) head(major byte) (uint64, bool, error) {
	if decoder.offset >= len(decoder.data) || decoder.data[decoder.offset]>>5 != major {
		return 0, false, ErrInvalidLink
	}
	initial := decoder.data[decoder.offset]
	decoder.offset++
	valueDecoder := valueDecoder{data: decoder.data, offset: decoder.offset}
	value, indefinite, err := valueDecoder.argument(initial & 0x1f)
	decoder.offset = valueDecoder.offset
	return value, indefinite, err
}

func (decoder *linkDecoder) consumeBreak() bool {
	if decoder.offset >= len(decoder.data) || decoder.data[decoder.offset] != 0xff {
		return false
	}
	decoder.offset++
	return true
}
