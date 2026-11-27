package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
)

// FuzzMountPaths covers the public operations with invalid paths, capability
// aliases, on-demand lookups, virtual directories, and image paths.
func FuzzMountPaths(f *testing.F) {
	blocks, _ := mountFixture(f)
	for ref := range blocks.Blocks {
		for _, layout := range []string{ref.String(), filepath.ToSlash(volume.BlockPath(ref))} {
			for _, name := range mountCaseVariants(layout) {
				f.Add(name, false)
				f.Add(name, true)
			}
		}
	}
	for _, name := range []string{"AAA", "aaa/BBB", "aBc/234/XYZ", "AAA/BBB/CCC/DD", "AAA/BBB/CCC/DDD/EEE", "FS", strings.ToLower(mountHello), "Fs/" + strings.ToLower(mountIndex) + "/hello.txt"} {
		f.Add(name, false)
	}
	for _, name := range []string{".", "fs", "", "/fs", "fs/../x", "fs//x", "\xff", "fs/" + mountIndex, "fs/" + mountIndex + "/docs", "fs/" + mountIndex + "/latest.txt", "fs/" + mountIndex + "/missing", mountHello + "/x", strings.Repeat("A", 52)} {
		f.Add(name, false)
		f.Add(name, true)
	}
	for _, urn := range []string{mountHello, mountIndex} {
		for _, alias := range mountAliases(f, urn) {
			f.Add(alias, false)
			f.Add(alias, true)
		}
	}
	m := NewMount(context.Background(), blocks, eris.BlockOptions{})
	namespace, err := m.Sub("fs")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, name string, treesOnly bool) {
		if len(name) > 4096 {
			t.Skip()
		}
		current := m
		if treesOnly {
			current = namespace.(*Mount)
		}
		for op, call := range mountOperations(current) {
			err := call(name)
			if !stdfs.ValidPath(name) {
				checkMountError(t, op, name, err, stdfs.ErrInvalid)
			} else if err != nil {
				var pe *stdfs.PathError
				if !errors.As(err, &pe) || pe.Path != name {
					t.Fatalf("lost public path: %v", err)
				}
			}
		}
		// Resolving names must never add them to the namespace's listing.
		entries, err := current.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if treesOnly {
			want = 0
		}
		if len(entries) != want {
			t.Fatalf("listing grew: %v", entries)
		}
	})
}

