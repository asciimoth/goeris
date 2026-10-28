package cbor

import (
	"bytes"
	"testing"

	"github.com/asciimoth/eris-tools/eris"
)

func FuzzUnmarshal(f *testing.F) {
	block, pair, _ := eris.EncryptNode(make([]byte, eris.BlockSize1KiB), eris.Secret{}, 0)
	element, _ := NewBlockMap(eris.BlockSet{pair.Reference: block})
	valid, _ := Marshal([]Element{element})
	selfDescribed, _ := MarshalSelfDescribed([]Element{element})
	untagged, _ := MarshalUntagged([]Element{element})
	f.Add(valid)
	f.Add(selfDescribed)
	f.Add(untagged)
	f.Add([]byte{0x9f, 0xff})
	f.Add([]byte(nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		elements, err := Unmarshal(data)
		if err != nil {
			return
		}
		encoded, err := Marshal(elements)
		if err != nil {
			t.Fatalf("Marshal after successful Unmarshal: %v", err)
		}
		roundTrip, err := Unmarshal(encoded)
		if err != nil {
			t.Fatalf("Unmarshal canonical output: %v", err)
		}
		assertElementsEqual(t, roundTrip, elements)
	})
}

func FuzzUnmarshalSequence(f *testing.F) {
	capability, _, _ := eris.Encode([]byte("fuzz"), eris.Secret{}, eris.BlockSize1KiB)
	element, _ := NewCapability(capability)
	valid, _ := MarshalSequence([]Element{element})
	untagged, _ := MarshalUntaggedSequence([]Element{element})
	f.Add(valid)
	f.Add(untagged)
	f.Add([]byte(nil))
	f.Add([]byte{0xda, 0x65, 0x72, 0x69, 0x73, 0xf6})
	f.Fuzz(func(t *testing.T, data []byte) {
		elements, err := UnmarshalSequence(data)
		if err != nil {
			return
		}
		encoded, err := MarshalSequence(elements)
		if err != nil {
			t.Fatalf("MarshalSequence after successful decode: %v", err)
		}
		roundTrip, err := UnmarshalSequence(encoded)
		if err != nil {
			t.Fatalf("UnmarshalSequence canonical output: %v", err)
		}
		assertElementsEqual(t, roundTrip, elements)
	})
}

func FuzzBlocks(f *testing.F) {
	f.Add(uint8(0), []byte("seed"))
	f.Add(uint8(1), []byte(nil))
	f.Fuzz(func(t *testing.T, large uint8, data []byte) {
		size := eris.BlockSize1KiB
		if large&1 != 0 {
			size = eris.BlockSize32KiB
		}
		node := make([]byte, size)
		for offset := 0; offset < len(node); offset += max(1, len(data)) {
			copy(node[offset:], data)
		}
		block, pair, err := eris.EncryptNode(node, eris.Secret{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		blockElement, err := NewBlock(block)
		if err != nil {
			t.Fatal(err)
		}
		mapElement, err := NewBlockMap(eris.BlockSet{pair.Reference: block})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := Marshal([]Element{blockElement, mapElement})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Unmarshal(encoded)
		if err != nil || len(decoded) != 2 {
			t.Fatalf("block round trip = (%d elements, %v)", len(decoded), err)
		}
		got, _ := decoded[0].Block()
		if !bytes.Equal(got, block) {
			t.Fatal("block changed during round trip")
		}
	})
}
