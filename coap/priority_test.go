package coap

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

type priorityStore struct {
	eris.BlockSet
	reads, writes []eris.BlockOptions
}

//nolint:staticcheck // ST1008: BlockSource requires options as the third return value.
func (s *priorityStore) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	s.reads = append(s.reads, options)
	return s.BlockSet.GetBlock(ctx, ref, ttl, options)
}
func (s *priorityStore) PutBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, refs ...eris.Reference) (eris.Reference, error) {
	s.writes = append(s.writes, options)
	return s.BlockSet.PutBlock(ctx, block, ttl, options, refs...)
}

func TestExperimentalPriorityRoundTrip(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		for _, enabled := range []bool{false, true} {
			for _, size := range []int{int(eris.BlockSize1KiB), int(eris.BlockSize32KiB)} {
				t.Run(fmt.Sprintf("%s/enabled=%v/size=%d", transport, enabled, size), func(t *testing.T) {
					block, ref := makeBlock(size, 12)
					terminal := &priorityStore{BlockSet: make(eris.BlockSet)}
					handler, err := (Handler{Blocks: terminal, AllowPut: true, ExperimentalHighPriority: enabled}).BlockWise(protocol.BlockWiseConfig{})
					if err != nil {
						t.Fatal(err)
					}
					wantHint := false
					wantStored := false
					exchanges := 0
					requester := RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
						exchanges++
						request = priorityWireMessage(t, transport, request)
						option, present := request.Option(ExperimentalHighPriorityOption)
						if present != (enabled && wantHint) || len(option.Value) != 0 {
							t.Fatalf("request priority option = %v, present=%v", option, present)
						}
						response, err := handler.ServeCOAP(ctx, request)
						if err == nil && response != nil && response.Code == protocol.Content {
							option, present := response.Option(ExperimentalHighPriorityOption)
							if present != (enabled && wantStored) || len(option.Value) != 0 {
								t.Fatalf("response priority option = %v, present=%v", option, present)
							}
						}
						if err != nil {
							return nil, err
						}
						return priorityWireMessage(t, transport, response), nil
					})
					client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{ExperimentalHighPriority: enabled})
					if err != nil {
						t.Fatal(err)
					}
					for _, high := range []bool{false, true, false} {
						wantHint = high
						if _, err := client.PutBlock(context.Background(), block, 0, eris.BlockOptions{HighPriority: high}, ref); err != nil {
							t.Fatal(err)
						}
						if terminal.writes[len(terminal.writes)-1].HighPriority != (enabled && high) {
							t.Fatal("lost put hint")
						}
						wantStored = wantStored || high
						for _, hint := range []bool{false, true} {
							wantHint = hint
							data, err, options := client.GetBlock(context.Background(), ref, 0, eris.BlockOptions{HighPriority: hint})
							if err != nil || !bytes.Equal(data, block) || options.HighPriority != (enabled && wantStored) {
								t.Fatalf("get: %v, %v", err, options)
							}
							if terminal.reads[len(terminal.reads)-1].HighPriority != (enabled && hint) {
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

func TestPriorityOption(t *testing.T) {
	if ExperimentalHighPriorityOption&3 != 0 || ExperimentalHighPriorityOption&0x1e != 0x1c {
		t.Fatal("incorrect option properties")
	}
	for _, options := range [][]protocol.Option{
		nil,
		{protocol.NewOption(ExperimentalHighPriorityOption, []byte{1})},
		{protocol.NewOption(ExperimentalHighPriorityOption, nil), protocol.NewOption(ExperimentalHighPriorityOption, nil)},
	} {
		if priorityOptions(&protocol.Message{Options: options}, true).HighPriority {
			t.Fatal("invalid hint accepted")
		}
	}
	request := &protocol.Message{}
	setPriority(request, eris.BlockOptions{HighPriority: true}, true)
	if !priorityOptions(request, true).HighPriority || priorityOptions(request, false).HighPriority {
		t.Fatal("enable switch ignored")
	}
}

func TestExperimentalPriorityOneEndpoint(t *testing.T) {
	for _, clientEnabled := range []bool{false, true} {
		serverEnabled := !clientEnabled
		block, ref := makeBlock(int(eris.BlockSize1KiB), 13)
		high := eris.BlockOptions{HighPriority: true}
		terminal := &priorityStore{BlockSet: eris.BlockSet{ref: {Data: block, Options: high}}}
		handler := Handler{Blocks: terminal, ExperimentalHighPriority: serverEnabled}
		client, err := NewClient(RequesterFunc(handler.ServeCOAP), mustStore(t, "coap://example/"), ClientConfig{ExperimentalHighPriority: clientEnabled})
		if err != nil {
			t.Fatal(err)
		}
		data, err, options := client.GetBlock(context.Background(), ref, 0, high)
		if err != nil || !bytes.Equal(data, block) || options.HighPriority {
			t.Fatalf("get: %v, %v", err, options)
		}
		if terminal.reads[0].HighPriority {
			t.Fatal("disabled endpoint forwarded a hint")
		}
	}
}

// Exercise the wire codecs: high option numbers need an extended delta, and
// block-wise fragments must retain the option after encoding and decoding.
func priorityWireMessage(t *testing.T, transport string, message *protocol.Message) *protocol.Message {
	t.Helper()
	var data []byte
	var err error
	if transport == "udp" {
		data, err = protocol.MarshalUDP(message)
	} else {
		data, err = protocol.MarshalTCP(message)
	}
	if err != nil {
		t.Fatal(err)
	}
	var decoded *protocol.Message
	if transport == "udp" {
		decoded, err = protocol.UnmarshalUDP(data)
	} else {
		decoded, err = protocol.UnmarshalTCP(data, 1<<20)
	}
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestExperimentalPriorityReadErrors(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 14)
	for _, code := range []protocol.Code{protocol.Content, protocol.NotFound, protocol.InternalServerError} {
		client, err := NewClient(RequesterFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
			// A priority hint must not escape on an error, including a successful
			// response whose block does not match the requested reference.
			bad := bytes.Clone(block)
			bad[0]++
			return &protocol.Message{Code: code, Payload: bad, Options: []protocol.Option{
				protocol.NewOption(ExperimentalHighPriorityOption, nil),
			}}, nil
		}), mustStore(t, "coap://example/"), ClientConfig{ExperimentalHighPriority: true})
		if err != nil {
			t.Fatal(err)
		}
		data, err, options := client.GetBlock(context.Background(), ref, 0, eris.BlockOptions{HighPriority: true})
		if err == nil || data != nil || options.HighPriority {
			t.Fatalf("code %v: data=%d bytes, err=%v, options=%v", code, len(data), err, options)
		}
	}
}
