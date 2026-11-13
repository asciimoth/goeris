package eris_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	eriscoap "github.com/asciimoth/goeris/coap"
	erishttp "github.com/asciimoth/goeris/http"
	"github.com/asciimoth/goeris/s3"
)

type ttlHTTPFunc func(*http.Request) (*http.Response, error)

func (f ttlHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type ttlCoAPFunc func(context.Context, *protocol.Message) (*protocol.Message, error)

func (f ttlCoAPFunc) Request(c context.Context, r *protocol.Message) (*protocol.Message, error) {
	return f(c, r)
}

type ttlStore struct {
	eris.BlockSet
	seen []eris.TTL
}

func (s *ttlStore) GetBlock(ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	s.seen = append(s.seen, ttl)
	return s.BlockSet.GetBlock(ref, ttl)
}
func (s *ttlStore) PutBlock(block []byte, ttl eris.TTL, refs ...eris.Reference) (eris.Reference, error) {
	s.seen = append(s.seen, ttl)
	return s.BlockSet.PutBlock(block, ttl, refs...)
}
func (s *ttlStore) GetBlockContext(_ context.Context, ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	return s.GetBlock(ref, ttl)
}
func (s *ttlStore) PutBlockContext(_ context.Context, block []byte, ttl eris.TTL, refs ...eris.Reference) (eris.Reference, error) {
	return s.PutBlock(block, ttl, refs...)
}

// Each transport uses real request builders and handlers, but no sockets. The
// hard request cap makes a TTL regression fail instead of recursing forever.
func ttlNetwork(t *testing.T, name string, target eris.BlockSource, wire *[]eris.TTL) *eris.CombinedBlockStore {
	t.Helper()
	record := func(ttl eris.TTL) error {
		*wire = append(*wire, ttl)
		if len(*wire) > 40 {
			return errors.New("request loop exceeded test limit")
		}
		return nil
	}
	if name == "coap" {
		handler, err := (eriscoap.Handler{Blocks: target, AllowPut: true}).BlockWise(protocol.BlockWiseConfig{})
		if err != nil {
			t.Fatal(err)
		}
		store, err := eriscoap.ParseStoreURL("coap://example/")
		if err != nil {
			t.Fatal(err)
		}
		client, err := eriscoap.NewClient(ttlCoAPFunc(func(ctx context.Context, r *protocol.Message) (*protocol.Message, error) {
			option, ok := r.Option(protocol.OptionHopLimit)
			if !ok {
				t.Fatal("missing Hop-Limit")
			}
			n, err := option.Uint32()
			if err != nil {
				t.Fatal(err)
			}
			if err := record(eris.TTL(n)); err != nil {
				return nil, err
			}
			return handler.ServeCOAP(ctx, r)
		}), store, eriscoap.ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		return eris.NewCombinedBlockStore(client)
	}
	var handler http.Handler
	creds := s3.Credentials{AccessKey: "access", SecretKey: "secret"}
	if name == "http" {
		handler = erishttp.Handler{Blocks: target}
	} else {
		handler = s3.Handler{Blocks: target, Bucket: "blocks", AllowPut: true, Auth: s3.RequestAuthenticatorFunc(func(r *http.Request, b []byte) (string, error) {
			if !strings.Contains(r.Header.Get("Authorization"), "eris-ttl") {
				return "", errors.New("TTL was not signed")
			}
			auth := s3.V4Authenticator{Region: "test", Credentials: s3.CredentialSourceFunc(func(context.Context, string) (s3.Credentials, error) { return creds, nil })}
			return auth.Authenticate(r, b)
		})}
	}
	transport := ttlHTTPFunc(func(r *http.Request) (*http.Response, error) {
		n, err := strconv.ParseUint(r.Header.Get(erishttp.TTLHeader), 10, 8)
		if err != nil {
			t.Fatal(err)
		}
		if err := record(eris.TTL(n)); err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})
	if name == "http" {
		base, _ := url.Parse("http://example/")
		client, err := erishttp.NewClient(transport, base)
		if err != nil {
			t.Fatal(err)
		}
		return eris.NewCombinedBlockStore(client)
	}
	store, err := s3.ParseStoreURL("s3://example/blocks/")
	if err != nil {
		t.Fatal(err)
	}
	client, err := s3.NewClient(transport, store, s3.StaticV4Signer{Credentials: creds, Region: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return eris.NewCombinedBlockStore(client)
}

func TestNetworkTTLRoundTrip(t *testing.T) {
	for _, name := range []string{"http", "s3", "coap"} {
		for _, ttl := range []eris.TTL{0, 1, 2, 9, 255} {
			for _, plain := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/plain=%v", name, ttl, plain), func(t *testing.T) {
					block := make([]byte, eris.BlockSize1KiB)
					ref, _ := eris.ReferenceOf(block)
					terminal := &ttlStore{BlockSet: eris.BlockSet{ref: block}}
					var target eris.BlockSource = terminal
					if plain {
						target = struct{ eris.BlockStore }{terminal}
					}
					var wire []eris.TTL
					client := ttlNetwork(t, name, target, &wire)
					got, err := client.GetBlockContext(context.Background(), ref, ttl)
					count := 1
					if ttl == 1 {
						if !errors.Is(err, eris.ErrTTLExpired) {
							t.Fatalf("GET: %v", err)
						}
					} else if err != nil || !bytes.Equal(got, block) {
						t.Fatalf("GET: %v", err)
					}
					if name != "http" {
						count++
						_, err = client.PutBlockContext(context.Background(), block, ttl, ref)
						if ttl == 1 {
							if !errors.Is(err, eris.ErrTTLExpired) {
								t.Fatalf("PUT: %v", err)
							}
						} else if err != nil {
							t.Fatalf("PUT: %v", err)
						}
					}
					if ttl == 1 {
						if len(wire) != 0 || len(terminal.seen) != 0 {
							t.Fatal("local-only request made network I/O")
						}
						return
					}
					want := ttl - 1
					if ttl == 0 {
						want = eris.DefaultTTL
					}
					expected := make([]eris.TTL, count)
					for i := range expected {
						expected[i] = want
					}
					if !reflect.DeepEqual(wire, expected) || !reflect.DeepEqual(terminal.seen, expected) {
						t.Fatalf("wire %v, storage %v; want %v", wire, terminal.seen, expected)
					}
				})
			}
		}
	}
}

func TestNetworkTTLStopsLoops(t *testing.T) {
	for _, name := range []string{"http", "s3", "coap"} {
		for _, ttl := range []eris.TTL{0, 4} {
			t.Run(fmt.Sprintf("%s/%d", name, ttl), func(t *testing.T) {
				var loop eris.CombinedBlockStore
				var wire []eris.TTL
				client := ttlNetwork(t, name, &loop, &wire)
				loop = *eris.NewCombinedBlockStore(client)
				block := make([]byte, eris.BlockSize1KiB)
				ref, _ := eris.ReferenceOf(block)
				_, err := client.GetBlock(ref, ttl)
				check := func(err error) {
					t.Helper()
					if !errors.Is(err, eris.ErrTTLExpired) {
						t.Fatalf("loop error: %v", err)
					}
					count := int(ttl) - 1
					if ttl == 0 {
						count = int(eris.DefaultTTL)
					}
					if len(wire) != count {
						t.Fatalf("got %d requests, want %d", len(wire), count)
					}
					for i, n := range wire {
						if n != eris.TTL(count-i) {
							t.Fatalf("wire budgets: %v", wire)
						}
					}
				}
				check(err)
				if name != "http" {
					wire = nil
					_, err = client.PutBlock(block, ttl, ref)
					check(err)
				}
			})
		}
	}
}

func TestCoAPBlockWisePreservesTTL(t *testing.T) {
	block := make([]byte, eris.BlockSize32KiB)
	ref, _ := eris.ReferenceOf(block)
	terminal := &ttlStore{BlockSet: make(eris.BlockSet)}
	var wire []eris.TTL
	client := ttlNetwork(t, "coap", terminal, &wire)
	if _, err := client.PutBlock(block, 7, ref); err != nil {
		t.Fatal(err)
	}
	if len(wire) < 2 {
		t.Fatal("PUT did not use block-wise transfer")
	}
	for _, ttl := range wire {
		if ttl != 6 {
			t.Fatalf("PUT TTL %d", ttl)
		}
	}
	wire = nil
	if got, err := client.GetBlock(ref, 7); err != nil || !bytes.Equal(got, block) {
		t.Fatalf("GET: %v", err)
	}
	if len(wire) < 2 {
		t.Fatal("GET did not use block-wise transfer")
	}
	for _, ttl := range wire {
		if ttl != 6 {
			t.Fatalf("GET TTL %d", ttl)
		}
	}
	if !reflect.DeepEqual(terminal.seen, []eris.TTL{6, 6}) {
		t.Fatalf("storage budgets: %v", terminal.seen)
	}
}

func TestHTTPContentPreservesTTL(t *testing.T) {
	content := bytes.Repeat([]byte("content"), 1000)
	cap, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	terminal := &ttlStore{BlockSet: blocks}
	handler := erishttp.Handler{Blocks: terminal}
	base, _ := url.Parse("http://example/")
	client, err := erishttp.NewClient(ttlHTTPFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	}), base)
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.OpenContent(context.Background(), cap, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			t.Errorf("close content: %v", err)
		}
	}()
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content: %v", err)
	}
	if len(terminal.seen) < 2 {
		t.Fatal("content did not span blocks")
	}
	for _, ttl := range terminal.seen {
		if ttl != 4 {
			t.Fatalf("content TTL: %d", ttl)
		}
	}
}

