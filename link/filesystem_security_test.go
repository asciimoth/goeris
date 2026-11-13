package link_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

type namedEntry struct {
	fs.DirEntry
	name string
}

func (entry namedEntry) Name() string { return entry.name }

type entryFS struct {
	fs.FS
	entries []fs.DirEntry
	err     error
	opens   []string
	closes  int
}

func (base *entryFS) Open(name string) (fs.File, error) {
	base.opens = append(base.opens, name)
	file, err := base.FS.Open(name)
	if err != nil || name != "dir" {
		return file, err
	}
	return &entryDirectory{File: file, base: base}, nil
}

func (base *entryFS) ReadDir(string) ([]fs.DirEntry, error) {
	return base.entries, base.err
}

type entryDirectory struct {
	fs.File
	base *entryFS
}

func (dir *entryDirectory) ReadDir(int) ([]fs.DirEntry, error) {
	return dir.base.entries, dir.base.err
}

func (dir *entryDirectory) Close() error {
	dir.base.closes++
	return dir.File.Close()
}

func TestFSRejectsInvalidEntryNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../secret", "a/../../secret", "/secret", "a/b", "a//b", string([]byte{0xff})} {
		for _, mode := range []fs.FileMode{0, fs.ModeDir, fs.ModeSymlink} {
			for _, viaOpen := range []bool{false, true} {
				base := &entryFS{FS: fstest.MapFS{"dir": {Mode: fs.ModeDir}, "secret": {Data: []byte("private")}}}
				info, err := fs.Stat(fstest.MapFS{"entry": {Mode: mode}}, "entry")
				if err != nil {
					t.Fatal(err)
				}
				base.entries = []fs.DirEntry{namedEntry{DirEntry: fs.FileInfoToDirEntry(info), name: name}}
				wrapped := newLinkFS(t, base, eris.BlockSet{})
				readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
				if viaOpen {
					file, err := wrapped.Open("dir")
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = file.Close() }()
					readDir = func() ([]fs.DirEntry, error) { return file.(fs.ReadDirFile).ReadDir(1) }
				}
				base.opens = nil
				entries, err := readDir()
				if len(entries) != 0 || !errors.Is(err, fs.ErrInvalid) {
					t.Errorf("name %q, mode %v, via Open %v: got %d entries, %v", name, mode, viaOpen, len(entries), err)
				}
				var pathErr *fs.PathError
				if !errors.As(err, &pathErr) || pathErr.Op != "readdir" || pathErr.Path != "dir" {
					t.Errorf("missing directory context: %v", err)
				}
				if len(base.opens) != 0 {
					t.Fatalf("invalid entry caused file access: %v", base.opens)
				}
			}
		}
	}
}

func TestFSReadDirPartialResults(t *testing.T) {
	base := &entryFS{FS: fstest.MapFS{"dir/file": {Data: []byte("plain")}}}
	info, err := fs.Stat(base.FS, "dir/file")
	if err != nil {
		t.Fatal(err)
	}
	base.entries = []fs.DirEntry{fs.FileInfoToDirEntry(info)}
	wrapped := newLinkFS(t, base, eris.BlockSet{})
	file, err := wrapped.Open("dir")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	base.err = io.EOF
	entries, err := file.(fs.ReadDirFile).ReadDir(2)
	if len(entries) != 1 || err != io.EOF {
		t.Fatalf("partial directory read: %v, %v", entries, err)
	}
	if info, err := entries[0].Info(); err != nil || info.Size() != 5 {
		t.Fatalf("partial entry Info: %v, %v", info, err)
	}
	base.err = fs.ErrPermission
	entries, err = wrapped.ReadDir("dir")
	if len(entries) != 1 || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("partial FS read: %v, %v", entries, err)
	}
}

