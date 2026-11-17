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
	capability, blocks, err := eris.Encode([]byte("hello"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		panic(err)
	}
	ref := capability.Root.Reference
	stored, err := eris.PutBlock(context.Background(), store, blocks.Blocks[ref].Data, 0, eris.BlockOptions{}, ref)
	if err != nil {
		panic(err)
	}
	fmt.Println("stored:", stored == ref)
	got, err, _ := eris.GetBlock(context.Background(), store, ref, 0, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println("read bytes:", len(got))

	// Output:
	// stored: true
	// store 0: eris: missing block
	// read bytes: 1024
}
