package cbor

import (
	"bytes"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/cborwire"
)

func TestElementConstructorsAndAccessors(t *testing.T) {
	t.Parallel()
	block, reference := testBlock(t, eris.BlockSize1KiB, 1)
	capability, _, err := eris.Encode([]byte("capability"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}

	blockElement, err := NewBlock(block)
	if err != nil || blockElement.Kind() != BlockKind || blockElement.Kind().String() != "block" {
		t.Fatalf("NewBlock() = (%v, %v)", blockElement.Kind(), err)
	}
	block[0] ^= 0xff
	gotBlock, ok := blockElement.Block()
	if !ok || gotBlock[0] == block[0] {
		t.Fatal("block constructor did not copy input")
	}
	gotBlock[0] ^= 0xff
	again, _ := blockElement.Block()
	if bytes.Equal(gotBlock, again) {
		t.Fatal("Block returned an aliased slice")
	}
	if _, ok := blockElement.Reference(); ok {
		t.Fatal("Block element returned a reference")
	}

	referenceElement := NewReference(reference)
	if got, ok := referenceElement.Reference(); !ok || got != reference || got.String() == "" {
		t.Fatalf("Reference() = (%v, %v)", got, ok)
	}
	if referenceElement.Kind().String() != "reference" {
		t.Fatal("wrong reference kind name")
	}
	if _, ok := referenceElement.Block(); ok {
		t.Fatal("Reference element returned a block")
	}

	validBlock, validReference := testBlock(t, eris.BlockSize1KiB, 2)
	mapElement, err := NewBlockMap(eris.BlockSet{validReference: validBlock})
	if err != nil || mapElement.Kind().String() != "block map" {
		t.Fatalf("NewBlockMap() error = %v", err)
	}
	validBlock[0] ^= 0xff
	gotMap, ok := mapElement.BlockMap()
	if !ok || gotMap[validReference][0] == validBlock[0] {
		t.Fatal("block-map constructor did not copy input")
	}
	gotMap[validReference][0] ^= 0xff
	againMap, _ := mapElement.BlockMap()
	if bytes.Equal(gotMap[validReference], againMap[validReference]) {
		t.Fatal("BlockMap returned aliased data")
	}
	if _, ok := mapElement.Capability(); ok {
		t.Fatal("Block-map element returned a capability")
	}
	if blocks, ok := referenceElement.BlockMap(); ok || blocks != nil {
		t.Fatal("Reference element returned a block map")
	}

	capabilityElement, err := NewCapability(capability)
	if got, ok := capabilityElement.Capability(); err != nil || !ok || got != capability ||
		capabilityElement.Kind().String() != "capability" {
		t.Fatalf("NewCapability() = (%v, %v, %v)", got, ok, err)
	}
	if Kind(99).String() != "unknown" || (Element{}).Kind() != 0 {
		t.Fatal("unknown kind has an invalid result")
	}
}

func TestConstructorsRejectInvalidValues(t *testing.T) {
	t.Parallel()
	if _, err := NewBlock(nil); !errors.Is(err, ErrInvalidElement) {
		t.Fatalf("NewBlock(nil) error = %v", err)
	} else if !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("NewBlock(nil) did not preserve the ERIS error: %v", err)
	}
	if _, err := NewCapability(eris.Capability{}); !errors.Is(err, ErrInvalidElement) {
		t.Fatalf("NewCapability(zero) error = %v", err)
	} else if !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatalf("NewCapability(zero) did not preserve the ERIS error: %v", err)
	}
	block, _ := testBlock(t, eris.BlockSize1KiB, 3)
	if _, err := NewBlockMap(eris.BlockSet{{}: block}); !errors.Is(err, ErrInvalidElement) {
		t.Fatalf("NewBlockMap(bad reference) error = %v", err)
	} else if !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("NewBlockMap(bad reference) did not preserve the ERIS error: %v", err)
	}
	if element, err := NewBlockMap(nil); err != nil {
		t.Fatalf("NewBlockMap(nil) error = %v", err)
	} else if blocks, ok := element.BlockMap(); !ok || len(blocks) != 0 {
		t.Fatalf("BlockMap() = (%v, %v)", blocks, ok)
	}
}

