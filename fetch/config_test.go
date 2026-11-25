package fetch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestConfig(t *testing.T) {
	routes, err := ParseConfig(strings.NewReader("git.exec tool\rurl.http https://[::1]:1\ngit.exec\ngit.exec  a\tb\n"))
	if err != nil || !slices.Equal(routes["git"].Exec, []string{"tool", "", " a\tb"}) || routes["url"].HTTP != "https://[::1]:1" {
		t.Fatal(routes, err)
	}
	for _, s := range []string{
		"Git.exec tool", "url.exec tool\nurl.http https://e",
		"git.http https://e\ngit.http https://e", "git.exec", "git.exec .",
		"git.exec ..", "git.exec d/p", "git.http ftp://e", "git.foo x",
		"type url", "#\xff", strings.Repeat("#", MaxLineSize+1),
	} {
		var failure Failure
		if got, err := ParseConfig(strings.NewReader(s)); got != nil || !errors.Is(err, ErrConfig) || errors.As(err, &failure) {
			t.Errorf("ParseConfig(%.40q) = %v, %v", s, got, err)
		}
	}
	_, err = ParseConfig(io.MultiReader(strings.NewReader("url.exec tool\n"), iotest.ErrReader(io.ErrUnexpectedEOF)))
	var failure Failure
	if !errors.Is(err, ErrConfig) || !errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &failure) {
		t.Fatal(err)
	}
}

func TestFileList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a;b")
	q := filepath.Join(filepath.Dir(p), "q")
	quoted := "\"" + p + "\";;\"" + q + "\"\"b\";"
	got, err := ParseFileList(quoted)
	if err != nil || !slices.Equal(got, []string{p, q + "\"b"}) {
		t.Fatal(got, err)
	}
	for _, v := range []string{"relative", p + "\"", "\"unterminated", "\"\"", "\"" + q + "\"extra", q + ";relative", "https://e", "\x00"} {
		if got, err := ParseFileList(v); got != nil || !errors.Is(err, ErrConfig) {
			t.Error(v, got, err)
		}
	}
	if runtime.GOOS != "windows" {
		v := "/dir/\xff"
		got, err := ParseFileList(v)
		if err != nil || got[0] != v {
			t.Fatal(got, err)
		}
	}
	for _, s := range []string{`C:\a`, `Z:/b`, `\\server\share`} {
		if !windowsAbsolute(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{`C:a`, `\a`, `\\?\C:\a`, `\\.\dev`, `\\server`} {
		if windowsAbsolute(s) {
			t.Fatal(s)
		}
	}
}

func TestDiscovery(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	broken := write("bad", "url.exec bad\nmalformed")
	first := write("first", "url.exec first\n")
	last := write("last", "url.exec last\ngit.http https://example.org\n")
	files := []string{filepath.Join(dir, "missing"), dir, broken, first, last}
	t.Setenv("ERIS_FETCH", strings.Join(files, ";"))
	for _, explicit := range [][]string{nil, files} {
		route, diag, err := LookupRoute("url", explicit)
		if err != nil || len(diag) != 2 || !slices.Equal(route.Exec, []string{"first"}) {
			t.Fatal(route, diag, err)
		}
		route, diag, err = LookupRoute("git", explicit)
		if err != nil || len(diag) != 2 || route.HTTP != "https://example.org" {
			t.Fatal(route, diag, err)
		}
	}
	// Discovery stops before the broken lower-priority file.
	if _, diag, err := LookupRoute("url", []string{first, broken}); err != nil || len(diag) != 0 {
		t.Fatal(diag, err)
	}
	t.Setenv("ERIS_FETCH", "invalid")
	if _, diag, err := LookupRoute("url", []string{}); !errors.Is(err, ErrNoFetcher) || len(diag) != 0 {
		t.Fatal(diag, err)
	}
	if _, _, err := LookupRoute("url", nil); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	if _, err := ConfigFiles([]string{first, "relative"}); err == nil {
		t.Fatal("relative explicit path")
	}
	t.Setenv("ERIS_FETCH", "")
	if got, err := ConfigFiles(nil); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if err := os.Unsetenv("ERIS_FETCH"); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigFiles(nil); err != nil {
		t.Fatal(err)
	}
}

func TestAbsoluteCommand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a tool")
	routes, err := ParseConfig(strings.NewReader("git.exec " + p))
	if err != nil || routes["git"].Exec[0] != p {
		t.Fatal(routes, err)
	}
}
