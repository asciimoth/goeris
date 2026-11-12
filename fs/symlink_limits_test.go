package fs

import (
	"errors"
	stdfs "io/fs"
	"strings"
	"testing"
)

func expandingLinkImage() *FS {
	index := Index{Entries: []Entry{{
		Path: []string{"loop"}, Type: Symlink,
		Target: strings.Repeat("loop/", 4096) + "loop",
	}}}
	return &FS{root: buildTree(index, nil, ".", DefaultMaxIndexSize), prefix: "."}
}

func TestExpandingSymlink(t *testing.T) {
	t.Parallel()
	image := expandingLinkImage()
	if _, err := image.Stat("loop"); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("expanding link error = %v", err)
	}
	if info, err := image.Lstat("loop"); err != nil || info.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat must not expand the target: %v", err)
	}
}

func TestExpandingSymlinkMemoryLimit(t *testing.T) {
	// Keep this test serial so allocations from parallel tests do not affect
	// the benchmark measurement. The old component-copying resolver allocated
	// more than 50 MiB for this 20 KiB target before rejecting its link loop.
	result := testing.Benchmark(BenchmarkExpandingSymlink)
	if allocated := result.AllocedBytesPerOp(); allocated > 64<<10 {
		t.Fatalf("link lookup allocated %d bytes; maximum is 64 KiB", allocated)
	}
}

// Each followed link adds thousands of pending components. Lookup must retain
// the target strings without allocating a growing list of their components.
func BenchmarkExpandingSymlink(b *testing.B) {
	image := expandingLinkImage()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := image.Stat("loop"); !errors.Is(err, stdfs.ErrInvalid) {
			b.Fatal(err)
		}
	}
}