func TestHTTPServersOptionalTTL(t *testing.T) {
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	for _, name := range []string{"http", "s3"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
			if name == "http" && method == http.MethodPut {
				continue
			}
			for _, test := range []struct {
				values []string
				want   eris.TTL
				status int
			}{
				{nil, 1, 200}, {[]string{"1"}, 1, 200}, {[]string{"9"}, 9, 200},
				{[]string{"0"}, 0, 400}, {[]string{"256"}, 0, 400}, {[]string{""}, 0, 400},
				{[]string{"2", "3"}, 0, 400}, {[]string{"2, 3"}, 0, 400},
			} {
				t.Run(fmt.Sprintf("%s/%s/%v", name, method, test.values), func(t *testing.T) {
					terminal := &ttlStore{BlockSet: eris.BlockSet{ref: block}}
					var handler http.Handler = erishttp.Handler{Blocks: terminal}
					path := erishttp.N2RPath + "?" + ref.URN()
					if name == "s3" {
						path = "/blocks/" + ref.String()
						handler = s3.Handler{Blocks: terminal, Bucket: "blocks", AllowPut: true, Auth: s3.RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) { return "test", nil })}
					}
					request := httptest.NewRequest(method, path, bytes.NewReader(block))
					if test.values != nil {
						request.Header[erishttp.TTLHeader] = test.values
					}
					request.Header.Set("x-amz-acl", s3.PublicReadACL)
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					if response.Code != test.status {
						t.Fatalf("status %d, want %d", response.Code, test.status)
					}
					if test.status == 400 {
						if len(terminal.seen) != 0 {
							t.Fatal("invalid TTL reached storage")
						}
					} else if !reflect.DeepEqual(terminal.seen, []eris.TTL{test.want}) {
						t.Fatalf("storage TTL: %v", terminal.seen)
					}
				})
			}
		}
	}
}

