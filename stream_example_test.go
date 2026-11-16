package eris_test

import (
	"fmt"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
)

func ExampleEncodeContentRecommended() {
	store := make(eris.BlockSet) // Any BlockSink can receive the encoded tree.
	capability, err := eris.EncodeContentRecommended(strings.NewReader("Hello, ERIS!"), eris.Secret{}, store)
	if err != nil {
		panic(err)
	}
	reader, err := eris.NewReader(capability, store)
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
