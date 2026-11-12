package link

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/asciimoth/goeris"
)

func FuzzParseValue(f *testing.F) {
	seeds := [][]byte{
		{0xf6},
		{0x9f, 1, 2, 0xff},
		{0xbf, 0x61, 'x', 0x82, 0xf4, 0xf5, 0xff},
		{0x7f, 0x62, 'a', 'b', 0x61, 'c', 0xff},
		{0xd9, 0x01, 0x14, 0x43, 1, 2, 3},
		append(bytes.Repeat([]byte{0x81}, maxCBORDepth), 0x9f, 0xff),
		append(bytes.Repeat([]byte{0xc0}, maxCBORDepth+1), 0),
		{0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0xbf, 0x00, 0xff},
		{0x7f, 0x61, 0xcf, 0x61, 0x80, 0xff},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := ParseValue(data)
		if err != nil {
			return
		}
		if !bytes.Equal(value.RawCBOR(), data) {
			t.Fatal("valid value changed")
		}
		if _, err := ParseValue(value.RawCBOR()); err != nil {
			t.Fatalf("accepted value was rejected: %v", err)
		}
	})
}

func FuzzParseLink(f *testing.F) {
	link, _ := New(testCapability(), 42, "application/octet-stream")
	link.Attributes = map[string]Value{"answer": UintValue(42)}
	seed, _ := link.MarshalBinary()
	f.Add(seed)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		checkLinkRoundTrip(t, data)
	})
}

// Keep the magic bytes and capability intact so mutations reach the variable
// fields, including malformed maps that MarshalBinary cannot produce.
func FuzzParseLinkBody(f *testing.F) {
	base, _ := New(testCapability(), 0, "")
	encoded, _ := base.MarshalBinary()
	prefix := encoded[:len(header)+eris.CapabilitySize]
	for _, seed := range []string{
		"0060a0", "0060a1617800", "187b7f6161ffbf61619f00ffff",
		"0060bf6178007f6178ff01ff", "0060bbffffffffffffffff",
		"007f61cf6180ffa0", "0060bf60ff",
	} {
		data, err := hex.DecodeString(seed)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		checkLinkRoundTrip(t, append(bytes.Clone(prefix), body...))
	})
}

func checkLinkRoundTrip(t *testing.T, data []byte) {
	t.Helper()
	parsed, err := Parse(data)
	if err != nil {
		return
	}
	encoded, err := parsed.MarshalBinary()
	if err != nil {
		t.Fatalf("parsed link cannot be encoded: %v", err)
	}
	reparsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("encoded link cannot be parsed: %v", err)
	}
	if !equalLinks(parsed, reparsed) {
		t.Fatal("link changed after round trip")
	}
	stable, err := reparsed.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, stable) {
		t.Fatalf("link encoding is not stable: %v", err)
	}
}

func FuzzLinkFields(f *testing.F) {
	f.Add(uint64(0), "", "key", []byte{0xf6})
	f.Add(uint64(42), "text/plain", "answer", []byte{0x18, 42})
	f.Fuzz(func(t *testing.T, size uint64, mime, key string, raw []byte) {
		value, err := ParseValue(raw)
		if err != nil {
			return
		}
		link := Link{
			Capability: testCapability(),
			Size:       size,
			MIME:       mime,
			Attributes: map[string]Value{key: value},
		}
		encoded, err := link.MarshalBinary()
		if err != nil {
			return
		}
		parsed, err := Parse(encoded)
		if err != nil {
			t.Fatalf("encoded fields cannot be parsed: %v", err)
		}
		if !equalLinks(link, parsed) {
			t.Fatal("fields changed after round trip")
		}
	})
}