func TestMarshalFormsAndRoundTrip(t *testing.T) {
	t.Parallel()
	elements := testElements(t)
	tests := []struct {
		name   string
		encode func([]Element) ([]byte, error)
		decode func([]byte) ([]Element, error)
		prefix []byte
	}{
		{"tagged array", Marshal, Unmarshal, []byte{0xda, 0x65, 0x72, 0x69, 0x73}},
		{"untagged array", MarshalUntagged, Unmarshal, []byte{0x84}},
		{"self-described array", MarshalSelfDescribed, Unmarshal, []byte{0xd9, 0xd9, 0xf7, 0xda, 0x65, 0x72, 0x69, 0x73}},
		{"tagged sequence", MarshalSequence, UnmarshalSequence, []byte{0xda, 0x65, 0x72, 0x69, 0x73, 0xf6}},
		{"untagged sequence", MarshalUntaggedSequence, UnmarshalSequence, []byte{0x59, 0x04, 0x00}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := test.encode(elements)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(encoded, test.prefix) {
				t.Fatalf("encoded prefix = %x, want %x", encoded[:min(len(encoded), len(test.prefix))], test.prefix)
			}
			decoded, err := test.decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			assertElementsEqual(t, decoded, elements)
		})
	}
}

func TestMarshalEmptyWireForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		encode func([]Element) ([]byte, error)
		want   []byte
	}{
		{"tagged array", Marshal, []byte{0xda, 0x65, 0x72, 0x69, 0x73, 0x80}},
		{"untagged array", MarshalUntagged, []byte{0x80}},
		{"self-described array", MarshalSelfDescribed, []byte{0xd9, 0xd9, 0xf7, 0xda, 0x65, 0x72, 0x69, 0x73, 0x80}},
		{"tagged sequence", MarshalSequence, []byte{0xda, 0x65, 0x72, 0x69, 0x73, 0xf6}},
		{"untagged sequence", MarshalUntaggedSequence, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.encode(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("encoding = %x, want %x", got, test.want)
			}
		})
	}
}

func TestMarshalIsDeterministic(t *testing.T) {
	t.Parallel()
	firstBlock, firstReference := testBlock(t, eris.BlockSize1KiB, 5)
	secondBlock, secondReference := testBlock(t, eris.BlockSize32KiB, 6)
	first, err := NewBlockMap(eris.BlockSet{
		secondReference: secondBlock,
		firstReference:  firstBlock,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewBlockMap(eris.BlockSet{
		firstReference:  firstBlock,
		secondReference: secondBlock,
	})
	firstEncoding, _ := Marshal([]Element{first})
	secondEncoding, _ := Marshal([]Element{second})
	if !bytes.Equal(firstEncoding, secondEncoding) {
		t.Fatal("map insertion order changed the encoding")
	}
	wantFirst := firstReference
	if bytes.Compare(secondReference[:], firstReference[:]) < 0 {
		wantFirst = secondReference
	}
	if !bytes.Contains(firstEncoding[:70], wantFirst[:]) {
		t.Fatal("map keys do not use deterministic byte order")
	}
}

func TestUnmarshalFlexibleCBORForms(t *testing.T) {
	t.Parallel()
	block, reference := testBlock(t, eris.BlockSize1KiB, 7)
	indefiniteBlock := []byte{0x9f, 0x5f, 0x59, 0x02, 0x00}
	indefiniteBlock = append(indefiniteBlock, block[:512]...)
	indefiniteBlock = append(indefiniteBlock, 0x59, 0x02, 0x00)
	indefiniteBlock = append(indefiniteBlock, block[512:]...)
	indefiniteBlock = append(indefiniteBlock, 0xff, 0xff)
	decoded, err := Unmarshal(indefiniteBlock)
	if err != nil || len(decoded) != 1 {
		t.Fatalf("Unmarshal(indefinite strings) = (%v, %v)", len(decoded), err)
	}
	got, _ := decoded[0].Block()
	if !bytes.Equal(got, block) {
		t.Fatal("indefinite byte string changed the block")
	}

	nonPreferredArray := append([]byte{0x98, 0x01, 0x59, 0x04, 0x00}, block...)
	if _, err := Unmarshal(nonPreferredArray); err != nil {
		t.Fatalf("valid non-preferred CBOR was rejected: %v", err)
	}

	mapElement, _ := NewBlockMap(eris.BlockSet{})
	encodedMap, _ := MarshalUntagged([]Element{mapElement})
	encodedMap[1] = 0xbf
	encodedMap = append(encodedMap, 0xff)
	if _, err := Unmarshal(encodedMap); err != nil {
		t.Fatalf("indefinite map was rejected: %v", err)
	}

	indefiniteMap := []byte{0x81, 0xbf, 0x5f, 0x50}
	indefiniteMap = append(indefiniteMap, reference[:16]...)
	indefiniteMap = append(indefiniteMap, 0x50)
	indefiniteMap = append(indefiniteMap, reference[16:]...)
	indefiniteMap = append(indefiniteMap, 0xff, 0x5f, 0x59, 0x02, 0x00)
	indefiniteMap = append(indefiniteMap, block[:512]...)
	indefiniteMap = append(indefiniteMap, 0x59, 0x02, 0x00)
	indefiniteMap = append(indefiniteMap, block[512:]...)
	indefiniteMap = append(indefiniteMap, 0xff, 0xff)
	decoded, err = Unmarshal(indefiniteMap)
	if err != nil || len(decoded) != 1 {
		t.Fatalf("Unmarshal(indefinite map) = (%v, %v)", len(decoded), err)
	}
	blocks, ok := decoded[0].BlockMap()
	if !ok || !bytes.Equal(blocks[reference], block) {
		t.Fatal("indefinite block map changed its key or value")
	}
}

func TestEmptySerializations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		encoded []byte
		decode  func([]byte) ([]Element, error)
	}{
		{[]byte{0x80}, Unmarshal},
		{[]byte{0x9f, 0xff}, Unmarshal},
		{nil, UnmarshalSequence},
		{[]byte{0xda, 0x65, 0x72, 0x69, 0x73, 0xf6}, UnmarshalSequence},
	}
	for _, test := range tests {
		got, err := test.decode(test.encoded)
		if err != nil || len(got) != 0 {
			t.Errorf("decode(%x) = (%v, %v)", test.encoded, got, err)
		}
	}
}

