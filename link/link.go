package link

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

const (
	// MIMEType is the specified media type for an ERIS link file.
	MIMEType = "application/x-eris-link+cbor"
	// Extension is the recommended file-name extension for an ERIS link file.
	Extension = ".eris"
	// CBORTag is the self-described CBOR tag used by ERIS link files.
	CBORTag = 55799
	// MagicPrefix is the nine-byte prefix of an ERIS link file.
	MagicPrefix = "\xd9\xd9\xf7\x84\xd9\x01\x14\x58\x42"
	// HeaderSize is the number of magic bytes at the start of an ERIS link file.
	HeaderSize = len(MagicPrefix)
)

var (
	// ErrInvalidLink means that input is not a valid ERIS link file.
	ErrInvalidLink = errors.New("eris link: invalid link")
	// ErrInvalidValue means that input fails the structural checks in ParseValue.
	ErrInvalidValue = errors.New("eris link: invalid CBOR value")
)

// Link identifies ERIS content and describes its decoded representation.
//
// Size is the declared size of the decoded content in bytes. It is not verified.
// MIME is a media type, but this package only requires valid UTF-8.
// The Attributes field maps text keys
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
	return len(data) >= HeaderSize && string(data[:HeaderSize]) == MagicPrefix
}

// Validate checks the structure of all fields of link. It does not check the
// referenced content or the meaning of metadata.
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
	out := append([]byte(nil), MagicPrefix...)
	capability, _ := link.Capability.MarshalBinary()
	out = append(out, capability...)
	out = cborwire.AppendHead(out, 0, link.Size)
	out = appendText(out, link.MIME)

	keys := make([]string, 0, len(link.Attributes))
	for key := range link.Attributes {
		keys = append(keys, key)
	}
	// All keys are text strings. Their encoded lengths increase with their
	// byte lengths, and equal-length keys have identical CBOR headers.
	// Sort the strings directly to avoid a temporary encoded copy of each key.
	slices.SortFunc(keys, func(left, right string) int {
		if len(left) != len(right) {
			return len(left) - len(right)
		}
		return strings.Compare(left, right)
	})
	out = cborwire.AppendHead(out, 5, uint64(len(keys)))
	for _, key := range keys {
		out = appendText(out, key)
		out = append(out, link.Attributes[key].data...)
	}
	return out, nil
}

// UnmarshalBinary parses an ERIS link file. It does not change link on error.
// Returned values do not alias data.
func (link *Link) UnmarshalBinary(data []byte) error {
	if link == nil {
		return ErrInvalidLink
	}
	next, err := parseLink(data)
	if err != nil {
		return err
	}
	for key, value := range next.attributes {
		next.attributes[key] = Value{data: append([]byte(nil), value.data...)}
	}
	if next.attributes == nil {
		next.attributes = make(map[string]Value)
	}
	*link = Link{
		Capability: next.capability, Size: next.size,
		MIME: next.mime.string(), Attributes: next.attributes,
	}
	return nil
}

// linkView holds validated metadata with borrowed MIME and attribute payloads.
// Keep it separate from Link, whose values must not alias the input.
type linkView struct {
	capability eris.Capability
	size       uint64
	mime       textView
	attributes map[string]Value
}

