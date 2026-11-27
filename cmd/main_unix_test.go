//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"io"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestRunProgressWithStoreSetupFailure(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", ";")
	captureOutput(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	t.Setenv("ZIG_PROGRESS", strconv.Itoa(int(writer.Fd())))

	if code := run([]string{"dump"}); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Read to EOF to check that run closed its copy of the progress pipe.
	if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	report, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) == 0 {
		t.Fatal("no progress report before the store setup failure")
	}
}
