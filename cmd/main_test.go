package main

import (
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
		{"unknown option", []string{"--unknown"}, 2, "", "goeris: unexpected arguments: [\"--unknown\"]\n" + usage},
		{"extra argument", []string{"--help", "file name"}, 2, "", "goeris: unexpected arguments: [\"--help\" \"file name\"]\n" + usage},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
