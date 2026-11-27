package fs

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

// HasIndexHeader reports whether data starts with the five-byte ERIS-FS CBOR
// tag. It detects a possible image but does not validate the remaining data.
func HasIndexHeader(data []byte) bool {
	return len(data) >= len(MagicPrefix) && string(data[:len(MagicPrefix)]) == MagicPrefix
}

// MarshalBinary returns the version 2 representation in RFC 8949 section
// 4.2.1 bytewise map order. Files with an unknown size return ErrUnknownSize.
func (index Index) MarshalBinary() ([]byte, error) {
	entries := make([]*Entry, len(index.Entries))
	uniformDepth := true
	unknownSize := false
	// Estimate with one-byte path and size headers. Larger CBOR headers
	// can still grow the buffer.
	capacity := len(MagicPrefix) + 2 + 9 // Outer array, version, and largest map header.
	for i := range entries {
		entry := &index.Entries[i]
		if err := validateEntry(*entry); err != nil {
			return nil, err
		}
		uniformDepth = uniformDepth && len(entry.Path) == len(index.Entries[0].Path)
		unknownSize = unknownSize || entry.Size < 0
		for _, component := range entry.Path {
			capacity += 1 + len(component)
		}
		if entry.Type == Symlink {
			capacity += 5 + len(entry.Target)
		} else {
			capacity += 9 + eris.CapabilitySize
		}
		entries[i] = entry
	}
	slices.SortFunc(entries, func(a, b *Entry) int { return compareWirePaths(a.Path, b.Path) })
	if uniformDepth {
		// At equal depth only identical paths conflict. Wire order puts them together.
		for i := 1; i < len(entries); i++ {
			if slices.Equal(entries[i-1].Path, entries[i].Path) {
				return nil, ErrPathConflict
			}
		}
	} else {
		paths := make([][]string, len(entries))
		for i := range index.Entries {
			paths[i] = index.Entries[i].Path
		}
		if err := checkPathConflicts(paths); err != nil {
			return nil, err
		}
	}
	if unknownSize {
		return nil, ErrUnknownSize
	}
	out := append(make([]byte, 0, capacity), MagicPrefix...)
	out = cborwire.AppendHead(out, 4, 2)
	out = cborwire.AppendHead(out, 0, SpecificationVersion)
	out = cborwire.AppendHead(out, 5, uint64(len(entries)))
	for _, entry := range entries {
		out = cborwire.AppendHead(out, 4, uint64(len(entry.Path)))
		for _, component := range entry.Path {
			out = appendText(out, component)
		}
		fields := uint64(2)
		if entry.Type == Regular || entry.Type == Executable {
			fields = 3
		}
		out = cborwire.AppendHead(out, 4, fields)
		out = cborwire.AppendHead(out, 0, uint64(entry.Type))
		if entry.Type == Symlink {
			parts := uint64(strings.Count(entry.Target, "/")) + 1
			out = cborwire.AppendHead(out, 4, parts)
			for part := range strings.SplitSeq(entry.Target, "/") {
				out = appendText(out, part)
			}
		} else {
			capability, _ := entry.Capability.MarshalBinary()
			out = cborwire.AppendHead(out, 6, eris.CapabilityCBORTag)
			out = appendBytes(out, capability)
			if fields == 3 {
				if entry.Compression == Lzip {
					out = cborwire.AppendHead(out, 6, LzipLengthTag)
				}
				out = cborwire.AppendHead(out, 0, uint64(entry.Size))
			}
		}
	}
	return out, nil
}

// Preferred CBOR headers sort by increasing length for a fixed major type.
// Compare the array length, then each text length and its UTF-8 bytes.
func compareWirePaths(a, b []string) int {
	if order := cmp.Compare(len(a), len(b)); order != 0 {
		return order
	}
	for i, component := range a {
		if order := cmp.Compare(len(component), len(b[i])); order != 0 {
			return order
		}
		if order := strings.Compare(component, b[i]); order != 0 {
			return order
		}
	}
	return 0
}

