//go:build windows

package fetch

import "testing"

func TestWindowsPaths(t *testing.T) {
	for _, s := range []string{`C:tool.exe`, `sub\tool.exe`, `\tool.exe`, `\\?\C:\tool.exe`} {
		if validCommand(s) {
			t.Fatal(s)
		}
	}
	for _, p := range defaultFiles() {
		if !windowsAbsolute(p) {
			t.Fatal(p)
		}
	}
}
