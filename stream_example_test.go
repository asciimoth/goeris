package eris_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
)

func ExampleCapabilityStore_EncodeContentRecommended() {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	capability, err := store.EncodeContentRecommended(context.Background(), strings.NewReader("Hello, ERIS!"), eris.Secret{}, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	reader, err := store.NewReader(context.Background(), capability, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(content))
	// Output: Hello, ERIS!
}
