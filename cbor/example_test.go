package cbor_test

import (
	"context"
	"fmt"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cbor"
)

func Example() {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	capability, err := eris.NewBlockCapabilityStore(blocks).EncodeRecommended(context.Background(), []byte("example"), eris.Secret{}, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	blockMap, err := cbor.NewBlockMap(blocks)
	if err != nil {
		panic(err)
	}
	readCapability, err := cbor.NewCapability(capability)
	if err != nil {
		panic(err)
	}

	encoded, err := cbor.MarshalSelfDescribed([]cbor.Element{blockMap, readCapability})
	if err != nil {
		panic(err)
	}
	elements, err := cbor.Unmarshal(encoded)
	if err != nil {
		panic(err)
	}
	for _, element := range elements {
		fmt.Println(element.Kind())
	}
	// Output:
	// block map
	// capability
}
