package main

import (
	"bytes"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/volume"
)

// imageCommand calls the real parser, storage setup, and command. Each call
// reopens the volume so the round trips also check persistence of the image.
func imageCommand(t *testing.T, args ...string) string {
	t.Helper()
	code, out, diag := runCommand(t, nil, append([]string{"fs"}, args...)...)
	if code != 0 || diag != "" {
		t.Fatalf("fs %v: exit %d, stderr %q", args, code, diag)
	}
	return out
}
func imageTestVolume(t *testing.T) string {
	t.Helper()
	blocks := t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", blocks)
	t.Setenv("ERIS_MNT", t.TempDir())
	t.Setenv("ERIS_FS_MNT", "")
	return blocks
}
func checkImageFile(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %s: got %d bytes, want %d, err %v", name, len(got), len(want), err)
	}
}

func TestImageDirectoryRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		compression string
		preserve    bool
	}{
		{"none", false}, {"auto", false}, {"lzip", true},
	} {
		t.Run(tc.compression, func(t *testing.T) {
			imageTestVolume(t)
			src := filepath.Join(t.TempDir(), "project")
			if err := os.MkdirAll(filepath.Join(src, "dir", "empty"), 0700); err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte("payload\x00\xff\n"), 11001)
			if err := os.WriteFile(filepath.Join(src, "dir", "some file"), data, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "zero"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"build", "--compression", tc.compression}
			prefix := "."
			if tc.preserve {
				args = append(args, "--preserve-root")
				prefix = "project"
			}
			urn := parseTestCapability(t, imageCommand(t, append(args, src)...)).String()
			if tc.preserve && imageCommand(t, "ls", urn) != "project\n" {
				t.Fatal("missing preserved root")
			}
			if got := imageCommand(t, "ls", urn, prefix); got != "dir\nzero\n" {
				t.Fatalf("ls = %q", got)
			}
			member := path.Join(prefix, "dir/some file")
			if got := imageCommand(t, "cat", urn, member); got != string(data) {
				t.Fatal("cat data differs")
			}
			if got := imageCommand(t, "decode", urn, path.Join(prefix, "zero")); got != "" {
				t.Fatal("expected empty file")
			}
			wantTree := prefix + `
├── dir
│   ├── empty
│   └── some file
└── zero
`
			if got := imageCommand(t, "tree", urn, prefix); got != wantTree {
				t.Fatalf("tree output:\n%s\nwant:\n%s", got, wantTree)
			}
			if tc.preserve {
				want := `d .
└── d project
    ├── d dir
    │   ├── d empty
    │   └── x some file
    └── f zero
`
				if runtime.GOOS == "windows" {
					want = strings.Replace(want, "x some file", "f some file", 1)
				}
				code, out, diag := runCommand(t, []byte("tree\nquit\n"), "fs", "repl", urn)
				if code != 0 || out != want || diag != "" {
					t.Fatalf("REPL tree: exit %d, stderr %q, output:\n%s", code, diag, out)
				}
			}
			imageCommand(t, "prefetch", urn, prefix)
			dest := filepath.Join(t.TempDir(), "unpacked")
			imageCommand(t, "unpack", urn, dest+string(filepath.Separator))
			checkImageFile(t, filepath.Join(dest, filepath.FromSlash(member)), data)
			if info, err := os.Stat(filepath.Join(dest, prefix, "dir", "empty")); err != nil || !info.IsDir() {
				t.Fatalf("missing empty directory: %v", err)
			}
			// Rebuild the unpacked contents with the same boundary and options.
			rebuilt := imageCommand(t, append(args, filepath.Join(dest, prefix))...)
			if strings.TrimSpace(rebuilt) != urn {
				t.Fatal("unpacked tree produced a different root")
			}
		})
	}
}

