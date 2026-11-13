package coap

import (
	"context"
	"errors"
	"reflect"
	"testing"

	protocol "github.com/asciimoth/coaperation"
)

func TestAdvertisements(t *testing.T) {
	t.Parallel()
	store, err := NewStoreAdvertisement("/stores/main", "coap+tcp:")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewStoreAdvertisement("coap+tcp://peer.example/", "")
	if err != nil {
		t.Fatal(err)
	}
	encodedPeer, err := NewStoreAdvertisement("coap://p%65er.example/", "")
	if err != nil || encodedPeer.Target != "coap://p%65er.example/" {
		t.Fatalf("encoded peer = %+v, %v", encodedPeer, err)
	}
	encodedData, err := MarshalAdvertisements([]Advertisement{encodedPeer})
	if err != nil || string(encodedData) != `<coap://peer.example/>;if="http://purl.org/eris/coap"` {
		t.Fatalf("encoded peer document = %q, %v", encodedData, err)
	}
	proxy, err := NewProxyAdvertisement("/", "ocapn", "gnunet")
	if err != nil {
		t.Fatal(err)
	}
	advertisements := []Advertisement{store, peer, proxy}
	data, err := MarshalAdvertisements(advertisements)
	if err != nil {
		t.Fatal(err)
	}
	want := `</stores/main>;if="http://purl.org/eris/coap";base=coap+tcp:,<coap+tcp://peer.example/>;if="http://purl.org/eris/coap",</>;if="http://purl.org/eris/coap";rt=core.proxy;proxy-scheme=ocapn;proxy-scheme=gnunet`
	if string(data) != want {
		t.Errorf("MarshalAdvertisements() = %q\nwant %q", data, want)
	}
	parsed, err := ParseAdvertisements(data)
	if err != nil || !reflect.DeepEqual(parsed, advertisements) {
		t.Fatalf("ParseAdvertisements() = %+v, %v", parsed, err)
	}
	parsed[2].ProxySchemes[0] = "changed"
	if advertisements[2].ProxySchemes[0] != "ocapn" {
		t.Fatal("parsed proxy schemes alias input")
	}

	withUnrelated := append([]byte(`</other>;if="other",`), data...)
	parsed, err = ParseAdvertisements(withUnrelated)
	if err != nil || len(parsed) != 3 {
		t.Fatalf("unrelated link result = %+v, %v", parsed, err)
	}
	combinedInterface := []byte(`</combined>;if="other http://purl.org/eris/coap"`)
	if parsed, err = ParseAdvertisements(combinedInterface); err != nil || len(parsed) != 1 {
		t.Fatalf("combined interface result = %+v, %v", parsed, err)
	}

	invalidAds := []Advertisement{
		{}, {Target: "bad target"}, {Target: "/store?query"}, {Target: "/store#"},
		{Target: "/store#fragment"}, {Target: "/", Base: "http:"},
		{Target: "http://example/"}, {Target: "/", ProxySchemes: []string{"bad scheme"}},
	}
	for _, advertisement := range invalidAds {
		if _, err := MarshalAdvertisements([]Advertisement{advertisement}); !errors.Is(err, ErrInvalidAdvertisement) {
			t.Errorf("MarshalAdvertisements(%+v) error = %v", advertisement, err)
		}
	}
	if _, err := NewProxyAdvertisement("/"); !errors.Is(err, ErrInvalidAdvertisement) {
		t.Errorf("empty proxy error = %v", err)
	}
	if _, err := NewProxyAdvertisement("/", ""); !errors.Is(err, ErrInvalidAdvertisement) {
		t.Errorf("empty proxy scheme error = %v", err)
	}
	if _, err := NewStoreAdvertisement("bad target", ""); !errors.Is(err, ErrInvalidAdvertisement) {
		t.Errorf("bad target error = %v", err)
	}

	invalidDocuments := [][]byte{
		[]byte("not-link-format"),
		[]byte(`</>;if="http://purl.org/eris/coap";base=coap:;base=coap+tcp:`),
		[]byte(`</>;if="http://purl.org/eris/coap";proxy-scheme=ocapn`),
		[]byte(`</>;if="http://purl.org/eris/coap";rt=core.proxy`),
		[]byte(`<coap://example/?query>;if="http://purl.org/eris/coap"`),
	}
	for _, document := range invalidDocuments {
		if _, err := ParseAdvertisements(document); !errors.Is(err, ErrInvalidAdvertisement) {
			t.Errorf("ParseAdvertisements(%q) error = %v", document, err)
		}
	}
}

