package fs

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"testing"

	"github.com/asciimoth/goeris/internal/cborwire"
)

func TestIndexRejectsInvalidPathBeforeAllocation(t *testing.T) {
	// A path array with 262144 empty components is invalid at its first
	// component. Include enough bytes to pass the minimum length check.
	data := []byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 2, 0xa1, 0x9a, 0, 4, 0, 0}
	data = append(data, bytes.Repeat([]byte{0x60}, 2<<18)...)
	data = append(data, 0x82, 2, 0x80)
	// This test is sequential to exclude parallel package tests from the count.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ParseIndex(data)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrInvalidIndex) {
		t.Fatalf("invalid path: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("invalid path allocated %d bytes; maximum is 4 MiB", allocated)
	}
}

func TestDecodedIndexPathsAreIndependent(t *testing.T) {
	for _, version := range []byte{1, 2} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			var original Index
			for _, depth := range []int{1, 8, 9, 16, 33} {
				for i := range 40 {
					components := make([]string, depth)
					components[0] = fmt.Sprintf("%02d-%02d", depth, i)
					for j := 1; j < depth; j++ {
						components[j] = "dir"
					}
					original.Entries = append(original.Entries, Entry{Path: components, Type: Symlink, Target: "target"})
				}
			}
			data, err := original.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			data[len(MagicPrefix)+1] = version
			decoded, err := ParseIndex(data)
			if err != nil {
				t.Fatal(err)
			}
			clear(data)
			if len(decoded.Entries) != len(original.Entries) {
				t.Fatalf("got %d entries, want %d", len(decoded.Entries), len(original.Entries))
			}
			for i, entry := range decoded.Entries {
				if !slices.Equal(entry.Path, original.Entries[i].Path) {
					t.Fatalf("path %d = %q, want %q", i, entry.Path, original.Entries[i].Path)
				}
				// Appending to one path must preserve the following path.
				extended := append(entry.Path, "extra")
				extended[len(entry.Path)] = "changed"
				entry.Path[0] = "changed"
			}
		})
	}
}

func TestIndexRejectsDuplicateKeysBeforeAllocation(t *testing.T) {
	for _, version := range []byte{1, 2} {
		// The second entry repeats the first key. Reject it before decoding
		// the rest of a large map or sorting the resulting entries.
		data := append([]byte(nil), []byte(MagicPrefix)...)
		data = append(data, 0x82, version)
		data = cborwire.AppendHead(data, 5, 1<<18)
		data = append(data, bytes.Repeat([]byte{0x81, 0x61, 'a', 0x82, 2, 0x80}, 1<<18)...)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := ParseIndex(data)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrInvalidIndex) {
			t.Fatalf("v%d duplicate keys: %v", version, err)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
			t.Fatalf("v%d duplicate keys allocated %d bytes; maximum is 4 MiB", version, allocated)
		}
	}
}
