package fs

import (
	"fmt"
	stdfs "io/fs"
	"testing"

	"github.com/asciimoth/goeris"
)

func wideIndex() Index {
	var index Index
	for i := 1023; i >= 0; i-- {
		index.Entries = append(index.Entries, Entry{
			Path: []string{fmt.Sprintf("file%d", i)}, Type: Regular,
			Capability: eris.Capability{BlockSize: eris.BlockSize1KiB},
		})
	}
	return index
}

func BenchmarkWideDirectory(b *testing.B) {
	image := &FS{root: buildTree(wideIndex(), nil, ".", DefaultMaxIndexSize), prefix: "."}
	ctx := b.Context()
	b.Run("ReadDir", func(b *testing.B) {
		for b.Loop() {
			if _, err := image.ReadDir(ctx, "."); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ReadOne", func(b *testing.B) {
		for b.Loop() {
			file, err := image.Open(ctx, ".")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := file.(stdfs.ReadDirFile).ReadDir(1); err != nil {
				b.Fatal(err)
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkMarshalWideIndex(b *testing.B) {
	index := wideIndex()
	for b.Loop() {
		if _, err := index.MarshalBinary(); err != nil {
			b.Fatal(err)
		}
	}
}
