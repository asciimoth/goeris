package link

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
	"unicode/utf8"

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
		input := bytes.Clone(data)
		value, err := ParseValue(input)
		if err != nil {
			if !errors.Is(err, ErrInvalidValue) || len(value.RawCBOR()) != 0 {
				t.Fatalf("rejected value returned data or an unexpected error: %v", err)
			}
			return
		}
		clear(input)
		if !bytes.Equal(value.RawCBOR(), data) {
			t.Fatal("valid value changed or aliases input")
		}
		raw := value.RawCBOR()
		clear(raw)
		if !bytes.Equal(value.RawCBOR(), data) {
			t.Fatal("value aliases output")
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
	prefix := encoded[:HeaderSize+eris.CapabilitySize]
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
	input := bytes.Clone(data)
	parsed, err := Parse(input)
	if err != nil {
		if !errors.Is(err, ErrInvalidLink) || parsed.Capability != (eris.Capability{}) ||
			parsed.Size != 0 || parsed.MIME != "" || parsed.Attributes != nil {
			t.Fatalf("rejected link returned metadata or an unexpected error: %v", err)
		}
		// Any rejected encoding must preserve an existing receiver, not only
		// the truncated valid encodings covered by the regression tests.
		previous := Link{Capability: testCapability(), Size: 7, MIME: "keep",
			Attributes: map[string]Value{"keep": BoolValue(true)}}
		before, _ := previous.MarshalBinary()
		if err := previous.UnmarshalBinary(input); !errors.Is(err, ErrInvalidLink) {
			t.Fatalf("UnmarshalBinary accepted a rejected link: %v", err)
		}
		after, err := previous.MarshalBinary()
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("rejected input changed the receiver: %v", err)
		}
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
	clear(input)
	for _, value := range parsed.Attributes {
		clear(value.RawCBOR())
	}
	owned, err := parsed.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, owned) {
		t.Fatalf("parsed link aliases input or output: %v", err)
	}
}

func FuzzLinkFields(f *testing.F) {
	f.Add(uint64(0), "", "key", []byte{0xf6})
	f.Add(uint64(42), "text/plain", "answer", []byte{0x18, 42})
	f.Add(uint64(0), "\xff", "key", []byte{0xf6})
	f.Add(uint64(0), "text/plain", "\xff", []byte{0xf6})
	f.Add(^uint64(0), "", "", []byte{0xf6})
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
		if !utf8.ValidString(mime) || !utf8.ValidString(key) {
			if !errors.Is(err, ErrInvalidLink) || encoded != nil {
				t.Fatalf("invalid UTF-8 was not rejected: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("valid fields were rejected: %v", err)
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
