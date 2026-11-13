package link_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

func TestFSLocalFiles(t *testing.T) {
	payload := []byte("content from blocks")
	encoded, blocks := encodeFSLink(t, payload)
	dir := t.TempDir()
	for name, data := range map[string][]byte{"linked": encoded, "plain": []byte("ordinary file")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapped := newLinkFS(t, os.DirFS(dir), blocks)
	if err := fstest.TestFS(wrapped, "linked", "plain"); err != nil {
		t.Fatal(err)
	}
	file, err := wrapped.Open("plain")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.(io.Seeker).Seek(9, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	if err != nil || string(got) != "file" {
		t.Fatalf("ordinary seek: %q, %v", got, err)
	}
	buffer := make([]byte, 8)
	if _, err := file.(io.ReaderAt).ReadAt(buffer, 0); err != nil || string(buffer) != "ordinary" {
		t.Fatalf("ordinary ReadAt: %q, %v", buffer, err)
	}
	got, err = fs.ReadFile(wrapped, "linked")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("linked read: %q, %v", got, err)
	}

	if err := os.Symlink("linked", filepath.Join(dir, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err = fs.ReadFile(wrapped, "alias")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("symlink read: %q, %v", got, err)
	}
	entries, err := wrapped.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Name() != "alias" || info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("symlink entry: %v, %v", info, err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": "linked", "broken": "missing"} {
		info, err := fs.Lstat(wrapped, name)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("Lstat(%q): %v, %v", name, info, err)
		}
		got, err := fs.ReadLink(wrapped, name)
		if err != nil || got != target {
			t.Errorf("ReadLink(%q): %q, %v", name, got, err)
		}
	}
	info, err = fs.Lstat(wrapped, "linked")
	if err != nil || info.Size() != int64(len(payload)) {
		t.Fatalf("Lstat regular link: %v, %v", info, err)
	}
}

func TestFSZip(t *testing.T) {
	encoded, blocks := encodeFSLink(t, []byte("zip link content"))
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{"dir/link": encoded, "dir/plain": []byte("ordinary content")} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	base, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := newLinkFS(t, base, blocks)
	if err := fstest.TestFS(wrapped, "dir/link", "dir/plain"); err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(wrapped, "dir/link")
	if err != nil || string(got) != "zip link content" {
		t.Fatalf("ZIP link: %q, %v", got, err)
	}
}

func TestFSConcurrentReads(t *testing.T) {
	payload := patternedPayload(70*1024 + 29)
	encoded, blocks := encodeFSLink(t, payload)
	source := &countingSource{BlockSource: blocks}
	wrapped := newLinkFS(t, fstest.MapFS{"file": {Data: encoded}}, source)
	shared, err := wrapped.Open("file")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	for i := range 16 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			t.Parallel()
			got, err := fs.ReadFile(wrapped, "file")
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("independent read: %v", err)
			}
			buffer := make([]byte, 1027)
			offset := i * 1031
			if _, err := shared.(io.ReaderAt).ReadAt(buffer, int64(offset)); err != nil || !bytes.Equal(buffer, payload[offset:offset+len(buffer)]) {
				t.Fatalf("shared ReadAt: %v", err)
			}
			info, err := shared.Stat()
			if err != nil || info.Size() != int64(len(payload)) {
				t.Fatalf("shared Stat: %v", err)
			}
		})
	}
}

func FuzzFS(f *testing.F) {
	capability, blocks, err := eris.EncodeRecommended([]byte("target"), eris.Secret{})
	if err != nil {
		f.Fatal(err)
	}
	encoded, err := (link.Link{Capability: capability, Size: 6}).MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add(append(bytes.Clone(encoded), make([]byte, 4096)...))
	f.Add([]byte{})
	f.Add([]byte("ordinary content"))
	f.Add([]byte(link.MagicPrefix[:5]))
	f.Add([]byte(link.MagicPrefix))
	f.Fuzz(func(t *testing.T, data []byte) {
		const limit = 4096
		base := plainFS{fstest.MapFS{"file": {Data: data}}}
		wrapped, err := link.NewFSWithLimit(base, blocks, limit)
		if err != nil {
			t.Fatal(err)
		}
		file, err := wrapped.Open("file")
		if !link.HasHeader(data) {
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			got, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("ordinary data changed: %v", err)
			}
			return
		}
		if len(data) > limit {
			if !errors.Is(err, link.ErrLinkTooLarge) {
				t.Fatalf("oversized link: %v", err)
			}
			return
		}
		if _, parseErr := link.Parse(data); parseErr != nil {
			if !errors.Is(err, link.ErrInvalidLink) {
				t.Fatalf("malformed link: %v", err)
			}
			return
		}
		if err == nil {
			defer func() { _ = file.Close() }()
			// A forged declared size must not cause a large allocation.
			_, _ = io.ReadAll(io.LimitReader(file, limit))
		}
	})
}
