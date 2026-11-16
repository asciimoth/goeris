package cbor

import (
	"bytes"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestCBOROmitsPriority(t *testing.T) {
	block, ref := testBlock(t, eris.BlockSize1KiB, 5)
	var normal []byte
	for _, high := range []bool{false, true} {
		element, err := NewBlockMap(eris.BlockSet{ref: {Data: block, Options: eris.BlockOptions{HighPriority: high}}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := Marshal([]Element{element})
		if err != nil {
			t.Fatal(err)
		}
		if high && !bytes.Equal(encoded, normal) {
			t.Fatal("priority changed the standard CBOR format")
		}
		normal = encoded
		decoded, err := Unmarshal(encoded)
		if err != nil {
			t.Fatal(err)
		}
		blocks, _ := decoded[0].BlockMap()
		if !bytes.Equal(blocks[ref].Data, block) || blocks[ref].Options.HighPriority {
			t.Fatal("incorrect decoded block")
		}
	}
}
