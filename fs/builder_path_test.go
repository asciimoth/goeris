package fs

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestBuilderDeepPathConflicts(t *testing.T) {
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
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
				builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
				if err := builder.AddSymlink(name, "target"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBuilderSharedPrefix(b *testing.B) {
	names := make([]string, 64)
	prefix := strings.Repeat("dir/", 64)
	for i := range names {
		names[i] = prefix + fmt.Sprint(i)
	}
	for b.Loop() {
		builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
		for _, name := range names {
			if err := builder.AddSymlink(name, "target"); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestBuilderCheckPathDoesNotReserve(t *testing.T) {
	builder := NewBuilder(newTestStore(), eris.Secret{}, eris.BlockOptions{})
	for _, name := range []string{"shared/a", "shared/b/c", "shared/b/d"} {
		if err := builder.CheckPath(name + "/child"); err != nil {
			t.Fatal(err)
		}
		if err := builder.CheckPath(name); err != nil {
			t.Fatal(err)
		}
		if err := builder.AddSymlink(name, "target"); err != nil {
			t.Fatal(err)
		}
		if err := builder.CheckPath(name); !errors.Is(err, ErrPathConflict) {
			t.Fatalf("CheckPath(%q) after add = %v", name, err)
		}
	}
}