// FuzzMountContent compares random-access and sequential reads with bytes.Reader
// for both block sizes, all name encodings, and raw and image file paths.
func FuzzMountContent(f *testing.F) {
	for _, data := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte("a"), 1024), bytes.Repeat([]byte("boundary"), 4097)} {
		for alias := uint8(0); alias < 4; alias++ {
			f.Add(data, uint32(7), uint16(19), alias, alias%2 == 0)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, offset uint32, length uint16, alias uint8, large bool) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		size := eris.BlockSize1KiB
		if large {
			size = eris.BlockSize32KiB
		}
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		store := eris.NewBlockCapabilityStore(blocks)
		cap, err := store.Encode(t.Context(), data, eris.Secret{}, size, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		index := Index{Entries: []Entry{{Path: []string{"file"}, Capability: cap, Size: int64(len(data))}, {Path: []string{"link"}, Type: Symlink, Target: "file"}}}
		root, err := EncodeIndex(context.Background(), index, eris.Secret{}, store, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		m := NewMount(context.Background(), blocks, eris.BlockOptions{})
		names := []string{mountAliases(t, cap.String())[alias%4], "fs/" + mountAliases(t, root.String())[alias%4] + "/file", "fs/" + root.String() + "/link"}
		for _, name := range names {
			file, err := m.OpenFile(name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			oracle := bytes.NewReader(data)
			// Include offsets just past EOF, and zero-length ReadAt buffers.
			off := int64(offset % uint32(len(data)+2))
			got, want := make([]byte, int(length)%4097), make([]byte, int(length)%4097)
			n, readErr := file.ReadAt(got, off)
			wn, wantErr := oracle.ReadAt(want, off)
			// ReaderAt permits either nil or EOF on a zero-length request at EOF.
			if n != wn || !bytes.Equal(got[:n], want[:wn]) || (len(got) != 0 && !errors.Is(readErr, wantErr)) {
				t.Fatalf("ReadAt(%d): n=%d/%d err=%v/%v", off, n, wn, readErr, wantErr)
			}
			pos, err := file.Seek(off, io.SeekStart)
			if err != nil || pos != off {
				t.Fatalf("Seek: %d, %v", pos, err)
			}
			tail, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(tail, data[min(int(off), len(data)):]) {
				t.Fatalf("tail: %v", err)
			}
			all, err := m.ReadFile(name)
			if err != nil || !bytes.Equal(all, data) {
				t.Fatalf("content: %v", err)
			}
		}
	})
}

// FuzzMountIndex passes malformed and valid wire bytes through the complete
// block-source-to-mount path. Raw access must preserve the bytes even when the
// same content is rejected as an ERIS-FS index.
func FuzzMountIndex(f *testing.F) {
	_, vector := mountFixture(f)
	empty, _ := (Index{}).MarshalBinary()
	for _, data := range [][]byte{nil, []byte("not an index"), vector, empty, []byte(MagicPrefix + "\x82\x01\xa1\x61a\x82\x02\x61.")} {
		for _, path := range []string{".", "hello.txt", "latest.txt", "a", "docs/readme.txt", "../escape"} {
			f.Add(data, path)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, inner string) {
		if len(data) > 1<<14 || len(inner) > 1024 {
			t.Skip()
		}
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewMountWithLimit(context.Background(), blocks, 1<<14, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := m.ReadFile(cap.String())
		if err != nil || !bytes.Equal(raw, data) {
			t.Fatalf("raw index changed: %v", err)
		}
		_, parseErr := ParseIndex(data)
		file, openErr := m.Open("fs/" + cap.String())
		if parseErr != nil {
			if !errors.Is(openErr, ErrInvalidIndex) {
				t.Fatalf("accepted malformed index: %v", openErr)
			}
			return
		}
		if openErr != nil {
			t.Fatal(openErr)
		}
		_ = file.Close()
		name := "fs/" + cap.String()
		if inner != "." {
			name += "/" + inner
		}
		for op, call := range mountOperations(m) {
			err := call(name)
			if !stdfs.ValidPath(name) {
				checkMountError(t, op, name, err, stdfs.ErrInvalid)
			} else if err != nil {
				var pe *stdfs.PathError
				if !errors.As(err, &pe) || pe.Path != name {
					t.Fatalf("lost image error path: %v", err)
				}
			}
		}
	})
}

// FuzzMountLinks compares mount link handling with the image interface. Targets
// include cycles, absolute paths, parent segments, and directory aliases.
func FuzzMountLinks(f *testing.F) {
	for _, target := range []string{"file", "dir/../file", "alias/../file", "alias/../../file", "../file", "../sibling/file", "/file", "link", "missing/../file", "dir/", ""} {
		f.Add(target, "link")
		f.Add(target, "link/file")
	}
	f.Fuzz(func(t *testing.T, target, name string) {
		if len(target) > 2048 || len(name) > 2048 || !stdfs.ValidPath(name) {
			t.Skip()
		}
		ctx := context.Background()
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		store := eris.NewBlockCapabilityStore(blocks)
		builder := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
		for _, path := range []string{"file", "dir/file", "dir/child/file"} {
			if err := builder.AddFile(ctx, path, strings.NewReader(path), false); err != nil {
				t.Fatal(err)
			}
		}
		if err := builder.AddSymlink("alias", "dir/child"); err != nil {
			t.Fatal(err)
		}
		if err := builder.AddSymlink("link", target); err != nil {
			return
		}
		cap, err := builder.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		image, err := OpenFS(ctx, cap, store, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		m := NewMount(ctx, blocks, eris.BlockOptions{})
		// A nested image must give the same results as the same image
		// opened alone, even when the parent and sibling contain the file.
		sibling := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
		if err := sibling.AddFile(ctx, "file", strings.NewReader("sibling content"), false); err != nil {
			t.Fatal(err)
		}
		siblingCap, err := sibling.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parent := NewBuilder(store, eris.Secret{}, eris.BlockOptions{})
		if err := parent.AddFile(ctx, "file", strings.NewReader("parent content"), false); err != nil {
			t.Fatal(err)
		}
		if err := parent.AddSubdirectory("child", cap); err != nil {
			t.Fatal(err)
		}
		if err := parent.AddSubdirectory("sibling", siblingCap); err != nil {
			t.Fatal(err)
		}
		parentCap, err := parent.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, wantErr := image.ReadFile(ctx, name)
		wantLink, wantLinkErr := image.ReadLink(ctx, "link")
		for _, root := range []string{"fs/" + cap.String(), "fs/" + parentCap.String() + "/child"} {
			got, err := m.ReadFile(path.Join(root, name))
			if !bytes.Equal(got, want) || (err == nil) != (wantErr == nil) {
				t.Fatalf("%s content mismatch: got %q, %v; want %q, %v", root, got, err, want, wantErr)
			}
			for _, sentinel := range []error{stdfs.ErrNotExist, stdfs.ErrInvalid} {
				if errors.Is(err, sentinel) != errors.Is(wantErr, sentinel) {
					t.Fatalf("%s link error mismatch: %v, %v", root, err, wantErr)
				}
			}
			link, err := m.ReadLink(root + "/link")
			if link != wantLink || (err == nil) != (wantLinkErr == nil) ||
				errors.Is(err, stdfs.ErrNotExist) != errors.Is(wantLinkErr, stdfs.ErrNotExist) ||
				errors.Is(err, stdfs.ErrInvalid) != errors.Is(wantLinkErr, stdfs.ErrInvalid) {
				t.Fatalf("%s link mismatch: %q, %v; image: %q, %v", root, link, err, wantLink, wantLinkErr)
			}
		}
		// A client can clean the target before it follows the link.
		clean := path.Clean(target)
		unsafe := clean == ".." || strings.HasPrefix(clean, "../") ||
			path.IsAbs(target) || strings.ContainsAny(target, "\\:\x00")
		if unsafe && wantLinkErr == nil {
			t.Fatalf("unsafe root link was exposed: %q", target)
		}
	})
}

// FuzzMountBlocks checks ciphertext, aliases, and seek positions against an
// independent byte reader. Prefix Sub views must select the same stored block.
func FuzzMountBlocks(f *testing.F) {
	for _, data := range [][]byte{nil, []byte("block"), bytes.Repeat([]byte("boundary"), 4097)} {
		for variant := uint8(0); variant < 4; variant++ {
			f.Add(data, variant%2 == 0, variant%2 != 0, variant, uint32(7), uint16(19))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, large, flat bool, variant uint8, offset uint32, length uint16) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		size := eris.BlockSize1KiB
		if large {
			size = eris.BlockSize32KiB
		}
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, size, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ref := cap.Root.Reference
		want := blocks.Blocks[ref].Data
		name := filepath.ToSlash(volume.BlockPath(ref))
		if flat {
			name = ref.String()
		}
		name = mountCaseVariants(name)[variant%4]
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m := NewMount(ctx, blocks, eris.BlockOptions{})
		got, err := m.ReadFile(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ciphertext: %v", err)
		}
		file, err := m.OpenFile(name)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		off := int64(offset % uint32(len(want)+2))
		b, expected := make([]byte, int(length)%4097), make([]byte, int(length)%4097)
		n, err := file.ReadAt(b, off)
		wn, we := bytes.NewReader(want).ReadAt(expected, off)
		if n != wn || !errors.Is(err, we) || !bytes.Equal(b, expected) {
			t.Fatalf("ReadAt: %d/%d, %v/%v", n, wn, err, we)
		}
		if _, err := file.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		tail, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(tail, want[min(int(off), len(want)):]) {
			t.Fatalf("tail: %v", err)
		}
		if !flat {
			parts := strings.Split(name, "/")
			depth := int(variant%3) + 1
			sub, err := m.Sub(strings.Join(parts[:depth], "/"))
			if err != nil {
				t.Fatal(err)
			}
			nested, err := stdfs.ReadFile(sub, strings.Join(parts[depth:], "/"))
			if err != nil || !bytes.Equal(nested, want) {
				t.Fatalf("Sub ciphertext: %v", err)
			}
		}
		cancel()
		if _, err := file.Read(b); !errors.Is(err, context.Canceled) {
			t.Fatalf("cached read ignored cancellation: %v", err)
		}
	})
}