// parseLink validates the complete link. FS needs only the capability and size,
// so it can use this view without copying MIME or attribute payloads.
func parseLink(data []byte) (linkView, error) {
	if !HasHeader(data) {
		return linkView{}, ErrInvalidLink
	}
	decoder := linkDecoder{data: data, offset: HeaderSize}
	var next linkView
	if len(data)-decoder.offset < eris.CapabilitySize ||
		next.capability.UnmarshalBinary(data[decoder.offset:decoder.offset+eris.CapabilitySize]) != nil {
		return linkView{}, ErrInvalidLink
	}
	decoder.offset += eris.CapabilitySize
	var err error
	if next.size, err = decoder.unsigned(); err != nil {
		return linkView{}, ErrInvalidLink
	}
	if next.mime, err = decoder.text(); err != nil {
		return linkView{}, ErrInvalidLink
	}
	count, indefinite, err := decoder.head(5)
	// Each attribute needs at least one byte for its key and one for its value.
	if err != nil || (!indefinite && count > uint64(len(data)-decoder.offset)/2) {
		return linkView{}, ErrInvalidLink
	}
	for index := uint64(0); indefinite || index < count; index++ {
		if indefinite && decoder.consumeBreak() {
			break
		}
		keyText, keyErr := decoder.text()
		if keyErr != nil {
			return linkView{}, ErrInvalidLink
		}
		start := decoder.offset
		valueDecoder := valueDecoder{data: data, offset: start}
		if valueDecoder.item(0) != nil {
			return linkView{}, ErrInvalidLink
		}
		decoder.offset = valueDecoder.offset
		// Validate the value before copying a potentially large key. A
		// malformed first attribute must not allocate its key or the map.
		key := keyText.string()
		if _, exists := next.attributes[key]; exists {
			return linkView{}, ErrInvalidLink
		}
		if next.attributes == nil {
			next.attributes = make(map[string]Value)
		}
		// Keep a temporary view until the entire link passes validation.
		// Rejected links must not copy potentially large attribute payloads.
		next.attributes[key] = Value{data: data[start:decoder.offset]}
	}
	// Each field was checked above. Do not scan attribute values a second time.
	if decoder.offset != len(data) {
		return linkView{}, ErrInvalidLink
	}
	return next, nil
}

// Parse parses an ERIS link file.
func Parse(data []byte) (Link, error) {
	var link Link
	err := link.UnmarshalBinary(data)
	return link, err
}

func validateValue(value Value) error {
	decoder := valueDecoder{data: value.data}
	if decoder.item(0) != nil || decoder.offset != len(value.data) {
		return ErrInvalidValue
	}
	return nil
}

func appendText(dst []byte, text string) []byte {
	dst = cborwire.AppendHead(dst, 3, uint64(len(text)))
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

// textView borrows a validated text payload until it is needed as a string.
// For chunked text, data contains the chunk headers but not the final break.
type textView struct {
	data    []byte
	length  int
	chunked bool
}

func (decoder *linkDecoder) text() (textView, error) {
	valueDecoder := valueDecoder{data: decoder.data, offset: decoder.offset}
	if valueDecoder.offset >= len(valueDecoder.data) || valueDecoder.data[valueDecoder.offset]>>5 != 3 {
		return textView{}, ErrInvalidLink
	}
	initial := valueDecoder.data[valueDecoder.offset]
	valueDecoder.offset++
	length, indefinite, err := valueDecoder.argument(initial & 0x1f)
	if err != nil {
		return textView{}, ErrInvalidLink
	}
	if !indefinite {
		start := valueDecoder.offset
		if valueDecoder.stringBytes(3, length) != nil {
			return textView{}, ErrInvalidLink
		}
		decoder.offset = valueDecoder.offset
		return textView{data: decoder.data[start:decoder.offset], length: int(length)}, nil
	}
	// Validate all chunks before allocating the decoded string. In
	// particular, a bad final chunk must not copy a large preceding payload.
	start := valueDecoder.offset
	textLength, err := valueDecoder.string(3, 0, true)
	if err != nil {
		return textView{}, ErrInvalidLink
	}
	decoder.offset = valueDecoder.offset
	return textView{data: decoder.data[start : decoder.offset-1], length: textLength, chunked: true}, nil
}

func (text textView) string() string {
	if !text.chunked {
		return string(text.data)
	}
	decoder := valueDecoder{data: text.data}
	var result strings.Builder
	result.Grow(text.length)
	for decoder.offset < len(decoder.data) {
		chunk := decoder.data[decoder.offset]
		decoder.offset++
		// The first pass checked the chunk type, length, and UTF-8.
		chunkLength, _, _ := decoder.argument(chunk & 0x1f)
		chunkEnd := decoder.offset + int(chunkLength)
		result.Write(decoder.data[decoder.offset:chunkEnd])
		decoder.offset = chunkEnd
	}
	return result.String()
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