func TestImageFileRoundTrip(t *testing.T) {
	imageTestVolume(t)
	src := filepath.Join(t.TempDir(), "data.bin")
	data := []byte{0, 255, 254, 0}
	if err := os.WriteFile(src, data, 0755); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", "--compression=lzip", src))
	if got := imageCommand(t, "ls", urn); got != "data.bin\n" {
		t.Fatal(got)
	}
	properties := imageCommand(t, "index", urn)
	if !strings.Contains(properties, "size=4") || !strings.Contains(properties, "compression=lzip") {
		t.Fatal(properties)
	}
	if runtime.GOOS != "windows" && !strings.Contains(imageCommand(t, "stat", urn, "data.bin"), "mode=-r-xr-xr-x size=4") {
		t.Fatal("lost executable flag")
	}
	uri := "magnet:?xt=" + urn + "&dn=example"
	if got := imageCommand(t, "decode", "--index-limit=4096", uri, "data.bin"); got != string(data) {
		t.Fatal("Magnet URI decode differs")
	}
	for _, op := range []string{"index", "ls"} {
		code, _, diag := runCommand(t, nil, "fs", op, "--index-limit=1", urn)
		if code != 1 || !strings.Contains(diag, eris.ErrDecodedTooLarge.Error()) {
			t.Errorf("%s with a small index limit: exit %d, stderr %q", op, code, diag)
		}
	}
	dest := filepath.Join(t.TempDir(), "decoded")
	imageCommand(t, "decode", urn, "data.bin", dest)
	checkImageFile(t, dest, data)
	code, _, _ := runCommand(t, nil, "fs", "decode", urn, "data.bin", dest)
	if code == 0 {
		t.Fatal("decode overwrote existing file")
	}
	checkImageFile(t, dest, data)
	code, _, _ = runCommand(t, nil, "fs", "unpack", urn, filepath.Dir(dest))
	if code == 0 {
		t.Fatal("unpack reused existing directory")
	}
}

