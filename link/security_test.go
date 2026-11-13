package link

import (
	"bytes"
	"encoding/hex"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestChunkedLinkText(t *testing.T) {
	t.Parallel()
	base := Link{Capability: testCapability()}
	encoded, err := base.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	prefix := encoded[:HeaderSize+eris.CapabilitySize]
	for _, text := range []string{"", "\u03c0", strings.Repeat("a", 24), strings.Repeat("b", 256), strings.Repeat("c", 65536)} {
		chunked := []byte{0x7f, 0x60}
		chunked = appendText(chunked, text)
		chunked = append(chunked, 0x60, 0x62, 0xcf, 0x80, 0xff)
		data := append(bytes.Clone(prefix), 0)
		data = append(data, chunked...)
		data = append(data, 0xa1)
		data = append(data, chunked...)
		data = append(data, 0xf6)
		parsed, err := Parse(data)
		if err != nil || parsed.MIME != text+"\u03c0" {
			t.Fatalf("text length %d: MIME mismatch or parse error: %v", len(text), err)
		}
		if value, ok := parsed.Attributes[text+"\u03c0"]; !ok || !bytes.Equal(value.RawCBOR(), []byte{0xf6}) {
			t.Fatalf("text length %d: attribute key mismatch", len(text))
		}
		// Reject wrong chunk types, nested strings, invalid UTF-8, and
		// missing breaks after an otherwise valid payload.
		for _, tail := range [][]byte{nil, {0}, {0x7f, 0xff}, {0x61, 0xff, 0xff}} {
			bad := append(bytes.Clone(prefix), 0)
			bad = append(bad, chunked[:len(chunked)-1]...)
			bad = append(bad, tail...)
			if _, err := Parse(bad); !errors.Is(err, ErrInvalidLink) {
				t.Fatalf("text length %d, tail %x: error = %v", len(text), tail, err)
			}
		}
	}
}

func TestValueDepthForms(t *testing.T) {
	t.Parallel()
	// A break closes a container. It is not a child data item and must not
	// consume another nesting level.
	for _, leaf := range [][]byte{{0}, {0x80}, {0xa0}, {0x9f, 0xff}, {0xbf, 0xff}} {
		for _, wrapper := range []byte{0x81, 0xc0} {
			data := append(bytes.Repeat([]byte{wrapper}, maxCBORDepth), leaf...)
			if _, err := ParseValue(data); err != nil {
				t.Errorf("wrapper %x, leaf %x: maximum depth rejected: %v", wrapper, leaf, err)
			}
			data = append([]byte{wrapper}, data...)
			if _, err := ParseValue(data); !errors.Is(err, ErrInvalidValue) {
				t.Errorf("wrapper %x, leaf %x: excess depth error = %v", wrapper, leaf, err)
			}
		}
	}
}

func TestUntrustedValueLengthsAndBreaks(t *testing.T) {
	t.Parallel()
	for _, encoded := range []string{
		// Lengths must be checked before conversion to int or allocation.
		"5bffffffffffffffff", "7bffffffffffffffff",
		"9bffffffffffffffff", "bbffffffffffffffff",
		"5b0000000100000000", "7b0000000100000000",
		"9b0000000100000000", "bb0000000100000000",
		"5f5bffffffffffffffffff", "7f7bffffffffffffffffff",
		// Breaks can only terminate indefinite containers, after a full pair
		// in maps. Each text chunk must contain complete UTF-8 sequences.
		"81ff", "a1ff00", "a100ff", "9fc0ffff", "bf00ff",
		"9fbf00ffff", "7f61cf6180ff",
	} {
		t.Run(encoded, func(t *testing.T) {
			data, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseValue(data); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUntrustedLinkFields(t *testing.T) {
	t.Parallel()
	base, _ := New(testCapability(), 0, "")
	encoded, _ := base.MarshalBinary()
	prefix := encoded[:HeaderSize+eris.CapabilitySize]
	for _, suffix := range []string{
		"007bffffffffffffffff",           // MIME length.
		"0060bbffffffffffffffff",         // Attribute count.
		"0060bf7bffffffffffffffff",       // Key length.
		"007f61cf6180ffa0",               // UTF-8 split across MIME chunks.
		"0060a17f61cf6180ff00",           // UTF-8 split across key chunks.
		"0060a261780078017801",           // Duplicate key, non-preferred length.
		"0060bf6178007f6078017860ff01ff", // Duplicate key, chunked text.
		"0060bf60ff",                     // Map break in place of a value.
	} {
		t.Run(suffix, func(t *testing.T) {
			tail, err := hex.DecodeString(suffix)
			if err != nil {
				t.Fatal(err)
			}
			data := append(bytes.Clone(prefix), tail...)
			if _, err := Parse(data); !errors.Is(err, ErrInvalidLink) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUnmarshalAtomicAtEveryTruncation(t *testing.T) {
	t.Parallel()
	original, _ := New(testCapability(), 7, "old")
	original.Attributes = map[string]Value{"keep": BytesValue([]byte{1, 2, 3})}
	wantOriginal := original
	wantOriginal.Attributes = maps.Clone(original.Attributes)
	next, _ := New(testCapability(), 8, "new")
	next.Attributes = map[string]Value{"first": NullValue(), "last": BytesValue([]byte{4, 5, 6})}
	encoded, _ := next.MarshalBinary()
	for end := range len(encoded) {
		receiver := original
		if err := receiver.UnmarshalBinary(encoded[:end]); !errors.Is(err, ErrInvalidLink) {
			t.Fatalf("prefix length %d: error = %v", end, err)
		}
		if !equalLinks(receiver, wantOriginal) {
			t.Fatalf("prefix length %d: receiver changed", end)
		}
	}
	receiver := original
	if err := receiver.UnmarshalBinary(append(bytes.Clone(encoded), 0)); !errors.Is(err, ErrInvalidLink) {
		t.Fatalf("trailing byte: error = %v", err)
	}
	if !equalLinks(receiver, wantOriginal) {
		t.Fatal("trailing byte changed receiver")
	}
	if err := receiver.UnmarshalBinary(encoded); err != nil || !equalLinks(receiver, next) {
		t.Fatalf("successful parse did not replace receiver: %v", err)
	}
	clear(encoded)
	if !equalLinks(receiver, next) || !equalLinks(original, wantOriginal) {
		t.Fatal("successful parse retained input or changed the old attribute map")
	}
}

func TestRejectedMIMEDoesNotAllocatePayload(t *testing.T) {
	encoded, err := (Link{Capability: testCapability()}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		mime := appendText(nil, strings.Repeat("x", 1<<20))
		if chunked {
			mime = append(append([]byte{0x7f}, mime...), 0xff)
		}
		data := append(bytes.Clone(encoded[:len(encoded)-2]), mime...)
		data = append(data, 0xff) // The attributes field must be a map.
		allocs := testing.AllocsPerRun(10, func() {
			if _, err := Parse(data); !errors.Is(err, ErrInvalidLink) {
				t.Fatalf("chunked %v: malformed map error = %v", chunked, err)
			}
		})
		if allocs != 0 {
			t.Errorf("chunked %v: rejected MIME allocated %g times", chunked, allocs)
		}
	}
}

func TestRejectedAttributeDoesNotAllocateKey(t *testing.T) {
	encoded, err := (Link{Capability: testCapability()}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		key := appendText(nil, strings.Repeat("x", 1<<20))
		if chunked {
			key = append(append([]byte{0x7f}, key...), 0xff)
		}
		for _, value := range [][]byte{nil, {0xff}, {0x81}, {0x61, 0xff}} {
			data := append(bytes.Clone(encoded[:len(encoded)-1]), 0xa1)
			data = append(data, key...)
			data = append(data, value...)
			allocs := testing.AllocsPerRun(10, func() {
				if _, err := Parse(data); !errors.Is(err, ErrInvalidLink) {
					t.Fatalf("chunked %v, value %x: error = %v", chunked, value, err)
				}
			})
			if allocs != 0 {
				t.Errorf("chunked %v, value %x: rejected attribute allocated %g times", chunked, value, allocs)
			}
		}
	}
}

func TestLinkViewDoesNotCopyMIME(t *testing.T) {
	encoded, err := (Link{Capability: testCapability()}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		mime := appendText(nil, strings.Repeat("x", 1<<20))
		if chunked {
			mime = append(append([]byte{0x7f}, mime...), 0xff)
		}
		data := append(bytes.Clone(encoded[:len(encoded)-2]), mime...)
		data = append(data, 0xa0)
		allocs := testing.AllocsPerRun(10, func() {
			view, err := parseLink(data)
			if err != nil || view.capability != testCapability() || view.mime.length != 1<<20 {
				t.Fatalf("chunked %v: invalid view: %v", chunked, err)
			}
		})
		if allocs != 0 {
			t.Errorf("chunked %v: borrowed metadata allocated %g times", chunked, allocs)
		}
	}
}
