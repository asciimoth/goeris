package coap

import (
	"bytes"
	"context"
	"errors"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/eris-tools/eris"
)

func TestClient(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 12)
	blocks := make(eris.BlockSet)
	handler := Handler{Blocks: blocks, AllowPut: true}
	requester := RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
		return handler.ServeCOAP(ctx, request)
	})
	client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutBlock(ref, block); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetBlockContext(context.Background(), ref)
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("GetBlockContext() = %x, %v", got, err)
	}
	got[0]++
	stored, _ := blocks.GetBlock(ref)
	if !bytes.Equal(stored, block) {
		t.Fatal("client result aliases response")
	}
	if got, err = client.GetBlock(ref); err != nil || len(got) != len(block) {
		t.Fatalf("GetBlock() = %d bytes, %v", len(got), err)
	}
}

func TestClientErrors(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 13)
	other, otherRef := makeBlock(int(eris.BlockSize1KiB), 14)
	sentinel := errors.New("transport")
	tests := []struct {
		name     string
		response *protocol.Message
		err      error
		want     error
	}{
		{"transport", nil, sentinel, sentinel},
		{"missing", &protocol.Message{Code: protocol.NotFound}, nil, eris.ErrMissingBlock},
		{"status", &protocol.Message{Code: protocol.InternalServerError}, nil, &ResponseError{}},
		{"format", &protocol.Message{Code: protocol.Content, Options: []protocol.Option{protocol.UintOption(protocol.OptionContentFormat, 0)}, Payload: block}, nil, ErrUnexpectedContentFormat},
		{"repeated format", &protocol.Message{Code: protocol.Content, Options: []protocol.Option{protocol.UintOption(protocol.OptionContentFormat, 42), protocol.UintOption(protocol.OptionContentFormat, 42)}, Payload: block}, nil, ErrUnexpectedContentFormat},
		{"large", &protocol.Message{Code: protocol.Content, Payload: make([]byte, int(eris.BlockSize32KiB)+1)}, nil, ErrResponseTooLarge},
		{"wrong digest", &protocol.Message{Code: protocol.Content, Payload: other}, nil, eris.ErrInvalidBlock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
				return test.response, test.err
			}), mustStore(t, "coap://example/"), ClientConfig{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetBlock(ref)
			var responseError *ResponseError
			if _, isResponse := test.want.(*ResponseError); isResponse {
				if !errors.As(err, &responseError) || responseError.Code != protocol.InternalServerError {
					t.Errorf("error = %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Errorf("error = %v, want %v", err, test.want)
			}
		})
	}

	if err := (&Client{}).PutBlock(ref, block); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil client PUT error = %v", err)
	}
	if _, err := (*Client)(nil).GetBlock(ref); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil client GET error = %v", err)
	}
	client, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.Created}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if err := client.PutBlock(otherRef, block); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("invalid PUT error = %v", err)
	}

	putTests := []struct {
		response *protocol.Message
		err      error
		want     error
	}{
		{nil, sentinel, sentinel},
		{&protocol.Message{Code: protocol.NotFound}, nil, eris.ErrMissingBlock},
		{&protocol.Message{Code: protocol.Content}, nil, &ResponseError{}},
	}
	for _, test := range putTests {
		client, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) { return test.response, test.err }), mustStore(t, "coap://example/"), ClientConfig{})
		err := client.PutBlock(ref, block)
		var responseError *ResponseError
		if _, ok := test.want.(*ResponseError); ok {
			if !errors.As(err, &responseError) || responseError.Code != protocol.Content {
				t.Errorf("PUT error = %v", err)
			}
		} else if !errors.Is(err, test.want) {
			t.Errorf("PUT error = %v, want %v", err, test.want)
		}
	}
}

func TestNewClientErrors(t *testing.T) {
	t.Parallel()
	store := mustStore(t, "coap://example/")
	if _, err := NewClient(nil, store, ClientConfig{}); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil requester error = %v", err)
	}
	if _, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) { return nil, nil }), StoreURL{}, ClientConfig{}); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("invalid store error = %v", err)
	}
	if got := (&ResponseError{Code: protocol.BadRequest}).Error(); got != "eris coap: response code 4.00" {
		t.Errorf("ResponseError.Error() = %q", got)
	}
}
