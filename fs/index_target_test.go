package fs

import (
	"strings"
	"testing"
)

func BenchmarkParseLinkTarget(b *testing.B) {
	index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink, Target: strings.Repeat("component/", 64)}}}
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

func TestLinkTargetRoundTrip(t *testing.T) {
	for _, target := range []string{"", "name", "/", "/a//b/", strings.Repeat("目/", 256)} {
		index := Index{Entries: []Entry{{Path: []string{"link"}, Type: Symlink, Target: target}}}
		data, err := index.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseIndex(data)
		if err != nil {
			t.Fatal(err)
		}
		clear(data)
		if len(got.Entries) != 1 || got.Entries[0].Target != target {
			t.Fatalf("decoded entries for %q: %v", target, got.Entries)
		}
	}
}
