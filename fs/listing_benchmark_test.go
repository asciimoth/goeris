package fs

import (
	"fmt"
	stdfs "io/fs"
	"strings"
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

func BenchmarkParseWideIndex(b *testing.B) {
	data, err := wideIndex().MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := ParseIndex(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseDeepIndex(b *testing.B) {
	index := wideIndex()
	for i := range index.Entries {
		index.Entries[i].Path = append([]string{"one", "two", "three", "four", "five", "six", "seven", "eight"}, index.Entries[i].Path...)
	}
	data, err := index.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := ParseIndex(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCachedMountRoot(b *testing.B) {
	blocks, _ := mountFixture(b)
	m := withMountCache(b, NewMount(b.Context(), blocks, eris.BlockOptions{}), 0, 1024)
	for i := range 1024 {
		ref := eris.Reference{byte(i >> 8), byte(i)}
		m.rememberBlock(ref, 1024)
	}
	b.Run("Stat", func(b *testing.B) {
		for b.Loop() {
			if _, err := m.Stat("."); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ReadDir", func(b *testing.B) {
		for b.Loop() {
			if _, err := m.ReadDir("."); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkBuildWideTree(b *testing.B) {
	index := wideIndex()
	for b.Loop() {
		buildTree(index, nil, ".", DefaultMaxIndexSize)
	}
}

func BenchmarkImageStat(b *testing.B) {
	for _, name := range []string{"file", "one/two/three/file"} {
		b.Run(name, func(b *testing.B) {
			index := Index{Entries: []Entry{{Path: strings.Split(name, "/"), Type: Regular}}}
			image := &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
			for b.Loop() {
				if _, err := image.Stat(b.Context(), name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBindMountedEntries(b *testing.B) {
	image := &FS{root: buildTree(wideIndex(), nil, ".", DefaultMaxIndexSize), prefix: "."}
	source, err := image.ReadDir(b.Context(), ".")
	if err != nil {
		b.Fatal(err)
	}
	entries := make([]stdfs.DirEntry, len(source))
	for b.Loop() {
		copy(entries, source)
		bindMountedEntries(b.Context(), entries)
	}
}
