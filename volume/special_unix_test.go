//go:build unix

package volume_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
	"golang.org/x/sys/unix"
)

// Run in a subprocess so a blocking FIFO open cannot hang the test suite.
func TestRejectFIFO(t *testing.T) {
	if root := os.Getenv("GOERIS_FIFO_TEST_ROOT"); root != "" {
		fifo := filepath.Join(root, "fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if v, err := volume.Open(fifo); v != nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("FIFO root: %v, %v", v, err)
		}
		v := openVolume(t, root)
		block, ref := testBlock(t, eris.BlockSize1KiB, 91)
		flat := filepath.Join(root, ref.String())
		if err := os.Rename(fifo, flat); err != nil {
			t.Fatal(err)
		}
		if _, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrInvalidBlock) {
			t.Fatalf("FIFO flat block: %v", err)
		}
		nested := filepath.Join(root, volume.BlockPath(ref))
		if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(flat, nested); err != nil {
			t.Fatal(err)
		}
		writeFile(t, flat, block)
		if _, err, _ := eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{}); !errors.Is(err, eris.ErrInvalidBlock) {
			t.Fatalf("FIFO canonical block bypassed: %v", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRejectFIFO$")
	cmd.Env = append(os.Environ(), "GOERIS_FIFO_TEST_ROOT="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO rejection: %v (context: %v)\n%s", err, ctx.Err(), output)
	}
}

func TestFIFOReplacement(t *testing.T) {
	if root := os.Getenv("GOERIS_FIFO_RACE_ROOT"); root != "" {
		fifo := filepath.Join(root, "fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		block, ref := testBlock(t, eris.BlockSize1KiB, 97)
		writeFile(t, filepath.Join(root, "regular"), block)
		if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
			t.Fatal(err)
		}
		v := openVolume(t, root)
		// Replace links atomically so Stat and Open can observe different types.
		for _, target := range []string{"regular", "directory"} {
			name := filepath.Join(root, ref.String())
			stop := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				for {
					for _, dest := range []string{target, "fifo"} {
						select {
						case <-stop:
							done <- nil
							return
						default:
						}
						tmp := filepath.Join(root, "swap")
						if err := os.Symlink(dest, tmp); err != nil {
							done <- err
							return
						}
						if err := os.Rename(tmp, name); err != nil {
							done <- err
							return
						}
					}
				}
			}()
			for range 3000 {
				opened, err := volume.Open(name)
				if err == nil {
					_ = opened.Close()
				}
				_, _, _ = eris.GetBlock(context.Background(), v, ref, eris.NoTTL, eris.BlockOptions{})
			}
			close(stop)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFIFOReplacement$")
	cmd.Env = append(os.Environ(), "GOERIS_FIFO_RACE_ROOT="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO replacement: %v (context: %v)\n%s", err, ctx.Err(), output)
	}
}