func TestAdvertisementResolve(t *testing.T) {
	t.Parallel()
	origin := mustStore(t, "coap://node.example/")
	tests := []struct {
		advertisement Advertisement
		want          string
	}{
		{Advertisement{Target: "/store"}, "coap://node.example/store"},
		{Advertisement{Target: "peer"}, "coap://node.example/.well-known/peer"},
		{Advertisement{Target: "/", Base: "coap+tcp:"}, "coap+tcp://node.example/"},
		{Advertisement{Target: "coap+tcp://peer.example/a"}, "coap+tcp://peer.example/a"},
		{Advertisement{Target: "coap://peer.example/a", Base: "coap+tcp:"}, "coap+tcp://peer.example/a"},
		{Advertisement{Target: "coap://p%65er.example/a"}, "coap://peer.example/a"},
	}
	for _, test := range tests {
		got, err := test.advertisement.Resolve(origin)
		if err != nil || got.String() != test.want {
			t.Errorf("Resolve(%+v) = %q, %v; want %q", test.advertisement, got.String(), err, test.want)
		}
	}
	if _, err := (Advertisement{}).Resolve(origin); !errors.Is(err, ErrInvalidAdvertisement) {
		t.Errorf("invalid advertisement error = %v", err)
	}
	if _, err := (Advertisement{Target: "/"}).Resolve(StoreURL{}); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("invalid origin error = %v", err)
	}
}

func TestAdvertisementResolveEscapedOrigin(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"example%2Fname", "exa%3Ample.net", "name%25zone", "[fe80::1%25eth0]"} {
		t.Run(host, func(t *testing.T) {
			origin := mustStore(t, "coap://"+host+":61616/old/path")
			for _, test := range []struct {
				advertisement Advertisement
				want          string
			}{
				{Advertisement{Target: "/store"}, "coap://" + host + ":61616/store"},
				{Advertisement{Target: "peer"}, "coap://" + host + ":61616/.well-known/peer"},
				{Advertisement{Target: "../a%2Fb", Base: "coap+tcp:"}, "coap+tcp://" + host + ":61616/a%2Fb"},
				{Advertisement{Target: "//peer.example/store"}, "coap://peer.example/store"},
				{Advertisement{Target: "coap+tcp://peer.example/store"}, "coap+tcp://peer.example/store"},
			} {
				got, err := test.advertisement.Resolve(origin)
				if err != nil || got.String() != test.want {
					t.Errorf("Resolve(%+v) = %q, %v; want %q", test.advertisement, got.String(), err, test.want)
				}
			}
		})
	}
}

func TestDiscoveryRequestResponse(t *testing.T) {
	t.Parallel()
	request := NewDiscoveryRequest()
	if request.Code != protocol.GET || request.Type != protocol.NonConfirmable || !reflect.DeepEqual(request.URIPath(), []string{".well-known", "core"}) {
		t.Fatalf("NewDiscoveryRequest() = %+v", request)
	}
	advertisement, _ := NewStoreAdvertisement("/", "coap+tcp:")
	handler := DiscoveryHandler{Advertisements: []Advertisement{advertisement}}
	response := serve(t, handler, request)
	parsed, err := ParseDiscoveryResponse(response)
	if err != nil || !reflect.DeepEqual(parsed, []Advertisement{advertisement}) {
		t.Fatalf("ParseDiscoveryResponse() = %+v, %v", parsed, err)
	}

	if got := serve(t, handler, &protocol.Message{Code: protocol.PUT, Options: request.Options}); got.Code != protocol.MethodNotAllowed {
		t.Errorf("PUT discovery = %s", got.Code)
	}
	invalidHop := request.Clone()
	invalidHop.Options = append(invalidHop.Options, protocol.UintOption(protocol.OptionHopLimit, 0))
	sortOptions(invalidHop.Options)
	if got := serve(t, handler, invalidHop); got.Code != protocol.BadRequest {
		t.Errorf("invalid discovery Hop-Limit = %s", got.Code)
	}
	multicastQuery := request.Clone()
	multicastQuery.Multicast = true
	multicastQuery.Options = append(multicastQuery.Options, protocol.StringOption(protocol.OptionURIQuery, "if=example"))
	response, err = handler.ServeCOAP(context.Background(), multicastQuery)
	if err != nil || response != nil {
		t.Errorf("multicast filtered response = %+v, %v", response, err)
	}
	if got := serve(t, handler, nil); got.Code != protocol.BadRequest {
		t.Errorf("nil discovery = %s", got.Code)
	}
	if got := serve(t, handler, &protocol.Message{Code: protocol.GET}); got.Code != protocol.NotFound {
		t.Errorf("unmatched discovery = %s", got.Code)
	}
	sentinel := errors.New("next")
	next := DiscoveryHandler{Next: protocol.HandlerFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) { return nil, sentinel })}
	if _, err := next.ServeCOAP(context.Background(), &protocol.Message{Code: protocol.GET}); !errors.Is(err, sentinel) {
		t.Errorf("Next error = %v", err)
	}
	broken := DiscoveryHandler{Advertisements: []Advertisement{{}}}
	if got := serve(t, broken, request); got.Code != protocol.InternalServerError {
		t.Errorf("invalid document = %s", got.Code)
	}

	responses := []*protocol.Message{
		nil,
		{Code: protocol.NotFound},
		{Code: protocol.Content},
		{Code: protocol.Content, Options: []protocol.Option{protocol.UintOption(protocol.OptionContentFormat, 0)}},
	}
	for _, candidate := range responses {
		if _, err := ParseDiscoveryResponse(candidate); err == nil {
			t.Errorf("ParseDiscoveryResponse(%+v) succeeded", candidate)
		}
	}
}

