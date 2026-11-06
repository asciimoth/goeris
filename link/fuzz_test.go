package link

import (
	"bytes"
	"testing"
)

func FuzzParseValue(f *testing.F) {
	seeds := [][]byte{
		{0xf6},
		{0x9f, 1, 2, 0xff},
		{0xbf, 0x61, 'x', 0x82, 0xf4, 0xf5, 0xff},
		{0x7f, 0x62, 'a', 'b', 0x61, 'c', 0xff},
		{0xd9, 0x01, 0x14, 0x43, 1, 2, 3},
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
	})
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
