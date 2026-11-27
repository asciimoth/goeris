//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/zpgo"
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

func TestBlockGetProgress(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	data := blockTestData(eris.BlockSize1KiB, 1)
	hash := putTestBlock(t, data, "block", "put")
	missing := eris.Reference{}.String()
	for _, tc := range []struct {
		name      string
		hashes    []string
		code      int
		completed uint32
		closeOut  bool
	}{
		{"success", []string{hash, hash, hash}, 0, 3, false},
		{"missing", []string{hash, missing, hash}, 1, 1, false},
		{"write failure", []string{hash, hash, hash}, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := captureOutput(t)
			if tc.closeOut {
				if err := stdout.Close(); err != nil {
					t.Fatal(err)
				}
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = reader.Close()
				_ = writer.Close()
			})
			t.Setenv("ZIG_PROGRESS", strconv.Itoa(int(writer.Fd())))
			if code := run(append([]string{"block", "get"}, tc.hashes...)); code != tc.code {
				t.Fatalf("exit code = %d, want %d", code, tc.code)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			report, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			// Each Zig progress packet holds one root node with two uint32
			// counters, a fixed-size name, and a parent index.
			const packetSize = 1 + 8 + zpgo.DefaultNameSize + 1
			if len(report) == 0 || len(report)%packetSize != 0 {
				t.Fatalf("progress report has %d bytes, want one or more %d-byte packets", len(report), packetSize)
			}
			last := report[len(report)-packetSize:]
			if nodes, completed, total := last[0], binary.LittleEndian.Uint32(last[1:]), binary.LittleEndian.Uint32(last[5:]); nodes != 1 || completed != tc.completed || total != uint32(len(tc.hashes)) {
				t.Errorf("progress reports %d/%d with %d nodes, want %d/%d with one node", completed, total, nodes, tc.completed, len(tc.hashes))
			}
		})
	}
}

func TestBlockGetProgressStandardStreams(t *testing.T) {
	const childHash = "GOERIS_TEST_BLOCK_GET_HASH"
	if hash := os.Getenv(childHash); hash != "" {
		os.Exit(run([]string{"block", "get", hash, hash}))
	}

	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	data := blockTestData(eris.BlockSize1KiB, 1)
	hash := putTestBlock(t, data, "block", "put")
	for _, descriptor := range []string{"1", "02"} {
		t.Run(descriptor, func(t *testing.T) {
			// The child needs real pipes at descriptors 1 and 2 for this check.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBlockGetProgressStandardStreams$")
			cmd.Env = append(os.Environ(), childHash+"="+hash, "ZIG_PROGRESS="+descriptor)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || string(out) != string(data)+string(data) || stderr.Len() != 0 {
				t.Fatalf("get with ZIG_PROGRESS=%s: %v, stdout = %d bytes, stderr = %q", descriptor, err, len(out), stderr.String())
			}
		})
	}
}
