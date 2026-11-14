package volume

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/iotest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cbor"
	"github.com/asciimoth/goeris/internal/cborwire"
)

// Compare the streaming lookup with the existing in-memory decoders. This also
// checks format detection, full-container validation, and duplicate map keys.
func FuzzCBORLookup(f *testing.F) {
	block := bytes.Repeat([]byte{42}, int(eris.BlockSize1KiB))
	ref, _ := eris.ReferenceOf(block)
	blockElement, _ := cbor.NewBlock(block)
	mapElement, _ := cbor.NewBlockMap(eris.BlockSet{ref: block})
	largeElement, _ := cbor.NewBlock(make([]byte, eris.BlockSize32KiB))
	capability, _, _ := eris.Encode([]byte("metadata"), eris.Secret{}, eris.BlockSize1KiB)
	capElement, _ := cbor.NewCapability(capability)
	for _, elements := range [][]cbor.Element{
		nil, {blockElement}, {mapElement}, {capElement}, {largeElement},
		{capElement, cbor.NewReference(ref), blockElement, mapElement},
	} {
		for _, marshal := range []func([]cbor.Element) ([]byte, error){
			cbor.Marshal, cbor.MarshalUntagged, cbor.MarshalSelfDescribed,
			cbor.MarshalSequence, cbor.MarshalUntaggedSequence,
		} {
			data, _ := marshal(elements)
			f.Add(data)
		}
	}
	// Exercise valid and invalid structures that random byte mutations are
	// unlikely to build, especially maps whose hashes must remain valid.
	mapData, _ := cbor.MarshalUntaggedSequence([]cbor.Element{mapElement})
	entry := mapData[1:]
	f.Add(append(append([]byte{0xbf}, entry...), 0xff))
	f.Add(append(append([]byte{0xa2}, entry...), entry...)) // Duplicate key.
	f.Add(append(append(append([]byte{0xbf}, entry...), entry...), 0xff))
	chunked := cborwire.AppendHead([]byte{0x5f}, 2, 512)
	chunked = append(chunked, block[:512]...)
	chunked = cborwire.AppendHead(chunked, 2, 512)
	chunked = append(chunked, block[512:]...)
	chunked = append(chunked, 0xff)
	f.Add(chunked)
	f.Add(append(append([]byte{0x9f}, chunked...), 0xff))
	// Non-preferred tag widths remain valid under the existing decoder rules.
	wideTag := []byte{0xdb, 0, 0, 0, 0, 0x65, 0x72, 0x69, 0x73}
	f.Add(append(append(bytes.Clone(wideTag), 0x81), chunked...))
	f.Add(append(append(bytes.Clone(wideTag), 0xf6), chunked...))
	for _, malformed := range [][]byte{
		{0xff}, {0x81}, {0x81, 0x80}, {0x9f}, {0xa1}, {0xbf},
		{0x5f, 0x5f, 0xff, 0xff}, {0x5c}, {0xc0}, {0xdf},
		{0xd9, 0xd9, 0xf7, 0x80},                   // Self-described array without ERIS tag.
		{0xda, 0x65, 0x72, 0x69, 0x73, 0xf6, 0x80}, // Array in a sequence.
	} {
		f.Add(malformed)
	}
	f.Add([]byte{0x9f, 0xbf, 0xff, 0xff})
	f.Add([]byte{0x5f, 0x58, 32, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff})
	f.Add([]byte{0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		elements, wantErr := cbor.Unmarshal(data)
		if wantErr != nil {
			elements, wantErr = cbor.UnmarshalSequence(data)
		}
		got, err := findCBOR(context.Background(), bytes.NewReader(data), ref)
		if wantErr != nil {
			if got != nil || !errors.Is(err, cbor.ErrInvalidEncoding) {
				t.Fatalf("invalid container returned %d bytes, %v", len(got), err)
			}
			return
		}
		var want []byte
		for _, element := range elements {
			if block, ok := element.Block(); ok {
				actual, _ := eris.ReferenceOf(block)
				if actual == ref {
					want = block
					break
				}
			} else if blocks, ok := element.BlockMap(); ok && blocks[ref] != nil {
				want = blocks[ref]
				break
			}
		}
		if want == nil {
			if got != nil || !errors.Is(err, eris.ErrMissingBlock) {
				t.Fatalf("missing block returned %d bytes, %v", len(got), err)
			}
		} else if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("valid block returned %d bytes, %v", len(got), err)
		}
	})
}

