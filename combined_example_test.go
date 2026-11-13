package eris_test

import (
	"fmt"

	"github.com/asciimoth/goeris"
)

func ExampleNewCombinedBlockStore() {
	// Expose only reads from the first store. The second store permits writes.
	readOnly := struct{ eris.BlockSource }{make(eris.BlockSet)}
	writable := make(eris.BlockSet)
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
	stored, err := store.PutBlock(blocks[ref], 0, ref)
	if err != nil {
		panic(err)
	}
	fmt.Println("stored:", stored == ref)
	got, err := store.GetBlock(ref, 0)
	if err != nil {
		panic(err)
	}
	fmt.Println("read bytes:", len(got))

	// Output:
	// stored: true
	// store 0: eris: missing block
	// read bytes: 1024
}
