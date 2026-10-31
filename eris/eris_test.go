package eris

import (
	"bytes"
	"errors"
	"testing"
)

func TestBlockSize(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		size  BlockSize
		valid bool
		arity int
	}{
		{BlockSize1KiB, true, 16},
		{BlockSize32KiB, true, 512},
		{12, false, 0},
	} {
		if got := test.size.Valid(); got != test.valid {
			t.Errorf("BlockSize(%d).Valid() = %t, want %t", test.size, got, test.valid)
		}
		arity, err := test.size.Arity()
		if arity != test.arity || (err == nil) != test.valid {
			t.Errorf("BlockSize(%d).Arity() = (%d, %v)", test.size, arity, err)
		}
	}

	if ChooseBlockSize((1<<14)-1) != BlockSize1KiB {
		t.Error("small content did not select 1 KiB")
	}
	if ChooseBlockSize(1<<14) != BlockSize32KiB {
		t.Error("large content did not select 32 KiB")
	}
}

func TestBlockSetStorage(t *testing.T) {
	t.Parallel()
	node := make([]byte, BlockSize1KiB)
	block, pair, _ := EncryptNode(node, Secret{}, 0)
	blocks := make(BlockSet)
	if ref, err := blocks.PutBlock(block); err != nil || ref != pair.Reference {
		t.Fatalf("PutBlock() = %v, %v", ref, err)
	}
	block[0] ^= 1
	got, err := blocks.GetBlock(pair.Reference)
	if err != nil || got[0] == block[0] {
		t.Fatal("block set did not copy input")
	}
	got[0] ^= 1
	again, _ := blocks.GetBlock(pair.Reference)
	if got[0] == again[0] {
		t.Fatal("block set did not copy output")
	}
	if _, err = blocks.GetBlock(Reference{1}); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing error = %v", err)
	}
	if _, err = (BlockSet(nil)).PutBlock(again, pair.Reference); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("nil set error = %v", err)
	}
	again[0] ^= 1
	if _, err = blocks.PutBlock(again, pair.Reference); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("invalid block error = %v", err)
	}
	if _, err = blocks.PutBlock(block, pair.Reference); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("mismatched reference error = %v", err)
	}
	if _, err = blocks.PutBlock(nil); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid size error = %v", err)
	}
	if _, err = blocks.PutBlock(again, pair.Reference, pair.Reference); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("multiple references error = %v", err)
	}
	blocks[pair.Reference][0] ^= 1
	if _, err = blocks.GetBlock(pair.Reference); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("corrupt stored block error = %v", err)
	}
}

func TestDecryptNodeFromSourceRejectsBadSize(t *testing.T) {
	t.Parallel()
	if _, err := DecryptNodeFromSource([]byte{1}, Key{}, 0); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("invalid size error = %v", err)
	}
}

func TestReferenceEncoding(t *testing.T) {
	t.Parallel()

	var want Reference
	for i := range want {
		want[i] = byte(i)
	}
	text := want.String()
	got, err := ParseReference(text)
	if err != nil || got != want {
		t.Fatalf("ParseReference() = (%v, %v), want (%v, nil)", got, err, want)
	}
	got, err = ParseReferenceURN(want.URN())
	if err != nil || got != want {
		t.Fatalf("ParseReferenceURN() = (%v, %v), want (%v, nil)", got, err, want)
	}

	for _, invalid := range []string{"", text[:51], "!" + text[1:], "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := ParseReference(invalid); !errors.Is(err, ErrInvalidBlock) {
			t.Errorf("ParseReference(%q) error = %v", invalid, err)
		}
	}
	for _, invalid := range []string{"", "urn:sha256:" + text, "urn:blake2b:" + text[:51]} {
		if _, err := ParseReferenceURN(invalid); !errors.Is(err, ErrInvalidBlock) {
			t.Errorf("ParseReferenceURN(%q) error = %v", invalid, err)
		}
	}
}

