//go:build windows

package volume_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestDriveRelativeRoot(t *testing.T) {
	root := t.TempDir()
	drive := filepath.VolumeName(root)
	if len(drive) != 2 || drive[1] != ':' {
		t.Skip("test requires a drive-letter path")
	}
	t.Chdir(root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 98)
	if err := os.WriteFile(ref.String(), block, 0o600); err != nil {
		t.Fatal(err)
	}
	v := openVolume(t, drive)
	got, err, _ := eris.GetBlock(t.Context(), v, ref, eris.NoTTL, eris.BlockOptions{})
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("drive-relative root did not select the current directory: %v", err)
	}
}
