package eris_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
)

func ExampleEncodeContentRecommended() {
	store := make(eris.BlockSet) // Any BlockSink can receive the encoded tree.
	capability, err := eris.EncodeContentRecommended(context.Background(), strings.NewReader("Hello, ERIS!"), eris.Secret{}, store, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	reader, err := eris.NewReader(context.Background(), capability, store, eris.BlockOptions{})
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
