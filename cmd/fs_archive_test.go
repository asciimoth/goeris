package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestImageArchiveRoundTrip(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "dir", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("abc\x00\xff"), 17000)
	if err := os.WriteFile(filepath.Join(src, "dir", "program"), data, 0755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("dir/program", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
	}
	root := strings.TrimSpace(imageCommand(t, "build", "--compression=lzip", src))
	for _, ext := range []string{".zip", ".tar", ".gz", ".tar.gz", ".tgz", ".zlib", ".tar.zz", ".tar.deflate", ".lzw"} {
		t.Run(ext, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "project"+ext)
			imageCommand(t, "pack", root, archive)
			code, _, _ := runCommand(t, nil, "fs", "pack", root, archive)
			if code == 0 {
				t.Fatal("overwrote archive")
			}
			rebuilt := strings.TrimSpace(imageCommand(t, "build", "--archive", "--compression=lzip", archive))
			if rebuilt != root {
				t.Fatalf("archive round trip changed image: %s", rebuilt)
			}
			wrapped := strings.TrimSpace(imageCommand(t, "build", "--archive", "--preserve-root", archive))
			if got := imageCommand(t, "cat", wrapped, "project/dir/program"); got != string(data) {
				t.Fatal("invalid wrapped archive")
			}
		})
	}
}

func TestImageArchiveBzip2(t *testing.T) {
	imageTestVolume(t)
	// bzip2-compressed tar with one file called ./hello.
	data, err := base64.StdEncoding.DecodeString("QlpoOTFBWSZTWSarffsAAFt7gMmAAAJAAf+AACBmRJ6ACAggAFQ0iNNBkD0Q2k9AkknqAMgAAfcWEEINXQhEucKjG2ulAhgYulJxOwjSBBsuzhI3vYKonppiFqiqtokRAXF3JFOFCQJqt9+w")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "src.tar.bz2")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(imageCommand(t, "build", "--archive", name))
	if got := imageCommand(t, "cat", root, "hello"); got != "world" {
		t.Fatalf("hello = %q", got)
	}
	dest := filepath.Join(t.TempDir(), "out.bz2")
	code, _, diag := runCommand(t, nil, "fs", "pack", root, dest)
	if code == 0 || !strings.Contains(diag, "bzip2") {
		t.Fatalf("pack bz2: exit %d, %s", code, diag)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output: %v", err)
	}
}

func TestImageArchiveInvalidMembers(t *testing.T) {
	imageTestVolume(t)
	for _, members := range [][]string{{"../escape"}, {"/absolute"}, {"/"}, {"dir/file", "dir"}, {"dir/", "dir"}, {"file", "file"}} {
		var data bytes.Buffer
		w := tar.NewWriter(&data)
		for _, name := range members {
			h := &tar.Header{Name: name, Typeflag: tar.TypeReg}
			if strings.HasSuffix(name, "/") {
				h.Typeflag = tar.TypeDir
			}
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "invalid.tar")
		if err := os.WriteFile(file, data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := runCommand(t, nil, "fs", "build", "--archive", file)
		if code == 0 || out != "" {
			t.Errorf("accepted %v", members)
		}
	}
}

func TestImageArchiveCorruptTrailer(t *testing.T) {
	imageTestVolume(t)
	root := strings.TrimSpace(imageCommand(t, "build", t.TempDir()))
	name := filepath.Join(t.TempDir(), "empty.gz")
	imageCommand(t, "pack", root, name)
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCommand(t, nil, "fs", "build", "--archive", name)
	if code == 0 || out != "" {
		t.Fatal("accepted corrupt gzip trailer")
	}
}

func TestImagePackRemovesPartialArchive(t *testing.T) {
	imageTestVolume(t)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "a", Typeflag: tar.TypeReg},
		{Name: "b", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "links.tar")
	if err := os.WriteFile(name, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(imageCommand(t, "build", "--archive", name))
	for _, ext := range []string{".zip", ".tar.gz"} {
		dest := filepath.Join(t.TempDir(), "output"+ext)
		code, _, _ := runCommand(t, nil, "fs", "pack", root, dest)
		if code == 0 {
			t.Fatalf("packed unsafe link in %s", ext)
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial archive %s: %v", dest, err)
		}
	}
}

func TestImagePackZipMetadata(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	name := filepath.Join(t.TempDir(), "image.zip")
	imageCommand(t, "pack", urn, name)
	z, err := zip.OpenReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	if len(z.File) != 1 {
		t.Fatalf("got %d entries", len(z.File))
	}
	f := z.File[0]
	if !f.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) || f.Mode() != 0644 {
		t.Fatalf("ZIP file has timestamp %s and mode %s", f.Modified, f.Mode())
	}
}

func TestImagePackMountedArchive(t *testing.T) {
	imageTestVolume(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "tool"), []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", src))
	if _, err := filepath.Localize(urn); err != nil {
		t.Skip("mount requires URN filenames")
	}
	mount := t.TempDir()
	root := filepath.Join(mount, urn)
	imageCommand(t, "unpack", urn, root)
	if err := os.Chmod(filepath.Join(root, "tool"), 0710); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".zip", ".tar.gz"} {
		dir := t.TempDir()
		first := filepath.Join(dir, "encoded"+ext)
		second := filepath.Join(dir, "mounted"+ext)
		imageCommand(t, "pack", urn, first)
		imageCommand(t, "pack", "--replace-fs", mount, urn, second)
		data, err := os.ReadFile(first)
		if err != nil {
			t.Fatal(err)
		}
		checkImageFile(t, second, data)
		nested := filepath.Join(root, "output"+ext)
		code, _, diag := runCommand(t, nil, "fs", "pack", "--replace-fs", mount, urn, nested)
		if code == 0 || !strings.Contains(diag, "destination is inside the image") {
			t.Fatalf("pack into mounted image: exit %d, stderr %q", code, diag)
		}
		if _, err := os.Stat(nested); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial output: %v", err)
		}
	}
}

func TestZipMemberSizeChanged(t *testing.T) {
	var data bytes.Buffer
	w := zipArchiveWriter{zip.NewWriter(&data)}
	err := w.writeMember("file", 0644, 12, "", strings.NewReader("short"))
	if err == nil {
		t.Fatal("packed file with unexpected size")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestImageArchiveZipChecksum(t *testing.T) {
	imageTestVolume(t)
	for _, mode := range []os.FileMode{0644, os.ModeSymlink | 0777} {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		h := &zip.FileHeader{Name: "member", Method: zip.Store}
		h.SetMode(mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("unique member contents")
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		i := bytes.Index(buf.Bytes(), payload)
		if i < 0 {
			t.Fatal("missing stored member")
		}
		buf.Bytes()[i] ^= 1
		name := filepath.Join(t.TempDir(), "damaged.zip")
		if err := os.WriteFile(name, buf.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		code, out, diag := runCommand(t, nil, "fs", "build", "--archive", name)
		if code == 0 || out != "" || !strings.Contains(diag, zip.ErrChecksum.Error()) {
			t.Fatalf("corrupt %s member: exit %d, stdout %q, stderr %q", mode, code, out, diag)
		}
	}
}
