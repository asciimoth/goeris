package fs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

var indexTagHeader = [...]byte{0xda, 0x65, 0x72, 0x69, 0x74}

// HasIndexHeader reports whether data starts with the five-byte ERIS-FS CBOR
// tag. It detects a possible image but does not validate the remaining data.
func HasIndexHeader(data []byte) bool {
	return len(data) >= len(indexTagHeader) && bytes.Equal(data[:len(indexTagHeader)], indexTagHeader[:])
}

// MarshalBinary returns the canonical deterministic CBOR representation.
func (index Index) MarshalBinary() ([]byte, error) {
	if err := index.Validate(); err != nil {
		return nil, err
	}
	type encodedEntry struct {
		key   []byte
		value []byte
	}
	encoded := make([]encodedEntry, len(index.Entries))
	for i, entry := range index.Entries {
		key := cborwire.AppendHead(nil, 4, uint64(len(entry.Path)))
		for _, component := range entry.Path {
			key = appendText(key, component)
		}
		value := cborwire.AppendHead(nil, 4, 2)
		value = cborwire.AppendHead(value, 0, uint64(entry.Type))
		if entry.Type == Symlink {
			value = appendText(value, entry.Target)
		} else {
			capability, _ := entry.Capability.MarshalBinary()
			value = cborwire.AppendHead(value, 6, eris.CapabilityCBORTag)
			value = appendBytes(value, capability)
		}
		encoded[i] = encodedEntry{key: key, value: value}
	}
	slices.SortFunc(encoded, func(a, b encodedEntry) int {
		if len(a.key) != len(b.key) {
			return len(a.key) - len(b.key)
		}
		return bytes.Compare(a.key, b.key)
	})
	out := append([]byte(nil), indexTagHeader[:]...)
	out = cborwire.AppendHead(out, 4, 2)
	out = cborwire.AppendHead(out, 0, SpecificationVersion)
	out = cborwire.AppendHead(out, 5, uint64(len(encoded)))
	for _, item := range encoded {
		out = append(out, item.key...)
		out = append(out, item.value...)
	}
	return out, nil
}

// UnmarshalBinary parses a canonical deterministic ERIS-FS version 1 index.
// The receiver is not changed on failure.
func (index *Index) UnmarshalBinary(data []byte) error {
	if index == nil || !HasIndexHeader(data) {
		return ErrInvalidIndex
	}
	decoder := cborDecoder{data: data, offset: len(indexTagHeader)}
	arrayLength, err := decoder.head(4)
	if err != nil || arrayLength != 2 {
		return ErrInvalidIndex
	}
	version, err := decoder.head(0)
	if err != nil || version != SpecificationVersion {
		return ErrInvalidIndex
	}
	count, err := decoder.head(5)
	if err != nil || count > uint64(len(data)) {
		return ErrInvalidIndex
	}
	// Grow only after entries are parsed. An untrusted count must not cause
	// allocation for entries that are absent or invalid.
	next := Index{Entries: []Entry{}}
	for range count {
		entry, decodeErr := decoder.entry()
		if decodeErr != nil {
			return ErrInvalidIndex
		}
		next.Entries = append(next.Entries, entry)
	}
	if decoder.offset != len(data) || next.Validate() != nil {
		return ErrInvalidIndex
	}
	canonical, err := next.MarshalBinary()
	if err != nil || !bytes.Equal(canonical, data) {
		return ErrInvalidIndex
	}
	*index = next
	return nil
}

// ParseIndex parses a canonical ERIS-FS index.
func ParseIndex(data []byte) (Index, error) {
	var index Index
	err := index.UnmarshalBinary(data)
	return index, err
}

type cborDecoder struct {
	data   []byte
	offset int
}

func (decoder *cborDecoder) entry() (Entry, error) {
	pathLength, err := decoder.head(4)
	if err != nil || pathLength == 0 || pathLength > uint64(len(decoder.data)) {
		return Entry{}, ErrInvalidIndex
	}
	var entry Entry
	for range pathLength {
		component, err := decoder.text()
		if err != nil {
			return Entry{}, err
		}
		entry.Path = append(entry.Path, component)
	}
	length, err := decoder.head(4)
	if err != nil || length != 2 {
		return Entry{}, ErrInvalidIndex
	}
	kind, err := decoder.head(0)
	if err != nil || kind > math.MaxUint8 {
		return Entry{}, ErrInvalidIndex
	}
	entry.Type = EntryType(kind)
	if entry.Type == Symlink {
		entry.Target, err = decoder.text()
		return entry, err
	}
	if entry.Type != Regular && entry.Type != Executable && entry.Type != Subdirectory {
		return Entry{}, ErrInvalidIndex
	}
	tag, err := decoder.head(6)
	if err != nil || tag != eris.CapabilityCBORTag {
		return Entry{}, ErrInvalidIndex
	}
	capability, err := decoder.byteString()
	if err != nil || entry.Capability.UnmarshalBinary(capability) != nil {
		return Entry{}, ErrInvalidIndex
	}
	return entry, nil
}

func (decoder *cborDecoder) text() (string, error) {
	data, err := decoder.stringData(3)
	if err != nil || !utf8.Valid(data) {
		return "", ErrInvalidIndex
	}
	return string(data), nil
}

func (decoder *cborDecoder) byteString() ([]byte, error) {
	return decoder.stringData(2)
}

func (decoder *cborDecoder) stringData(major byte) ([]byte, error) {
	length, err := decoder.head(major)
	if err != nil || length > uint64(len(decoder.data)-decoder.offset) {
		return nil, ErrInvalidIndex
	}
	start := decoder.offset
	decoder.offset += int(length)
	return decoder.data[start:decoder.offset], nil
}

func (decoder *cborDecoder) head(major byte) (uint64, error) {
	if decoder.offset >= len(decoder.data) {
		return 0, ErrInvalidIndex
	}
	initial := decoder.data[decoder.offset]
	decoder.offset++
	if initial>>5 != major {
		return 0, ErrInvalidIndex
	}
	additional := initial & 0x1f
	switch {
	case additional < 24:
		return uint64(additional), nil
	case additional == 24:
		return decoder.fixed(1, 24)
	case additional == 25:
		return decoder.fixed(2, 1<<8)
	case additional == 26:
		return decoder.fixed(4, 1<<16)
	case additional == 27:
		return decoder.fixed(8, 1<<32)
	default:
		return 0, ErrInvalidIndex
	}
}

func (decoder *cborDecoder) fixed(size int, minimum uint64) (uint64, error) {
	if len(decoder.data)-decoder.offset < size {
		return 0, ErrInvalidIndex
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
	default:
		return 0, errors.New("eris-fs: invalid CBOR integer size")
	}
	decoder.offset += size
	if value < minimum {
		return 0, ErrInvalidIndex
	}
	return value, nil
}

func appendText(dst []byte, value string) []byte {
	dst = cborwire.AppendHead(dst, 3, uint64(len(value)))
	return append(dst, value...)
}

func appendBytes(dst, value []byte) []byte {
	dst = cborwire.AppendHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
}
