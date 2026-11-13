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

func FuzzAdvertisementResolve(f *testing.F) {
	f.Add("coap://example/", "peer", "")
	f.Add("coap://example%2Fname:61616/", "/store", "coap+tcp:")
	f.Add("coap://exa%3Ample/", "../a%2Fb", "")
	f.Add("coap://name%25zone/", "//peer.example/store", "")
	f.Add("coap://[fe80::1%25eth0]/", "coap://peer.example/", "")
	f.Fuzz(func(t *testing.T, originText, target, base string) {
		origin, err := ParseStoreURL(originText)
		if err != nil {
			return
		}
		advertisement, err := NewStoreAdvertisement(target, base)
		if err != nil {
			return
		}
		resolved, err := advertisement.Resolve(origin)
		if err != nil {
			return
		}
		reparsed, err := ParseStoreURL(resolved.String())
		if err != nil || !reflect.DeepEqual(reparsed, resolved) {
			t.Fatalf("resolved URL cannot round trip: %+v, %v", resolved, err)
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
		options := []protocol.Option{protocol.StringOption(protocol.OptionURIPath, BlocksResource)}
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
			protocol.StringOption(protocol.OptionURIPath, BlocksResource),
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
	f.Add(uint8(protocol.GET), []byte(nil), []byte{6}, []byte(nil), false)
	f.Add(uint8(protocol.GET), []byte(nil), []byte(nil), []byte(nil), false)
	f.Fuzz(func(t *testing.T, code uint8, block1, block2, payload []byte, duplicate bool) {
		if len(payload) > int(eris.BlockSize32KiB)+1 {
			payload = payload[:int(eris.BlockSize32KiB)+1]
		}
		blocks := make(eris.BlockSet)
		download, reference := makeBlock(int(eris.BlockSize32KiB), 7)
		blocks[reference] = download
		handler, err := (Handler{Blocks: blocks, AllowPut: true, MaxTransfers: 2}).BlockWise(protocol.BlockWiseConfig{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			request := &protocol.Message{
				Type: protocol.Confirmable, Code: protocol.Code(code), Payload: payload,
				Options: []protocol.Option{
					protocol.StringOption(protocol.OptionURIPath, BlocksResource),
					protocol.NewOption(65000, []byte{byte(i % 3)}),
				},
			}
			if request.Code == protocol.GET {
				request.Options = append(request.Options, protocol.NewOption(protocol.OptionURIQuery, reference[:]))
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
			if response.Code == protocol.Content {
				option, present := response.Option(protocol.OptionBlock2)
				if !present {
					t.Fatal("large download has no Block2 option")
				}
				block, err := protocol.ParseBlock(option.Value)
				if err != nil {
					t.Fatal(err)
				}
				start := block.Offset()
				end := start + uint64(len(response.Payload))
				if end > uint64(len(download)) || len(response.Payload) > block.Size ||
					block.More != (end < uint64(len(download))) ||
					!bytes.Equal(response.Payload, download[start:end]) {
					t.Fatal("download fragment does not match the stored block")
				}
			}
		}
		for ref, block := range blocks {
			if err := eris.VerifyBlock(block, ref); err != nil {
				t.Fatalf("invalid stored block: %v", err)
			}
		}
	})
}

// A peer controls response codes, tokens, options, and bodies. Successful
// reads must still match the requested block, and successful writes must
// return the reference of the supplied block.
func FuzzClientResponse(f *testing.F) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 9)
	f.Add(uint8(protocol.Content), uint16(protocol.OptionContentFormat), []byte{42}, block, []byte(nil), false)
	f.Add(uint8(protocol.Created), uint16(protocol.OptionSize2), []byte{32}, ref[:], []byte(nil), false)
	f.Add(uint8(protocol.Content), uint16(protocol.OptionBlock2), []byte{0x0e}, block, []byte(nil), false)
	f.Add(uint8(protocol.Content), uint16(protocol.OptionContentFormat), []byte{42}, block, []byte(nil), true)
	f.Fuzz(func(t *testing.T, code uint8, number uint16, value, payload, token []byte, duplicate bool) {
		response := &protocol.Message{
			Code: protocol.Code(code), Token: token, Payload: payload,
			Options: []protocol.Option{{Number: protocol.OptionNumber(number), Value: value}},
		}
		if duplicate {
			response.Options = append(response.Options, response.Options[0])
		}
		calls := 0
		requester := RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			calls++
			// Repeating the same fragment must not keep a transfer alive.
			if calls > 2 {
				t.Fatal("client did not stop a repeated response")
			}
			return response, nil
		})
		store, err := ParseStoreURL("coap://example/")
		if err != nil {
			t.Fatal(err)
		}
		client, err := NewClient(requester, store, ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.GetBlock(ref, 0)
		if err == nil && !bytes.Equal(got, block) {
			t.Fatal("client accepted an incorrect block")
		}
		calls = 0
		returned, err := client.PutBlock(block, 0, ref)
		if err == nil && returned != ref {
			t.Fatal("client accepted an incorrect write reference")
		}
	})
}
