package eris

import (
	"bytes"
	"encoding"
	"slices"
)

const (
	// CapabilityCBORTag is the registered CBOR tag for an ERIS capability.
	CapabilityCBORTag = 276
	// CapabilityURNPrefix starts a canonical ERIS read-capability URN.
	CapabilityURNPrefix = "urn:eris:"
	// CapabilityTextSize is the length of a canonical capability URN.
	CapabilityTextSize = len(CapabilityURNPrefix) + 106
	// CapabilityCBORSize is the length of a tagged CBOR capability.
	CapabilityCBORSize = 71
)

var capabilityCBORHeader = [5]byte{0xd9, 0x01, 0x14, 0x58, 0x42}

// Capability contains all information required to decode ERIS content.
type Capability struct {
	BlockSize BlockSize
	Level     Level
	Root      Pair
}

// Validate checks the fields that have constraints in the capability format.
func (c Capability) Validate() error {
	if !c.BlockSize.Valid() {
		return ErrInvalidCapability
	}
	return nil
}

// MarshalBinary returns the 66-byte ERIS capability encoding.
func (c Capability) MarshalBinary() ([]byte, error) {
	return c.appendBinary(nil)
}

func (c Capability) appendBinary(dst []byte) ([]byte, error) {
	code, err := c.BlockSize.code()
	if err != nil {
		return nil, ErrInvalidCapability
	}
	dst = slices.Grow(dst, CapabilitySize)
	dst = append(dst, code, byte(c.Level))
	dst = append(dst, c.Root.Reference[:]...)
	return append(dst, c.Root.Key[:]...), nil
}

// UnmarshalBinary parses a 66-byte ERIS capability encoding.
func (c *Capability) UnmarshalBinary(data []byte) error {
	if c == nil || len(data) != CapabilitySize {
		return ErrInvalidCapability
	}
	size, err := blockSizeFromCode(data[0])
	if err != nil {
		return err
	}
	var next Capability
	next.BlockSize = size
	next.Level = Level(data[1])
	copy(next.Root.Reference[:], data[2:34])
	copy(next.Root.Key[:], data[34:])
	*c = next
	return nil
}

// ParseCapability parses a binary ERIS capability.
func ParseCapability(data []byte) (Capability, error) {
	var capability Capability
	err := capability.UnmarshalBinary(data)
	return capability, err
}

// MarshalText returns the canonical ERIS capability URN.
func (c Capability) MarshalText() ([]byte, error) {
	return c.appendText(nil)
}

func (c Capability) appendText(dst []byte) ([]byte, error) {
	var buffer [CapabilitySize]byte
	binary, err := c.appendBinary(buffer[:0])
	if err != nil {
		return nil, err
	}
	dst = slices.Grow(dst, CapabilityTextSize)
	dst = append(dst, CapabilityURNPrefix...)
	return base32Encoding.AppendEncode(dst, binary), nil
}

// UnmarshalText parses a canonical ERIS capability URN.
func (c *Capability) UnmarshalText(text []byte) error {
	if c == nil || len(text) != CapabilityTextSize || !bytes.HasPrefix(text, []byte(CapabilityURNPrefix)) {
		return ErrInvalidCapability
	}
	text = text[len(CapabilityURNPrefix):]
	var binary [CapabilitySize]byte
	var canonical [CapabilityTextSize - len(CapabilityURNPrefix)]byte
	n, err := base32Encoding.Decode(binary[:], text)
	if err != nil || n != len(binary) {
		return ErrInvalidCapability
	}
	base32Encoding.Encode(canonical[:], binary[:])
	if !bytes.Equal(canonical[:], text) {
		return ErrInvalidCapability
	}
	return c.UnmarshalBinary(binary[:])
}

// ParseURN parses a canonical ERIS capability URN.
func ParseURN(urn string) (Capability, error) {
	var capability Capability
	err := capability.UnmarshalText([]byte(urn))
	return capability, err
}

// String returns the canonical capability URN. It returns an empty string if
// the capability has an invalid block size.
func (c Capability) String() string {
	var buffer [CapabilityTextSize]byte
	text, err := c.appendText(buffer[:0])
	if err != nil {
		return ""
	}
	return string(text)
}

// MarshalCBOR returns tag 276 containing the binary capability.
func (c Capability) MarshalCBOR() ([]byte, error) {
	// The output grows beyond the header array, so each call gets its own bytes.
	return c.appendBinary(capabilityCBORHeader[:])
}

// UnmarshalCBOR parses tag 276 containing a binary capability.
func (c *Capability) UnmarshalCBOR(data []byte) error {
	if len(data) != CapabilityCBORSize || !bytes.Equal(data[:len(capabilityCBORHeader)], capabilityCBORHeader[:]) {
		return ErrInvalidCapability
	}
	return c.UnmarshalBinary(data[len(capabilityCBORHeader):])
}

var (
	_ encoding.BinaryMarshaler   = Capability{}
	_ encoding.BinaryUnmarshaler = (*Capability)(nil)
	_ encoding.TextMarshaler     = Capability{}
	_ encoding.TextUnmarshaler   = (*Capability)(nil)
)
