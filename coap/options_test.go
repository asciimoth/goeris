package coap

import (
	"context"
	"errors"
	"fmt"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

func TestHandlersRejectUnsupportedCriticalOptions(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 19)
	store := mustStore(t, "coap://example/")
	get, _ := NewGetRequest(store, ref, RequestConfig{})
	put, _ := NewPutRequest(store, block, RequestConfig{})
	for _, number := range []protocol.OptionNumber{1, 5, 17, 65001} {
		for _, test := range []struct {
			request   *protocol.Message
			discovery bool
		}{{get, false}, {put, false}, {NewDiscoveryRequest(), true}} {
			for _, kind := range []protocol.MessageType{protocol.Confirmable, protocol.NonConfirmable} {
				called := false
				blocks := storeFunc{
					get: func(eris.Reference) ([]byte, error) { called = true; return block, nil },
					put: func(eris.Reference, []byte) error { called = true; return nil },
				}
				var handler protocol.Handler = Handler{Blocks: blocks, AllowPut: true}
				if test.discovery {
					handler = DiscoveryHandler{}
				}
				clone := addOption(test.request, protocol.NewOption(number, nil))
				clone.Type = kind
				response, err := handler.ServeCOAP(context.Background(), clone)
				if err != nil {
					t.Fatal(err)
				}
				if kind == protocol.NonConfirmable {
					if response != nil {
						t.Errorf("option %d: non-confirmable request was not discarded", number)
					}
				} else if response == nil || response.Code != protocol.BadOption {
					t.Errorf("option %d: expected BadOption response", number)
				}
				if called {
					t.Fatal("unsupported critical option reached storage")
				}
			}
		}
	}
}

func TestHandlersIgnoreUnknownElectiveOptions(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 21)
	blocks := make(eris.BlockSet)
	handler := Handler{Blocks: blocks, AllowPut: true}
	store := mustStore(t, "coap://example/")
	put, _ := NewPutRequest(store, block, RequestConfig{})
	get, _ := NewGetRequest(store, ref, RequestConfig{})
	option := protocol.NewOption(65000, []byte("extension"))
	if response := serve(t, handler, addOption(put, option)); response.Code != protocol.Created {
		t.Fatalf("PUT with elective option: %s", response.Code)
	}
	if response := serve(t, handler, addOption(get, option)); response.Code != protocol.Content {
		t.Fatalf("GET with elective option: %s", response.Code)
	}
	if response := serve(t, DiscoveryHandler{}, addOption(NewDiscoveryRequest(), option)); response.Code != protocol.Content {
		t.Fatalf("discovery with elective option: %s", response.Code)
	}
}

func TestResponseMetadataLimits(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 22)
	for _, options := range [][]protocol.Option{
		{protocol.NewOption(65000, make([]byte, MaxResponseOptionSize))},
		make([]protocol.Option, MaxResponseOptionSize/8+1),
		{protocol.UintOption(protocol.OptionContentFormat, 42), protocol.UintOption(protocol.OptionContentFormat, 42)},
		{protocol.NewOption(protocol.OptionContentFormat, []byte{1, 0, 0})},
	} {
		calls := 0
		client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			calls++
			fragment, _ := (protocol.Block{More: true, Size: 16}).Option(protocol.OptionBlock2)
			return &protocol.Message{Code: protocol.Content, Options: append(options, fragment), Payload: block[:16]}, nil
		}), mustStore(t, "coap://example/"), ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetBlock(ref); err == nil {
			t.Fatal("invalid response metadata accepted")
		}
		if calls != 1 {
			t.Fatalf("invalid response caused %d requests", calls)
		}
	}
	response := &protocol.Message{Options: []protocol.Option{protocol.NewOption(65000, make([]byte, MaxResponseOptionSize-8))}}
	if err := validateResponseOptions(response); err != nil {
		t.Fatalf("metadata at limit rejected: %v", err)
	}
}

func TestRequestValidationDoesNotCopyLargeValues(t *testing.T) {
	large := make([]byte, 1<<20)
	query := &protocol.Message{Options: []protocol.Option{{Number: protocol.OptionURIQuery, Value: large}}}
	path := &protocol.Message{Options: []protocol.Option{{Number: protocol.OptionURIPath, Value: large}}}
	if allocations := testing.AllocsPerRun(10, func() {
		if _, err := parseReferenceQuery(query); err == nil {
			t.Fatal("oversized reference accepted")
		}
		if matchesPath(path, nil, blocksSegment) {
			t.Fatal("oversized path matched")
		}
	}); allocations != 0 {
		t.Fatalf("validation allocated %.0f times; want no copies", allocations)
	}
}

func TestSingleOptionRejectsRepeatedEmptyValues(t *testing.T) {
	message := &protocol.Message{Options: []protocol.Option{
		{Number: protocol.OptionSize1},
		{Number: protocol.OptionSize1, Value: []byte{}},
	}}
	if _, present, err := oneUintOption(message, protocol.OptionSize1); !present || err == nil {
		t.Fatalf("repeated empty integer: present=%v, err=%v", present, err)
	}
}

func TestOversizedTokensStopBeforeTransfer(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 23)
	store := mustStore(t, "coap://example/")
	for _, size := range []int{8, 9, 1 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			calls := 0
			client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
				calls++
				fragment, _ := (protocol.Block{Size: 1024}).Option(protocol.OptionBlock2)
				return &protocol.Message{Code: protocol.Content, Token: make([]byte, size), Options: []protocol.Option{fragment}, Payload: block}, nil
			}), store, ClientConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetBlock(ref)
			if size <= 8 && err != nil || size > 8 && !errors.Is(err, ErrResponseTooLarge) {
				t.Fatalf("GET error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("requests = %d; want 1", calls)
			}

			blocks := make(eris.BlockSet)
			handler, err := (Handler{Blocks: blocks, AllowPut: true}).BlockWise(protocol.BlockWiseConfig{})
			if err != nil {
				t.Fatal(err)
			}
			put, _ := NewPutRequest(store, block, RequestConfig{})
			fragment, _ := (protocol.Block{Size: 1024}).Option(protocol.OptionBlock1)
			put = addOption(put, fragment)
			put.Token = make([]byte, size)
			response := serve(t, handler, put)
			if size <= 8 {
				if response.Code != protocol.Created || len(blocks) != 1 {
					t.Fatalf("valid token: response = %v, stored = %d", response.Code, len(blocks))
				}
			} else if response.Code != protocol.BadRequest || len(blocks) != 0 || len(handler.transfers) != 0 {
				t.Fatalf("oversized token: response = %v, stored = %d, transfers = %d", response.Code, len(blocks), len(handler.transfers))
			}
		})
	}
}
