package cbor

import (
	"errors"
	"fmt"

	"github.com/asciimoth/goeris"
)

var (
	// ErrInvalidEncoding means that input is not an ERIS CBOR serialization.
	ErrInvalidEncoding = errors.New("eris-cbor: invalid encoding")
	// ErrInvalidElement means that an element has invalid or inconsistent data.
	ErrInvalidElement = errors.New("eris-cbor: invalid element")
)

// Kind identifies the value stored in an Element.
type Kind uint8

const (
	// BlockKind identifies a 1 KiB or 32 KiB encrypted ERIS block.
	BlockKind Kind = iota + 1
	// ReferenceKind identifies a 32-byte ERIS block reference.
	ReferenceKind
	// BlockMapKind identifies a map from references to encrypted ERIS blocks.
	BlockMapKind
	// CapabilityKind identifies an ERIS read capability.
	CapabilityKind
)

// String returns the name of a kind.
func (kind Kind) String() string {
	switch kind {
	case BlockKind:
		return "block"
	case ReferenceKind:
		return "reference"
	case BlockMapKind:
		return "block map"
	case CapabilityKind:
		return "capability"
	default:
		return "unknown"
	}
}

// Element is one ERIS CBOR array or sequence element. Use the constructors to
// create an element. Accessors return false if the element has another kind.
// Byte slices and block maps are copied at the package boundary.
type Element struct {
	kind       Kind
	block      []byte
	reference  eris.Reference
	blockMap   eris.BlockSet
	capability eris.Capability
}

// NewBlock creates a block element.
func NewBlock(block []byte) (Element, error) {
	if _, err := eris.ReferenceOf(block); err != nil {
		return Element{}, fmt.Errorf("%w: %w", ErrInvalidElement, err)
	}
	return Element{kind: BlockKind, block: cloneBytes(block)}, nil
}

// NewReference creates a reference element.
func NewReference(reference eris.Reference) Element {
	return Element{kind: ReferenceKind, reference: reference}
}

// NewBlockMap creates a block-map element. It verifies that each reference
// identifies its block.
func NewBlockMap(blocks eris.BlockSet) (Element, error) {
	cloned := make(eris.BlockSet, len(blocks))
	for reference, block := range blocks {
		if err := eris.VerifyBlock(block, reference); err != nil {
			return Element{}, fmt.Errorf("%w: %w", ErrInvalidElement, err)
		}
		cloned[reference] = cloneBytes(block)
	}
	return Element{kind: BlockMapKind, blockMap: cloned}, nil
}

// NewCapability creates a read-capability element.
func NewCapability(capability eris.Capability) (Element, error) {
	if err := capability.Validate(); err != nil {
		return Element{}, fmt.Errorf("%w: %w", ErrInvalidElement, err)
	}
	return Element{kind: CapabilityKind, capability: capability}, nil
}

// Kind returns the element kind. The zero Element has an unknown kind.
func (element Element) Kind() Kind {
	return element.kind
}

// Block returns a copy of the block and true for a block element.
func (element Element) Block() ([]byte, bool) {
	if element.kind != BlockKind {
		return nil, false
	}
	return cloneBytes(element.block), true
}

// Reference returns the reference and true for a reference element.
func (element Element) Reference() (eris.Reference, bool) {
	return element.reference, element.kind == ReferenceKind
}

// BlockMap returns a copy of the map and true for a block-map element.
func (element Element) BlockMap() (eris.BlockSet, bool) {
	if element.kind != BlockMapKind {
		return nil, false
	}
	cloned := make(eris.BlockSet, len(element.blockMap))
	for reference, block := range element.blockMap {
		cloned[reference] = cloneBytes(block)
	}
	return cloned, true
}

// Capability returns the capability and true for a capability element.
func (element Element) Capability() (eris.Capability, bool) {
	return element.capability, element.kind == CapabilityKind
}

func (element Element) validate() error {
	switch element.kind {
	case BlockKind:
		_, err := eris.ReferenceOf(element.block)
		return err
	case ReferenceKind:
		return nil
	case BlockMapKind:
		for reference, block := range element.blockMap {
			if err := eris.VerifyBlock(block, reference); err != nil {
				return err
			}
		}
		return nil
	case CapabilityKind:
		return element.capability.Validate()
	default:
		return ErrInvalidElement
	}
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}