func TestImageNestedIndexLimit(t *testing.T) {
	imageTestVolume(t)
	src := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second", "third"} {
		if err := os.WriteFile(filepath.Join(src, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	urn := strings.TrimSpace(imageCommand(t, "build", "--preserve-root", src))
	const limit = "--index-limit=128"
	if got := imageCommand(t, "ls", limit, urn); got != "root\n" {
		t.Fatalf("root listing = %q", got)
	}
	for _, op := range []string{"ls", "prefetch"} {
		code, _, diag := runCommand(t, nil, "fs", op, limit, urn, "root")
		if code != 1 || !strings.Contains(diag, eris.ErrDecodedTooLarge.Error()) {
			t.Errorf("%s with a small nested index limit: exit %d, stderr %q", op, code, diag)
		}
	}
}

func TestImageReadErrorsWithMissingMount(t *testing.T) {
	imageTestVolume(t)
	src := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(src, []byte("plain text"), 0600); err != nil {
		t.Fatal(err)
	}
	image := strings.TrimSpace(imageCommand(t, "build", src))
	content := encodeTestContent(t, []byte("plain text"), "cap", "encode").String()
	type testCase struct {
		args []string
		want string
	}
	cases := []testCase{
		{[]string{"ls", image, "file"}, "readdir file: " + os.ErrInvalid.Error()},
		{[]string{"resolve", image}, erisfs.ErrNoHostPath.Error()},
		{[]string{"index", content}, erisfs.ErrInvalidIndex.Error()},
	}
	if filepath.Separator == '/' {
		cases = append(cases, testCase{[]string{"ls", content}, erisfs.ErrInvalidIndex.Error()})
	}
	for _, tc := range cases {
		code, out, diag := runCommand(t, nil, append([]string{"fs"}, tc.args...)...)
		if code != 1 || out != "" || !strings.Contains(diag, tc.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q, want %q in stderr", tc.args, code, out, diag, tc.want)
		}
	}
}

func TestImageDecodeDestinationPath(t *testing.T) {
	imageTestVolume(t)
	src := filepath.Join(t.TempDir(), "file")
	data := []byte("decoded\n")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	parent := t.TempDir()
	dest := filepath.Join(parent, "output")
	code, _, diag := runCommand(t, nil, "fs", "decode", urn, "file", dest+string(filepath.Separator))
	if code != 1 || !strings.Contains(diag, "expected a new file path") {
		t.Fatalf("decode with a trailing slash: exit %d, stderr %q", code, diag)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decode created %q: %v", dest, err)
	}

	if runtime.GOOS == "windows" {
		return // Windows cleans parent steps before following directory links.
	}
	other := t.TempDir()
	subdir := filepath.Join(other, "subdir")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(subdir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	imageCommand(t, "decode", urn, "file", alias+"/../output")
	checkImageFile(t, filepath.Join(other, "output"), data)
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decode created %q: %v", dest, err)
	}
}

// Remove an unrelated payload. Index operations and reads of the selected
// file must still succeed. A full extraction must fail and clean up.
func TestImageSelectiveReads(t *testing.T) {
	blocks := imageTestVolume(t)
	src := t.TempDir()
	data := bytes.Repeat([]byte("b"), 1500)
	for name, content := range map[string][]byte{"a": []byte("a"), "b": data} {
		if err := os.WriteFile(filepath.Join(src, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	cap := encodeTestContent(t, data, "cap", "encode")
	block, err := os.ReadFile(filepath.Join(blocks, volume.BlockPath(cap.Root.Reference)))
	if err != nil {
		t.Fatal(err)
	}
	node, err := eris.DecryptNode(block, cap.Root, cap.Level)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := eris.ParseInternalNode(node)
	if err != nil || cap.Level != 1 || len(leaves) != 2 {
		t.Fatalf("expected two leaf blocks, got %d children at level %d: %v", len(leaves), cap.Level, err)
	}
	if err := os.Remove(filepath.Join(blocks, volume.BlockPath(leaves[1].Reference))); err != nil {
		t.Fatal(err)
	}
	if imageCommand(t, "cat", urn, "a") != "a" || imageCommand(t, "ls", urn) != "a\nb\n" {
		t.Fatal("could not read unaffected entries")
	}
	code, out, diag := runCommand(t, []byte("ls\ntree\n"), "fs", "repl", urn)
	if code != 0 || out != "f a\nf b\nd .\n├── f a\n└── f b\n" || diag != "" {
		t.Fatalf("listing with missing payload: exit %d, stdout %q, stderr %q", code, out, diag)
	}
	code, out, _ = runCommand(t, nil, "fs", "cat", urn, "b")
	if code != 1 || out != string(data[:cap.BlockSize]) {
		t.Fatalf("cat with a missing leaf: exit %d, stdout length %d", code, len(out))
	}
	for _, op := range []string{"unpack", "decode"} {
		dest := filepath.Join(t.TempDir(), "out")
		args := []string{"fs", op, urn}
		if op == "decode" {
			args = append(args, "b")
		}
		code, _, _ := runCommand(t, nil, append(args, dest)...)
		if code != 1 {
			t.Fatalf("%s exit = %d", op, code)
		}
		if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial %s output: %v", op, err)
		}
	}
}

// A v1 file has no stored size. Browsing its entry must work even if the
// payload is offline. Only a command that needs the contents or size may fail.
func TestImageBrowseLegacyFileWithoutPayload(t *testing.T) {
	imageTestVolume(t)
	missing := eris.Capability{BlockSize: eris.BlockSize1KiB}
	ref, err := missing.MarshalCBOR()
	if err != nil {
		t.Fatal(err)
	}
	// CBOR tag "erit", [version 1, {"tool": [executable, capability]}].
	index := append([]byte("\xda\x65\x72\x69\x74\x82\x01\xa1\x64tool\x82\x01"), ref...)
	urn := encodeTestContent(t, index, "cap", "encode").String()
	if got := imageCommand(t, "tree", urn, "tool"); got != "tool\n" {
		t.Fatalf("tree = %q", got)
	}
	if got := imageCommand(t, "index", urn); !strings.Contains(got, `"tool" file size=unknown executable=true`) {
		t.Fatalf("index = %q", got)
	}
	code, out, diag := runCommand(t, []byte("ls\ntree tool\ncat tool\npwd\n"), "fs", "repl", urn)
	if code != 0 || out != "x tool\nx tool\n/\n" || !strings.Contains(diag, "tool") {
		t.Fatalf("legacy listing and read: exit %d, output %q, stderr %q", code, out, diag)
	}
}

// A traversal can read one index from blocks and the next from the mount.
// Changing the REPL working directory must keep both backends available.
func TestImageREPLTreeUsesMountFallback(t *testing.T) {
	blocks := imageTestVolume(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "docs", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	if _, err := filepath.Localize(urn); err != nil {
		t.Skip("mount requires URN filenames")
	}
	mount := t.TempDir()
	imageCommand(t, "unpack", urn, filepath.Join(mount, urn))
	empty := parseTestCapability(t, imageCommand(t, "build", filepath.Join(src, "docs", "empty")))
	if err := os.Remove(filepath.Join(blocks, volume.BlockPath(empty.Root.Reference))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERIS_FS_MNT", mount)
	code, out, diag := runCommand(t, []byte("cd docs\ntree\n"), "fs", "repl", urn)
	if code != 0 || out != "d .\n└── d empty\n" || diag != "" {
		t.Fatalf("REPL tree with mount fallback: exit %d, output %q, stderr %q", code, out, diag)
	}
}

func TestImageLinksAndREPL(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "some file"), []byte("ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("some file", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	if got := imageCommand(t, "readlink", urn, "link"); got != "some file\n" {
		t.Fatal(got)
	}
	dest := filepath.Join(t.TempDir(), "out")
	imageCommand(t, "unpack", urn, dest)
	if target, err := os.Readlink(filepath.Join(dest, "link")); err != nil || target != "some file" {
		t.Fatalf("restored link = %q, %v", target, err)
	}
	code, out, diag := runCommand(t, []byte("bad\nls\ncat some file\ncat link\nquit\nls\n"), "fs", "repl", urn)
	if code != 0 || out != "l link\nf some file\nok\nok\n" || !strings.Contains(diag, "unknown command") {
		t.Fatalf("repl: exit %d, stdout %q, stderr %q", code, out, diag)
	}
	if err := os.Symlink("../escape", filepath.Join(src, "unsafe")); err != nil {
		t.Fatal(err)
	}
	urn = strings.TrimSpace(imageCommand(t, "build", src))
	dest = filepath.Join(t.TempDir(), "new")
	code, _, _ = runCommand(t, nil, "fs", "unpack", urn, dest)
	if code != 1 {
		t.Fatal("exported escaping symlink")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial unpack after unsafe symlink")
	}
}

func TestImageUnpackDirectoryLinks(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "z-dir"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("inside\n")
	if err := os.WriteFile(filepath.Join(src, "z-dir", "file"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// The directory and dependency both sort after the links that use them.
	if err := os.Symlink("z-dir", filepath.Join(src, "b-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("b-link", filepath.Join(src, "a-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	dest := filepath.Join(t.TempDir(), "unpacked")
	imageCommand(t, "unpack", urn, dest)
	checkImageFile(t, filepath.Join(dest, "a-link", "file"), data)
	if target, err := os.Readlink(filepath.Join(dest, "dangling")); err != nil || target != "missing" {
		t.Fatalf("dangling link = %q, %v", target, err)
	}
}

func TestImageResolveMountedFile(t *testing.T) {
	imageTestVolume(t)
	mount := t.TempDir()
	urn := (eris.Capability{BlockSize: eris.BlockSize1KiB}).String()
	if _, err := filepath.Localize(urn); err != nil {
		t.Skip("mount requires URN filenames")
	}
	image := filepath.Join(mount, urn)
	if err := os.Mkdir(image, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(image, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(image, "copy")
	code, _, diag := runCommand(t, nil, "fs", "unpack", "--replace-fs", mount, urn, dest)
	if code != 1 || !strings.Contains(diag, "destination is inside the image") {
		t.Fatalf("unpack into mounted source: exit %d, stderr %q", code, diag)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete destination: %v", err)
	}
	if got := imageCommand(t, "--replace-fs", mount, "resolve", urn, "file"); strings.TrimSpace(got) != file {
		t.Fatalf("resolve = %q", got)
	}
}

func TestImageCommandArguments(t *testing.T) {
	urn := (eris.Capability{BlockSize: eris.BlockSize1KiB}).String()
	t.Setenv("ERIS_BLOCK_PATH", ";")
	for _, args := range [][]string{{"fs"}, {"fs", "--help"}, {"fs", "cat", "--help"}} {
		code, out, diag := runCommand(t, nil, args...)
		if code != 0 || out != fsUsage || diag != "" {
			t.Fatalf("help %v: exit %d, stderr %s", args, code, diag)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"build"}, {"build", ""}, {"build", "--compression=x", "dir"}, {"unpack", urn}, {"ls", "invalid"}, {"ls", "--index-limit=0", urn}, {"ls", urn, ".."}, {"cat", urn}, {"repl", urn, "extra"}} {
		code, _, diag := runCommand(t, nil, append([]string{"fs"}, args...)...)
		if code != 2 || diag == "" {
			t.Fatalf("%v exit %d stderr %q", args, code, diag)
		}
	}
}

func TestImageEmptyDirectoryAndSecret(t *testing.T) {
	imageTestVolume(t)
	dir := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	plain := strings.TrimSpace(imageCommand(t, "build", dir))
	if out := imageCommand(t, "ls", plain); out != "" {
		t.Fatalf("empty root listing %q", out)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	wrapped := strings.TrimSpace(imageCommand(t, "build", "--secret-file", secret, "--preserve-root", dir))
	if index := imageCommand(t, "index", wrapped); !strings.HasPrefix(index, "\"empty\" directory ") || strings.Contains(index, plain) {
		t.Fatalf("expected empty child with its own secret, got %q", index)
	}
	if tree := imageCommand(t, "tree", wrapped); tree != ".\n└── empty\n" {
		t.Fatal(tree)
	}
	if err := os.WriteFile(secret, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCommand(t, nil, "fs", "build", "--secret-file", secret, dir)
	if code != 1 || out != "" {
		t.Fatal("build accepted invalid secret")
	}
}

// Each line is a separate command in the same session. Check both the path
// reported by pwd and the actual files selected by the other commands.
func TestImageREPLWorkingDirectory(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	dir := filepath.Join(src, "user docs", "guides")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"README":                  "root file\n",
		"user docs/README":        "directory file\n",
		"user docs/guides/manual": "manual text\n",
	} {
		if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(name)), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	script := `pwd
cd user docs
pwd
cd ./guides/
pwd
ls
tree .
cat ./manual
cat ../README
cat /README
stat ../README
prefetch ../README
cd ..
pwd
cd ///user docs/./guides/../
pwd
cd ../../../../
pwd
cat ../../../README
cd /user docs/guides
cd
pwd
quit
`
	want := `/
/user docs
/user docs/guides
f manual
d .
└── f manual
manual text
directory file
root file
mode=-r--r--r-- size=15
/user docs
/user docs
/
root file
/
`
	code, out, diag := runCommand(t, []byte(script), "fs", "repl", urn)
	if code != 0 || out != want || diag != "" {
		t.Fatalf("exit %d, stderr %q\nstdout:\n%s\nwant:\n%s", code, diag, out, want)
	}

	for _, bad := range []string{"cd README", "cd /missing", "cat README/", "pwd /"} {
		script := "cd /user docs\n" + bad + "\npwd\ncat README\n"
		code, out, diag := runCommand(t, []byte(script), "fs", "repl", urn)
		if code != 0 || out != "/user docs\ndirectory file\n" || diag == "" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", bad, code, out, diag)
		}
	}
}

// Use an image builder so even hosts that disallow these names test the
// command grammar. The exact name printed by ls must work in cd and cat.
func TestImageREPLQuotedPaths(t *testing.T) {
	imageTestVolume(t)
	stores, err := (storageFlags{}).open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	b, err := stores.Images.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	const dir, file = "docs ", "\"draft\n\x1b.txt"
	if err := b.AddFile(t.Context(), path.Join(dir, file), strings.NewReader("text\n"), false); err != nil {
		t.Fatal(err)
	}
	root, err := b.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	quotedDir, quotedFile := strconv.Quote(dir), strconv.Quote(file)
	if got := imageCommand(t, "ls", root.String(), dir); got != quotedFile+"\n" {
		t.Fatalf("quoted ls = %q", got)
	}
	if got := imageCommand(t, "tree", root.String()); got != ".\n└── "+quotedDir+"\n    └── "+quotedFile+"\n" {
		t.Fatalf("quoted tree = %q", got)
	}
	script := "ls\ncd " + quotedDir + "\npwd\ntree\ncat " + quotedFile + "\ncat \"unterminated\npwd\n"
	want := "d " + quotedDir + "\n\"/docs \"\nd .\n└── f " + quotedFile + "\ntext\n\"/docs \"\n"
	code, out, diag := runCommand(t, []byte(script), "fs", "repl", root.String())
	if code != 0 || out != want || !strings.Contains(diag, "invalid quoted image path") {
		t.Fatalf("quoted paths: exit %d, output %q, stderr %q", code, out, diag)
	}
}

func TestImageREPLLongPath(t *testing.T) {
	imageTestVolume(t)
	stores, err := (storageFlags{}).open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	b, err := stores.Images.NewBuilder(eris.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	// Image members are not subject to the host's filename length limit.
	name := strings.Repeat("a", 70<<10)
	if err := b.AddFile(t.Context(), name, strings.NewReader("contents\n"), false); err != nil {
		t.Fatal(err)
	}
	root, err := b.Finish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	code, out, diag := runCommand(t, []byte("cat "+name), "fs", "repl", root.String())
	if code != 0 || out != "contents\n" || diag != "" {
		t.Fatalf("long path at EOF: exit %d, stdout %q, stderr %q", code, out, diag)
	}
}

// Exercise the same image boundary on encoded trees and expanded host mounts.
func TestImageREPLLinkBoundaries(t *testing.T) {
	imageTestVolume(t)
	parent := t.TempDir()
	src := filepath.Join(parent, "image")
	if err := os.MkdirAll(filepath.Join(src, "nested", "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "dir", "entry"), []byte("inside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "private"), []byte("outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "tool"), nil, 0755); err != nil {
		t.Fatal(err)
	}
	toolType := "x"
	if runtime.GOOS == "windows" {
		toolType = "f"
	}
	for name, target := range map[string]string{"alias": "nested/dir", "escape-dir": "..", "escape-file": "../private"} {
		if err := os.Symlink(target, filepath.Join(src, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	for _, backend := range []string{"encoded", "mounted"} {
		t.Run(backend, func(t *testing.T) {
			args := []string{"fs", "repl"}
			if backend == "mounted" {
				if _, err := filepath.Localize(urn); err != nil {
					t.Skip("mount requires URN filenames")
				}
				if err := os.Rename(src, filepath.Join(parent, urn)); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--replace-fs", parent)
			}
			args = append(args, urn)
			script := "cd /alias\npwd\ntree\ncat ./entry\nreadlink ../alias\ncd ..\npwd\n"
			code, out, diag := runCommand(t, []byte(script), args...)
			if code != 0 || out != "/alias\nd .\n└── f entry\ninside\nnested/dir\n/\n" || diag != "" {
				t.Fatalf("valid link: exit %d, stdout %q, stderr %q", code, out, diag)
			}
			// Mark links to directories and escaping targets without walking them.
			code, out, diag = runCommand(t, []byte("ls /\ntree /alias\ntree /tool\nls /nested/dir\n"), args...)
			want := "l alias\nl escape-dir\nl escape-file\nd nested\n" + toolType + " tool\n" +
				"l /alias\n" + toolType + " /tool\nf entry\n"
			if code != 0 || out != want || diag != "" {
				t.Fatalf("typed listing: exit %d, stdout %q, stderr %q", code, out, diag)
			}
			for _, bad := range []string{"cd ../escape-dir", "cat ../escape-file", "ls /escape-dir", "resolve /escape-file", "cat ../../../../private"} {
				code, out, diag := runCommand(t, []byte("cd /alias\n"+bad+"\npwd\n"), args...)
				if code != 0 || out != "/alias\n" || diag == "" {
					t.Errorf("%q: exit %d, stdout %q, stderr %q", bad, code, out, diag)
				}
			}
			if backend == "mounted" {
				code, out, diag := runCommand(t, []byte("cd /nested/dir\nresolve ../dir/entry\n"), args...)
				if code != 0 || out != filepath.Join(parent, urn, "nested", "dir", "entry")+"\n" || diag != "" {
					t.Fatalf("resolve: exit %d, stdout %q, stderr %q", code, out, diag)
				}
			}
		})
	}
}
