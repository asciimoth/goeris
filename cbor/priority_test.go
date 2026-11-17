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
		element, err := NewBlockMap(eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block, Options: eris.BlockOptions{HighPriority: high, Root: high}}}})
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
		if !bytes.Equal(blocks.Blocks[ref].Data, block) || blocks.Blocks[ref].Options != (eris.BlockOptions{}) {
			t.Fatal("incorrect decoded block")
		}
	}
}
