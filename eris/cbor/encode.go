package cbor

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/asciimoth/eris-tools/eris"
)

// Marshal returns a deterministically encoded array with the ERIS CBOR tag.
func Marshal(elements []Element) ([]byte, error) {
	return marshalArray(elements, false, true)
}

// MarshalUntagged returns a deterministically encoded untagged CBOR array.
func MarshalUntagged(elements []Element) ([]byte, error) {
	return marshalArray(elements, false, false)
}

// MarshalSelfDescribed returns a deterministically encoded array with the
// self-described CBOR tag followed by the ERIS CBOR tag.
func MarshalSelfDescribed(elements []Element) ([]byte, error) {
	return marshalArray(elements, true, true)
}

func marshalArray(elements []Element, selfDescribed, tagged bool) ([]byte, error) {
	if err := validateElements(elements); err != nil {
		return nil, err
	}
	var output []byte
	if selfDescribed {
		output = appendHead(output, 6, SelfDescribedTag)
	}
	if tagged {
		output = appendHead(output, 6, Tag)
	}
	output = appendHead(output, 4, uint64(len(elements)))
	for _, element := range elements {
		output = appendElement(output, element)
	}
	return output, nil
}

// MarshalSequence returns a deterministic CBOR sequence. The first item is
// the ERIS CBOR tag applied to null, as required for a tagged sequence.
func MarshalSequence(elements []Element) ([]byte, error) {
	if err := validateElements(elements); err != nil {
		return nil, err
	}
	output := appendHead(nil, 6, Tag)
	output = append(output, 0xf6)
	for _, element := range elements {
		output = appendElement(output, element)
	}
	return output, nil
}

// MarshalUntaggedSequence returns a deterministic CBOR sequence without a
// leading marker.
func MarshalUntaggedSequence(elements []Element) ([]byte, error) {
	if err := validateElements(elements); err != nil {
		return nil, err
	}
	var output []byte
	for _, element := range elements {
		output = appendElement(output, element)
	}
	return output, nil
}

func validateElements(elements []Element) error {
	for index, element := range elements {
		if err := element.validate(); err != nil {
			return fmt.Errorf("%w at index %d: %w", ErrInvalidElement, index, err)
		}
	}
	return nil
}

func appendElement(dst []byte, element Element) []byte {
	switch element.kind {
	case BlockKind:
		return appendByteString(dst, element.block)
	case ReferenceKind:
		return appendByteString(dst, element.reference[:])
	case BlockMapKind:
		return appendBlockMap(dst, element.blockMap)
	case CapabilityKind:
		binaryCapability, _ := element.capability.MarshalBinary()
		dst = appendHead(dst, 6, eris.CapabilityCBORTag)
		return appendByteString(dst, binaryCapability)
	default:
		panic("validated ERIS CBOR element has an unknown kind")
	}
}

func appendBlockMap(dst []byte, blocks eris.BlockSet) []byte {
	references := make([]eris.Reference, 0, len(blocks))
	for reference := range blocks {
		references = append(references, reference)
	}
	slices.SortFunc(references, func(left, right eris.Reference) int {
		return bytes.Compare(left[:], right[:])
	})
	dst = appendHead(dst, 5, uint64(len(references)))
	for _, reference := range references {
		dst = appendByteString(dst, reference[:])
		dst = appendByteString(dst, blocks[reference])
	}
	return dst
}

func appendByteString(dst, value []byte) []byte {
	dst = appendHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
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
