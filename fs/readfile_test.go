package fs

import (
	"errors"
	stdfs "io/fs"
	"testing"
)

func TestReadFileDoesNotAllocateDeclaredSize(t *testing.T) {
	// The standard fs.ReadFile fallback allocates Size()+1 bytes before Read.
	// A maximum native int makes that allocation panic on both 32 and 64 bits.
	size := int64(int(^uint(0) >> 1))
	image := compressedImage(t, lzipMember(t, []byte("small")), size)
	got, err := stdfs.ReadFile(image, "file")
	if string(got) != "small" || !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("forged size: got %q, %v", got, err)
	}
}
