package coap

import (
	"bytes"
	"context"
	"errors"
	"net"
	neturl "net/url"
	"reflect"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	"golang.org/x/crypto/blake2b"
)

func makeBlock(size int, value byte) ([]byte, eris.Reference) {
	block := bytes.Repeat([]byte{value}, size)
	return block, eris.Reference(blake2b.Sum256(block))
}

func mustStore(t *testing.T, text string) StoreURL {
	t.Helper()
	store, err := ParseStoreURL(text)
	if err != nil {
		t.Fatalf("ParseStoreURL(%q): %v", text, err)
	}
	return store
}

func optionValue(t *testing.T, message *protocol.Message, number protocol.OptionNumber) []byte {
	t.Helper()
	values := message.OptionValues(number)
	if len(values) != 1 {
		t.Fatalf("option %d values = %q", number, values)
	}
	return values[0]
}

func TestStoreURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		path  []string
	}{
		{"coap://EXAMPLE.net", "coap://example.net/", nil},
		{"coap://example.net:5683/", "coap://example.net/", nil},
		{"coap+tcp://example.net/a/b", "coap+tcp://example.net/a/b", []string{"a", "b"}},
		{"coap://example.net/a%20b/a%2Fb", "coap://example.net/a%20b/a%2Fb", []string{"a b", "a/b"}},
		{"coap://exa%6Dple.net/a/", "coap://example.net/a", []string{"a"}},
		{"coap://exa%3Ample.net/", "coap://exa%3Ample.net/", nil},
		{"coap://example.net/a//b", "coap://example.net/a//b", []string{"a", "", "b"}},
		{"coap://example.net/a/../b", "coap://example.net/b", []string{"b"}},
		{"coap://127.0.0.1/", "coap://127.0.0.1/", nil},
		{"coap://[2001:db8::1]:7000/x", "coap://[2001:db8::1]:7000/x", []string{"x"}},
		{"coap://[fe80::1%25eth0]/", "coap://[fe80::1%25eth0]/", nil},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			store, err := ParseStoreURL(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := store.String(); got != test.want {
				t.Errorf("String() = %q, want %q", got, test.want)
			}
			if !reflect.DeepEqual(store.Path(), test.path) {
				t.Errorf("Path() = %q, want %q", store.Path(), test.path)
			}
			path := store.Path()
			path = append(path, "changed")
			if reflect.DeepEqual(store.Path(), path) {
				t.Fatal("Path returned aliased storage")
			}
			if store.Target().Scheme != store.Scheme() || store.Target().Host != store.Host() || store.Target().Port != store.Port() {
				t.Fatal("Target does not match accessors")
			}
		})
	}

	invalid := []string{
		"", "http://example.net/", "coap:/x", "coap:///x",
		"coap://user@example.net/", "coap://example.net:0/", "coap://example.net:99999/",
		"coap://example.net/a?x", "coap://example.net/a?", "coap://example.net/a#x",
		"coap://example.net/a#", "coap://[fe80::1%25]/",
		"coap://[example%25zone]/", "coap://example.net/%FF",
		"coap://exa%00mple.net/", "coap://exa%0Ample.net/", "coap://exa%C2%85mple.net/",
	}
	for _, text := range invalid {
		if store, err := ParseStoreURL(text); !errors.Is(err, ErrInvalidStoreURL) || store.String() != "" {
			t.Errorf("ParseStoreURL(%q) = %q, %v", text, store.String(), err)
		}
	}

	store, err := NewStoreURL(protocol.Target{Scheme: "coap+tcp", Host: "::1", Port: 5683}, []string{"a/b"})
	if err != nil || store.String() != "coap+tcp://[::1]/a%2Fb" {
		t.Fatalf("NewStoreURL() = %q, %v", store.String(), err)
	}
	for _, target := range []protocol.Target{{}, {Scheme: "http", Host: "x", Port: 1}, {Scheme: "coap", Port: 1}, {Scheme: "coap", Host: "x"}} {
		if _, err := NewStoreURL(target, nil); !errors.Is(err, ErrInvalidStoreURL) {
			t.Errorf("NewStoreURL(%+v) error = %v", target, err)
		}
	}
	if _, err := NewStoreURL(protocol.Target{Scheme: "coap", Host: "bad\x00host", Port: 5683}, nil); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("host with control byte: error = %v", err)
	}
	if _, err := NewStoreURL(protocol.Target{Scheme: "coap", Host: "x", Port: 1}, []string{"."}); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("invalid path error = %v", err)
	}
	if store, err := NewStoreURL(protocol.Target{Scheme: "coap", Host: "example", Port: 7000}, nil); err != nil || store.String() != "coap://example:7000/" {
		t.Errorf("non-default target = %q, %v", store.String(), err)
	}
	if store, err := NewStoreURL(protocol.Target{Scheme: "coap", Host: "example", Port: 5683}, []string{"path"}); err != nil || store.String() != "coap://example/path" {
		t.Errorf("default target with path = %q, %v", store.String(), err)
	}
	if store, err := NewStoreURL(protocol.Target{Scheme: "coap", Host: "example", Port: 5683}, []string{"a", "", "b"}); err != nil || store.String() != "coap://example/a//b" {
		t.Errorf("target with empty segment = %q, %v", store.String(), err)
	}
	if got := (StoreURL{}).String(); got != "" {
		t.Errorf("zero String() = %q", got)
	}
}

