package coap

import (
	"bytes"
	"context"
	"errors"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
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
	if gotRef, err := client.PutBlock(block); err != nil || gotRef != ref {
		t.Fatalf("PutBlock() = %v, %v", gotRef, err)
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

	if _, err := (&Client{}).PutBlock(block, ref); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil client PUT error = %v", err)
	}
	if _, err := (*Client)(nil).GetBlock(ref); !errors.Is(err, ErrNilRequester) {
		t.Errorf("nil client GET error = %v", err)
	}
	client, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.Created, Payload: ref[:]}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if _, err := client.PutBlock(block, otherRef); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("invalid PUT error = %v", err)
	}
	requests := 0
	checkedClient, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		requests++
		return &protocol.Message{Code: protocol.Created, Payload: ref[:]}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if _, err := checkedClient.PutBlock(block, otherRef); !errors.Is(err, eris.ErrInvalidBlock) || requests != 0 {
		t.Errorf("mismatched expected reference sent %d requests, error = %v", requests, err)
	}
	wrongReferenceClient, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.Created, Payload: otherRef[:]}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if _, err := wrongReferenceClient.PutBlock(block); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("wrong returned reference error = %v", err)
	}
	if _, err := wrongReferenceClient.PutBlock(block, ref); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("wrong returned expected reference error = %v", err)
	}
	badRequestClient, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if _, err := badRequestClient.PutBlock(block, ref); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("rejected reference error = %v", err)
	}
	malformedClient, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.Created}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if gotRef, err := malformedClient.PutBlock(block); err != nil || gotRef != ref {
		t.Errorf("legacy response reference = %v, %v", gotRef, err)
	}
	if _, err := malformedClient.PutBlock(block, ref); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("missing response reference with expectation error = %v", err)
	}
	shortResponseClient, _ := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return &protocol.Message{Code: protocol.Created, Payload: []byte{1}}, nil
	}), mustStore(t, "coap://example/"), ClientConfig{})
	if _, err := shortResponseClient.PutBlock(block); !errors.Is(err, ErrInvalidReference) {
		t.Errorf("short response reference error = %v", err)
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
		_, err := client.PutBlock(block, ref)
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

func TestClientStopsOversizedBlockWiseResponse(t *testing.T) {
	t.Parallel()
	_, ref := makeBlock(int(eris.BlockSize1KiB), 15)
	requests := 0
	requester := RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
		requests++
		index := uint32(0)
		if option, ok := request.Option(protocol.OptionBlock2); ok {
			block, err := protocol.ParseBlock(option.Value)
			if err != nil {
				return nil, err
			}
			index = block.Number
		}
		option, err := (protocol.Block{Number: index, More: true, Size: 1024}).Option(protocol.OptionBlock2)
		if err != nil {
			return nil, err
		}
		return &protocol.Message{Code: protocol.Content, Options: []protocol.Option{option}, Payload: make([]byte, 1024)}, nil
	})
	client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetBlock(ref); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized block-wise response error = %v", err)
	}
	if requests != 33 {
		t.Fatalf("requests = %d, want 33", requests)
	}
}

func TestClientRejectsOversizedSize2BeforeContinuation(t *testing.T) {
	for _, size := range []uint32{uint32(eris.BlockSize32KiB) + 1, ^uint32(0)} {
		calls := 0
		requester := RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			calls++
			fragment, _ := (protocol.Block{More: true, Size: 16}).Option(protocol.OptionBlock2)
			return &protocol.Message{
				Code: protocol.Content, Payload: make([]byte, 16),
				Options: []protocol.Option{fragment, protocol.UintOption(protocol.OptionSize2, size)},
			}, nil
		})
		client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetBlock(eris.Reference{}); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("Size2 %d: got %v, want ErrResponseTooLarge", size, err)
		}
		if calls != 1 {
			t.Fatalf("Size2 %d caused %d requests", size, calls)
		}
	}
}

func TestClientChecksCompleteResponseSize(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 17)
	for _, size := range []uint32{0, 32, 1023, 1024, 1025, 32768} {
		client, err := NewClient(RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
			response := &protocol.Message{Code: protocol.Content, Payload: block}
			if request.Code == protocol.PUT {
				response.Code, response.Payload = protocol.Created, ref[:]
			}
			response.Options = []protocol.Option{protocol.UintOption(protocol.OptionSize2, size)}
			return response, nil
		}), mustStore(t, "coap://example/"), ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		_, getErr := client.GetBlock(ref)
		_, putErr := client.PutBlock(block)
		for _, result := range []struct {
			name   string
			length int
			err    error
		}{{"GET", len(block), getErr}, {"PUT", len(ref), putErr}} {
			if size == uint32(result.length) {
				if result.err != nil {
					t.Fatalf("%s Size2 %d: %v", result.name, size, result.err)
				}
			} else if !errors.Is(result.err, ErrInvalidResponseOption) {
				t.Fatalf("%s Size2 %d: got %v, want ErrInvalidResponseOption", result.name, size, result.err)
			}
		}
	}
}

func TestClientLimitsTotalResponseOptions(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 16)
	calls := 0
	requester := RequesterFunc(func(_ context.Context, request *protocol.Message) (*protocol.Message, error) {
		calls++
		index := uint32(0)
		if option, ok := request.Option(protocol.OptionBlock2); ok {
			decoded, err := protocol.ParseBlock(option.Value)
			if err != nil {
				return nil, err
			}
			index = decoded.Number
		}
		fragment, _ := (protocol.Block{Number: index, More: index < 63, Size: 16}).Option(protocol.OptionBlock2)
		return &protocol.Message{
			Code: protocol.Content,
			Options: []protocol.Option{
				fragment,
				protocol.NewOption(65000, make([]byte, 32<<10)),
			},
			Payload: block[int(index)*16 : (int(index)+1)*16],
		}, nil
	})
	client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetBlock(ref); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("total response options: %v", err)
	}
	if calls >= 64 {
		t.Fatalf("client accepted all %d response fragments", calls)
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

func TestClientRejectsAmbiguousTransferResponses(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 31)
	for _, options := range [][]protocol.Option{
		{protocol.UintOption(protocol.OptionBlock2, 6), protocol.UintOption(protocol.OptionBlock2, 14)},
		{protocol.UintOption(protocol.OptionBlock1, 6), protocol.UintOption(protocol.OptionBlock1, 14)},
		{protocol.UintOption(protocol.OptionSize2, 1024), protocol.UintOption(protocol.OptionSize2, 32768)},
		{protocol.NewOption(protocol.OptionSize2, make([]byte, 5))},
		{protocol.UintOption(protocol.OptionBlock1, 7)},
		{protocol.NewOption(65001, nil)},
	} {
		calls := 0
		client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			calls++
			return &protocol.Message{Code: protocol.Content, Options: options, Payload: block}, nil
		}), mustStore(t, "coap://example/"), ClientConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.GetBlock(ref); !errors.Is(err, ErrInvalidResponseOption) || calls != 1 {
			t.Fatalf("options %+v: calls %d, error %v", options, calls, err)
		}
	}
}