func TestCapabilityEncoding(t *testing.T) {
	t.Parallel()

	capability := Capability{BlockSize: BlockSize1KiB, Level: 7}
	for i := range capability.Root.Reference {
		capability.Root.Reference[i] = byte(i)
		capability.Root.Key[i] = byte(255 - i)
	}

	binary, err := capability.MarshalBinary()
	if err != nil || len(binary) != CapabilitySize || binary[0] != 0x0a || binary[1] != 7 {
		t.Fatalf("MarshalBinary() returned invalid data: %x, %v", binary, err)
	}
	parsed, err := ParseCapability(binary)
	if err != nil || parsed != capability {
		t.Fatalf("ParseCapability() = (%v, %v)", parsed, err)
	}

	text, err := capability.MarshalText()
	if err != nil || string(text) != capability.String() {
		t.Fatalf("MarshalText() = (%q, %v)", text, err)
	}
	parsed, err = ParseURN(string(text))
	if err != nil || parsed != capability {
		t.Fatalf("ParseURN() = (%v, %v)", parsed, err)
	}

	cbor, err := capability.MarshalCBOR()
	if err != nil || len(cbor) != capabilityCBORSize {
		t.Fatalf("MarshalCBOR() = (%x, %v)", cbor, err)
	}
	var fromCBOR Capability
	if err := fromCBOR.UnmarshalCBOR(cbor); err != nil || fromCBOR != capability {
		t.Fatalf("UnmarshalCBOR() = (%v, %v)", fromCBOR, err)
	}

	capability.BlockSize = BlockSize32KiB
	binary, err = capability.MarshalBinary()
	if err != nil || binary[0] != 0x0f {
		t.Fatalf("32 KiB MarshalBinary() = (%x, %v)", binary, err)
	}
	parsed, err = ParseCapability(binary)
	if err != nil || parsed != capability {
		t.Fatalf("32 KiB ParseCapability() = (%v, %v)", parsed, err)
	}
}

func TestCapabilityRejectsInvalidEncoding(t *testing.T) {
	t.Parallel()

	invalid := Capability{BlockSize: 3}
	if !errors.Is(invalid.Validate(), ErrInvalidCapability) || invalid.String() != "" {
		t.Error("invalid capability was accepted")
	}
	if _, err := invalid.MarshalBinary(); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("MarshalBinary() error = %v", err)
	}
	if _, err := invalid.MarshalText(); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("MarshalText() error = %v", err)
	}
	if _, err := invalid.MarshalCBOR(); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("MarshalCBOR() error = %v", err)
	}

	var capability Capability
	if err := capability.UnmarshalBinary(nil); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("UnmarshalBinary(nil) error = %v", err)
	}
	if err := (*Capability)(nil).UnmarshalBinary(make([]byte, CapabilitySize)); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("nil UnmarshalBinary() error = %v", err)
	}
	badBinary := make([]byte, CapabilitySize)
	badBinary[0] = 1
	if err := capability.UnmarshalBinary(badBinary); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("UnmarshalBinary(bad size) error = %v", err)
	}

	valid, _, err := Encode(nil, Secret{}, BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	validText, _ := valid.MarshalText()
	for _, text := range [][]byte{
		nil,
		append([]byte("URN:eris:"), validText[len(capabilityURNPrefix):]...),
		append([]byte(capabilityURNPrefix), bytes.Repeat([]byte{'!'}, 106)...),
		append([]byte(capabilityURNPrefix), bytes.Repeat([]byte{'a'}, 106)...),
	} {
		if err := capability.UnmarshalText(text); !errors.Is(err, ErrInvalidCapability) {
			t.Errorf("UnmarshalText(%q) error = %v", text, err)
		}
	}
	if err := (*Capability)(nil).UnmarshalText(validText); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("nil UnmarshalText() error = %v", err)
	}

	validCBOR, _ := valid.MarshalCBOR()
	if err := capability.UnmarshalCBOR(nil); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("UnmarshalCBOR(nil) error = %v", err)
	}
	validCBOR[0] = 0
	if err := capability.UnmarshalCBOR(validCBOR); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("UnmarshalCBOR(bad tag) error = %v", err)
	}
}

