//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/auto"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/zpgo"
)

type imageDirectoryReadHook struct {
	erisfs.FileSystem
	beforeRead func(string) error
}

func (s imageDirectoryReadHook) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	if err := s.beforeRead(name); err != nil {
		return nil, err
	}
	return s.FileSystem.ReadDir(ctx, name)
}

func TestImagePreserveRootThroughParentStep(t *testing.T) {
	imageTestVolume(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	child := filepath.Join(source, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(child, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want := imageCommand(t, "build", "--preserve-root", source)
	if got := imageCommand(t, "build", "--preserve-root", alias+"/.."); got != want {
		t.Fatalf("preserved image root = %q, want %q", got, want)
	}
}

func TestImageUnpackKeepsConcurrentOutput(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file"), []byte("partial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "last"), 0700); err != nil {
		t.Fatal(err)
	}
	capability := parseTestCapability(t, imageCommand(t, "build", src))
	stores, err := (storageFlags{}).open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	view, err := stores.Images.OpenFS(t.Context(), capability)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		readErr error
		want    string
	}{
		{"read failure", io.ErrUnexpectedEOF, "unexpected EOF"},
		{"successful read", nil, "changed during the transfer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			destination, moved := filepath.Join(parent, "out"), filepath.Join(parent, "moved")
			unrelated := []byte("keep\n")
			hooked := imageDirectoryReadHook{view, func(name string) error {
				if name != "last" {
					return nil
				}
				if err := os.Rename(destination, moved); err != nil {
					return err
				}
				if err := os.Mkdir(destination, 0700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(destination, "unrelated"), unrelated, 0600); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(moved, "last", "unrelated"), unrelated, 0600); err != nil {
					return err
				}
				return tc.readErr
			}}
			browser := imageBrowser{ctx: t.Context(), view: hooked, p: &zpgo.Progress{}}
			err := browser.unpack(destination)
			if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.readErr != nil && !errors.Is(err, tc.readErr)) {
				t.Fatalf("unpack error = %v, want %q", err, tc.want)
			}
			checkImageFile(t, filepath.Join(destination, "unrelated"), unrelated)
			checkImageFile(t, filepath.Join(moved, "last", "unrelated"), unrelated)
			if _, err := os.Stat(filepath.Join(moved, "file")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial output after moving the directory: %v", err)
			}
		})
	}
}

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

// pauseImageBlock holds a file's first block until the test sees a progress
// packet. All index reads still go through the normal volume chain.
type pauseImageBlock struct {
	eris.BlockStore
	ref     eris.Reference
	release <-chan struct{}
}

func (s pauseImageBlock) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	for _, r := range requests {
		if r.Reference == s.ref {
			select {
			case <-s.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			break
		}
	}
	return s.BlockStore.GetBlock(ctx, requests)
}

func TestImageProgressDuringRead(t *testing.T) {
	imageTestVolume(t)
	data := bytes.Repeat([]byte("abcd"), 160)
	src := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	fileCap := encodeTestContent(t, data, "cap", "encode")
	cmd, paths, err := parseCommand([]string{"fs", "cat", urn, "file"})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := captureOutput(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	t.Setenv("ZIG_PROGRESS", strconv.Itoa(int(writer.Fd())))
	p, err := commandProgress(zpgo.Options{RootName: cmd.name})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	release := make(chan struct{})
	cmd.image.meter = new(imageMeter)
	stores, err := paths.open(auto.WrapBlocks(func(s eris.BlockStore) eris.BlockStore {
		return cmd.image.meter.wrap(pauseImageBlock{s, fileCap.Root.Reference, release})
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stores.Close() }()
	done := make(chan error, 1)
	go func() { done <- cmd.image.run(p, stores.Images, os.Stdin) }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
		if got := readOutput(t, stdout); got != string(data) {
			t.Errorf("stdout has %d bytes, want %d", len(got), len(data))
		}
		if got := readOutput(t, stderr); got != "" {
			t.Errorf("stderr = %q", got)
		}
	}()
	if err := reader.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var count [1]byte
		if _, err := io.ReadFull(reader, count[:]); err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, int(count[0])*(9+zpgo.DefaultNameSize))
		if _, err := io.ReadFull(reader, packet); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(packet, []byte("blocks read")) && bytes.Contains(packet, []byte("Reading KiB: file")) {
			break
		}
	}
}
