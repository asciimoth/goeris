package volume_test

import (
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