func TestUnmarshalDoesNotAliasInput(t *testing.T) {
	t.Parallel()
	elements := testElements(t)
	encoded, err := MarshalUntagged(elements)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Unmarshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want, err := MarshalUntagged(decoded)
	if err != nil {
		t.Fatal(err)
	}
	for index := range encoded {
		encoded[index] ^= 0xff
	}
	got, err := MarshalUntagged(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("decoded elements alias the input buffer")
	}
}

func TestUnmarshalRejectsInvalidData(t *testing.T) {
	t.Parallel()
	block, reference := testBlock(t, eris.BlockSize1KiB, 8)
	validElement := append([]byte{0x59, 0x04, 0x00}, block...)
	validMapPair := append([]byte{0x58, 0x20}, reference[:]...)
	validMapPair = append(validMapPair, validElement...)
	badCapability := make([]byte, eris.CapabilitySize)
	badCapability[0] = 0xff

	tests := [][]byte{
		nil,
		{0x81},
		{0x80, 0x00},
		{0xd9, 0xd9, 0xf7, 0x80},
		{0xda, 0x65, 0x72, 0x69, 0x73},
		{0xff},
		{0x81, 0x00},
		{0x81, 0x5c},
		{0x81, 0x5a, 0xff, 0xff, 0xff, 0xff},
		{0x81, 0x40},
		{0x81, 0x5f, 0x5f, 0xff, 0xff},
		{0x81, 0x5f, 0x41, 0x00},
		{0x81, 0xd9, 0x01, 0x14, 0x40},
		{0x81, 0xd9, 0x01, 0x14, 0x5a},
		append([]byte{0x81, 0xd9, 0x01, 0x14, 0x58, eris.CapabilitySize}, badCapability...),
		{0x81, 0xbb},
		{0x81, 0xa1, 0x40, 0x40},
		{0x81, 0xbf},
		{0x81, 0xbf, 0xff, 0x00},
		{0x81, 0x9f, 0xff},
		{0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}

	wrongReferencePair := slices.Clone(validMapPair)
	wrongReferencePair[2] ^= 0xff
	tests = append(tests, append([]byte{0x81, 0xa1}, wrongReferencePair...))
	duplicateMap := append([]byte{0x81, 0xa2}, validMapPair...)
	duplicateMap = append(duplicateMap, validMapPair...)
	tests = append(tests, duplicateMap)
	missingMapValue := append([]byte{0x81, 0xa1, 0x58, 0x20}, reference[:]...)
	tests = append(tests, missingMapValue)

	tooLargeIndefinite := []byte{0x81, 0x5f, 0x59, 0x80, 0x00}
	tooLargeIndefinite = append(tooLargeIndefinite, make([]byte, eris.BlockSize32KiB)...)
	tooLargeIndefinite = append(tooLargeIndefinite, 0x41, 0, 0xff)
	tests = append(tests, tooLargeIndefinite)

	for _, data := range tests {
		if _, err := Unmarshal(data); !errors.Is(err, ErrInvalidEncoding) {
			t.Errorf("Unmarshal(%x) error = %v", data[:min(20, len(data))], err)
		}
	}
}

func TestUnmarshalSequenceRejectsInvalidData(t *testing.T) {
	t.Parallel()
	tests := [][]byte{
		{0xda, 0x65, 0x72, 0x69, 0x73},
		{0xda, 0x65, 0x72, 0x69, 0x73, 0xf5},
		{0x00},
		{0xff},
		{0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}
	for _, data := range tests {
		if _, err := UnmarshalSequence(data); !errors.Is(err, ErrInvalidEncoding) {
			t.Errorf("UnmarshalSequence(%x) error = %v", data, err)
		}
	}
}

func TestMarshalRejectsInvalidElements(t *testing.T) {
	t.Parallel()
	invalid := []Element{
		{},
		{kind: BlockKind, block: []byte{1}},
		{kind: BlockMapKind, blockMap: eris.BlockSet{{}: make([]byte, eris.BlockSize1KiB)}},
		{kind: CapabilityKind},
	}
	encoders := []func([]Element) ([]byte, error){Marshal, MarshalUntagged, MarshalSelfDescribed, MarshalSequence, MarshalUntaggedSequence}
	for _, element := range invalid {
		for _, encode := range encoders {
			if _, err := encode([]Element{element}); !errors.Is(err, ErrInvalidElement) {
				t.Errorf("encode(%v) error = %v", element.Kind(), err)
			}
		}
	}
}

func TestCBORIntegerHeads(t *testing.T) {
	t.Parallel()
	values := []uint64{0, 23, 24, math.MaxUint8, math.MaxUint8 + 1, math.MaxUint16, math.MaxUint16 + 1, math.MaxUint32, math.MaxUint32 + 1, math.MaxUint64}
	for _, value := range values {
		encoded := cborwire.AppendHead(nil, 0, value)
		decoder := decoder{data: encoded}
		got, indefinite, err := decoder.head(0, false)
		if err != nil || indefinite || got != value || decoder.offset != len(encoded) {
			t.Errorf("head(%d) = (%d, %v), bytes %x", value, got, err, encoded)
		}
	}
	for _, data := range [][]byte{{0x18}, {0x19, 0}, {0x1a, 0, 0, 0}, {0x1b, 0, 0, 0, 0, 0, 0, 0}, {0x1c}, {0x1f}} {
		decoder := decoder{data: data}
		if _, _, err := decoder.head(0, false); err == nil {
			t.Errorf("head(%x) accepted invalid data", data)
		}
	}
	decoder := decoder{data: []byte{0x9f}}
	if value, indefinite, err := decoder.head(4, true); err != nil || value != 0 || !indefinite {
		t.Fatalf("indefinite head = (%d, %v, %v)", value, indefinite, err)
	}
}

func TestAppendElementPanicsForUnknownKind(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("appendElement did not panic for an unknown kind")
		}
	}()
	appendElement(nil, Element{})
}

func testElements(t *testing.T) []Element {
	t.Helper()
	block, reference := testBlock(t, eris.BlockSize1KiB, 4)
	largeBlock, largeReference := testBlock(t, eris.BlockSize32KiB, 9)
	blockElement, _ := NewBlock(block)
	mapElement, _ := NewBlockMap(eris.BlockSet{largeReference: largeBlock})
	capability, _, err := eris.Encode([]byte("test content"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	capabilityElement, _ := NewCapability(capability)
	return []Element{blockElement, NewReference(reference), mapElement, capabilityElement}
}

func testBlock(t *testing.T, size eris.BlockSize, fill byte) ([]byte, eris.Reference) {
	t.Helper()
	node := bytes.Repeat([]byte{fill}, int(size))
	block, pair, err := eris.EncryptNode(node, eris.Secret{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return block, pair.Reference
}

func assertElementsEqual(t *testing.T, got, want []Element) {
	t.Helper()
	gotEncoding, err := MarshalUntagged(got)
	if err != nil {
		t.Fatal(err)
	}
	wantEncoding, err := MarshalUntagged(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotEncoding, wantEncoding) {
		t.Fatalf("elements differ:\n got %x\nwant %x", gotEncoding, wantEncoding)
	}
}
