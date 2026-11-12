package coap

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

func FuzzParseStoreURL(f *testing.F) {
	for _, seed := range []string{
		"", "coap://example/", "coap+tcp://example/a%20b", "coap://[::1]:5683/",
		"http://example/", "coap://user@example/", "coap://example/a?query",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		store, err := ParseStoreURL(text)
		if err != nil {
			return
		}
		canonical := store.String()
		if canonical == "" {
			t.Fatal("successful parse produced an empty URL")
		}
		reparsed, err := ParseStoreURL(canonical)
		if err != nil || reparsed.String() != canonical || !reflect.DeepEqual(reparsed.Path(), store.Path()) {
			t.Fatalf("canonical round trip = %q, %v", reparsed.String(), err)
		}
	})
}

func FuzzReferenceQuery(f *testing.F) {
	_, ref := makeBlock(int(eris.BlockSize1KiB), 1)
	f.Add(ref[:])
	f.Add([]byte(ref.String()))
	f.Add([]byte("bad"))
	f.Fuzz(func(t *testing.T, query []byte) {
		message := &protocol.Message{Options: []protocol.Option{protocol.NewOption(protocol.OptionURIQuery, query)}}
		parsed, err := parseReferenceQuery(message)
		if err != nil {
			return
		}
		if len(query) == len(parsed) {
			if !bytes.Equal(parsed[:], query) {
				t.Fatal("raw reference changed")
			}
			return
		}
		if parsed.String() != string(query) {
			t.Fatal("Base32 reference is not canonical")
		}
	})
}

func FuzzAdvertisements(f *testing.F) {
	for _, seed := range [][]byte{
		{}, []byte(`</>;if="http://purl.org/eris/coap";base=coap+tcp:`),
		[]byte(`</>;if="http://purl.org/eris/coap";rt=core.proxy;proxy-scheme=ocapn`),
		[]byte(`<coap://example:5683/store/>;if="http://purl.org/eris/coap"`),
		[]byte("invalid"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		advertisements, err := ParseAdvertisements(data)
		if err != nil {
			return
		}
		encoded, err := MarshalAdvertisements(advertisements)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := ParseAdvertisements(encoded)
		if err != nil {
			t.Fatalf("round trip = %+v, %v", roundTrip, err)
		}
		// Absolute targets are canonicalized during encoding. The canonical
		// document must be stable even when the input uses another spelling.
		reencoded, err := MarshalAdvertisements(roundTrip)
		if err != nil || !bytes.Equal(reencoded, encoded) {
			t.Fatalf("canonical round trip = %q, %v; want %q", reencoded, err, encoded)
		}
	})
}

func FuzzHandler(f *testing.F) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 2)
	f.Add(uint8(protocol.GET), []byte(ref.String()), uint32(1024), []byte(nil))
	f.Add(uint8(protocol.PUT), []byte(nil), uint32(1024), bytes.Repeat([]byte{1}, 1024))
	f.Add(uint8(protocol.PUT), ref[:], uint32(1024), block)
	f.Add(uint8(255), []byte("bad"), uint32(0), []byte("bad"))
	f.Fuzz(func(t *testing.T, code uint8, query []byte, size uint32, payload []byte) {
		if len(payload) > int(eris.BlockSize32KiB)+1 {
			payload = payload[:int(eris.BlockSize32KiB)+1]
		}
		options := []protocol.Option{protocol.StringOption(protocol.OptionURIPath, blocksSegment)}
		if query != nil {
			options = append(options, protocol.NewOption(protocol.OptionURIQuery, query))
		}
		if size != 0 {
			options = append(options, protocol.UintOption(protocol.OptionSize1, size))
		}
		sortOptions(options)
		request := &protocol.Message{Code: protocol.Code(code), Options: options, Payload: append([]byte(nil), payload...)}
		handler := Handler{Blocks: make(eris.BlockSet), AllowPut: true}
		response, err := handler.ServeCOAP(context.Background(), request)
		if err != nil || response == nil || !response.Code.IsResponse() {
			t.Fatalf("response = %+v, %v", response, err)
		}
		if response.Code == protocol.Created {
			actual, hashErr := eris.ReferenceOf(payload)
			if hashErr != nil || !bytes.Equal(response.Payload, actual[:]) {
				t.Fatalf("PUT reference = %x, hash error %v", response.Payload, hashErr)
			}
		}
	})
}

func FuzzPrepareForward(f *testing.F) {
	f.Add([]byte{1})
	f.Add([]byte{16})
	f.Add([]byte{0})
	f.Add([]byte{1, 0})
	f.Fuzz(func(t *testing.T, value []byte) {
		request := &protocol.Message{Code: protocol.GET, Options: []protocol.Option{
			protocol.StringOption(protocol.OptionURIPath, blocksSegment),
			protocol.NewOption(protocol.OptionHopLimit, value),
		}}
		forwarded, err := PrepareForward(request)
		if err != nil {
			return
		}
		before, beforeErr := (protocol.Option{Number: protocol.OptionHopLimit, Value: value}).Uint32()
		after, present, afterErr := oneUintOption(forwarded, protocol.OptionHopLimit)
		if beforeErr != nil || afterErr != nil || !present || before < 2 || after != before-1 {
			t.Fatalf("Hop-Limit %d became %d", before, after)
		}
	})
}

func FuzzBlockWiseHandler(f *testing.F) {
	f.Add(uint8(protocol.PUT), []byte{0x0d}, []byte(nil), bytes.Repeat([]byte{1}, 512), false)
	f.Add(uint8(protocol.PUT), []byte{0x15}, []byte(nil), bytes.Repeat([]byte{1}, 512), false)
	f.Add(uint8(protocol.GET), []byte(nil), []byte{6}, []byte(nil), true)
	f.Fuzz(func(t *testing.T, code uint8, block1, block2, payload []byte, duplicate bool) {
		if len(payload) > int(eris.BlockSize32KiB)+1 {
			payload = payload[:int(eris.BlockSize32KiB)+1]
		}
		blocks := make(eris.BlockSet)
		handler, err := (Handler{Blocks: blocks, AllowPut: true, MaxTransfers: 2}).BlockWise(protocol.BlockWiseConfig{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			request := &protocol.Message{
				Type: protocol.Confirmable, Code: protocol.Code(code), Payload: payload,
				Options: []protocol.Option{
					protocol.StringOption(protocol.OptionURIPath, blocksSegment),
					protocol.NewOption(65000, []byte{byte(i % 3)}),
				},
			}
			for _, option := range []protocol.Option{
				protocol.NewOption(protocol.OptionBlock1, block1),
				protocol.NewOption(protocol.OptionBlock2, block2),
			} {
				if len(option.Value) > 0 {
					request.Options = append(request.Options, option)
					if duplicate {
						request.Options = append(request.Options, option)
					}
				}
			}
			sortOptions(request.Options)
			response, err := handler.ServeCOAP(context.Background(), request)
			if err != nil || response == nil || !response.Code.IsResponse() {
				t.Fatalf("response = %+v, %v", response, err)
			}
			if len(handler.transfers) > 2 {
				t.Fatal("transfer state exceeded the configured limit")
			}
		}
		for ref, block := range blocks {
			if err := eris.VerifyBlock(block, ref); err != nil {
				t.Fatalf("invalid stored block: %v", err)
			}
		}
	})
}