func TestDiscoveryRejectsPartialResponses(t *testing.T) {
	t.Parallel()
	payload, err := MarshalAdvertisements([]Advertisement{{Target: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []uint32{8, 16, 7} {
		response := &protocol.Message{Code: protocol.Content, Payload: payload, Options: []protocol.Option{
			protocol.UintOption(protocol.OptionContentFormat, protocol.ContentFormatLinkFormat),
			protocol.UintOption(protocol.OptionBlock2, value),
		}}
		if _, err := ParseDiscoveryResponse(response); err == nil {
			t.Fatalf("partial or invalid Block2 %d accepted", value)
		}
	}
}

func TestDiscoveryResponseBodySize(t *testing.T) {
	t.Parallel()
	payload, err := MarshalAdvertisements([]Advertisement{{Target: "/"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		options []protocol.Option
		valid   bool
	}{
		{"no size", nil, true},
		{"exact size", []protocol.Option{protocol.UintOption(protocol.OptionSize2, uint32(len(payload)))}, true},
		{"truncated", []protocol.Option{protocol.UintOption(protocol.OptionSize2, uint32(len(payload)+1))}, false},
		{"excess data", []protocol.Option{protocol.UintOption(protocol.OptionSize2, uint32(len(payload)-1))}, false},
		{"empty declared", []protocol.Option{protocol.UintOption(protocol.OptionSize2, 0)}, false},
		{"block too small", []protocol.Option{protocol.UintOption(protocol.OptionBlock2, 0)}, false},
		{"complete block", []protocol.Option{protocol.UintOption(protocol.OptionBlock2, 6)}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &protocol.Message{Code: protocol.Content, Payload: payload, Options: append([]protocol.Option{
				protocol.UintOption(protocol.OptionContentFormat, protocol.ContentFormatLinkFormat),
			}, test.options...)}
			ads, err := ParseDiscoveryResponse(response)
			if test.valid {
				if err != nil || len(ads) != 1 {
					t.Fatalf("complete response: advertisements = %v, error = %v", ads, err)
				}
			} else if !errors.Is(err, ErrInvalidAdvertisement) {
				t.Fatalf("invalid response: error = %v", err)
			}
		})
	}
}

func TestDiscoveryDocumentLimit(t *testing.T) {
	prefix := []byte(`</>;if="http://purl.org/eris/coap"`)
	data := make([]byte, MaxDiscoveryDocumentSize+1)
	copy(data, prefix)
	for index := len(prefix); index < len(data); index++ {
		data[index] = ' '
	}
	if ads, err := ParseAdvertisements(data[:MaxDiscoveryDocumentSize]); err != nil || len(ads) != 1 {
		t.Fatalf("document at limit: %v, %v", ads, err)
	}
	if _, err := ParseAdvertisements(data); !errors.Is(err, ErrResponseTooLarge) || !errors.Is(err, ErrInvalidAdvertisement) {
		t.Fatalf("oversized document: %v", err)
	}
	response := &protocol.Message{Code: protocol.Content, Options: []protocol.Option{
		protocol.UintOption(protocol.OptionContentFormat, protocol.ContentFormatLinkFormat),
	}, Payload: data}
	if _, err := ParseDiscoveryResponse(response); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized discovery response: %v", err)
	}
}

func TestDiscoveryRejectsRequestBodies(t *testing.T) {
	for _, request := range []*protocol.Message{
		addOption(NewDiscoveryRequest(), protocol.NewOption(protocol.OptionBlock1, nil)),
		addOption(NewDiscoveryRequest(), protocol.NewOption(protocol.OptionBlock1, []byte{7})),
		addOption(NewDiscoveryRequest(), protocol.NewOption(protocol.OptionBlock2, nil)),
		addOption(NewDiscoveryRequest(), protocol.NewOption(protocol.OptionBlock2, []byte{7})),
	} {
		if response := serve(t, DiscoveryHandler{}, request); response.Code != protocol.BadOption {
			t.Fatalf("unsupported block option response = %s", response.Code)
		}
	}
	request := NewDiscoveryRequest()
	request.Payload = []byte("unexpected")
	if response := serve(t, DiscoveryHandler{}, request); response.Code != protocol.BadRequest {
		t.Fatalf("GET body response = %s", response.Code)
	}
}