func TestPadding(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, 1, 1023, 1024, 2048} {
		content := bytes.Repeat([]byte{0x42}, length)
		padded, err := Pad(content, BlockSize1KiB)
		if err != nil || len(padded)%int(BlockSize1KiB) != 0 || bytes.Equal(padded, content) {
			t.Fatalf("Pad(%d bytes) returned %d bytes, %v", length, len(padded), err)
		}
		decoded, err := Unpad(padded, BlockSize1KiB)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("Unpad(Pad(%d bytes)) failed: %v", length, err)
		}
		if len(decoded) > 0 {
			decoded[0] ^= 0xff
			if content[0] != 0x42 {
				t.Error("Unpad returned aliased memory")
			}
		}
	}

	if _, err := Pad(nil, 1); !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("Pad() error = %v", err)
	}
	if _, err := Unpad(nil, 1); !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("Unpad(invalid size) error = %v", err)
	}
	if _, err := Unpad(nil, BlockSize1KiB); !errors.Is(err, ErrInvalidPadding) {
		t.Errorf("Unpad(short) error = %v", err)
	}
	noMarker := make([]byte, BlockSize1KiB)
	if _, err := Unpad(noMarker, BlockSize1KiB); !errors.Is(err, ErrInvalidPadding) {
		t.Errorf("Unpad(no marker) error = %v", err)
	}
	badByte := make([]byte, BlockSize1KiB)
	badByte[len(badByte)-1] = 1
	if _, err := Unpad(badByte, BlockSize1KiB); !errors.Is(err, ErrInvalidPadding) {
		t.Errorf("Unpad(bad byte) error = %v", err)
	}
}

func TestCrypto(t *testing.T) {
	t.Parallel()

	node := bytes.Repeat([]byte{0x5a}, int(BlockSize1KiB))
	secret := Secret{1, 2, 3}
	for _, level := range []Level{0, 1, 255} {
		block, pair, err := EncryptNode(node, secret, level)
		if err != nil || bytes.Equal(block, node) {
			t.Fatalf("EncryptNode(level %d) error = %v", level, err)
		}
		if err := VerifyBlock(block, pair.Reference); err != nil {
			t.Fatalf("VerifyBlock(level %d) error = %v", level, err)
		}
		plain, err := DecryptNode(block, pair, level)
		if err != nil || !bytes.Equal(plain, node) {
			t.Fatalf("DecryptNode(level %d) error = %v", level, err)
		}
	}

	if _, err := ReferenceOf(nil); !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("ReferenceOf(nil) error = %v", err)
	}
	if err := VerifyBlock(nil, Reference{}); !errors.Is(err, ErrInvalidBlock) || !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("VerifyBlock(nil) error = %v", err)
	}
	if _, _, err := EncryptNode(nil, secret, 0); !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("EncryptNode(nil) error = %v", err)
	}
	block := make([]byte, BlockSize1KiB)
	pair := Pair{}
	if _, err := DecryptNode(block, pair, 0); !errors.Is(err, ErrInvalidBlock) {
		t.Errorf("DecryptNode(bad ref) error = %v", err)
	}
}