func TestFSRejectsNilDirectoryEntries(t *testing.T) {
	for _, mode := range []string{"ReadDirFS", "fallback", "open directory"} {
		t.Run(mode, func(t *testing.T) {
			files := fstest.MapFS{"dir/file": {Data: []byte("plain")}}
			info, err := fs.Stat(files, "dir/file")
			if err != nil {
				t.Fatal(err)
			}
			base := &entryFS{
				FS: files, entries: []fs.DirEntry{fs.FileInfoToDirEntry(info), nil},
				err: fs.ErrPermission,
			}
			var source fs.FS = base
			if mode == "fallback" {
				source = openFSFunc(base.Open)
			}
			wrapped := newLinkFS(t, source, eris.BlockSet{})
			readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
			if mode == "open directory" {
				file, err := wrapped.Open("dir")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				readDir = func() ([]fs.DirEntry, error) { return file.(fs.ReadDirFile).ReadDir(-1) }
			}
			entries, err := readDir()
			if len(entries) != 0 || !errors.Is(err, fs.ErrInvalid) || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("invalid directory result: %v, %v", entries, err)
			}
			var pathErr *fs.PathError
			if !errors.As(err, &pathErr) || pathErr.Op != "readdir" || pathErr.Path != "dir" {
				t.Fatalf("missing directory context: %v", err)
			}
			if mode == "fallback" && base.closes != 1 {
				t.Fatalf("directory close count = %d, want 1", base.closes)
			}
		})
	}
}

func TestFSReadDirSortsWithoutChangingBaseEntries(t *testing.T) {
	files := fstest.MapFS{"dir/a": {}, "dir/z": {}}
	entries, err := fs.ReadDir(files, "dir")
	if err != nil {
		t.Fatal(err)
	}
	base := &entryFS{FS: files, entries: []fs.DirEntry{entries[1], entries[0]}}
	for _, source := range []fs.FS{base, openFSFunc(base.Open)} {
		got, err := newLinkFS(t, source, eris.BlockSet{}).ReadDir("dir")
		if err != nil || len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "z" {
			t.Fatalf("ReadDir order: %v, %v", got, err)
		}
		if base.entries[0].Name() != "z" || base.entries[1].Name() != "a" {
			t.Fatal("ReadDir changed the base entry slice")
		}
	}
}

type changingEntry struct {
	fs.DirEntry
	name string
	mode fs.FileMode
}

func (entry *changingEntry) Name() string      { return entry.name }
func (entry *changingEntry) Type() fs.FileMode { return entry.mode }
func (entry *changingEntry) IsDir() bool       { return entry.mode.IsDir() }

func TestFSKeepsCheckedEntryNamesAndTypes(t *testing.T) {
	for _, viaOpen := range []bool{false, true} {
		files := fstest.MapFS{"dir/file": {Data: []byte("plain")}}
		info, err := files.Stat("dir/file")
		if err != nil {
			t.Fatal(err)
		}
		entry := &changingEntry{DirEntry: fs.FileInfoToDirEntry(info), name: "file"}
		base := &entryFS{FS: files, entries: []fs.DirEntry{entry}}
		wrapped := newLinkFS(t, base, eris.BlockSet{})
		readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
		if viaOpen {
			dir, err := wrapped.Open("dir")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			readDir = func() ([]fs.DirEntry, error) { return dir.(fs.ReadDirFile).ReadDir(-1) }
		}
		entries, err := readDir()
		if err != nil || len(entries) != 1 {
			t.Fatalf("ReadDir: %v, %v", entries, err)
		}
		entry.name, entry.mode = "../private", fs.ModeDir
		if entries[0].Name() != "file" || entries[0].Type() != 0 || entries[0].IsDir() {
			t.Fatal("base mutation changed the checked entry name or type")
		}
		base.opens = nil
		got, err := entries[0].Info()
		if err != nil || got.Name() != "file" || got.Size() != 5 {
			t.Fatalf("Info: %v, %v", got, err)
		}
		for _, name := range base.opens {
			if name != "dir/file" {
				t.Fatalf("Info opened unchecked path %q", name)
			}
		}
	}
}

func TestFSSymlinkInspectionDoesNotFollowTargets(t *testing.T) {
	for _, target := range []string{"../private", "/private", "loop", "missing"} {
		t.Run(target, func(t *testing.T) {
			base := fstest.MapFS{
				"loop": {Mode: fs.ModeSymlink, Data: []byte(target)},
			}
			source := &countingSource{err: errors.New("unexpected block access")}
			wrapped := newLinkFS(t, base, source)
			info, err := fs.Lstat(wrapped, "loop")
			if err != nil || info.Mode()&fs.ModeSymlink == 0 || info.Size() != int64(len(target)) {
				t.Fatalf("Lstat followed target %q: %v, %v", target, info, err)
			}
			got, err := fs.ReadLink(wrapped, "loop")
			if err != nil || got != target {
				t.Fatalf("ReadLink changed target %q: %q, %v", target, got, err)
			}
			if source.calls.Load() != 0 {
				t.Fatal("symbolic link inspection fetched blocks")
			}
		})
	}
}

