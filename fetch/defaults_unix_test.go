//go:build !windows

package fetch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestUnixDefaults(t *testing.T) {
	t.Setenv("HOME", "/home/ada")
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_CONFIG_DIRS", "")
	for _, tc := range []struct {
		os    string
		paths []string
	}{
		{"linux", []string{"/home/ada/.config/eris/fetch.conf", "/etc/xdg/eris/fetch.conf", "/etc/eris/fetch.conf"}},
		{"darwin", []string{"/home/ada/Library/Application Support/ERIS/fetch.conf", "/Library/Application Support/ERIS/fetch.conf"}},
	} {
		if p := unixFiles(tc.os); !slices.Equal(p, tc.paths) {
			t.Fatal(p)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/user")
	t.Setenv("XDG_CONFIG_DIRS", "/one:rel::/two")
	if p := unixFiles("linux"); !slices.Equal(p, []string{"/user/eris/fetch.conf", "/one/eris/fetch.conf", "/two/eris/fetch.conf", "/etc/eris/fetch.conf"}) {
		t.Fatal(p)
	}
}

func TestUnreadableRouteFile(t *testing.T) {
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	_, diag, err := LookupRoute("git", []string{loop})
	if len(diag) != 1 || !errors.Is(err, ErrNoFetcher) {
		t.Fatal(diag, err)
	}
}

func TestXDGConfigHomeThroughSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	child := filepath.Join(target, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	for base, command := range map[string]string{root: "outer", target: "selected"} {
		dir := filepath.Join(base, "eris")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fetch.conf"), []byte("url.exec "+command), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", link+"/../")
	route, diag, err := LookupRoute("url", unixFiles("linux"))
	if err != nil || len(diag) != 0 || !slices.Equal(route.Exec, []string{"selected"}) {
		t.Fatalf("got route %v, diagnostics %v, error %v", route, diag, err)
	}
}
