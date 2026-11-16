package volume_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

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
