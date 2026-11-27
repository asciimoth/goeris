package main

import (
	"bytes"
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

func TestBlockOptionCommandsWithVolume(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", ";")
	root := t.TempDir()
	data := blockTestData(eris.BlockSize1KiB, 1)
	hash := putTestBlock(t, data, "block", "put", "--replace-blocks", root, "--root", "--high-priority")
	missing := eris.Reference{}.String()
	corrupt := eris.Reference{1}.String()
	if err := os.WriteFile(filepath.Join(root, corrupt), data, 0o600); err != nil {
		t.Fatal(err)
	}
	checkLine := hash + " high-priority=false root=false\n"
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"prefetch", []string{"prefetch", "--high-priority", missing, hash}, 0, "", ""},
		{"check", []string{"check", hash}, 0, checkLine, ""},
		{"check missing", []string{"check", hash, missing, hash}, 1, checkLine, "block check: fetch block " + missing + ": " + eris.ErrMissingBlock.Error()},
		{"check corrupt", []string{"check", corrupt}, 1, "", "block check: fetch block " + corrupt + ": " + eris.ErrInvalidBlock.Error()},
		{"update", []string{"update", "--root", hash, missing}, 0,
			fmt.Sprintf("%s changed=false\n%s changed=false\n", hash, missing), ""},
		{"update without storage", []string{"update", "--replace-blocks=", hash}, 1, "", eris.ErrNoBlockStorage.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errout := runBlockCommand(t, nil, append([]string{"block", "--replace-blocks", root}, tc.args...)...)
			if code != tc.code || out != tc.stdout || tc.stderr == "" && errout != "" || !strings.Contains(errout, tc.stderr) {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, out, errout)
			}
		})
	}
}

func TestBlockOptionsRoundTrip(t *testing.T) {
	stdout, _ := captureOutput(t)
	store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	dataA := blockTestData(eris.BlockSize1KiB, 1)
	dataB := blockTestData(eris.BlockSize32KiB, 2)
	refA, err := eris.ReferenceOf(dataA)
	if err != nil {
		t.Fatal(err)
	}
	refB, err := eris.ReferenceOf(dataB)
	if err != nil {
		t.Fatal(err)
	}
	a, b := refA.String(), refB.String()
	outputOffset := 0
	for _, tc := range []struct {
		name   string
		args   []string
		input  []byte
		output string
		err    error
	}{
		{"put both flags", []string{"put", "--root", "--high-priority"}, dataA, a + "\n", nil},
		{"put explicit values", []string{"put", "--root", "--root=false", "--high-priority=true"}, dataB, b + "\n", nil},
		{"put same block with defaults", []string{"put"}, dataA, a + "\n", nil},
		{"reject invalid hash", []string{"update", "--root", "--high-priority=false", a, "invalid", b}, nil, "", eris.ErrInvalidBlock},
		{"check after rejected update", []string{"check", b, a}, nil,
			b + " high-priority=true root=false\n" + a + " high-priority=true root=true\n", nil},
		{"update both blocks", []string{"update", "--root", "--high-priority=false", a, b}, nil,
			a + " changed=true\n" + b + " changed=true\n", nil},
		{"repeat update with default priority", []string{"update", "--root", b}, nil, b + " changed=false\n", nil},
		{"clear both flags", []string{"update", a}, nil, a + " changed=true\n", nil},
		{"check updated flags", []string{"check", a, b}, nil,
			a + " high-priority=false root=false\n" + b + " high-priority=false root=true\n", nil},
	} {
		if err := runBlockWithStore(t, store, tc.input, tc.args...); !errors.Is(err, tc.err) {
			t.Fatalf("%s: error = %v, want %v", tc.name, err, tc.err)
		}
		out := readOutput(t, stdout)
		if actual := out[outputOffset:]; actual != tc.output {
			t.Fatalf("%s: stdout = %q, want %q", tc.name, actual, tc.output)
		}
		outputOffset = len(out)
	}
	if !bytes.Equal(store.Blocks[refA].Data, dataA) || !bytes.Equal(store.Blocks[refB].Data, dataB) {
		t.Error("stored data differs from input")
	}
}

func TestBlockUpdateStorageFailure(t *testing.T) {
	stdout, _ := captureOutput(t)
	a, b, c := eris.Reference{1}, eris.Reference{2}, eris.Reference{3}
	failure := errors.New("storage unavailable")
	store := &failingUpdateStore{
		BlockSet: eris.BlockSet{
			Blocks:       map[eris.Reference]eris.Block{a: {}, b: {}, c: {}},
			MaxBatchSize: 1,
		},
		failAt:  b,
		failure: failure,
	}
	err := runBlockWithStore(t, store, nil, "update", "--root", a.String(), b.String(), c.String())
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), b.String()) {
		t.Errorf("update error = %v, want %v for %s", err, failure, b)
	}
	if out, want := readOutput(t, stdout), a.String()+" changed=true\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if !store.Blocks[a].Options.Root || store.Blocks[b].Options.Root || store.Blocks[c].Options.Root {
		t.Errorf("updated blocks = %v, want only %s", store.Blocks, a)
	}
}

// failingUpdateStore returns its error before updating the selected block.
type failingUpdateStore struct {
	eris.BlockSet
	failAt  eris.Reference
	failure error
}

func (s *failingUpdateStore) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	if len(requests) == 1 && requests[0].Reference == s.failAt {
		return nil, s.failure
	}
	return s.BlockSet.UpdateBlockOptions(ctx, requests)
}

func TestBlockPrefetchBatches(t *testing.T) {
	a, b, c := eris.Reference{1}, eris.Reference{2}, eris.Reference{3}
	requests := []eris.BlockRequest{
		{Reference: a, Options: eris.BlockOptions{HighPriority: true, Root: true}},
		{Reference: b, Options: eris.BlockOptions{HighPriority: true, Root: true}},
		{Reference: c, Options: eris.BlockOptions{HighPriority: true, Root: true}},
	}
	for _, tc := range []struct {
		name  string
		limit int
		want  [][]eris.BlockRequest
	}{
		{"storage limit", 2, [][]eris.BlockRequest{requests[:2], requests[2:]}},
		{"unlimited storage", 0, [][]eris.BlockRequest{requests}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingPrefetchStore{BlockSet: eris.BlockSet{MaxBatchSize: tc.limit}}
			err := runBlockWithStore(t, store, nil, "prefetch", "--root", "--high-priority", a.String(), b.String(), c.String(), "invalid", a.String())
			if !errors.Is(err, eris.ErrInvalidBlock) || !strings.Contains(err.Error(), `invalid block hash "invalid"`) {
				t.Errorf("prefetch error = %v, want invalid block hash", err)
			}
			if !slices.EqualFunc(store.requests, tc.want, slices.Equal[[]eris.BlockRequest]) {
				t.Errorf("prefetch requests = %v, want %v", store.requests, tc.want)
			}
		})
	}
}

// runBlockWithStore uses the CLI parser and dispatcher with a test store.
func runBlockWithStore(t *testing.T, store eris.BlockStore, input []byte, args ...string) error {
	t.Helper()
	command, _, err := parseCommand(append([]string{"block"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	var progress zpgo.Progress
	return command.runBlock(&progress, store, bytes.NewReader(input))
}

// recordingPrefetchStore copies each batch because the CLI reuses its buffer.
type recordingPrefetchStore struct {
	eris.BlockSet
	requests [][]eris.BlockRequest
}

func (s *recordingPrefetchStore) PrefetchBlock(_ context.Context, requests []eris.BlockRequest) {
	s.requests = append(s.requests, slices.Clone(requests))
}
