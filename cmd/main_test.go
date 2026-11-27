package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"no arguments", nil, 0, usage, ""},
		{"short help", []string{"-h"}, 0, usage, ""},
		{"long help", []string{"--help"}, 0, usage, ""},
		{"help command", []string{"help"}, 0, usage, ""},
		{"help for dump", []string{"dump", "--help"}, 0, usage, ""},
		{"unknown option", []string{"--unknown"}, 2, usage, "goeris: unknown command \"--unknown\"\n"},
		{"unknown command", []string{"unknown"}, 2, usage, "goeris: unknown command \"unknown\"\n"},
		{"extra dump argument", []string{"dump", "file name"}, 2, usage, "goeris: dump: unexpected arguments: [\"file name\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.code == 0 {
				t.Setenv("ERIS_BLOCK_PATH", ";")
			} else {
				t.Setenv("ERIS_BLOCK_PATH", filepath.Join(t.TempDir(), "missing"))
			}
			var stdout, stderr strings.Builder
			if code := run(tc.args, &stdout, &stderr); code != tc.code {
				t.Fatalf("exit code = %d, want %d", code, tc.code)
			}
			if stdout.String() != tc.stdout || stderr.String() != tc.stderr {
				t.Errorf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestDump(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	pathList := strings.Join([]string{first, missing, second}, ";")
	t.Setenv("ERIS_BLOCK_PATH", pathList)
	t.Setenv("ERIS_FETCH", "")
	t.Setenv("ERIS_MNT", "capability mount")
	t.Setenv("ERIS_FS_MNT", "")

	var stdout, stderr strings.Builder
	if code := run([]string{"dump"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("dump exit code = %d, stderr = %q", code, stderr.String())
	}

	env, setup, ok := strings.Cut(stdout.String(), "\nStorage lookup order\n")
	if !ok {
		t.Fatalf("dump output missing storage setup: %q", stdout.String())
	}
	for _, line := range []string{
		fmt.Sprintf("  ERIS_BLOCK_PATH=%q\n", pathList),
		"  ERIS_FETCH=\"\"\n",
		"  ERIS_MNT=\"capability mount\"\n",
		"  ERIS_FS_MNT=\"\"\n",
	} {
		if !strings.Contains(env, line) {
			t.Errorf("dump environment missing %q", line)
		}
	}
	want := fmt.Sprintf(`  Block volumes %q
  Capabilities
    Block store
    Mount roots %q
  ERIS-FS images
    Capability store
    Mount roots %q
`, []string{first, second}, []string{"capability mount"}, []string{filepath.Join("capability mount", "fs")})
	if setup != want {
		t.Errorf("storage setup = %q, want %q", setup, want)
	}
}

func TestRunSetupFailure(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", ";")
	for _, command := range []string{"dump", "unknown"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := run([]string{command}, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "open block volumes") {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
		})
	}
}
