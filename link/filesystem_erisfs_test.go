package link_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

func TestFSWithERISFSBase(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "separate stores"
		if shared {
			name = "shared store"
		}
		t.Run(name, func(t *testing.T) {
			payload := patternedPayload(70*1024 + 29)
			encoded, targets := encodeFSLink(t, payload)
			emptyLink, emptyBlocks := encodeFSLink(t, nil)
			for ref, block := range emptyBlocks {
				targets[ref] = block
			}
			baseBlocks := make(eris.BlockSet)
			if shared {
				baseBlocks = targets
			}
			builder := erisfs.NewBuilder(baseBlocks, eris.Secret{})
			ordinary := []byte("ordinary content in an ERIS-FS image")
			for _, entry := range []struct {
				name                   string
				data                   []byte
				compressed, executable bool
			}{
				{"plain.eris", ordinary, false, false},
				{"docs/content", encoded, false, true},
				{"docs/compressed.eris", encoded, true, false},
				{"docs/plainlz", ordinary, true, false},
				{"empty", emptyLink, false, false},
			} {
				add := builder.AddFile
				if entry.compressed {
					add = builder.AddCompressedFile
				}
				if err := add(entry.name, bytes.NewReader(entry.data), entry.executable); err != nil {
					t.Fatal(err)
				}
			}
			nested := erisfs.NewBuilder(baseBlocks, eris.Secret{})
			if err := nested.AddFile("inner", bytes.NewReader(encoded), false); err != nil {
				t.Fatal(err)
			}
			nestedRoot, err := nested.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if err := builder.AddSubdirectory("nested", nestedRoot); err != nil {
				t.Fatal(err)
			}
			if err := builder.AddSymlink("alias", "docs/content"); err != nil {
				t.Fatal(err)
			}
			root, err := builder.Finish()
			if err != nil {
				t.Fatal(err)
			}
			baseSource := &countingSource{BlockSource: baseBlocks}
			targetSource := &countingSource{BlockSource: targets}
			base, err := erisfs.OpenFS(root, baseSource)
			if err != nil {
				t.Fatal(err)
			}
			baseSource.calls.Store(0)
			wrapped := newLinkFS(t, base, targetSource)
			if baseSource.calls.Load() != 0 || targetSource.calls.Load() != 0 {
				t.Fatal("NewFS fetched blocks")
			}

			// The base must fetch the encoded link. The overlay must defer
			// target block reads, including when the base link is compressed.
			for _, name := range []string{"docs/content", "docs/compressed.eris", "nested/inner", "empty"} {
				file, err := wrapped.Open(name)
				if err != nil {
					t.Fatalf("Open(%q): %v", name, err)
				}
				info, err := file.Stat()
				if err != nil {
					t.Fatal(err)
				}
				wantSize := int64(len(payload))
				if name == "empty" {
					wantSize = 0
				}
				if info.Size() != wantSize {
					t.Fatalf("%s size = %d, want %d", name, info.Size(), wantSize)
				}
				if name == "docs/content" && info.Mode() != 0o555 {
					t.Fatalf("executable mode = %v", info.Mode())
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := wrapped.ReadDir("docs")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if _, err := entry.Info(); err != nil {
					t.Fatal(err)
				}
			}
			if baseSource.calls.Load() == 0 {
				t.Fatal("link detection did not read the base image")
			}
			if targetSource.calls.Load() != 0 {
				t.Fatal("opening or metadata read target blocks")
			}

			want := map[string][]byte{
				"plain.eris": ordinary, "docs/content": payload,
				"docs/compressed.eris": payload, "docs/plainlz": ordinary,
				"empty": nil, "nested/inner": payload, "alias": payload,
			}
			paths := make([]string, 0, len(want))
			for name, content := range want {
				paths = append(paths, name)
				got, err := fs.ReadFile(wrapped, name)
				if err != nil || !bytes.Equal(got, content) {
					t.Fatalf("ReadFile(%q): %d bytes, %v", name, len(got), err)
				}
			}
			if targetSource.calls.Load() == 0 {
				t.Fatal("content reads did not fetch target blocks")
			}
			if err := fstest.TestFS(wrapped, paths...); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"docs/content", "docs/compressed.eris", "nested/inner"} {
				file, err := wrapped.Open(name)
				if err != nil {
					t.Fatal(err)
				}
				buffer := make([]byte, 31)
				n, err := file.(io.ReaderAt).ReadAt(buffer, 1020)
				if err != nil || n != len(buffer) || !bytes.Equal(buffer, payload[1020:1051]) {
					t.Fatalf("%s ReadAt: %d, %v", name, n, err)
				}
				if _, err := file.(io.Seeker).Seek(-31, io.SeekEnd); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(file)
				if err != nil || !bytes.Equal(got, payload[len(payload)-31:]) {
					t.Fatalf("%s Seek/Read: %v", name, err)
				}
				_ = file.Close()
			}
			// Sub must work both before and after adding the overlay.
			subOverlay, err := fs.Sub(wrapped, "docs")
			if err != nil {
				t.Fatal(err)
			}
			subBase, err := fs.Sub(base, "docs")
			if err != nil {
				t.Fatal(err)
			}
			for _, sub := range []fs.FS{subOverlay, newLinkFS(t, subBase, targetSource)} {
				got, err := fs.ReadFile(sub, "content")
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("Sub read: %v", err)
				}
			}
			for _, name := range []string{"docs/content", "docs/compressed.eris"} {
				got, err := fs.ReadFile(base, name)
				if err != nil || !bytes.Equal(got, encoded) {
					t.Fatalf("base link %s changed: %v", name, err)
				}
			}
		})
	}
}

func TestFSWithERISFSBaseErrors(t *testing.T) {
	encoded, targets := encodeFSLink(t, []byte("target content"))
	for _, test := range []struct {
		name               string
		baseErr, targetErr error
	}{
		{"base block missing", eris.ErrMissingBlock, nil},
		{"base source EOF", io.EOF, nil},
		{"target block missing", nil, eris.ErrMissingBlock},
		{"target source EOF", nil, io.EOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseBlocks := make(eris.BlockSet)
			builder := erisfs.NewBuilder(baseBlocks, eris.Secret{})
			if err := builder.AddFile("file", bytes.NewReader(encoded), false); err != nil {
				t.Fatal(err)
			}
			root, err := builder.Finish()
			if err != nil {
				t.Fatal(err)
			}
			baseSource := &countingSource{BlockSource: baseBlocks}
			base, err := erisfs.OpenFS(root, baseSource)
			if err != nil {
				t.Fatal(err)
			}
			baseSource.err = test.baseErr
			targetSource := &countingSource{BlockSource: targets, err: test.targetErr}
			wrapped := newLinkFS(t, base, targetSource)
			file, err := wrapped.Open("file")
			want := test.baseErr
			if want == nil {
				if err != nil {
					t.Fatalf("Open fetched target: %v", err)
				}
				if targetSource.calls.Load() != 0 {
					t.Fatal("Open fetched target blocks")
				}
				_, err = io.ReadAll(file)
				_ = file.Close()
				want = test.targetErr
			}
			if want == io.EOF {
				want = io.ErrUnexpectedEOF
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			var pathErr *fs.PathError
			if !errors.As(err, &pathErr) || pathErr.Path != "file" {
				t.Fatalf("lost overlay path: %v", err)
			}
		})
	}
}
