package coap

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

func TestExperimentalRootRoundTrip(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		for _, enabled := range []bool{false, true} {
			for _, size := range []int{int(eris.BlockSize1KiB), int(eris.BlockSize32KiB)} {
				t.Run(fmt.Sprintf("%s/enabled=%v/size=%d", transport, enabled, size), func(t *testing.T) {
					block, ref := makeBlock(size, 12)
					terminal := &priorityStore{BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
					handler, err := (Handler{Blocks: terminal, AllowPut: true, ExperimentalRoot: enabled}).BlockWise(protocol.BlockWiseConfig{})
					if err != nil {
						t.Fatal(err)
					}
					wantHint := false
					wantStored := false
					exchanges := 0
					requester := RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
						exchanges++
						request = priorityWireMessage(t, transport, request)
						option, present := request.Option(ExperimentalRootOption)
						if present != (enabled && wantHint) || len(option.Value) != 0 {
							t.Fatalf("request root option = %v, present=%v", option, present)
						}
						response, err := handler.ServeCOAP(ctx, request)
						if err == nil && response != nil && response.Code == protocol.Content {
							option, present := response.Option(ExperimentalRootOption)
							if present != (enabled && wantStored) || len(option.Value) != 0 {
								t.Fatalf("response root option = %v, present=%v", option, present)
							}
						}
						if err != nil {
							return nil, err
						}
						return priorityWireMessage(t, transport, response), nil
					})
					client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{ExperimentalRoot: enabled})
					if err != nil {
						t.Fatal(err)
					}
					for _, high := range []bool{false, true, false} {
						wantHint = high
						if _, err := eris.PutBlock(context.Background(), client, block, 0, eris.BlockOptions{Root: high}, ref); err != nil {
							t.Fatal(err)
						}
						if terminal.writes[len(terminal.writes)-1].Root != (enabled && high) {
							t.Fatal("lost put hint")
						}
						wantStored = wantStored || high
						for _, hint := range []bool{false, true} {
							wantHint = hint
							data, err, options := eris.GetBlock(context.Background(), client, ref, 0, eris.BlockOptions{Root: hint})
							if err != nil || !bytes.Equal(data, block) || options.Root != (enabled && wantStored) {
								t.Fatalf("get: %v, %v", err, options)
							}
							if terminal.reads[len(terminal.reads)-1].Root != (enabled && hint) {
								t.Fatal("lost get hint")
							}
						}
					}
					if size == int(eris.BlockSize32KiB) && exchanges <= 9 {
						t.Fatal("block-wise transfer was not used")
					}
				})
			}
		}
	}
}

func TestRootOption(t *testing.T) {
	if ExperimentalRootOption&3 != 0 || ExperimentalRootOption&0x1e != 0x1c {
		t.Fatal("incorrect option properties")
	}
	for _, options := range [][]protocol.Option{
		nil,
		{protocol.NewOption(ExperimentalRootOption, []byte{1})},
		{protocol.NewOption(ExperimentalRootOption, nil), protocol.NewOption(ExperimentalRootOption, nil)},
	} {
		if rootOptions(&protocol.Message{Options: options}, true).Root {
			t.Fatal("invalid hint accepted")
		}
	}
	request := &protocol.Message{}
	setRoot(request, eris.BlockOptions{Root: true}, true)
	if !rootOptions(request, true).Root || rootOptions(request, false).Root {
		t.Fatal("enable switch ignored")
	}
}

func TestExperimentalRootOneEndpoint(t *testing.T) {
	for _, clientEnabled := range []bool{false, true} {
		serverEnabled := !clientEnabled
		block, ref := makeBlock(int(eris.BlockSize1KiB), 13)
		high := eris.BlockOptions{Root: true}
		terminal := &priorityStore{BlockSet: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block, Options: high}}}}
		handler := Handler{Blocks: terminal, ExperimentalRoot: serverEnabled}
		client, err := NewClient(RequesterFunc(handler.ServeCOAP), mustStore(t, "coap://example/"), ClientConfig{ExperimentalRoot: clientEnabled})
		if err != nil {
			t.Fatal(err)
		}
		data, err, options := eris.GetBlock(context.Background(), client, ref, 0, high)
		if err != nil || !bytes.Equal(data, block) || options.Root {
			t.Fatalf("get: %v, %v", err, options)
		}
		if terminal.reads[0].Root {
			t.Fatal("disabled endpoint forwarded a hint")
		}
	}
}

func TestExperimentalRootReadErrors(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 14)
	for _, code := range []protocol.Code{protocol.Content, protocol.NotFound, protocol.InternalServerError} {
		client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			// A root hint must not escape on an error, including a successful
			// response whose block does not match the requested reference.
			bad := bytes.Clone(block)
			bad[0]++
			return &protocol.Message{Code: code, Payload: bad, Options: []protocol.Option{
				protocol.NewOption(ExperimentalRootOption, nil),
			}}, nil
		}), mustStore(t, "coap://example/"), ClientConfig{ExperimentalRoot: true})
		if err != nil {
			t.Fatal(err)
		}
		data, err, options := eris.GetBlock(context.Background(), client, ref, 0, eris.BlockOptions{Root: true})
		if err == nil || data != nil || options.Root {
			t.Fatalf("code %v: data=%d bytes, err=%v, options=%v", code, len(data), err, options)
		}
	}
}

func TestIndependentBlockOptions(t *testing.T) {
	for _, high := range []bool{false, true} {
		for _, root := range []bool{false, true} {
			message := &protocol.Message{}
			options := eris.BlockOptions{HighPriority: high, Root: root}
			setPriority(message, options, true)
			setRoot(message, options, true)
			for _, priorityEnabled := range []bool{false, true} {
				for _, rootEnabled := range []bool{false, true} {
					want := eris.BlockOptions{HighPriority: high && priorityEnabled, Root: root && rootEnabled}
					if got := blockOptions(message, priorityEnabled, rootEnabled); got != want {
						t.Fatalf("got %v, want %v", got, want)
					}
				}
			}
		}
	}
}
