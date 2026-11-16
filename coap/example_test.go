package coap_test

import (
	"context"
	"fmt"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	eriscoap "github.com/asciimoth/goeris/coap"
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
	block, _ := encodedBlocks.GetBlock(context.Background(), reference, 0)

	_, _ = client.PutBlock(context.Background(), block, 0, reference)
	received, _ := client.GetBlock(context.Background(), reference, 0)
	fmt.Println(len(received))
	// Output: 1024
}