type metadataFS struct {
	fs.FS
	info         fs.FileInfo
	err          error
	opens, stats int
}

func (base *metadataFS) Stat(string) (fs.FileInfo, error) {
	base.stats++
	return base.info, base.err
}

func (base *metadataFS) Open(string) (fs.File, error) {
	base.opens++
	return nil, fs.ErrPermission
}

func TestFSStatDoesNotOpenSpecialFiles(t *testing.T) {
	for _, mode := range []fs.FileMode{fs.ModeNamedPipe, fs.ModeDevice, fs.ModeSocket, fs.ModeDir} {
		info, err := fs.Stat(fstest.MapFS{"special": {Mode: mode}}, "special")
		if err != nil {
			t.Fatal(err)
		}
		base := &metadataFS{info: info}
		wrapped := newLinkFS(t, base, eris.BlockSet{})
		for _, stat := range []func(string) (fs.FileInfo, error){wrapped.Stat, wrapped.Lstat} {
			got, err := stat("special")
			if err != nil || got != info || base.opens != 0 {
				t.Fatalf("mode %v: info %v, error %v, opens %d", mode, got, err, base.opens)
			}
		}
	}
}

func TestFSStatValidatesBeforeBaseAccess(t *testing.T) {
	base := &metadataFS{err: fs.ErrNotExist}
	wrapped := newLinkFS(t, base, eris.BlockSet{})
	if _, err := wrapped.Stat("../secret"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("invalid path: %v", err)
	}
	if base.stats != 0 || base.opens != 0 {
		t.Fatal("invalid path reached the base FS")
	}
	_, err := wrapped.Stat("missing")
	var pathErr *fs.PathError
	if !errors.Is(err, fs.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Op != "stat" || pathErr.Path != "missing" || base.opens != 0 {
		t.Fatalf("base Stat failure: %v, opens %d", err, base.opens)
	}
	for _, invalid := range []*link.FS{nil, {}} {
		if _, err := invalid.Stat("valid"); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("uninitialized FS: %v", err)
		}
	}
}

func TestFSEntryInfoDoesNotFollowReplacementSymlink(t *testing.T) {
	for _, viaOpen := range []bool{false, true} {
		encoded, blocks := encodeFSLink(t, []byte("private"))
		base := fstest.MapFS{"dir/file": {Data: []byte("plain")}, "private": {Data: encoded}}
		source := &countingSource{BlockSource: blocks}
		wrapped := newLinkFS(t, base, source)
		readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
		if viaOpen {
			dir, err := wrapped.Open("dir")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			readDir = func() ([]fs.DirEntry, error) { return dir.(fs.ReadDirFile).ReadDir(-1) }
		}
		entries, err := readDir()
		if err != nil || len(entries) != 1 {
			t.Fatalf("ReadDir: %v, %v", entries, err)
		}
		base["dir/file"] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("../private")}
		info, err := entries[0].Info()
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("replacement symlink was followed: %v, %v", info, err)
		}
		if source.calls.Load() != 0 {
			t.Fatal("entry Info fetched target blocks")
		}
	}
}

// Some base file systems expose Lstat only through DirEntry.Info.
type inspectedEntry struct {
	fs.DirEntry
	info func() (fs.FileInfo, error)
}

func (entry inspectedEntry) Info() (fs.FileInfo, error) { return entry.info() }

