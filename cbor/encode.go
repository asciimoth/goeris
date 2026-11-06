package cbor

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
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
		output = cborwire.AppendHead(output, 6, SelfDescribedTag)
	}
	if tagged {
		output = cborwire.AppendHead(output, 6, Tag)
	}
	output = cborwire.AppendHead(output, 4, uint64(len(elements)))
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
	output := cborwire.AppendHead(nil, 6, Tag)
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
		dst = cborwire.AppendHead(dst, 6, eris.CapabilityCBORTag)
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
	dst = cborwire.AppendHead(dst, 5, uint64(len(references)))
	for _, reference := range references {
		dst = appendByteString(dst, reference[:])
		dst = appendByteString(dst, blocks[reference])
	}
	return dst
}

func appendByteString(dst, value []byte) []byte {
	dst = cborwire.AppendHead(dst, 2, uint64(len(value)))
	return append(dst, value...)
}
