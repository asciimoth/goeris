package eris

import (
	"bytes"
	"encoding"
)

const (
	// CapabilityCBORTag is the registered CBOR tag for an ERIS capability.
	CapabilityCBORTag = 276

	capabilityURNPrefix = "urn:eris:"
	capabilityTextSize  = 115
	capabilityCBORSize  = 71
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
	code, err := c.BlockSize.code()
	if err != nil {
		return nil, ErrInvalidCapability
	}
	out := make([]byte, CapabilitySize)
	out[0] = code
	out[1] = byte(c.Level)
	copy(out[2:34], c.Root.Reference[:])
	copy(out[34:], c.Root.Key[:])
	return out, nil
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
	binary, err := c.MarshalBinary()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, capabilityTextSize)
	out = append(out, capabilityURNPrefix...)
	out = base32Encoding.AppendEncode(out, binary)
	return out, nil
}

// UnmarshalText parses a canonical ERIS capability URN.
func (c *Capability) UnmarshalText(text []byte) error {
	if c == nil || len(text) != capabilityTextSize || !bytes.HasPrefix(text, []byte(capabilityURNPrefix)) {
		return ErrInvalidCapability
	}
	binary, err := base32Encoding.DecodeString(string(text[len(capabilityURNPrefix):]))
	if err != nil || len(binary) != CapabilitySize ||
		base32Encoding.EncodeToString(binary) != string(text[len(capabilityURNPrefix):]) {
		return ErrInvalidCapability
	}
	return c.UnmarshalBinary(binary)
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
	text, err := c.MarshalText()
	if err != nil {
		return ""
	}
	return string(text)
}

// MarshalCBOR returns tag 276 containing the binary capability.
func (c Capability) MarshalCBOR() ([]byte, error) {
	binary, err := c.MarshalBinary()
	if err != nil {
		return nil, err
	}
	out := make([]byte, capabilityCBORSize)
	copy(out, capabilityCBORHeader[:])
	copy(out[len(capabilityCBORHeader):], binary)
	return out, nil
}

// UnmarshalCBOR parses tag 276 containing a binary capability.
func (c *Capability) UnmarshalCBOR(data []byte) error {
	if len(data) != capabilityCBORSize || !bytes.Equal(data[:len(capabilityCBORHeader)], capabilityCBORHeader[:]) {
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
