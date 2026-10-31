package cborwire

import (
	"bytes"
	"testing"
)

func TestAppendHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value uint64
		want  []byte
	}{
		{0, []byte{0xc0}},
		{23, []byte{0xd7}},
		{24, []byte{0xd8, 0x18}},
		{255, []byte{0xd8, 0xff}},
		{256, []byte{0xd9, 0x01, 0x00}},
		{65535, []byte{0xd9, 0xff, 0xff}},
		{65536, []byte{0xda, 0x00, 0x01, 0x00, 0x00}},
		{1 << 32, []byte{0xdb, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}},
	}
	for _, test := range tests {
		got := AppendHead([]byte{0xaa}, 6, test.value)
		want := append([]byte{0xaa}, test.want...)
		if !bytes.Equal(got, want) {
			t.Errorf("AppendHead(%d) = %x, want %x", test.value, got, want)
		}
	}
}
