package volume

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestChainCloseOwnsAllHandles(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("first.cbor", []byte{0x80}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("second", 0o755); err != nil {
		t.Fatal(err)
	}
	chain, err := OpenChain("first.cbor;second")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	// Closing the first handle early forces Close to encounter an error.
	if err := chain.volumes[0].Close(); err != nil {
		t.Fatal(err)
	}
	if err := chain.Close(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("close error not retained: %v", err)
	}
	for index, v := range chain.volumes {
		if _, err := v.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("volume %d left open: %v", index, err)
		}
	}
}
