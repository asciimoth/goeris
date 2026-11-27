package coap_test

import (
	"context"
	"fmt"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	eriscoap "github.com/asciimoth/goeris/coap"
)

func ExampleClient() {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	server := eriscoap.Handler{Blocks: blocks, AllowPut: true}
	requester := eriscoap.RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
		return server.ServeCOAP(ctx, request)
	})
	store, _ := eriscoap.ParseStoreURL("coap+tcp://store.example/")
	client, _ := eriscoap.NewClient(requester, store, eriscoap.ClientConfig{})

	encodedBlocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	encoded, _ := eris.NewBlockCapabilityStore(encodedBlocks).Encode(context.Background(), []byte("hello"), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	reference := encoded.Root.Reference
	block, _, _ := eris.GetBlock(context.Background(), encodedBlocks, reference, 0, eris.BlockOptions{})

	_, _ = eris.PutBlock(context.Background(), client, block, 0, eris.BlockOptions{}, reference)
	received, _, _ := eris.GetBlock(context.Background(), client, reference, 0, eris.BlockOptions{})
	fmt.Println(len(received))
	// Output: 1024
}
