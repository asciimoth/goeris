package volume_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

func openVolume(t *testing.T, root string) *volume.Volume {
	t.Helper()
	v, err := volume.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func testBlock(t *testing.T, size eris.BlockSize, value byte) ([]byte, eris.Reference) {
	t.Helper()
	block := bytes.Repeat([]byte{value}, int(size))
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	return block, ref
}

func writeFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s: length %d, error %v; want length %d with matching bytes", name, len(got), err, len(want))
	}
}

func assertEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory is not empty: %v, %v", entries, err)
	}
}

func TestBlockPath(t *testing.T) {
	for _, path := range []string{
		"FGI/BBZ/22V/4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA",
		"RY2/DQN/YEP/JD5TOJZME7MEK3RFFF75ZOIIFAJTKHP4SEMI3CPJXAQ",
		"EHQ/2P3/JZU/X6TP3PZKFPMWOK4SBQKOEWKU52YJJQTKQJ66WWEQX4Q",
		"CMC/7JN/IYG/VRPDTU4M2OUPKFNEEUPXYDWUNSINMAOBJXYUZA32CBA",
	} {
		ref, err := eris.ParseReference(string(bytes.ReplaceAll([]byte(path), []byte("/"), nil)))
		if err != nil {
			t.Fatal(err)
		}
		if got := volume.BlockPath(ref); got != filepath.FromSlash(path) {
			t.Errorf("BlockPath = %q, want %q", got, path)
		}
	}
}

func TestOpenRequiresRoot(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := volume.Open(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open missing directory: %v", err)
	}
	assertEmpty(t, root)
	file := filepath.Join(root, "file")
	writeFile(t, file, nil)
	fileVolume := openVolume(t, file)
	block, _ := testBlock(t, eris.BlockSize1KiB, 0)
	if _, err := fileVolume.PutBlock(context.Background(), block, eris.NoTTL); !errors.Is(err, volume.ErrNotDirectory) {
		t.Fatalf("write to a regular file root: %v", err)
	}
	assertFile(t, file, nil)
	root = t.TempDir()
	v := openVolume(t, root)
	assertEmpty(t, root)
	if _, err := v.GetBlock(context.Background(), eris.Reference{}, eris.NoTTL); !errors.Is(err, eris.ErrMissingBlock) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing block: %v", err)
	}
	assertEmpty(t, root)
}

func TestRoundTripAndReopen(t *testing.T) {
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			root := t.TempDir()
			v := openVolume(t, root)
			block, ref := testBlock(t, size, 17)
			// A terminal store accepts TTL 1 without forwarding it.
			got, err := v.PutBlock(context.Background(), block, 1, ref)
			if err != nil || got != ref {
				t.Fatalf("PutBlock: %s, %v", got, err)
			}
			assertFile(t, filepath.Join(root, volume.BlockPath(ref)), block)
			if _, err := os.Stat(filepath.Join(root, ref.String())); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unexpected flat copy: %v", err)
			}
			if err := v.Close(); err != nil {
				t.Fatal(err)
			}
			v = openVolume(t, root)
			read, err := v.GetBlock(context.Background(), ref, 1)
			if err != nil || !bytes.Equal(read, block) {
				t.Fatalf("GetBlock: length %d, %v", len(read), err)
			}
			read[0]++
			assertFile(t, filepath.Join(root, volume.BlockPath(ref)), block)
			if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err != nil {
				t.Fatalf("duplicate write: %v", err)
			}
		})
	}
}

func TestFlatFallbackAndPrecedence(t *testing.T) {
	root := t.TempDir()
	v := openVolume(t, root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 2)
	flat := filepath.Join(root, ref.String())
	nested := filepath.Join(root, volume.BlockPath(ref))
	writeFile(t, flat, block)
	for _, createParents := range []bool{false, true} {
		if createParents {
			if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		got, err := v.GetBlock(context.Background(), ref, eris.NoTTL)
		if err != nil || !bytes.Equal(got, block) {
			t.Fatalf("flat fallback: length %d, %v", len(got), err)
		}
		if _, err := os.Stat(nested); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read created canonical block: %v", err)
		}
	}
	writeFile(t, nested, []byte("corrupt"))
	if got, err := v.GetBlock(context.Background(), ref, eris.NoTTL); !errors.Is(err, eris.ErrInvalidBlock) || got != nil {
		t.Fatalf("corrupt canonical file bypassed: %v", err)
	}
	if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err != nil {
		t.Fatalf("repair: %v", err)
	}
	assertFile(t, nested, block)
	assertFile(t, flat, block)
	writeFile(t, flat, []byte("invalid flat copy"))
	got, err := v.GetBlock(context.Background(), ref, eris.NoTTL)
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("canonical precedence: %v", err)
	}
	if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err != nil {
		t.Fatal(err)
	}
	assertFile(t, flat, []byte("invalid flat copy"))
}

