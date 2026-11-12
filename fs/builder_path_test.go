package fs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestBuilderDeepPathConflicts(t *testing.T) {
	builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
	prefix := strings.Repeat("d/", 4096)
	for _, name := range []string{prefix + "a", prefix + "b", "other"} {
		if err := builder.AddSymlink(name, "target"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"d", strings.TrimSuffix(prefix, "/"), prefix + "a", prefix + "a/child"} {
		if err := builder.AddSymlink(name, "target"); !errors.Is(err, ErrPathConflict) {
			t.Fatalf("conflict at depth %d: %v", strings.Count(name, "/"), err)
		}
	}
	if len(builder.Index().Entries) != 3 {
		t.Fatal("path conflicts changed the index")
	}
	if err := builder.Index().Validate(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkBuilderDeepPath(b *testing.B) {
	for _, depth := range []int{1024, 4096} {
		b.Run(fmt.Sprint(depth), func(b *testing.B) {
			name := strings.Repeat("d/", depth) + "file"
			b.ReportAllocs()
			for b.Loop() {
				builder := NewBuilder(make(eris.BlockSet), eris.Secret{})
				if err := builder.AddSymlink(name, "target"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
