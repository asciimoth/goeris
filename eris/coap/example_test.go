package coap_test

import (
	"context"
	"fmt"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/eris-tools/eris"
	eriscoap "github.com/asciimoth/eris-tools/eris/coap"
)

func ExampleClient() {
	blocks := make(eris.BlockSet)
	server := eriscoap.Handler{Blocks: blocks, AllowPut: true}
	requester := eriscoap.RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
		return server.ServeCOAP(ctx, request)
	})
	store, _ := eriscoap.ParseStoreURL("coap+tcp://store.example/")
	client, _ := eriscoap.NewClient(requester, store, eriscoap.ClientConfig{})

	encoded, encodedBlocks, _ := eris.Encode([]byte("hello"), eris.Secret{}, eris.BlockSize1KiB)
	reference := encoded.Root.Reference
	block, _ := encodedBlocks.GetBlock(reference)

	_ = client.PutBlock(reference, block)
	received, _ := client.GetBlock(reference)
	fmt.Println(len(received))
	// Output: 1024
}