func TestCBORRejectsLengthBeforeReadingBody(t *testing.T) {
	for _, header := range [][]byte{
		{0x5a, 0, 1, 0, 0}, // A 64 KiB byte string cannot be an ERIS element.
		{0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0x5f, 0x5a, 0, 1, 0, 0}, // Oversized chunk.
		{0xa1, 0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0xa1, 0x58, 33},             // A map key is at most 32 bytes.
		{0xa1, 0x5f, 0x58, 33},       // The same bound applies to chunks.
		{0xd9, 0x01, 0x14, 0x58, 67}, // A capability is at most 66 bytes.
		{0xd9, 0x01, 0x14, 0x5f, 0x58, 67},
		// A sequence of valid chunks must obey the total size limit too.
		append(append([]byte{0xa1, 0x5f, 0x58, 32}, make([]byte, 32)...), 0x41),
		append(append([]byte{0xd9, 0x01, 0x14, 0x5f, 0x58, 66}, make([]byte, 66)...), 0x41),
	} {
		reader := io.MultiReader(bytes.NewReader(header), failReader{})
		if block, err := findCBOR(context.Background(), reader, eris.Reference{}); block != nil || !errors.Is(err, cbor.ErrInvalidEncoding) {
			t.Fatalf("header %x: %d bytes, %v", header, len(block), err)
		}
	}
}

func TestCBORChunkedMetadataAndShortReads(t *testing.T) {
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB, Root: eris.Pair{Reference: ref}}
	binary, err := capability.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	chunked := func(data []byte) []byte {
		encoded := []byte{0x5f, 0x40} // Empty chunks are valid.
		for _, b := range data {
			encoded = append(encoded, 0x41, b)
		}
		return append(encoded, 0x40, 0xff)
	}
	data := append([]byte{0xd9, 0x01, 0x14}, chunked(binary)...)
	data = append(data, chunked(ref[:])...)
	data = append(data, 0xbf)
	data = append(data, chunked(ref[:])...)
	data = append(data, chunked(block)...)
	data = append(data, 0xff)
	for _, reader := range []io.Reader{
		bytes.NewReader(data),
		iotest.OneByteReader(bytes.NewReader(data)),
		iotest.HalfReader(bytes.NewReader(data)),
		iotest.DataErrReader(bytes.NewReader(data)),
	} {
		got, err := findCBOR(context.Background(), reader, ref)
		if err != nil || !bytes.Equal(got, block) {
			t.Fatalf("chunked metadata with %T: %d bytes, %v", reader, len(got), err)
		}
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) {
	return 0, errors.New("read past invalid header")
}

func TestCBORCancellationDuringDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Empty indefinite-string chunks force parser work without block output.
	reader := io.MultiReader(bytes.NewReader(append([]byte{0x5f}, bytes.Repeat([]byte{0x40}, 8192)...)), cancelReader{cancel})
	if got, err := findCBOR(ctx, reader, eris.Reference{}); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parser: %d bytes, %v", len(got), err)
	}
}

func TestCBORCancellationDuringRead(t *testing.T) {
	for _, prefix := range [][]byte{
		{0x59},       // Cancellation while reading a multi-byte length.
		{0x59, 4, 0}, // Cancellation while reading the block body.
	} {
		ctx, cancel := context.WithCancel(context.Background())
		reader := io.MultiReader(bytes.NewReader(prefix), &cancelOnceReader{cancel: cancel}, failReader{})
		got, err := findCBOR(ctx, reader, eris.Reference{})
		cancel()
		if got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation after prefix %x: %d bytes, %v", prefix, len(got), err)
		}
	}
}

// A short read can complete after cancellation. No further read may start.
type cancelOnceReader struct{ cancel context.CancelFunc }

func (r *cancelOnceReader) Read(p []byte) (int, error) {
	if r.cancel == nil {
		return 0, io.EOF
	}
	r.cancel()
	r.cancel = nil
	p[0] = 0
	return 1, nil
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read(p []byte) (int, error) {
	r.cancel()
	p[0] = 0x40
	return 1, nil
}

func TestCBORReadErrorAfterMatch(t *testing.T) {
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	element, _ := cbor.NewBlock(block)
	data, _ := cbor.MarshalUntaggedSequence([]cbor.Element{element})
	if got, err := findCBOR(context.Background(), io.MultiReader(bytes.NewReader(data), failReader{}), ref); got != nil || err == nil {
		t.Fatalf("returned a match before reading the complete container: %d, %v", len(got), err)
	}
}
