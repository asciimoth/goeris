package coap

import (
	"context"
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
