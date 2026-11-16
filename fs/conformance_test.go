package fs

import (
	"bytes"
	"context"
	"encoding/hex"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

// This fixture is hand-written from the v2 CDDL and RFC 8949 section 4.2.1.
// It distinguishes bytewise ordering from length-first and native path order.
func TestV2CanonicalMapVector(t *testing.T) {
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB}
	entries := []Entry{
		{Path: []string{"z"}, Capability: capability, Size: 23},
		{Path: []string{"aa"}, Type: Executable, Capability: capability, Size: 24},
		{Path: []string{"é"}, Type: Symlink, Target: "/a//b/"},
		{Path: []string{"aaa"}, Capability: capability, Size: 256, Compression: Lzip},
		{Path: []string{"long-name"}, Type: Subdirectory, Capability: capability},
		{Path: []string{"a", "b"}, Type: Symlink, Target: "../c"},
		{Path: []string{"a", "z"}, Type: Symlink, Target: ""},
	}
	capHex := "d9011458420a00" + strings.Repeat("00", 64)
	wire := "da657269748202a7" +
		"81617a8300" + capHex + "17" +
		"816261618301" + capHex + "1818" +
		"8162c3a982028560616160616260" +
		"81636161618300" + capHex + "da4c5a4950190100" +
		"81696c6f6e672d6e616d658203" + capHex +
		"8261616162820282622e2e6163" +
		"826161617a82028160"
	want, err := hex.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	for range len(entries) {
		for range 2 {
			got, err := (Index{Entries: entries}).MarshalBinary()
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("canonical map bytes: %x, %v; want %x", got, err, want)
			}
			slices.Reverse(entries)
		}
		entries = append(entries[1:], entries[0])
	}
	parsed, err := ParseIndex(want)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := parsed.MarshalBinary()
	if err != nil || !bytes.Equal(encoded, want) {
		t.Fatalf("canonical map round trip: %v", err)
	}
	empty, err := (Index{}).MarshalBinary()
	if err != nil || hex.EncodeToString(empty) != "da657269748202a0" {
		t.Fatalf("empty v2 index: %x, %v", empty, err)
	}
}

func TestIndexRecommendedBlockSizeBoundary(t *testing.T) {
	for _, size := range []int{16383, 16384, 16385} {
		index := Index{Entries: []Entry{{Path: []string{"f"}, Type: Symlink, Target: strings.Repeat("a", size-17)}}}
		wire, err := index.MarshalBinary()
		if err != nil || len(wire) != size {
			t.Fatalf("index length: %d, %v; want %d", len(wire), err, size)
		}
		blockSize := eris.BlockSize1KiB
		if size >= 16384 {
			blockSize = eris.BlockSize32KiB
		}
		want, wantBlocks, err := eris.Encode(wire, eris.Secret{}, blockSize)
		if err != nil {
			t.Fatal(err)
		}
		blocks := make(eris.BlockSet)
		got, err := EncodeIndex(context.Background(), index, eris.Secret{}, blocks, eris.BlockOptions{})
		if err != nil || got != want || !reflect.DeepEqual(blocks, wantBlocks) {
			t.Fatalf("index size %d: wrong capability or blocks: %v", size, err)
		}
	}
}
