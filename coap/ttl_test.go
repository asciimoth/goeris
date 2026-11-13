package coap

import (
	"context"
	"errors"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

func TestClientTTLOverridesConfiguredHopLimit(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 12)
	for _, test := range []struct {
		ttl  eris.TTL
		want uint32
	}{{0, 42}, {1, 0}, {2, 1}, {9, 8}, {255, 254}} {
		calls := 0
		client, err := NewClient(RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
			calls++
			got, present, err := oneUintOption(request, protocol.OptionHopLimit)
			if err != nil || !present || got != test.want {
				t.Fatalf("TTL %d: wire %d, present %v, %v", test.ttl, got, present, err)
			}
			if request.Code == protocol.PUT {
				return &protocol.Message{Code: protocol.Created, Payload: ref[:]}, nil
			}
			return &protocol.Message{Code: protocol.Content, Payload: block}, nil
		}), mustStore(t, "coap://example/"), ClientConfig{Request: RequestConfig{HopLimit: 42}})
		if err != nil {
			t.Fatal(err)
		}
		_, getErr := client.GetBlock(ref, test.ttl)
		_, putErr := client.PutBlock(block, test.ttl, ref)
		if test.ttl == 1 {
			if calls != 0 || !errors.Is(getErr, eris.ErrTTLExpired) || !errors.Is(putErr, eris.ErrTTLExpired) {
				t.Fatalf("expired: %d calls, %v, %v", calls, getErr, putErr)
			}
		} else if calls != 2 || getErr != nil || putErr != nil {
			t.Fatalf("TTL %d: %d calls, %v, %v", test.ttl, calls, getErr, putErr)
		}
		if client.config.Request.HopLimit != 42 {
			t.Fatal("operation changed shared configuration")
		}
	}
}

func TestClientBlockWiseTTLExpired(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize32KiB), 19)
	for _, test := range []struct {
		name   string
		method protocol.Code
		failAt int
	}{
		{"GET first response", protocol.GET, 1},
		{"GET continuation", protocol.GET, 2},
		{"PUT first response", protocol.PUT, 1},
		{"PUT continuation", protocol.PUT, 2},
		{"PUT final response", protocol.PUT, 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client, err := NewClient(RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
				calls++
				if calls > test.failAt {
					t.Fatal("client continued after TTL expired")
				}
				if calls == test.failAt {
					return &protocol.Message{Code: protocol.HopLimitReached, Payload: []byte("peer")}, nil
				}
				if request.Code == protocol.PUT {
					option, present := request.Option(protocol.OptionBlock1)
					if !present {
						t.Fatal("PUT did not use Block1")
					}
					return &protocol.Message{Code: protocol.Continue, Options: []protocol.Option{option}}, nil
				}
				option, err := (protocol.Block{Number: 0, More: true, Size: 1024}).Option(protocol.OptionBlock2)
				if err != nil {
					t.Fatal(err)
				}
				return &protocol.Message{Code: protocol.Content, Options: []protocol.Option{option}, Payload: block[:1024]}, nil
			}), mustStore(t, "coap://example/"), ClientConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if test.method == protocol.PUT {
				_, err = client.PutBlock(block, 4, ref)
			} else {
				_, err = client.GetBlock(ref, 4)
			}
			var responseError *ResponseError
			if !errors.Is(err, eris.ErrTTLExpired) || !errors.As(err, &responseError) || responseError.Code != protocol.HopLimitReached {
				t.Fatalf("error = %v; want a TTL response error", err)
			}
			if calls != test.failAt {
				t.Fatalf("requests = %d, want %d", calls, test.failAt)
			}
		})
	}
}
