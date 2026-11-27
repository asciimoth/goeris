package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/zpgo"
)

func TestBlockRoundTrip(t *testing.T) {
	for i, size := range []eris.BlockSize{
		eris.BlockSize1KiB, eris.BlockSize32KiB, eris.BlockSize1KiB, eris.BlockSize32KiB,
	} {
		t.Run(fmt.Sprintf("%d bytes case %d", size, i+1), func(t *testing.T) {
			t.Setenv("ERIS_BLOCK_PATH", ";")
			volume := t.TempDir()
			data := blockTestData(size, byte(i))
			hash := putTestBlock(t, data, "--replace-blocks", volume, "block", "put")
			code, out, errout := runBlockCommand(t, nil, "block", "get", "--replace-blocks", volume, hash)
			if code != 0 || out != string(data) || errout != "" {
				t.Fatalf("get exit code = %d, stdout = %d bytes (match: %t), stderr = %q",
					code, len(out), out == string(data), errout)
			}
		})
	}
}

func TestBlockGetMultiple(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", ";")
	a := blockTestData(eris.BlockSize1KiB, 1)
	b := blockTestData(eris.BlockSize32KiB, 2)
	c := blockTestData(eris.BlockSize1KiB, 3)
	hashA := putTestBlock(t, a, "block", "--replace-blocks", first, "put")
	hashB := putTestBlock(t, b, "block", "put", "--replace-blocks", second)
	hashC := putTestBlock(t, c, "block", "put", "--replace-blocks", first)
	want := string(a) + string(b) + string(c) + string(b)
	code, out, errout := runBlockCommand(t, nil,
		"block", "--replace-blocks="+first, "get", "--append-blocks="+second, "--batch-size=4", hashA, hashB, hashC, hashB)
	if code != 0 || out != want || errout != "" {
		t.Fatalf("get exit code = %d, stdout = %d bytes (match: %t), stderr = %q", code, len(out), out == want, errout)
	}
}

func TestBlockPutInvalidSize(t *testing.T) {
	for _, size := range []int{0, 1023, 1025, 32767, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ERIS_BLOCK_PATH", root)
			code, out, errout := runBlockCommand(t, make([]byte, size), "block", "put")
			if code != 1 || out != "" || !strings.Contains(errout, eris.ErrInvalidBlockSize.Error()) {
				t.Errorf("put exit code = %d, stdout = %q, stderr = %q", code, out, errout)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Errorf("volume entries = %v, error = %v, want an empty volume", entries, err)
			}
		})
	}
}

func TestBlockPutStorageFailure(t *testing.T) {
	code, out, errout := runBlockCommand(t, blockTestData(eris.BlockSize1KiB, 1),
		"block", "put", "--replace-blocks=")
	if code != 1 || out != "" || !strings.Contains(errout, eris.ErrNoBlockStorage.Error()) {
		t.Fatalf("put exit code = %d, stdout = %q, stderr = %q", code, out, errout)
	}
}

func TestBlockGetMissing(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	first := blockTestData(eris.BlockSize1KiB, 1)
	hashA := putTestBlock(t, first, "block", "put")
	hashB := putTestBlock(t, blockTestData(eris.BlockSize32KiB, 2), "block", "put")
	missing := eris.Reference{}.String()
	code, out, errout := runBlockCommand(t, nil, "block", "get", hashA, missing, hashB)
	if code != 1 || out != string(first) || !strings.Contains(errout, missing) || !strings.Contains(errout, eris.ErrMissingBlock.Error()) {
		t.Fatalf("get exit code = %d, stdout = %d bytes (match: %t), stderr = %q", code, len(out), out == string(first), errout)
	}
}

