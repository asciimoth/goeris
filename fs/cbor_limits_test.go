package fs

import (
	"bytes"
	"errors"
	"runtime"
	"testing"

	"github.com/asciimoth/goeris/internal/cborwire"
)

func TestIndexRejectsInvalidPathBeforeAllocation(t *testing.T) {
	// A path array with 262144 empty components is invalid at its first
	// component. Do not allocate a large path before rejecting that component.
	data := []byte{0xda, 0x65, 0x72, 0x69, 0x74, 0x82, 2, 0xa1, 0x9a, 0, 4, 0, 0}
	data = append(data, bytes.Repeat([]byte{0x60}, 1<<18)...)
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

func TestIndexRejectsDuplicateKeysBeforeAllocation(t *testing.T) {
	for _, version := range []byte{1, 2} {
		// The second entry repeats the first key. Reject it before decoding
		// the rest of a large map or sorting the resulting entries.
		data := append([]byte(nil), indexTagHeader[:]...)
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