func TestOfficialVectors(t *testing.T) {
	t.Parallel()

	const hello = "Hello world!"
	vectors := []struct {
		size   BlockSize
		secret string
		urn    string
	}{
		{BlockSize1KiB, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M"},
		{BlockSize32KiB, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "urn:eris:B4ABLHUAHUMZ3G4FBXZWOZJTE4CTQPFNA5DE5YITWWYDUQD2K6AHDMTQL4XVKKVZY3FHASKREASE5BFG2SHMK73MNEGZNNOX5R6ZKCOL6A"},
		{BlockSize1KiB, "2JOARHFRTKGSQ4D6HIWPTOXAIKKZGHLII4GJBIWHQ5S27Q4EPLFQ", "urn:eris:BIAJ6GJYEZLZTGU4EOTUT2BJUE2EF7FNQLVNLLBPQSCCCTCDIYXAO4BKJPD3M3623DQ7GMXGF2W3NJXNXCBBRTHFFB7YAGPN76NNRZDJQQ"},
		{BlockSize32KiB, "2JOARHFRTKGSQ4D6HIWPTOXAIKKZGHLII4GJBIWHQ5S27Q4EPLFQ", "urn:eris:B4ANHVUBQO6MQV5RW3WDTBM5O2DZ7BP6JRDW3SA6Q3VENROLSCAYVTNPBH7CQUVVQTDSSROFCSVE6BAK35JOMICHQXKS2UTE2ETMGRR6AM"},
	}
	for _, vector := range vectors {
		secretBytes, err := base32Encoding.DecodeString(vector.secret)
		if err != nil {
			t.Fatal(err)
		}
		var secret Secret
		copy(secret[:], secretBytes)
		capability, blocks, err := Encode([]byte(hello), secret, vector.size)
		if err != nil || capability.String() != vector.urn {
			t.Errorf("Encode(%d) URN = %q, error = %v", vector.size, capability, err)
		}
		decoded, err := Decode(capability, blocks)
		if err != nil || string(decoded) != hello {
			t.Errorf("Decode(%d) = (%q, %v)", vector.size, decoded, err)
		}
	}

	capability, _, err := Encode(make([]byte, 1024), Secret{}, BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	const zeroVector = "urn:eris:BIARQXFLRHNRCHN7ZTQOD4TYLPZHYX2Q3MWBPDBIP4WHJSCCMMW43MZ6633MO4XF4AF7BVE4UX7IDTKKUVKBMACMFOUMLAGBSFSXYWYUJY"
	if capability.String() != zeroVector {
		t.Errorf("1 KiB zero vector URN = %q", capability)
	}

	zeroVectors := []struct {
		length int
		size   BlockSize
		urn    string
	}{
		{1023, BlockSize1KiB, "urn:eris:BIAOPGHUAEIMSBPEO4HJZALI7KYB5DHKZYFCD2BD24KNJ56K2W6PNRS2LFBUKLVNQ5Z3BDW5333NCFOQ5XOLIWGKYXV7XXW4SW55VQACTY"},
		{4096, BlockSize1KiB, "urn:eris:BIA3QV7BGU5A2LO74F7R4AKQ6QS7B74XKGHHWUA5BGPEVW2QPG5PXOIOOKP5L2NAABINZDSXZG7NPB5SU6YGPVNUUT6GRAZWWA5ZLZMKGQ"},
		{32767, BlockSize32KiB, "urn:eris:B4AGXQSNFXWXK2DWVJCZP5C7DISMYEZWG7Z7V25U5LODKYZEZAG4SJPDMVCTGBAYOHRPWAIH2MUWKENSDCM3IZIZ4DX3IBMOQCID65AJWM"},
		{32768, BlockSize32KiB, "urn:eris:B4A7DX6F54NI56VZX7RC6GTTYRMYXE7LKCXKOZEB5WVO6GEFRWVFRA5RAYNTGERPMX2HBFXBSHMBFZIB7BZYXWSVMI2WCCHZR7K7C5T2H4"},
	}
	for _, vector := range zeroVectors {
		capability, blocks, encodeErr := Encode(make([]byte, vector.length), Secret{}, vector.size)
		if encodeErr != nil || capability.String() != vector.urn {
			t.Errorf("zero vector (%d, %d) URN = %q, error = %v", vector.length, vector.size, capability, encodeErr)
			continue
		}
		decoded, decodeErr := Decode(capability, blocks)
		if decodeErr != nil || !bytes.Equal(decoded, make([]byte, vector.length)) {
			t.Errorf("zero vector (%d, %d) decode error = %v", vector.length, vector.size, decodeErr)
		}
	}
}

func TestEncodeDecodeTrees(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, 1, 1023, 1024, 17 * 1024, 300 * 1024} {
		content := make([]byte, length)
		for i := range content {
			content[i] = byte(i * 31)
		}
		capability, blocks, err := Encode(content, Secret{9}, BlockSize1KiB)
		if err != nil {
			t.Fatalf("Encode(%d bytes) error = %v", length, err)
		}
		decoded, err := Decode(capability, blocks)
		if err != nil || !bytes.Equal(decoded, content) {
			t.Fatalf("Decode(Encode(%d bytes)) error = %v", length, err)
		}
	}

	content := bytes.Repeat([]byte("recommended"), 2000)
	capability, blocks, err := EncodeRecommended(content, Secret{})
	if err != nil || capability.BlockSize != BlockSize32KiB {
		t.Fatalf("EncodeRecommended() = (%v, %v)", capability, err)
	}
	decoded, err := Decode(capability, blocks)
	if err != nil || !bytes.Equal(decoded, content) {
		t.Fatalf("Decode(EncodeRecommended()) error = %v", err)
	}

	if _, _, err := Encode(nil, Secret{}, 7); !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("Encode(invalid size) error = %v", err)
	}
}

