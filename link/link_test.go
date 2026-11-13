package link

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func testCapability() eris.Capability {
	var capability eris.Capability
	capability.BlockSize = eris.BlockSize1KiB
	capability.Level = 2
	for index := range capability.Root.Reference {
		capability.Root.Reference[index] = byte(index)
		capability.Root.Key[index] = byte(255 - index)
	}
	return capability
}

func TestLinkRoundTrip(t *testing.T) {
	t.Parallel()
	text, err := TextValue("document")
	if err != nil {
		t.Fatal(err)
	}
	original := Link{
		Capability: testCapability(),
		Size:       math.MaxUint64,
		MIME:       "text/plain; charset=utf-8",
		Attributes: map[string]Value{
			"z":    NullValue(),
			"name": text,
			"ok":   BoolValue(true),
			"data": BytesValue([]byte{1, 2, 3}),
		},
	}
	first, err := original.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	second, err := original.MarshalBinary()
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("non-deterministic encoding: %x != %x (%v)", first, second, err)
	}
	if !HasHeader(first) || HasHeader(first[:HeaderSize-1]) || HasHeader(nil) {
		t.Fatal("header detection failed")
	}

	parsed, err := Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	if !equalLinks(original, parsed) {
		t.Fatalf("round trip differs:\nwant %#v\n got %#v", original, parsed)
	}
	first[0] = 0
	if got := parsed.Attributes["data"].RawCBOR(); !bytes.Equal(got, []byte{0x43, 1, 2, 3}) {
		t.Fatalf("parse result aliases input: %x", got)
	}
}

func TestKnownEncoding(t *testing.T) {
	t.Parallel()
	link, err := New(testCapability(), 23, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := link.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	capability, _ := testCapability().MarshalBinary()
	want := append([]byte{0xd9, 0xd9, 0xf7, 0x84, 0xd9, 0x01, 0x14, 0x58, 0x42}, capability...)
	want = append(want, 0x17, 0x6a)
	want = append(want, "text/plain"...)
	want = append(want, 0xa0)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("wrong encoding:\nwant %x\n got %x", want, encoded)
	}
	if MIMEType != "application/x-eris-link+cbor" || Extension != ".eris" || CBORTag != 55799 {
		t.Fatal("identification constants changed")
	}
}

func TestAttributeOrder(t *testing.T) {
	t.Parallel()
	link, _ := New(testCapability(), 0, "")
	link.Attributes = map[string]Value{
		"aa": UintValue(1),
		"b":  UintValue(2),
		"a":  UintValue(3),
	}
	encoded, err := link.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// Canonical CBOR sorts encoded map keys by length and then by bytes.
	wantSuffix := []byte{0xa3, 0x61, 'a', 3, 0x61, 'b', 2, 0x62, 'a', 'a', 1}
	if !bytes.HasSuffix(encoded, wantSuffix) {
		t.Fatalf("wrong attribute order: %x", encoded)
	}
}

func TestAttributeOrderAcrossHeaderSizes(t *testing.T) {
	t.Parallel()
	// Include CBOR length boundaries and equal byte lengths with different
	// rune counts. Order must use encoded bytes, not Unicode character counts.
	keys := []string{
		"", "a", "zz", "π", strings.Repeat("z", 23),
		strings.Repeat("a", 24), strings.Repeat("z", 255),
		strings.Repeat("a", 256), strings.Repeat("z", 65535),
		strings.Repeat("a", 65536),
	}
	link, _ := New(testCapability(), 0, "")
	link.Attributes = make(map[string]Value, len(keys))
	wantSuffix := []byte{0xaa}
	for i, key := range keys {
		link.Attributes[key] = UintValue(uint64(i))
		text, err := TextValue(key)
		if err != nil {
			t.Fatal(err)
		}
		wantSuffix = append(wantSuffix, text.RawCBOR()...)
		wantSuffix = append(wantSuffix, byte(i))
	}
	encoded, err := link.MarshalBinary()
	if err != nil || !bytes.HasSuffix(encoded, wantSuffix) {
		t.Fatalf("attribute order changed at a length boundary: %v", err)
	}
}

