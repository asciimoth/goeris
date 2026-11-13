package link

import (
	"bytes"
	"strings"
	"testing"
)

func BenchmarkParseManyTextChunks(b *testing.B) {
	base := Link{Capability: testCapability()}
	encoded, err := base.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	data := append([]byte(nil), encoded[:len(encoded)-2]...)
	data = append(data, 0x7f)
	data = append(data, bytes.Repeat(appendText(nil, strings.Repeat("x", 1024)), 1024)...)
	data = append(data, 0xff, 0xa0)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		parsed, err := Parse(data)
		if err != nil || len(parsed.MIME) != 1<<20 {
			b.Fatalf("Parse: MIME length %d, error %v", len(parsed.MIME), err)
		}
	}
}

// Large attributes in a rejected link must not require a second payload copy.
func BenchmarkParseLargeAttribute(b *testing.B) {
	link := Link{
		Capability: testCapability(),
		Attributes: map[string]Value{"payload": BytesValue(make([]byte, 1<<20))},
	}
	encoded, err := link.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	for _, valid := range []bool{true, false} {
		name, data := "valid", encoded
		if !valid {
			name, data = "trailing-byte", append(encoded[:len(encoded):len(encoded)], 0)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := Parse(data); (err == nil) != valid {
					b.Fatalf("Parse: %v", err)
				}
			}
		})
	}
}

// A bad final chunk must not cause a copy of the preceding text payload.
func BenchmarkParseChunkedText(b *testing.B) {
	base := Link{Capability: testCapability()}
	encoded, err := base.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	prefix := encoded[:len(encoded)-2] // Keep the capability and size.
	chunk := appendText(nil, strings.Repeat("x", 1<<20))
	for _, valid := range []bool{true, false} {
		name, last := "valid", byte(0xff)
		if !valid {
			name, last = "invalid-final-chunk", 0
		}
		data := append([]byte(nil), prefix...)
		data = append(data, 0x7f)
		data = append(data, chunk...)
		data = append(data, last, 0xa0)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := Parse(data); (err == nil) != valid {
					b.Fatalf("Parse: %v", err)
				}
			}
		})
	}
}

// Invalid fields after MIME must not cause a copy of a large text payload.
func BenchmarkParseRejectedMIME(b *testing.B) {
	encoded, err := (Link{Capability: testCapability()}).MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	for _, chunked := range []bool{false, true} {
		name := "definite"
		mime := appendText(nil, strings.Repeat("x", 1<<20))
		if chunked {
			name = "chunked"
			mime = append(append([]byte{0x7f}, mime...), 0xff)
		}
		data := append(bytes.Clone(encoded[:len(encoded)-2]), mime...)
		data = append(data, 0xa0, 0) // A valid empty map followed by extra data.
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := Parse(data); err != ErrInvalidLink {
					b.Fatalf("Parse: %v", err)
				}
			}
		})
	}
}
