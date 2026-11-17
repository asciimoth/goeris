package eris

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkPrefetchReader measures the cost of automatic hints when the
// terminal source ignores them. Small reads expose repeated path work.
func BenchmarkPrefetchReader(b *testing.B) {
	data := make([]byte, 128*1024)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	capability, blocks, err := Encode(data, Secret{}, BlockSize1KiB)
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{1, 1024, 32768} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			reader, err := NewReaderWithSize(context.Background(), capability, blocks, int64(len(data)), BlockOptions{})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = reader.Close() }()
			buffer := make([]byte, size)
			offset := int64(0)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if _, err := reader.ReadAt(buffer, offset); err != nil {
					b.Fatal(err)
				}
				offset += int64(size)
				if offset+int64(size) > int64(len(data)) {
					offset = 0
				}
			}
		})
	}
}
