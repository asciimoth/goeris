package volume_test

import (
	"fmt"
	"os"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func Example() {
	// The application creates the volume root.
	root, err := os.MkdirTemp("", "eris-volume-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	store, err := volume.Open(root)
	if err != nil {
		panic(err)
	}
	defer func() { _ = store.Close() }()

	_, blocks, err := eris.Encode([]byte("Hello, ERIS!"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		panic(err)
	}
	for ref, block := range blocks {
		if _, err := store.PutBlock(block, eris.NoTTL, ref); err != nil {
			panic(err)
		}
		stored, err := store.GetBlock(ref, eris.NoTTL)
		if err != nil {
			panic(err)
		}
		fmt.Println(len(stored))
	}
	// Output: 1024
}