func TestValidateAndNewErrors(t *testing.T) {
	t.Parallel()
	valid, err := New(testCapability(), 1, "application/octet-stream")
	if err != nil || valid.Validate() != nil {
		t.Fatalf("valid link rejected: %v", err)
	}

	tests := []Link{
		{},
		{Capability: testCapability(), MIME: string([]byte{0xff})},
		{Capability: testCapability(), Attributes: map[string]Value{string([]byte{0xff}): NullValue()}},
		{Capability: testCapability(), Attributes: map[string]Value{"bad": {}}},
	}
	for _, test := range tests {
		if !errors.Is(test.Validate(), ErrInvalidLink) {
			t.Fatalf("invalid link accepted: %#v", test)
		}
		if _, err := test.MarshalBinary(); !errors.Is(err, ErrInvalidLink) {
			t.Fatalf("MarshalBinary error = %v", err)
		}
	}
	if _, err := New(eris.Capability{}, 0, ""); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("New error = %v", err)
	}
}

func TestUnmarshalFlexibleCBOR(t *testing.T) {
	t.Parallel()
	base, _ := New(testCapability(), 0, "")
	encoded, _ := base.MarshalBinary()
	prefix := append([]byte(nil), encoded[:HeaderSize+eris.CapabilitySize]...)
	// Non-minimal uint(1), an indefinite text string, and an indefinite map.
	data := append(prefix, 0x18, 0x01, 0x7f, 0x62, 't', 'e', 0x62, 'x', 't', 0xff, 0xbf)
	data = append(data, 0x61, 'x', 0x9f, 0x01, 0x02, 0xff, 0xff)
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Size != 1 || parsed.MIME != "text" {
		t.Fatalf("wrong fields: %#v", parsed)
	}
	if got := parsed.Attributes["x"].RawCBOR(); !bytes.Equal(got, []byte{0x9f, 1, 2, 0xff}) {
		t.Fatalf("wrong raw value: %x", got)
	}
	canonical, err := parsed.MarshalBinary()
	if err != nil || bytes.Equal(canonical, data) {
		t.Fatalf("input was not normalized at link level: %x (%v)", canonical, err)
	}
}

func TestUnmarshalDoesNotChangeReceiver(t *testing.T) {
	t.Parallel()
	original, _ := New(testCapability(), 7, "old")
	receiver := original
	if err := receiver.UnmarshalBinary([]byte{0}); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("error = %v", err)
	}
	if !equalLinks(receiver, original) {
		t.Fatal("receiver changed after an error")
	}
	var nilLink *Link
	if err := nilLink.UnmarshalBinary(nil); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("nil receiver error = %v", err)
	}
}

func TestInvalidLinks(t *testing.T) {
	t.Parallel()
	valid, _ := New(testCapability(), 0, "")
	good, _ := valid.MarshalBinary()
	capEnd := HeaderSize + eris.CapabilitySize
	prefix := append([]byte(nil), good[:capEnd]...)

	cases := [][]byte{
		nil,
		good[:HeaderSize-1],
		append([]byte(nil), good[:capEnd-1]...),
		append(append([]byte(nil), good[:HeaderSize]...), make([]byte, eris.CapabilitySize)...),
		append(append([]byte(nil), prefix...), 0x20),
		append(append([]byte(nil), prefix...), 0x00, 0x7c),
		append(append([]byte(nil), prefix...), 0x00, 0x7f),
		append(append([]byte(nil), prefix...), 0x00, 0x7f, 0x01),
		append(append([]byte(nil), prefix...), 0x00, 0x7f, 0x7f),
		append(append([]byte(nil), prefix...), 0x00, 0x7f, 0x62, 'x'),
		append(append([]byte(nil), prefix...), 0x00, 0x7f, 0x61, 0xff),
		append(append([]byte(nil), prefix...), 0x00, 0x40),
		append(append([]byte(nil), prefix...), 0x00, 0x61, 0xff),
		append(append([]byte(nil), prefix...), 0x00, 0x60, 0x80),
		append(append([]byte(nil), prefix...), 0x00, 0x60, 0xa1, 0x01, 0x00),
		append(append([]byte(nil), prefix...), 0x00, 0x60, 0xa1, 0x61, 'x'),
		append(append([]byte(nil), prefix...), 0x00, 0x60, 0xbf, 0xff, 0x00),
		append(append([]byte(nil), prefix...), 0x00, 0x60, 0xa0, 0x00),
	}
	duplicate := append(append([]byte(nil), prefix...), 0x00, 0x60, 0xa2, 0x61, 'x', 0x01, 0x61, 'x', 0x02)
	cases = append(cases, duplicate)
	for index, data := range cases {
		if _, err := Parse(data); !errors.Is(err, ErrInvalidLink) {
			t.Fatalf("case %d (%x): error = %v", index, data, err)
		}
	}
}