func TestBlockArguments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"group help", []string{"block"}, 0, usage, ""},
		{"put help", []string{"block", "put", "--help"}, 0, usage, ""},
		{"get help", []string{"block", "get", "--help"}, 0, usage, ""},
		{"prefetch help", []string{"block", "prefetch", "--help"}, 0, usage, ""},
		{"check help", []string{"block", "check", "--help"}, 0, usage, ""},
		{"update help", []string{"block", "update", "--help"}, 0, usage, ""},
		{"no hashes", []string{"block", "get"}, 0, "", ""},
		{"prefetch without hashes", []string{"block", "prefetch"}, 2, "", "expected at least one block hash"},
		{"check without hashes", []string{"block", "check"}, 2, "", "block check: expected at least one block hash"},
		{"update without hashes", []string{"block", "update"}, 2, "", "block update: expected at least one block hash"},
		{"unknown subcommand", []string{"block", "wrong"}, 2, "", `unknown subcommand "wrong"`},
		{"old options group", []string{"block", "options", "check"}, 2, "", `block: unknown subcommand "options"`},
		{"put argument", []string{"block", "put", "extra"}, 2, "", "unexpected arguments"},
		{"batch size on put", []string{"block", "put", "--batch-size=4"}, 2, "", "flag provided but not defined"},
		{"root flag on check", []string{"block", "check", "--root"}, 2, "", "flag provided but not defined"},
		{"invalid bool on put", []string{"block", "put", "--root=maybe"}, 2, "", `invalid boolean value "maybe"`},
		{"negative batch size", []string{"block", "get", "--batch-size=-1"}, 2, "", "--batch-size must be at least 0"},
		{"invalid hash", []string{"block", "get", "--replace-blocks=", "invalid"}, 1, "", `invalid block hash "invalid"`},
		{"invalid prefetch hash", []string{"block", "prefetch", "--replace-blocks=", "invalid"}, 1, "", `invalid block hash "invalid"`},
		{"invalid update hash", []string{"block", "update", "--replace-blocks=", "invalid"}, 1, "", `block update: invalid block hash "invalid"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ERIS_BLOCK_PATH", ";")
			code, out, errout := runBlockCommand(t, nil, tc.args...)
			if code != tc.code || out != tc.stdout || tc.stderr == "" && errout != "" || !strings.Contains(errout, tc.stderr) {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, out, errout)
			}
		})
	}
}

func TestBlockGetBatchSizes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int
		batchSize int
		want      []int
	}{
		{"automatic", 0, 0, []int{64, 3}},
		{"explicit", 0, 65, []int{65, 2}},
		{"single", 0, 1, slices.Repeat([]int{1}, 67)},
		{"storage limit", 35, 65, []int{35, 32}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := captureOutput(t)
			data := blockTestData(eris.BlockSize1KiB, 1)
			ref, err := eris.ReferenceOf(data)
			if err != nil {
				t.Fatal(err)
			}
			source := &recordingBlockSource{BlockSet: eris.BlockSet{
				Blocks:       map[eris.Reference]eris.Block{ref: {Data: data}},
				MaxBatchSize: tc.limit,
			}}
			var progress zpgo.Progress
			hashes := slices.Repeat([]string{ref.String()}, 67)
			if err := blockGet(&progress, source, hashes, tc.batchSize); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(source.sizes, tc.want) {
				t.Errorf("batch sizes = %v, want %v", source.sizes, tc.want)
			}
			if out := readOutput(t, stdout); out != strings.Repeat(string(data), len(hashes)) {
				t.Errorf("get output mismatch (%d bytes)", len(out))
			}
		})
	}
}

func TestBlockGetBatchFailure(t *testing.T) {
	bad := eris.Reference{}
	for _, tc := range []struct {
		name     string
		hash     string
		want     error
		wantSize int
	}{
		{"missing", bad.String(), eris.ErrMissingBlock, 3},
		{"corrupt", bad.String(), eris.ErrInvalidBlock, 3},
		{"malformed hash", "invalid", eris.ErrInvalidBlock, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := captureOutput(t)
			data := blockTestData(eris.BlockSize1KiB, 1)
			ref, err := eris.ReferenceOf(data)
			if err != nil {
				t.Fatal(err)
			}
			blocks := map[eris.Reference]eris.Block{ref: {Data: data}}
			if tc.name == "corrupt" {
				blocks[bad] = eris.Block{Data: data}
			}
			source := &recordingBlockSource{BlockSet: eris.BlockSet{Blocks: blocks}}
			var progress zpgo.Progress
			hashes := []string{ref.String(), tc.hash, ref.String(), ref.String()}
			err = blockGet(&progress, source, hashes, 3)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.hash) {
				t.Errorf("get error = %v, want %v for %q", err, tc.want, tc.hash)
			}
			if !slices.Equal(source.sizes, []int{tc.wantSize}) {
				t.Errorf("batch sizes = %v, want [%d]", source.sizes, tc.wantSize)
			}
			if out := readOutput(t, stdout); out != string(data) {
				t.Errorf("get output mismatch (%d bytes)", len(out))
			}
		})
	}
}

// blockTestData includes every byte value so text conversion changes the output.
func blockTestData(size eris.BlockSize, seed byte) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = seed + byte(i)
	}
	return data
}

// putTestBlock verifies that the command prints the reference for the raw data.
func putTestBlock(t *testing.T, data []byte, args ...string) string {
	t.Helper()
	ref, err := eris.ReferenceOf(data)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errout := runBlockCommand(t, data, args...)
	if code != 0 || out != ref.String()+"\n" || errout != "" {
		t.Fatalf("put exit code = %d, stdout = %q, stderr = %q, want hash %s", code, out, errout, ref)
	}
	return strings.TrimSuffix(out, "\n")
}

// runBlockCommand replaces the process streams for a serial CLI test.
func runBlockCommand(t *testing.T, input []byte, args ...string) (int, string, string) {
	t.Helper()
	stdout, stderr := captureOutput(t)
	if input != nil {
		stdin := createOutputFile(t, filepath.Join(t.TempDir(), "stdin"))
		if _, err := stdin.Write(input); err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		original := os.Stdin
		os.Stdin = stdin
		defer func() { os.Stdin = original }()
	}
	code := run(args)
	return code, readOutput(t, stdout), readOutput(t, stderr)
}

// recordingBlockSource records requests while using BlockSet's batch behavior.
type recordingBlockSource struct {
	eris.BlockSet
	sizes []int
}

func (s *recordingBlockSource) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	s.sizes = append(s.sizes, len(requests))
	return s.BlockSet.GetBlock(ctx, requests)
}