func TestInvalidWritesDoNotChangeVolume(t *testing.T) {
	block, ref := testBlock(t, eris.BlockSize1KiB, 3)
	for _, tc := range []struct {
		name     string
		block    []byte
		expected []eris.Reference
	}{
		{name: "empty"},
		{name: "short", block: block[:len(block)-1]},
		{name: "oversized", block: make([]byte, int(eris.BlockSize32KiB)+1)},
		{name: "mismatch", block: block, expected: []eris.Reference{{}}},
		{name: "multiple", block: block, expected: []eris.Reference{ref, ref}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			v := openVolume(t, root)
			if _, err := v.PutBlock(context.Background(), tc.block, eris.NoTTL, tc.expected...); !errors.Is(err, eris.ErrInvalidBlock) {
				t.Fatalf("invalid write: %v", err)
			}
			assertEmpty(t, root)
		})
	}
}

func TestInvalidStoredBlocks(t *testing.T) {
	block, ref := testBlock(t, eris.BlockSize1KiB, 4)
	for _, flat := range []bool{false, true} {
		for _, data := range [][]byte{nil, block[:100], bytes.Repeat([]byte{5}, len(block)), make([]byte, 1<<20)} {
			root := t.TempDir()
			v := openVolume(t, root)
			path := volume.BlockPath(ref)
			if flat {
				path = ref.String()
			}
			writeFile(t, filepath.Join(root, path), data)
			got, err := v.GetBlock(context.Background(), ref, eris.NoTTL)
			if !errors.Is(err, eris.ErrInvalidBlock) || got != nil {
				t.Fatalf("invalid stored block (flat=%v, size=%d): %v", flat, len(data), err)
			}
		}
	}
}

func TestUnrelatedFilesAndCollisions(t *testing.T) {
	root := t.TempDir()
	v := openVolume(t, root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 6)
	nested := filepath.Join(root, volume.BlockPath(ref))
	for _, name := range []string{"README.md", "other/data", filepath.Join(filepath.Dir(volume.BlockPath(ref)), ".eris-tmp-unrelated")} {
		writeFile(t, filepath.Join(root, name), []byte(name))
	}
	if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "other/data", filepath.Join(filepath.Dir(volume.BlockPath(ref)), ".eris-tmp-unrelated")} {
		assertFile(t, filepath.Join(root, name), []byte(name))
	}
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ref.String()), block)
	if _, err := v.GetBlock(context.Background(), ref, eris.NoTTL); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("directory accepted as block or flat fallback used: %v", err)
	}
	if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err == nil {
		t.Fatal("write replaced a directory")
	}
	entries, err := os.ReadDir(filepath.Dir(nested))
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed write left temporary file: %v, %v", entries, err)
	}
	for parent := filepath.Dir(volume.BlockPath(ref)); parent != "."; parent = filepath.Dir(parent) {
		root = t.TempDir()
		v = openVolume(t, root)
		writeFile(t, filepath.Join(root, parent), []byte("prefix collision"))
		writeFile(t, filepath.Join(root, ref.String()), block)
		if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err == nil {
			t.Fatal("write accepted a non-directory prefix")
		}
		if got, err := v.GetBlock(context.Background(), ref, eris.NoTTL); got != nil || err == nil || errors.Is(err, eris.ErrMissingBlock) || errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("non-directory prefix %s did not produce an I/O error: %v", parent, err)
		}
		assertFile(t, filepath.Join(root, parent), []byte("prefix collision"))
	}
}

func TestCancellationAndClosedVolume(t *testing.T) {
	root := t.TempDir()
	v := openVolume(t, root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 7)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.GetBlock(ctx, ref, eris.NoTTL); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
	if _, err := v.PutBlock(ctx, block, eris.NoTTL); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	assertEmpty(t, root)
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	for _, closed := range []*volume.Volume{v, nil, {}} {
		if _, err := closed.GetBlock(context.Background(), ref, eris.NoTTL); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("closed read: %v", err)
		}
		if _, err := closed.PutBlock(context.Background(), block, eris.NoTTL); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("closed write: %v", err)
		}
		if _, err := closed.PutBlock(context.Background(), nil, eris.NoTTL); !errors.Is(err, fs.ErrClosed) {
			t.Fatalf("invalid write to closed volume: %v", err)
		}
	}
}

