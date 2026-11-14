//go:build unix

package volume_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

// This tests process termination, not loss of the OS page cache or device power.
func TestKilledWriter(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashWriterProcess$")
	cmd.Env = append(os.Environ(), "GOERIS_CRASH_TEST_ROOT="+root)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The writer signals after each successful write. Kill it while it continues
	// to create new blocks and replace an existing block, without running defers.
	reader := bufio.NewReader(stdout)
	for range 10 {
		if line, err := reader.ReadString('\n'); err != nil || line != "stored\n" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("writer did not report success: %q, %v; %s", line, err, stderr.String())
		}
	}
	if err := cmd.Process.Kill(); err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("writer exited without being killed")
	}
	if ctx.Err() != nil {
		t.Fatalf("writer timed out: %v", ctx.Err())
	}
	v := openVolume(t, root)
	count := 0
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".eris-tmp-") {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		ref, err := eris.ParseReference(strings.ReplaceAll(filepath.ToSlash(rel), "/", ""))
		if err != nil {
			return err
		}
		if _, err := v.GetBlock(ref, eris.NoTTL); err != nil {
			return fmt.Errorf("published block after termination: %w", err)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count < 10 {
		t.Fatalf("lost completed blocks: got %d, want at least 10", count)
	}
}

func TestCrashWriterProcess(t *testing.T) {
	root := os.Getenv("GOERIS_CRASH_TEST_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	v := openVolume(t, root)
	block := make([]byte, eris.BlockSize32KiB)
	for i := uint64(0); ; i++ {
		binary.LittleEndian.PutUint64(block, i)
		if _, err := v.PutBlock(block, eris.NoTTL); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "stored"); err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint64(block, 0)
		if _, err := v.PutBlock(block, eris.NoTTL); err != nil {
			t.Fatal(err)
		}
	}
}