func TestDecodeRejectsInvalidData(t *testing.T) {
	t.Parallel()

	if _, err := Decode(Capability{}, nil); !errors.Is(err, ErrInvalidCapability) {
		t.Errorf("Decode(invalid capability) error = %v", err)
	}
	capability, blocks, err := Encode(bytes.Repeat([]byte{1}, 1024), Secret{}, BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(capability, nil); !errors.Is(err, ErrMissingBlock) {
		t.Errorf("Decode(missing root) error = %v", err)
	}

	badSize := cloneBlocks(blocks)
	badSize[capability.Root.Reference] = nil
	if _, err := Decode(capability, badSize); !errors.Is(err, ErrInvalidBlock) || !errors.Is(err, ErrInvalidBlockSize) {
		t.Errorf("Decode(bad block size) error = %v", err)
	}

	corrupt := cloneBlocks(blocks)
	corrupt[capability.Root.Reference][0] ^= 1
	if _, err := Decode(capability, corrupt); !errors.Is(err, ErrInvalidBlock) {
		t.Errorf("Decode(corrupt block) error = %v", err)
	}

	badKey := capability
	badKey.Root.Key[0] ^= 1
	if _, err := Decode(badKey, blocks); !errors.Is(err, ErrInvalidRootKey) {
		t.Errorf("Decode(bad root key) error = %v", err)
	}

	leafNode := make([]byte, BlockSize1KiB)
	leafNode[len(leafNode)-1] = 1
	leafBlock, leafPair, _ := EncryptNode(leafNode, Secret{}, 0)
	if _, err := Decode(
		Capability{BlockSize: BlockSize1KiB, Root: leafPair},
		BlockSet{leafPair.Reference: leafBlock},
	); !errors.Is(err, ErrInvalidPadding) {
		t.Errorf("Decode(bad padding) error = %v", err)
	}
}

func TestInvalidInternalNodes(t *testing.T) {
	t.Parallel()

	if _, err := parseInternalNode(nil); !errors.Is(err, ErrInvalidNode) {
		t.Errorf("parseInternalNode(nil) error = %v", err)
	}
	for _, node := range [][]byte{
		make([]byte, BlockSize1KiB),
		func() []byte {
			n := make([]byte, BlockSize1KiB)
			n[PairSize] = 1
			return n
		}(),
	} {
		block, pair, err := EncryptNode(node, Secret{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(
			Capability{BlockSize: BlockSize1KiB, Level: 1, Root: pair},
			BlockSet{pair.Reference: block},
		)
		if !errors.Is(err, ErrInvalidNode) {
			t.Errorf("Decode(invalid internal node) error = %v", err)
		}
	}

	missingChild := Pair{Reference: Reference{1}, Key: Key{2}}
	node := make([]byte, BlockSize1KiB)
	copy(node[:32], missingChild.Reference[:])
	copy(node[32:64], missingChild.Key[:])
	block, root, _ := EncryptNode(node, Secret{}, 1)
	_, err := Decode(
		Capability{BlockSize: BlockSize1KiB, Level: 1, Root: root},
		BlockSet{root.Reference: block},
	)
	if !errors.Is(err, ErrMissingBlock) {
		t.Errorf("Decode(missing child) error = %v", err)
	}
}

func cloneBlocks(source BlockSet) BlockSet {
	clone := make(BlockSet, len(source))
	for ref, block := range source {
		clone[ref] = bytes.Clone(block)
	}
	return clone
}
