package eris

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"testing"
)

func FuzzBlockSetPut(f *testing.F) {
	block := make([]byte, BlockSize1KiB)
	ref, _ := ReferenceOf(block)
	f.Add(block, false, []byte(nil))
	f.Add(block, true, ref[:])
	f.Add([]byte{1}, false, []byte(nil))
	f.Fuzz(func(t *testing.T, block []byte, check bool, reference []byte) {
		if len(block) > int(BlockSize32KiB) {
			t.Skip()
		}
		store := BlockSet{Blocks: make(map[Reference]Block)}
		var expected []Reference
		if check {
			var ref Reference
			copy(ref[:], reference)
			expected = append(expected, ref)
		}
		got, err := PutBlock(context.Background(), store, block, 0, BlockOptions{}, expected...)
		actual, sizeErr := ReferenceOf(block)
		if sizeErr != nil {
			if !errors.Is(err, ErrInvalidBlockSize) || len(store.Blocks) != 0 {
				t.Fatalf("invalid size: ref %v, error %v, stored %d", got, err, len(store.Blocks))
			}
			return
		}
		if check && expected[0] != actual {
			if !errors.Is(err, ErrInvalidBlock) || len(store.Blocks) != 0 {
				t.Fatalf("mismatch: ref %v, error %v, stored %d", got, err, len(store.Blocks))
			}
			return
		}
		stored, readErr, _ := GetBlock(context.Background(), store, actual, 0, BlockOptions{})
		if err != nil || readErr != nil || got != actual || !bytes.Equal(stored, block) {
			t.Fatalf("stored block mismatch: ref %v, write %v, read %v", got, err, readErr)
		}
	})
}

func FuzzCapabilityBinary(f *testing.F) {
	valid := Capability{BlockSize: BlockSize1KiB, Root: Pair{Reference: Reference{1}, Key: Key{2}}}
	binary, err := valid.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
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
	valid := Capability{BlockSize: BlockSize32KiB, Root: Pair{Reference: Reference{1}, Key: Key{2}}}
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
	valid := Capability{BlockSize: BlockSize1KiB, Root: Pair{Reference: Reference{1}, Key: Key{2}}}
	cbor, err := valid.MarshalCBOR()
	if err != nil {
		f.Fatal(err)
	}
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
	padded, _ := Pad([]byte("content"), BlockSize1KiB)
	f.Add(padded, uint8(0))
	f.Add(append([]byte{0}, padded...), uint8(0))
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
		if !bytes.Equal(repadded, padded) {
			t.Fatal("accepted padding is not canonical")
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
	for _, length := range []int{1023, 1024, 16383, 16384, 32768} {
		f.Add(deterministicBytes(length), []byte{1, 2, 3}, uint8(36))
	}
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
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, secret, size, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := NewBlockCapabilityStore(blocks).Decode(t.Context(), capability, BlockOptions{})
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("encoding round trip failed: %v", err)
		}
		// The streaming APIs must produce the same complete tree even when
		// input arrives in small chunks and the final read includes EOF.
		input := smallChunkReader{Reader: &terminalEOFReader{data: content}, max: int(sizeCode) + 1}
		streamed := BlockSet{Blocks: make(map[Reference]Block)}
		streamCap, err := NewBlockCapabilityStore(streamed).EncodeContent(context.Background(), input, secret, size, BlockOptions{})
		if err != nil || streamCap != capability || !maps.EqualFunc(streamed.Blocks, blocks.Blocks, equalBlocks) {
			t.Fatalf("streaming encoding differs: %v", err)
		}
		blocks = BlockSet{Blocks: make(map[Reference]Block)}
		capability, err = NewBlockCapabilityStore(blocks).EncodeRecommended(t.Context(), content, secret, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		input.Reader = &terminalEOFReader{data: content}
		streamed = BlockSet{Blocks: make(map[Reference]Block)}
		streamCap, err = NewBlockCapabilityStore(streamed).EncodeContentRecommended(context.Background(), input, secret, BlockOptions{})
		if err != nil || streamCap != capability || !maps.EqualFunc(streamed.Blocks, blocks.Blocks, equalBlocks) {
			t.Fatalf("recommended streaming encoding differs: %v", err)
		}
	})
}

func FuzzDecodeMutations(f *testing.F) {
	content := bytes.Repeat([]byte("seed"), 300)
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	capability, err := NewBlockCapabilityStore(blocks).Encode(f.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		f.Fatal(err)
	}
	root := blocks.Blocks[capability.Root.Reference]
	f.Add(root.Data)
	changed := bytes.Clone(root.Data)
	changed[0] ^= 1
	f.Add(changed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, candidate []byte) {
		mutated := cloneBlocks(blocks)
		mutated.Blocks[capability.Root.Reference] = Block{Data: bytes.Clone(candidate)}
		decoded, err := NewBlockCapabilityStore(mutated).Decode(t.Context(), capability, BlockOptions{})
		if bytes.Equal(candidate, root.Data) {
			if err != nil || !bytes.Equal(decoded, content) {
				t.Fatalf("original root decoded to %d bytes: %v", len(decoded), err)
			}
		} else if !errors.Is(err, ErrInvalidBlock) {
			t.Fatalf("changed root: got %v, want ErrInvalidBlock", err)
		}
	})
}

func equalBlocks(a, b Block) bool { return bytes.Equal(a.Data, b.Data) && a.Options == b.Options }
