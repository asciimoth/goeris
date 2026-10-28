package eris

import (
	"bytes"
	"testing"
)

func FuzzCapabilityBinary(f *testing.F) {
	valid, _, _ := Encode([]byte("seed"), Secret{}, BlockSize1KiB)
	binary, _ := valid.MarshalBinary()
	f.Add(binary)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		capability, err := ParseCapability(data)
		if err != nil {
			return
		}
		encoded, err := capability.MarshalBinary()
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("binary round trip failed: %v", err)
		}
	})
}

func FuzzCapabilityText(f *testing.F) {
	valid, _, _ := Encode([]byte("seed"), Secret{}, BlockSize32KiB)
	f.Add(valid.String())
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		capability, err := ParseURN(text)
		if err != nil {
			return
		}
		if capability.String() != text {
			t.Fatal("text round trip changed the capability")
		}
	})
}

func FuzzCapabilityCBOR(f *testing.F) {
	valid, _, _ := Encode([]byte("seed"), Secret{}, BlockSize1KiB)
	cbor, _ := valid.MarshalCBOR()
	f.Add(cbor)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var capability Capability
		if err := capability.UnmarshalCBOR(data); err != nil {
			return
		}
		encoded, err := capability.MarshalCBOR()
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("CBOR round trip failed: %v", err)
		}
	})
}

func FuzzReferenceText(f *testing.F) {
	ref := Reference{1, 2, 3}
	f.Add(ref.String())
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		parsed, err := ParseReference(text)
		if err != nil {
			return
		}
		if parsed.String() != text {
			t.Fatal("text round trip changed the reference")
		}
		fromURN, err := ParseReferenceURN(parsed.URN())
		if err != nil || fromURN != parsed {
			t.Fatalf("URN round trip failed: %v", err)
		}
	})
}

func FuzzPadding(f *testing.F) {
	f.Add([]byte(""), uint8(0))
	f.Add([]byte("content"), uint8(1))
	f.Fuzz(func(t *testing.T, content []byte, sizeCode uint8) {
		if len(content) > 1<<20 {
			t.Skip()
		}
		size := BlockSize1KiB
		if sizeCode&1 != 0 {
			size = BlockSize32KiB
		}
		padded, err := Pad(content, size)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Unpad(padded, size)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("padding round trip failed: %v", err)
		}
	})
}

func FuzzUnpad(f *testing.F) {
	f.Add(make([]byte, BlockSize1KiB), uint8(0))
	f.Add([]byte{0x80}, uint8(1))
	f.Fuzz(func(t *testing.T, padded []byte, sizeCode uint8) {
		if len(padded) > 1<<20 {
			t.Skip()
		}
		size := BlockSize1KiB
		if sizeCode&1 != 0 {
			size = BlockSize32KiB
		}
		content, err := Unpad(padded, size)
		if err != nil {
			return
		}
		repadded, err := Pad(content, size)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Unpad(repadded, size)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("successful unpadding did not round trip: %v", err)
		}
	})
}

func FuzzNodeCrypto(f *testing.F) {
	f.Add([]byte("node"), make([]byte, 32), uint8(0), uint8(0))
	f.Fuzz(func(t *testing.T, data, secretBytes []byte, levelByte, sizeCode uint8) {
		size := BlockSize1KiB
		if sizeCode&1 != 0 {
			size = BlockSize32KiB
		}
		node := make([]byte, size)
		copy(node, data)
		var secret Secret
		copy(secret[:], secretBytes)
		block, pair, err := EncryptNode(node, secret, Level(levelByte))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecryptNode(block, pair, Level(levelByte))
		if err != nil || !bytes.Equal(decoded, node) {
			t.Fatalf("node crypto round trip failed: %v", err)
		}
	})
}

func FuzzEncodeDecode(f *testing.F) {
	f.Add([]byte(""), make([]byte, 32), uint8(0))
	f.Add([]byte("content"), []byte{1}, uint8(1))
	f.Fuzz(func(t *testing.T, content, secretBytes []byte, sizeCode uint8) {
		if len(content) > 1<<20 {
			t.Skip()
		}
		size := BlockSize1KiB
		if sizeCode&1 != 0 {
			size = BlockSize32KiB
		}
		var secret Secret
		copy(secret[:], secretBytes)
		capability, blocks, err := Encode(content, secret, size)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(capability, blocks)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("encoding round trip failed: %v", err)
		}
	})
}

func FuzzDecodeMutations(f *testing.F) {
	capability, blocks, _ := Encode(bytes.Repeat([]byte("seed"), 300), Secret{}, BlockSize1KiB)
	root := blocks[capability.Root.Reference]
	f.Add(root)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, candidate []byte) {
		mutated := cloneBlocks(blocks)
		mutated[capability.Root.Reference] = bytes.Clone(candidate)
		decoded, err := Decode(capability, mutated)
		if err == nil && !bytes.Equal(decoded, bytes.Repeat([]byte("seed"), 300)) {
			t.Fatal("accepted mutation changed decoded content")
		}
	})
}
