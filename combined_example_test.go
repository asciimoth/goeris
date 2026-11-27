package eris_test

import (
	"context"
	"fmt"

	"github.com/asciimoth/goeris"
)

func ExampleNewCombinedBlockStore() {
	// Expose only reads from the first store. The second store permits writes.
	readOnly := struct{ eris.BlockSource }{eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
	writable := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewCombinedBlockStore(readOnly, writable)
	store.OnError = func(index int, err error) {
		fmt.Printf("store %d: %v\n", index, err)
	}

	// This content fits in one encrypted block.
	ctx := context.Background()
	content := eris.NewBlockCapabilityStore(store)
	capability, err := content.Encode(ctx, []byte("hello"), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	ref := capability.Root.Reference
	_, stored := writable.Blocks[ref]
	fmt.Println("stored:", stored)
	got, err, _ := eris.GetBlock(ctx, store, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println("read bytes:", len(got))

	// Output:
	// stored: true
	// store 0: eris: missing block
	// read bytes: 1024
}
