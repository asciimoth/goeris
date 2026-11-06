package link_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

const serializationRounds = 12

func TestRepeatedSerializationPreservesPayloadAndMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		mime    string
	}{
		{
			name:    "empty payload",
			payload: nil,
			mime:    "application/octet-stream",
		},
		{
			name:    "UTF-8 payload",
			payload: []byte("ERIS link round trip: Καλημέρα κόσμε\n"),
			mime:    "text/plain; charset=utf-8",
		},
		{
			name:    "multi-block binary payload",
			payload: patternedPayload(5*1024 + 137),
			mime:    "application/octet-stream",
		},
		{
			name:    "large binary payload",
			payload: patternedPayload(70*1024 + 29),
			mime:    "application/vnd.example.binary",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testRepeatedSerialization(t, test.payload, test.mime)
		})
	}
}

func testRepeatedSerialization(t *testing.T, payload []byte, mime string) {
	t.Helper()

	secret := eris.Secret{0: 0x45, 7: 0x52, 15: 0x49, 31: 0x53}
	capability, blocks, err := eris.EncodeRecommended(payload, secret)
	if err != nil {
		t.Fatal(err)
	}

	name, err := link.TextValue("payload-π.bin")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := link.ParseValue([]byte{
		0xa3,
		0x64, 'n', 'a', 'm', 'e', 0x66, 's', 'a', 'm', 'p', 'l', 'e',
		0x65, 'f', 'l', 'a', 'g', 's', 0x83, 0xf4, 0xf5, 0xf6,
		0x67, 'v', 'e', 'r', 's', 'i', 'o', 'n', 0x18, 0x2a,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAttributes := map[string]link.Value{
		"checksum-hint": link.BytesValue([]byte{0x00, 0xff, 0x80, 0x01}),
		"enabled":       link.BoolValue(true),
		"filename":      name,
		"metadata":      nested,
		"revision":      link.UintValue(42),
	}
	current := link.Link{
		Capability: capability,
		Size:       uint64(len(payload)),
		MIME:       mime,
		Attributes: wantAttributes,
	}

	var firstEncoding []byte
	for round := 1; round <= serializationRounds; round++ {
		encoded, marshalErr := current.MarshalBinary()
		if marshalErr != nil {
			t.Fatalf("round %d: marshal: %v", round, marshalErr)
		}
		if round == 1 {
			firstEncoding = slices.Clone(encoded)
		} else if !bytes.Equal(encoded, firstEncoding) {
			t.Fatalf("round %d: serialization changed", round)
		}

		parsed, parseErr := link.Parse(encoded)
		if parseErr != nil {
			t.Fatalf("round %d: parse: %v", round, parseErr)
		}
		// Destroy the transport buffer. The parsed link must own its values.
		clear(encoded)

		assertLinkMetadata(t, round, parsed, capability, uint64(len(payload)), mime, wantAttributes)
		decoded, decodeErr := eris.Decode(parsed.Capability, blocks)
		if decodeErr != nil {
			t.Fatalf("round %d: decode payload: %v", round, decodeErr)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("round %d: payload changed: got %d bytes, want %d", round, len(decoded), len(payload))
		}
		current = parsed
	}
}

func assertLinkMetadata(
	t *testing.T,
	round int,
	got link.Link,
	wantCapability eris.Capability,
	wantSize uint64,
	wantMIME string,
	wantAttributes map[string]link.Value,
) {
	t.Helper()
	if got.Capability != wantCapability {
		t.Fatalf("round %d: capability changed", round)
	}
	if got.Size != wantSize {
		t.Fatalf("round %d: size = %d, want %d", round, got.Size, wantSize)
	}
	if got.MIME != wantMIME {
		t.Fatalf("round %d: MIME = %q, want %q", round, got.MIME, wantMIME)
	}
	if len(got.Attributes) != len(wantAttributes) {
		t.Fatalf("round %d: got %d attributes, want %d", round, len(got.Attributes), len(wantAttributes))
	}
	for key, want := range wantAttributes {
		value, ok := got.Attributes[key]
		if !ok {
			t.Fatalf("round %d: attribute %q is missing", round, key)
		}
		if !bytes.Equal(value.RawCBOR(), want.RawCBOR()) {
			t.Fatalf("round %d: attribute %q changed: got %x, want %x", round, key, value.RawCBOR(), want.RawCBOR())
		}
	}
}

func patternedPayload(size int) []byte {
	payload := make([]byte, size)
	for index := range payload {
		payload[index] = byte((index*131 + index/7 + 29) % 256)
	}
	if size > 0 {
		copy(payload, fmt.Appendf(nil, "size=%d%c", size, 0))
	}
	return payload
}
