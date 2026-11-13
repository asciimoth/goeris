package link

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

func BenchmarkFSOpen(b *testing.B) {
	for _, kind := range []string{"ordinary", "link", "large-attribute", "large-mime"} {
		b.Run(kind, func(b *testing.B) {
			data := []byte("ordinary content")
			if kind != "ordinary" {
				metadata := Link{Capability: eris.Capability{BlockSize: eris.BlockSize1KiB}}
				if kind == "large-attribute" {
					metadata.Attributes = map[string]Value{"payload": BytesValue(make([]byte, 512<<10))}
				}
				if kind == "large-mime" {
					metadata.MIME = strings.Repeat("x", 512<<10)
				}
				var err error
				data, err = metadata.MarshalBinary()
				if err != nil {
					b.Fatal(err)
				}
			}
			files, err := NewFS(fstest.MapFS{"file": {Data: data}}, eris.BlockSet{})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				file, err := files.Open("file")
				if err != nil {
					b.Fatal(err)
				}
				if err := file.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
