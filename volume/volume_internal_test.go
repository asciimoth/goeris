package volume

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestAbsentParents(t *testing.T) {
	name := BlockPath(eris.Reference{})
	for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
		t.Run(parent, func(t *testing.T) {
			root := t.TempDir()
			v, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = v.Close() })
			ctx := context.Background()
			if err := v.checkAbsentParents(ctx, name); err != nil {
				t.Fatalf("absent parents: %v", err)
			}
			path := filepath.Join(root, parent)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// Exercise the fallback guard on Unix too, where the initial Stat
			// would already distinguish this error from an absent path.
			if err := v.checkAbsentParents(ctx, name); err == nil || errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("non-directory parent treated as absent: %v", err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := v.checkAbsentParents(ctx, name); err != nil {
				t.Fatalf("directory parent: %v", err)
			}
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			if err := v.checkAbsentParents(ctx, name); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled parent check: %v", err)
			}
		})
	}
}
