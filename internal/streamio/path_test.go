package streamio

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestDefaultMountRoots(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("ProgramData", first)
	t.Setenv("LOCALAPPDATA", second)
	want := []string{"/n/eris", "/eris"}
	if filepath.Separator == '\\' {
		want = []string{filepath.Join(first, "eris"), filepath.Join(second, "eris")}
	}
	if got := DefaultMountRoots(); !slices.Equal(got, want) {
		t.Fatalf("default roots = %q, want %q", got, want)
	}

	if filepath.Separator == '\\' {
		t.Setenv("ProgramData", "relative")
		if got := DefaultMountRoots(); !slices.Equal(got, want[1:]) {
			t.Fatalf("roots with relative ProgramData = %q, want %q", got, want[1:])
		}
		t.Setenv("LOCALAPPDATA", "")
		if got := DefaultMountRoots(); len(got) != 0 {
			t.Fatalf("roots without absolute bases = %q, want no roots", got)
		}
	}
}
