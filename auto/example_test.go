package auto_test

import (
	"context"
	"fmt"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/auto"
)

func ExampleOpen() {
	// Use an in-memory block store for this example. In a tool, auto.Open()
	// can open volumes from the environment.
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	stores, err := auto.Open(auto.ReplaceBlocks(blocks))
	if err != nil {
		panic(err)
	}
	defer func() { _ = stores.Close() }()

	ctx := context.Background()
	options := eris.BlockOptions{}
	capability, err := stores.Capabilities.EncodeRecommended(ctx, []byte("Hello, ERIS!"), eris.Secret{}, options)
	if err != nil {
		panic(err)
	}
	content, err := stores.Capabilities.Decode(ctx, capability, options)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(content))
	// Output: Hello, ERIS!
}