func TestInternalValidationErrors(t *testing.T) {
	t.Parallel()
	_, ref := makeBlock(int(eris.BlockSize1KiB), 5)
	badStore := StoreURL{}
	if _, err := NewGetRequest(badStore, ref, RequestConfig{}); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("GET store error = %v", err)
	}
	block, blockRef := makeBlock(int(eris.BlockSize1KiB), 6)
	if _, err := NewPutRequest(badStore, block, RequestConfig{}); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("PUT store error = %v", err)
	}
	if _, err := NewPutRequest(mustStore(t, "coap://example/"), block, RequestConfig{ProxyURI: "bad", HopLimit: 1}); !errors.Is(err, ErrInvalidProxyURI) {
		t.Errorf("PUT routing error = %v", err)
	}
	if err := (StoreURL{scheme: "coap", host: "x", port: 1, path: []string{""}}).validate(); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("path validation error = %v", err)
	}
	if _, err := badStore.requestOptions(); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("requestOptions error = %v", err)
	}
	if _, err := storeFromURL(nil); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("nil store URL error = %v", err)
	}
	parsed, _ := neturl.Parse("http://example/")
	if _, err := storeFromURL(parsed); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("unsupported resolved URL error = %v", err)
	}
	if err := validateContentFormat(nil, ContentFormatOctetStream); err == nil {
		t.Fatal("nil content format response succeeded")
	}
	badClient := &Client{requester: RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) { return nil, nil }), store: badStore}
	if _, err := badClient.GetBlock(ref); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("bad client GET error = %v", err)
	}
	if _, err := badClient.PutBlock(block, blockRef); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("bad client PUT error = %v", err)
	}
}

func TestRequestConstruction(t *testing.T) {
	t.Parallel()
	store := mustStore(t, "coap+tcp://example.net:7000/a")
	block, ref := makeBlock(int(eris.BlockSize1KiB), 3)

	request, err := NewGetRequest(store, ref, RequestConfig{SizeHint: eris.BlockSize1KiB, ProxyURI: "ocapn://peer/store", HopLimit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if request.Code != protocol.GET || request.Type != protocol.Confirmable || len(request.Payload) != 0 {
		t.Fatalf("GET request = %+v", request)
	}
	if !bytes.Equal(optionValue(t, request, protocol.OptionURIQuery), ref[:]) {
		t.Fatal("GET did not use raw reference")
	}
	if got := request.URIPath(); !reflect.DeepEqual(got, []string{"a", "blocks"}) {
		t.Errorf("path = %q", got)
	}
	if got, _, _ := oneUintOption(request, protocol.OptionSize1); got != uint32(eris.BlockSize1KiB) {
		t.Errorf("Size1 = %d", got)
	}
	if string(optionValue(t, request, protocol.OptionProxyURI)) != "ocapn://peer/store" {
		t.Fatal("missing Proxy-Uri")
	}
	if _, err := protocol.MarshalUDP(request); err != nil {
		t.Fatalf("request options are not in wire order: %v", err)
	}

	base32Request, err := NewGetRequest(store, ref, RequestConfig{ReferenceEncoding: ReferenceBase32})
	if err != nil || string(optionValue(t, base32Request, protocol.OptionURIQuery)) != ref.String() {
		t.Fatalf("Base32 request = %+v, %v", base32Request, err)
	}
	if _, err := NewGetRequest(store, ref, RequestConfig{ReferenceEncoding: 9}); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("invalid encoding error = %v", err)
	}
	if _, err := NewGetRequest(store, ref, RequestConfig{SizeHint: 99}); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Errorf("invalid size error = %v", err)
	}
	if _, err := NewGetRequest(store, ref, RequestConfig{ProxyURI: ":bad", HopLimit: 1}); !errors.Is(err, ErrInvalidProxyURI) {
		t.Errorf("invalid proxy error = %v", err)
	}
	if _, err := NewGetRequest(store, ref, RequestConfig{ProxyURI: "ocapn://peer"}); !errors.Is(err, ErrInvalidProxyURI) {
		t.Errorf("proxy without Hop-Limit error = %v", err)
	}
	for _, proxyURI := range []string{"ocapn://peer/store#part", "ocapn://peer/store#"} {
		if _, err := NewGetRequest(store, ref, RequestConfig{ProxyURI: proxyURI, HopLimit: 1}); !errors.Is(err, ErrInvalidProxyURI) {
			t.Errorf("proxy URI with fragment %q: error = %v", proxyURI, err)
		}
	}

	put, err := NewPutRequest(store, block, RequestConfig{HopLimit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if put.Code != protocol.PUT || !bytes.Equal(put.Payload, block) {
		t.Fatalf("PUT request = %+v", put)
	}
	block[0]++
	if bytes.Equal(put.Payload, block) {
		t.Fatal("PUT payload aliases input")
	}
	if format, _, _ := oneUintOption(put, protocol.OptionContentFormat); format != ContentFormatOctetStream {
		t.Errorf("Content-Format = %d", format)
	}
	if size, _, _ := oneUintOption(put, protocol.OptionSize1); size != uint32(eris.BlockSize1KiB) {
		t.Errorf("PUT Size1 = %d", size)
	}
	base32Put, err := NewPutRequest(store, put.Payload, RequestConfig{ReferenceEncoding: ReferenceBase32}, ref)
	if err != nil || !bytes.Equal(optionValue(t, base32Put, protocol.OptionURIQuery), []byte(ref.String())) {
		t.Fatalf("Base32 PUT reference = %v, %v", base32Put, err)
	}
	if _, err := NewPutRequest(store, put.Payload, RequestConfig{ReferenceEncoding: 255}, ref); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("invalid PUT reference encoding error = %v", err)
	}
	if _, err := NewPutRequest(store, []byte("short"), RequestConfig{}); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("short PUT error = %v", err)
	}
	if _, err := NewPutRequest(store, put.Payload, RequestConfig{}, ref, ref); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("multiple expected references error = %v", err)
	}
}