// UnmarshalBinary parses a deterministic ERIS-FS version 1 or 2 index.
// Version 1 also accepts slash-separated text paths used by eris-go, and the
// text targets and length-first key order written by older releases.
// Version 1 file sizes are set to -1.
// The receiver is not changed on failure.
func (index *Index) UnmarshalBinary(data []byte) error {
	if index == nil || !HasIndexHeader(data) {
		return ErrInvalidIndex
	}
	decoder := cborDecoder{data: data, offset: len(MagicPrefix)}
	arrayLength, err := decoder.head(4)
	if err != nil || arrayLength != 2 {
		return ErrInvalidIndex
	}
	version, err := decoder.head(0)
	if err != nil || (version != LegacySpecificationVersion && version != SpecificationVersion) {
		return ErrInvalidIndex
	}
	count, err := decoder.head(5)
	if err != nil || count > uint64(len(data)) {
		return ErrInvalidIndex
	}
	// Grow only after entries are parsed. An untrusted count must not cause
	// allocation for entries that are absent or invalid.
	next := Index{Entries: []Entry{}}
	var previous []byte
	bytewise, lengthFirst := true, true
	for range count {
		start := decoder.offset
		entry, keyEnd, decodeErr := decoder.entry(version)
		if decodeErr != nil {
			return ErrInvalidIndex
		}
		key := data[start:keyEnd]
		if previous != nil {
			order := bytes.Compare(previous, key)
			bytewise = bytewise && order < 0
			lengthFirst = lengthFirst && (len(previous) < len(key) || (len(previous) == len(key) && order < 0))
			// Stop as soon as no permitted ordering can accept this map.
			// Otherwise repeated keys can allocate entries for the entire
			// input before validation rejects the duplicate paths.
			if !bytewise && (version != LegacySpecificationVersion || !lengthFirst) {
				return ErrInvalidIndex
			}
		}
		previous = key
		next.Entries = append(next.Entries, entry)
	}
	if decoder.offset != len(data) || next.Validate() != nil {
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

func (decoder *cborDecoder) entry(version uint64) (Entry, int, error) {
	var entry Entry
	if version == LegacySpecificationVersion && decoder.offset < len(decoder.data) && decoder.data[decoder.offset]>>5 == 3 {
		name, err := decoder.text()
		if err != nil {
			return Entry{}, 0, err
		}
		entry.Path, err = splitPath(name)
		if err != nil {
			return Entry{}, 0, ErrInvalidIndex
		}
	} else {
		pathLength, err := decoder.head(4)
		if err != nil || pathLength == 0 || pathLength > uint64(len(decoder.data)) {
			return Entry{}, 0, ErrInvalidIndex
		}
		for range pathLength {
			component, err := decoder.text()
			if err != nil {
				return Entry{}, 0, err
			}
			// Reject invalid components before allocating the rest of an
			// untrusted path array. Validate uses the same component rules.
			if !validPathComponent(component) {
				return Entry{}, 0, ErrInvalidIndex
			}
			entry.Path = append(entry.Path, component)
		}
	}
	keyEnd := decoder.offset
	length, err := decoder.head(4)
	if err != nil {
		return Entry{}, 0, ErrInvalidIndex
	}
	kind, err := decoder.head(0)
	if err != nil || kind > math.MaxUint8 {
		return Entry{}, 0, ErrInvalidIndex
	}
	entry.Type = EntryType(kind)
	fields := uint64(2)
	if version == 2 && (entry.Type == Regular || entry.Type == Executable) {
		fields = 3
	}
	if length != fields {
		return Entry{}, 0, ErrInvalidIndex
	}
	if entry.Type == Symlink {
		if version == LegacySpecificationVersion && decoder.offset < len(decoder.data) && decoder.data[decoder.offset]>>5 == 3 {
			entry.Target, err = decoder.text()
		} else {
			entry.Target, err = decoder.target()
		}
		return entry, keyEnd, err
	}
	if entry.Type != Regular && entry.Type != Executable && entry.Type != Subdirectory {
		return Entry{}, 0, ErrInvalidIndex
	}
	tag, err := decoder.head(6)
	if err != nil || tag != eris.CapabilityCBORTag {
		return Entry{}, 0, ErrInvalidIndex
	}
	capability, err := decoder.byteString()
	if err != nil || entry.Capability.UnmarshalBinary(capability) != nil {
		return Entry{}, 0, ErrInvalidIndex
	}
	if entry.Type == Regular || entry.Type == Executable {
		entry.Size = -1
		if version == 2 {
			if decoder.offset < len(decoder.data) && decoder.data[decoder.offset]>>5 == 6 {
				tag, err = decoder.head(6)
				if err != nil || tag != LzipLengthTag {
					return Entry{}, 0, ErrInvalidIndex
				}
				entry.Compression = Lzip
			}
			size, err := decoder.head(0)
			if err != nil || size > math.MaxInt64 {
				return Entry{}, 0, ErrInvalidIndex
			}
			entry.Size = int64(size)
		}
	}
	return entry, keyEnd, nil
}

func (decoder *cborDecoder) target() (string, error) {
	count, err := decoder.head(4)
	if err != nil || count > uint64(len(decoder.data)-decoder.offset) {
		return "", ErrInvalidIndex
	}
	var target strings.Builder
	for i := uint64(0); i < count; i++ {
		part, err := decoder.textBytes()
		if err != nil || bytes.IndexByte(part, '/') >= 0 {
			return "", ErrInvalidIndex
		}
		if i != 0 {
			target.WriteByte('/')
		}
		target.Write(part)
	}
	return target.String(), nil
}

func (decoder *cborDecoder) text() (string, error) {
	data, err := decoder.textBytes()
	return string(data), err
}

func (decoder *cborDecoder) textBytes() ([]byte, error) {
	data, err := decoder.stringData(3)
	if err != nil || !utf8.Valid(data) {
		return nil, ErrInvalidIndex
	}
	return data, nil
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