func TestCoAPServerMissingHopLimitIsLocal(t *testing.T) {
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	terminal := &ttlStore{BlockSet: eris.BlockSet{ref: block}}
	handler := eriscoap.Handler{Blocks: terminal, AllowPut: true}
	store, err := eriscoap.ParseStoreURL("coap://example/")
	if err != nil {
		t.Fatal(err)
	}
	get, err := eriscoap.NewGetRequest(store, ref, eriscoap.RequestConfig{})
	if err != nil {
		t.Fatal(err)
	}
	put, err := eriscoap.NewPutRequest(store, block, eriscoap.RequestConfig{}, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*protocol.Message{get, put} {
		response, err := handler.ServeCOAP(context.Background(), request)
		if err != nil || response.Code != protocol.Content && response.Code != protocol.Created {
			t.Fatalf("response %v, %v", response, err)
		}
	}
	if !reflect.DeepEqual(terminal.seen, []eris.TTL{1, 1}) {
		t.Fatalf("storage TTL: %v", terminal.seen)
	}
}

func TestTTLStopsMixedProtocolLoops(t *testing.T) {
	for _, write := range []bool{false, true} {
		var loop eris.CombinedBlockStore
		var wire []eris.TTL
		coap := ttlNetwork(t, "coap", &loop, &wire)
		client := ttlNetwork(t, "s3", coap, &wire)
		if !write {
			client = ttlNetwork(t, "http", client, &wire)
		}
		loop = *eris.NewCombinedBlockStore(client)
		block := make([]byte, eris.BlockSize1KiB)
		ref, _ := eris.ReferenceOf(block)
		var err error
		if write {
			_, err = client.PutBlock(block, 7, ref)
		} else {
			_, err = client.GetBlock(ref, 7)
		}
		if !errors.Is(err, eris.ErrTTLExpired) {
			t.Fatalf("write %v: %v", write, err)
		}
		if !reflect.DeepEqual(wire, []eris.TTL{6, 5, 4, 3, 2, 1}) {
			t.Fatalf("write %v: budgets %v", write, wire)
		}
	}
}

func TestLocalTTLAllowsFallbackAfterNetworkStore(t *testing.T) {
	block := make([]byte, eris.BlockSize1KiB)
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"http", "s3", "coap"} {
		t.Run(name, func(t *testing.T) {
			local := &ttlStore{BlockSet: eris.BlockSet{ref: block}}
			var wire []eris.TTL
			network := ttlNetwork(t, name, local, &wire)
			combined := eris.NewCombinedBlockStore(network, local)
			got, err := combined.GetBlockContext(context.Background(), ref, eris.LocalTTL)
			if err != nil || !bytes.Equal(got, block) {
				t.Fatalf("local read: %v", err)
			}
			stored, err := combined.PutBlockContext(context.Background(), block, eris.LocalTTL, ref)
			if err != nil || stored != ref {
				t.Fatalf("local write: %v", err)
			}
			if len(wire) != 0 {
				t.Fatalf("local operations made %d network requests", len(wire))
			}
			if !reflect.DeepEqual(local.seen, []eris.TTL{1, 1}) {
				t.Fatalf("local budgets: %v", local.seen)
			}
		})
	}
}