func TestReferenceParsing(t *testing.T) {
	t.Parallel()
	_, ref := makeBlock(int(eris.BlockSize1KiB), 4)
	for _, query := range [][]byte{ref[:], []byte(ref.String())} {
		got, err := parseReferenceQuery(&protocol.Message{Options: []protocol.Option{protocol.NewOption(protocol.OptionURIQuery, query)}})
		if err != nil || got != ref {
			t.Errorf("parseReferenceQuery(%x) = %x, %v", query, got, err)
		}
	}
	for _, message := range []*protocol.Message{nil, {}, {Options: []protocol.Option{protocol.StringOption(protocol.OptionURIQuery, "bad")}}, {Options: []protocol.Option{protocol.NewOption(protocol.OptionURIQuery, ref[:]), protocol.NewOption(protocol.OptionURIQuery, ref[:])}}} {
		if _, err := parseReferenceQuery(message); !errors.Is(err, ErrInvalidReference) {
			t.Errorf("invalid query error = %v", err)
		}
	}
}

func TestRemoteRequester(t *testing.T) {
	t.Parallel()
	address := &net.UDPAddr{IP: net.IPv6loopback, Port: 5683}
	original := &protocol.Message{Code: protocol.GET}
	requester := RemoteRequester{Requester: RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
		if request.RemoteAddr.String() != address.String() {
			t.Errorf("RemoteAddr = %v", request.RemoteAddr)
		}
		request.Code = protocol.PUT
		return &protocol.Message{Code: protocol.Content}, nil
	}), RemoteAddr: address}
	if _, err := requester.Request(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if original.Code != protocol.GET || original.RemoteAddr != nil {
		t.Fatal("RemoteRequester changed input")
	}
	for _, bad := range []RemoteRequester{{}, {Requester: requester.Requester}} {
		if _, err := bad.Request(context.Background(), original); err == nil {
			t.Fatal("invalid RemoteRequester succeeded")
		}
	}
	if _, err := requester.Request(context.Background(), nil); err == nil {
		t.Fatal("nil request succeeded")
	}
	if _, err := RequesterFunc(nil).Request(context.Background(), original); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil RequesterFunc error = %v", err)
	}
}

func TestBlockLengthDoesNotTruncate(t *testing.T) {
	t.Parallel()
	for _, size := range []int{1024, 32768} {
		if !validBlockLength(size) {
			t.Fatalf("valid block length %d rejected", size)
		}
		for _, invalid := range []int{size - 1, size + 1} {
			if validBlockLength(invalid) {
				t.Fatalf("invalid block length %d accepted", invalid)
			}
		}
		// Check the integer boundary without allocating a multi-gigabyte body.
		large := int64(1<<32) + int64(size)
		if int64(int(large)) == large && validBlockLength(int(large)) {
			t.Fatalf("block length %d truncated to %d", large, size)
		}
	}
}