func TestValueConstructors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value Value
		hex   string
	}{
		{NullValue(), "f6"},
		{BoolValue(false), "f4"},
		{BoolValue(true), "f5"},
		{UintValue(0), "00"},
		{UintValue(24), "1818"},
		{UintValue(256), "190100"},
		{UintValue(65536), "1a00010000"},
		{UintValue(math.MaxUint64), "1bffffffffffffffff"},
		{IntValue(1), "01"},
		{IntValue(-1), "20"},
		{IntValue(math.MinInt64), "3b7fffffffffffffff"},
		{BytesValue([]byte("ab")), "426162"},
	}
	for _, test := range tests {
		want, _ := hex.DecodeString(test.hex)
		if got := test.value.RawCBOR(); !bytes.Equal(got, want) {
			t.Errorf("RawCBOR = %x, want %x", got, want)
		}
		if _, err := ParseValue(test.value.RawCBOR()); err != nil {
			t.Errorf("constructor made invalid value: %v", err)
		}
	}
	text, err := TextValue("π")
	if err != nil || !bytes.Equal(text.RawCBOR(), []byte{0x62, 0xcf, 0x80}) {
		t.Fatalf("TextValue = %x, %v", text.RawCBOR(), err)
	}
	if _, err := TextValue(string([]byte{0xff})); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("invalid text error = %v", err)
	}

	input := []byte{0x41, 7}
	value := BytesValue(input)
	input[0] = 9
	output := value.RawCBOR()
	output[0] = 9
	if got := value.RawCBOR(); !bytes.Equal(got, []byte{0x42, 0x41, 7}) {
		t.Fatalf("value aliases input or output: %x", got)
	}
}

func TestParseValueValid(t *testing.T) {
	t.Parallel()
	valid := []string{
		"00", "17", "1817", "190100", "1a00010000", "1b0000000100000000",
		"20", "3817", "59000100", "5a00000000", "5b0000000000000000",
		"40", "43010203", "5f4201024103ff",
		"60", "62cf80", "7f61616162ff",
		"80", "83010203", "9f019f02ffff",
		"a0", "a1616101", "bf616101ff",
		"c0f6", "d81700", "d90100f4", "da00010000f5", "db0000000100000000f6",
		"e0", "f4", "f5", "f6", "f7", "f820", "f8ff", "f90000", "fa00000000", "fb0000000000000000",
	}
	for _, encoded := range valid {
		data, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		value, err := ParseValue(data)
		if err != nil {
			t.Errorf("ParseValue(%s): %v", encoded, err)
			continue
		}
		data[0] ^= 0xff
		if bytes.Equal(data, value.RawCBOR()) {
			t.Errorf("ParseValue(%s) retained input", encoded)
		}
	}
}

func TestParseValueInvalid(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"", "0000", "18", "19ff", "1affffff", "1bffffffffffffff",
		"1c", "1d", "1e", "1f", "3f",
		"41", "61ff", "5f", "5f01ff", "5f5fff", "5f4201ff", "5f4301ff", "7f", "7f01ff", "7f7fff",
		"81", "9f", "a1", "bf6161ff", "c0", "df", "f8", "f9ff", "faffffff", "fbffffffffffffff",
		"f800", "f817", "f818", "f81f", "fc", "fd", "fe", "ff",
	}
	for _, encoded := range invalid {
		data, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseValue(data); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("ParseValue(%s) error = %v", encoded, err)
		}
	}
}

func TestValueDepthLimit(t *testing.T) {
	t.Parallel()
	valid := append(bytes.Repeat([]byte{0x81}, maxCBORDepth), 0)
	if _, err := ParseValue(valid); err != nil {
		t.Fatalf("maximum valid depth rejected: %v", err)
	}
	tooDeep := append([]byte{0x81}, valid...)
	if _, err := ParseValue(tooDeep); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("depth error = %v", err)
	}
}

func equalLinks(left, right Link) bool {
	if left.Capability != right.Capability || left.Size != right.Size || left.MIME != right.MIME ||
		len(left.Attributes) != len(right.Attributes) {
		return false
	}
	for key, value := range left.Attributes {
		other, ok := right.Attributes[key]
		if !ok || !reflect.DeepEqual(value.RawCBOR(), other.RawCBOR()) {
			return false
		}
	}
	return true
}
