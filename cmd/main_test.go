package main

import (
	"fmt"
	"os"
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
		{"help with storage flags", []string{"--prepend-blocks=missing", "dump", "--help"}, 0, usage, ""},
		{"unknown option", []string{"--unknown"}, 2, usage, "goeris: flag provided but not defined: -unknown\n"},
		{"unknown dump option", []string{"dump", "--unknown"}, 2, usage, "goeris: dump: flag provided but not defined: -unknown\n"},
		{"unknown command", []string{"wrongcmd"}, 2, usage, "goeris: unknown command \"wrongcmd\"\n"},
		{"extra dump argument", []string{"dump", "file name"}, 2, usage, "goeris: dump: unexpected arguments: [\"file name\"]\n"},
		{"missing value", []string{"dump", "--replace-fs"}, 2, usage, "goeris: dump: flag needs an argument: -replace-fs\n"},
		{"empty prepend", []string{"dump", "--prepend-blocks="}, 2, usage, "goeris: dump: invalid value \"\" for flag -prepend-blocks: path list contains an empty entry\n"},
		{"empty list entry", []string{"dump", "--append-capabilities=first;;last"}, 2, usage, "goeris: dump: invalid value \"first;;last\" for flag -append-capabilities: path list contains an empty entry\n"},
		{"empty replacement entry", []string{"dump", "--replace-fs=first;"}, 2, usage, "goeris: dump: invalid value \"first;\" for flag -replace-fs: path list contains an empty entry\n"},
		{"end of flags", []string{"dump", "--", "--replace-blocks="}, 2, usage, "goeris: dump: unexpected arguments: [\"--replace-blocks=\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ERIS_BLOCK_PATH", ";")
			stdout, stderr := captureOutput(t)
			if code := run(tc.args); code != tc.code {
				t.Fatalf("exit code = %d, want %d", code, tc.code)
			}
			if out, errout := readOutput(t, stdout), readOutput(t, stderr); out != tc.stdout || errout != tc.stderr {
				t.Errorf("stdout = %q, stderr = %q", out, errout)
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

	stdout, stderr := captureOutput(t)
	code := run([]string{"dump"})
	if errout := readOutput(t, stderr); code != 0 || errout != "" {
		t.Fatalf("dump exit code = %d, stderr = %q", code, errout)
	}

	out := readOutput(t, stdout)
	env, setup, ok := strings.Cut(out, "\nStorage lookup order\n")
	if !ok {
		t.Fatalf("dump output missing storage setup: %q", out)
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
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("ERIS_BLOCK_PATH", ";")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"defaults", []string{"dump"}, "open block volumes"},
		{"prepend", []string{"dump", "--prepend-blocks", missing}, "open --prepend-blocks"},
		{"replace", []string{"dump", "--replace-blocks", missing}, "open --replace-blocks"},
		{"append", []string{"dump", "--replace-blocks=" + t.TempDir(), "--append-blocks", missing}, "open --append-blocks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := captureOutput(t)
			code := run(tc.args)
			out, errout := readOutput(t, stdout), readOutput(t, stderr)
			if code != 1 || out != "" || !strings.Contains(errout, tc.want) {
				t.Errorf("exit code = %d, stdout = %q, stderr = %q", code, out, errout)
			}
		})
	}
}

func TestRunOutputFailure(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	for _, tc := range []struct {
		command string
		message string
	}{
		{"help", "goeris: write help: "},
		{"dump", "goeris: dump: "},
	} {
		t.Run(tc.command, func(t *testing.T) {
			stdout, stderr := captureOutput(t)
			if err := stdout.Close(); err != nil {
				t.Fatal(err)
			}
			code := run([]string{tc.command})
			if errout := readOutput(t, stderr); code != 1 || !strings.HasPrefix(errout, tc.message) {
				t.Errorf("exit code = %d, stderr = %q", code, errout)
			}
		})
	}
}

// captureOutput replaces the process streams for a serial test of run.
func captureOutput(t *testing.T) (stdout, stderr *os.File) {
	t.Helper()
	t.Setenv("ZIG_PROGRESS", "")
	dir := t.TempDir()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdout = createOutputFile(t, filepath.Join(dir, "stdout"))
	stderr = createOutputFile(t, filepath.Join(dir, "stderr"))
	os.Stdout, os.Stderr = stdout, stderr
	t.Cleanup(func() { os.Stdout, os.Stderr = oldStdout, oldStderr })
	return stdout, stderr
}

func createOutputFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func readOutput(t *testing.T, f *os.File) string {
	t.Helper()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
