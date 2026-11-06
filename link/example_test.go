package link_test

import (
	"fmt"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

func Example() {
	payload := []byte("hello")
	capability, _, err := eris.EncodeRecommended(payload, eris.Secret{})
	if err != nil {
		panic(err)
	}

	file, err := link.New(capability, uint64(len(payload)), "text/plain")
	if err != nil {
		panic(err)
	}
	filename, err := link.TextValue("greeting.txt")
	if err != nil {
		panic(err)
	}
	file.Attributes = map[string]link.Value{"filename": filename}

	data, err := file.MarshalBinary()
	if err != nil {
		panic(err)
	}
	parsed, err := link.Parse(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(parsed.Size, parsed.MIME, len(parsed.Attributes))
	// Output: 5 text/plain 1
}
