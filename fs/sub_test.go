package fs

import (
	"errors"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestSubSymlinkRoot(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	builder := NewBuilder(blocks, eris.Secret{})
	if err := builder.AddFile("dir/file", strings.NewReader("content"), false); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"alias": "dir", "chain": "alias", "dir/link": "file",
	} {
		if err := builder.AddSymlink(name, target); err != nil {
			t.Fatal(err)
		}
	}
	capability, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	image, err := OpenFS(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir", "alias", "chain"} {
		t.Run(name, func(t *testing.T) {
			sub, err := image.Sub(name)
			if err != nil {
				t.Fatal(err)
			}
			// Sub(".") must preserve the same directory-root semantics.
			for range 2 {
				info, err := stdfs.Lstat(sub, ".")
				if err != nil {
					t.Fatal(err)
				}
				if !info.IsDir() {
					t.Errorf("Lstat root mode = %v; want directory", info.Mode())
				}
				if _, err := stdfs.ReadLink(sub, "."); !errors.Is(err, stdfs.ErrInvalid) {
					t.Errorf("ReadLink root = %v; want ErrInvalid", err)
				}
				info, err = stdfs.Lstat(sub, "link")
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode()&stdfs.ModeSymlink == 0 {
					t.Errorf("Lstat child mode = %v; want symlink", info.Mode())
				}
				if target, err := stdfs.ReadLink(sub, "link"); err != nil || target != "file" {
					t.Errorf("ReadLink child = %q, %v", target, err)
				}
				sub, err = sub.(FileSystem).Sub(".")
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
