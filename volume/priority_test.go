package volume_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestVolumeIgnoresPriority(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	v := openVolume(t, root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 71)
	high := eris.BlockOptions{HighPriority: true, Root: true}
	for _, options := range []eris.BlockOptions{high, {}, high} {
		if _, err := eris.PutBlock(ctx, v, block, 0, options, ref); err != nil {
			t.Fatal(err)
		}
		got, err, stored := eris.GetBlock(ctx, v, ref, 0, options)
		if err != nil || !bytes.Equal(got, block) || stored != (eris.BlockOptions{}) {
			t.Fatalf("get: %v, %v", err, stored)
		}
		if changed, err := eris.UpdateBlockOptions(ctx, v, ref, 0, high); err != nil || changed {
			t.Fatalf("update: %v, %v", changed, err)
		}
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v = openVolume(t, root)
	if _, err, stored := eris.GetBlock(ctx, v, ref, 0, high); err != nil || stored != (eris.BlockOptions{}) {
		t.Fatalf("reopened get: %v, %v", err, stored)
	}
}