func TestConcurrentPublication(t *testing.T) {
	root := t.TempDir()
	writers := []*volume.Volume{openVolume(t, root), openVolume(t, root)}
	block, ref := testBlock(t, eris.BlockSize32KiB, 8)
	var group sync.WaitGroup
	for i := range 12 {
		group.Go(func() {
			v := writers[i%len(writers)]
			for range 15 {
				if i%3 == 0 {
					got, err := v.GetBlock(context.Background(), ref, eris.NoTTL)
					if errors.Is(err, eris.ErrMissingBlock) {
						continue
					}
					if err != nil || !bytes.Equal(got, block) {
						t.Errorf("concurrent read: length %d, %v", len(got), err)
						return
					}
				} else if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err != nil {
					t.Errorf("concurrent write: %v", err)
					return
				}
			}
		})
	}
	group.Wait()
	assertFile(t, filepath.Join(root, volume.BlockPath(ref)), block)
	entries, err := os.ReadDir(filepath.Join(root, filepath.Dir(volume.BlockPath(ref))))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestSymlinkCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	v := openVolume(t, root)
	block, ref := testBlock(t, eris.BlockSize1KiB, 9)
	if err := os.Symlink(outside, filepath.Join(root, ref.String()[:3])); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := v.PutBlock(context.Background(), block, eris.NoTTL); err == nil {
		t.Fatal("write followed a link outside the root")
	}
	assertEmpty(t, outside)
	if _, err := v.GetBlock(context.Background(), ref, eris.NoTTL); err == nil || errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("read did not reject escaping link: %v", err)
	}
	if err := os.Remove(filepath.Join(root, ref.String()[:3])); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "block")
	writeFile(t, outsideFile, block)
	if err := os.Symlink(outsideFile, filepath.Join(root, ref.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := v.GetBlock(context.Background(), ref, eris.NoTTL); err == nil {
		t.Fatal("flat read followed a link outside the root")
	}
	assertFile(t, outsideFile, block)
}

func TestEncodedContentRoundTrip(t *testing.T) {
	content := bytes.Repeat([]byte("volume integration test\n"), 3000)
	capability, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	v := openVolume(t, t.TempDir())
	for ref, block := range blocks {
		if _, err := v.PutBlock(context.Background(), block, eris.NoTTL, ref); err != nil {
			t.Fatal(err)
		}
	}
	stored := make(eris.BlockSet)
	for ref := range blocks {
		stored[ref], err = v.GetBlock(context.Background(), ref, eris.NoTTL)
		if err != nil {
			t.Fatal(err)
		}
	}
	decoded, err := eris.Decode(capability, stored)
	if err != nil || !bytes.Equal(decoded, content) {
		t.Fatalf("Decode stored content: length %d, %v", len(decoded), err)
	}
}

func TestPublicationReplacesLinksWithoutChangingTargets(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprintf("hard=%v", hard), func(t *testing.T) {
			root := t.TempDir()
			v := openVolume(t, root)
			block, ref := testBlock(t, eris.BlockSize1KiB, 95)
			target := filepath.Join(t.TempDir(), "unrelated")
			original := []byte("do not change this file")
			writeFile(t, target, original)
			name := filepath.Join(root, volume.BlockPath(ref))
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if hard {
				link = os.Link
			}
			if err := link(target, name); err != nil {
				t.Skipf("links unavailable: %v", err)
			}
			if _, err := v.PutBlock(context.Background(), block, eris.NoTTL, ref); err != nil {
				t.Fatal(err)
			}
			assertFile(t, target, original)
			assertFile(t, name, block)
			info, err := os.Lstat(name)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("published path is not a regular file: %v, %v", info, err)
			}
		})
	}
}

func TestRelativeDirectoryLinks(t *testing.T) {
	for _, escape := range []bool{false, true} {
		t.Run(fmt.Sprintf("escape=%v", escape), func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			target := filepath.Join(root, "inside")
			link := "inside"
			if escape {
				target = filepath.Join(base, "outside")
				link = filepath.Join("..", "outside")
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			block, ref := testBlock(t, eris.BlockSize1KiB, 96)
			if err := os.Symlink(link, filepath.Join(root, ref.String()[:3])); err != nil {
				t.Skipf("symbolic links unavailable: %v", err)
			}
			v := openVolume(t, root)
			_, err := v.PutBlock(context.Background(), block, eris.NoTTL)
			if escape {
				if err == nil {
					t.Fatal("write escaped root through a relative link")
				}
				assertEmpty(t, target)
				if _, err := v.GetBlock(context.Background(), ref, eris.NoTTL); err == nil || errors.Is(err, eris.ErrMissingBlock) {
					t.Fatalf("escaping relative link: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := v.GetBlock(context.Background(), ref, eris.NoTTL)
			if err != nil || !bytes.Equal(got, block) {
				t.Fatalf("read through an internal relative link: %v", err)
			}
		})
	}
}