func TestFSEntryInfoPreservesBaseChecks(t *testing.T) {
	for _, viaOpen := range []bool{false, true} {
		for _, mode := range []fs.FileMode{fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeDir} {
			baseFiles := fstest.MapFS{"dir/file": {Data: []byte("plain")}}
			info, err := fs.Stat(baseFiles, "dir/file")
			if err != nil {
				t.Fatal(err)
			}
			base := &entryFS{FS: baseFiles}
			var infoErr error
			base.entries = []fs.DirEntry{inspectedEntry{
				DirEntry: fs.FileInfoToDirEntry(info),
				info: func() (fs.FileInfo, error) {
					if infoErr != nil {
						return nil, infoErr
					}
					return baseFiles.Lstat("dir/file")
				},
			}}
			wrapped := newLinkFS(t, base, eris.BlockSet{})
			readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
			if viaOpen {
				dir, err := wrapped.Open("dir")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = dir.Close() }()
				readDir = func() ([]fs.DirEntry, error) { return dir.(fs.ReadDirFile).ReadDir(1) }
			}
			entries, err := readDir()
			if err != nil || len(entries) != 1 {
				t.Fatalf("ReadDir: %v, %v", entries, err)
			}
			base.opens = nil
			baseFiles["dir/file"] = &fstest.MapFile{Mode: mode, Data: []byte("../private")}
			got, err := entries[0].Info()
			if err != nil || got.Mode().Type() != mode {
				t.Errorf("mode %v, via Open %v: Info = %v, %v", mode, viaOpen, got, err)
			}
			infoErr = fs.ErrPermission
			if _, err := entries[0].Info(); !errors.Is(err, infoErr) {
				t.Errorf("Info lost base error: %v", err)
			}
			if len(base.opens) != 0 {
				t.Fatalf("Info opened a replacement or bypassed an error: %v", base.opens)
			}
		}
	}
}

func TestFSHugeDeclaredSizeDoesNotAllocateContent(t *testing.T) {
	// These levels can contain MaxInt64 bytes. Size checks alone must not
	// cause an allocation or fetch content before the caller reads it.
	for _, capability := range []eris.Capability{
		{BlockSize: eris.BlockSize1KiB, Level: 14},
		{BlockSize: eris.BlockSize32KiB, Level: 6},
	} {
		encoded, err := (link.Link{Capability: capability, Size: math.MaxInt64}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		source := &countingSource{err: eris.ErrMissingBlock}
		wrapped := newLinkFS(t, fstest.MapFS{"huge": {Data: encoded}}, source)
		info, err := wrapped.Stat("huge")
		if err != nil || info.Size() != math.MaxInt64 || source.calls.Load() != 0 {
			t.Fatalf("huge file metadata: %v, %v, fetches %d", info, err, source.calls.Load())
		}
		data, err := wrapped.ReadFile("huge")
		if len(data) != 0 || !errors.Is(err, eris.ErrMissingBlock) || source.calls.Load() != 1 {
			t.Fatalf("huge file read: %d bytes, %v, fetches %d", len(data), err, source.calls.Load())
		}
	}
}

func TestFSValidatesUnusedMetadata(t *testing.T) {
	encoded, _ := encodeFSLink(t, nil)
	prefix := encoded[:link.HeaderSize+eris.CapabilitySize]
	for _, test := range []struct {
		name string
		body []byte
	}{
		{"invalid MIME", []byte{0, 0x61, 0xff, 0xa0}},
		{"invalid key", []byte{0, 0x60, 0xa1, 0x61, 0xff, 0xf6}},
		{"duplicate keys", []byte{0, 0x60, 0xa2, 0x61, 'x', 0xf6, 0x7f, 0x61, 'x', 0xff, 0xf6}},
		{"missing value", []byte{0, 0x60, 0xbf, 0x60, 0xff}},
		{"invalid nested text", []byte{0, 0x60, 0xa1, 0x60, 0x81, 0x61, 0xff}},
		{"trailing data", []byte{0, 0x60, 0xa0, 0}},
		{"deep value", append(append([]byte{0, 0x60, 0xa1, 0x60}, bytes.Repeat([]byte{0xc0}, 257)...), 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append(bytes.Clone(prefix), test.body...)
			for _, seekable := range []bool{false, true} {
				var base fs.FS = fstest.MapFS{"file": {Data: data}}
				if !seekable {
					base = plainFS{base}
				}
				source := &countingSource{err: errors.New("unexpected block access")}
				files := newLinkFS(t, base, source)
				file, err := files.Open("file")
				if file != nil {
					_ = file.Close()
					t.Fatal("invalid metadata returned a file")
				}
				if !errors.Is(err, link.ErrInvalidLink) || source.calls.Load() != 0 {
					t.Fatalf("seekable %v: error %v, block reads %d", seekable, err, source.calls.Load())
				}
			}
		})
	}
}
