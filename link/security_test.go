package link

import (
	"bytes"
	"encoding/hex"
	"errors"
	"maps"
	"testing"

	"github.com/asciimoth/goeris"
)

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
