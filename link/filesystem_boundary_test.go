package link_test

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
)

type failedReadLinkFS struct{ fs.FS }

func (failedReadLinkFS) Lstat(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
func (failedReadLinkFS) ReadLink(string) (string, error) {
	return "/private/target", fs.ErrPermission
}

func TestFSDiscardsFailedReadLinkTarget(t *testing.T) {
	wrapped := newLinkFS(t, failedReadLinkFS{fstest.MapFS{}}, eris.BlockSet{})
	if target, err := wrapped.ReadLink("public"); target != "" || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("failed ReadLink returned %q, %v", target, err)
	}
}

type failedStatFile struct {
	fs.File
	stats int
}

func (file *failedStatFile) Stat() (fs.FileInfo, error) {
	file.stats++
	info, err := file.File.Stat()
	if err == nil && file.stats > 1 {
		return info, fs.ErrPermission
	}
	return info, err
}

func TestFSDiscardsFailedMetadata(t *testing.T) {
	for _, name := range []string{"file", "dir"} {
		for _, method := range []string{"Stat", "Lstat"} {
			files := fstest.MapFS{"file": {}, "dir": {Mode: fs.ModeDir}}
			file, err := files.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			base := openFSFunc(func(string) (fs.File, error) {
				return &failedStatFile{File: file}, nil
			})
			wrapped := newLinkFS(t, base, eris.BlockSet{})
			stat := wrapped.Stat
			if method == "Lstat" {
				stat = wrapped.Lstat
			}
			if info, err := stat(name); info != nil || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("%s(%q): failed metadata returned %v, %v", method, name, info, err)
			}
		}
	}
}

type nilInfoFile struct{ fs.File }

func (nilInfoFile) Stat() (fs.FileInfo, error) { return nil, nil }

type nilInfoFS struct{ fs.FS }

func (nilInfoFS) Lstat(string) (fs.FileInfo, error) { return nil, nil }
func (nilInfoFS) ReadLink(string) (string, error)   { return "", fs.ErrInvalid }

func TestFSRejectsMissingBaseResults(t *testing.T) {
	files := fstest.MapFS{"file": {}, "dir": {Mode: fs.ModeDir}}
	for _, method := range []string{"Open", "ReadDir", "Stat", "Lstat", "File.Stat", "DirEntry.Info"} {
		t.Run(method, func(t *testing.T) {
			var base fs.FS
			var observed *observedFile
			switch method {
			case "Open", "ReadDir":
				base = openFSFunc(func(string) (fs.File, error) { return nil, nil })
			case "Stat":
				base = &metadataFS{}
			case "Lstat":
				base = nilInfoFS{files}
			case "File.Stat":
				observed, base = observedBase(t, nil)
				observed.File = nilInfoFile{observed.File}
			case "DirEntry.Info":
				info, err := files.Stat("file")
				if err != nil {
					t.Fatal(err)
				}
				base = &entryFS{FS: files, entries: []fs.DirEntry{inspectedEntry{
					DirEntry: fs.FileInfoToDirEntry(info),
					info:     func() (fs.FileInfo, error) { return nil, nil },
				}}}
			}
			wrapped := newLinkFS(t, base, eris.BlockSet{})
			var err error
			switch method {
			case "Open", "File.Stat":
				_, err = wrapped.Open("file")
			case "ReadDir":
				_, err = wrapped.ReadDir("dir")
			case "Stat":
				_, err = wrapped.Stat("file")
			case "Lstat":
				_, err = wrapped.Lstat("file")
			case "DirEntry.Info":
				entries, readErr := wrapped.ReadDir("dir")
				if readErr != nil {
					t.Fatal(readErr)
				}
				_, err = entries[0].Info()
			}
			if !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("missing base result: %v", err)
			}
			var pathErr *fs.PathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("missing path context: %v", err)
			}
			if observed != nil && observed.closes != 1 {
				t.Fatalf("base file closed %d times", observed.closes)
			}
		})
	}
}

// A base file can supply valid metadata during detection and fail later.
type changingInfoFile struct {
	fs.File
	stats int
}

func (file *changingInfoFile) Stat() (fs.FileInfo, error) {
	file.stats++
	if file.stats > 1 {
		return nil, nil
	}
	return file.File.Stat()
}

func TestFSRejectsMissingMetadataAfterOpen(t *testing.T) {
	for _, name := range []string{"file", "dir"} {
		for _, method := range []string{"Stat", "Lstat"} {
			t.Run(name+"/"+method, func(t *testing.T) {
				baseFiles := fstest.MapFS{"file": {}, "dir": {Mode: fs.ModeDir}}
				file, err := baseFiles.Open(name)
				if err != nil {
					t.Fatal(err)
				}
				observed := &observedFile{File: &changingInfoFile{File: file}, reader: file}
				base := openFSFunc(func(string) (fs.File, error) { return observed, nil })
				wrapped := newLinkFS(t, base, eris.BlockSet{})
				stat := wrapped.Stat
				if method == "Lstat" {
					stat = wrapped.Lstat
				}
				if info, err := stat(name); info != nil || !errors.Is(err, fs.ErrInvalid) {
					t.Fatalf("missing metadata: %v, %v", info, err)
				}
				if observed.closes != 1 {
					t.Fatalf("base file closed %d times", observed.closes)
				}
			})
		}
	}
}

func TestFSNonRegularEntryInfo(t *testing.T) {
	for _, mode := range []fs.FileMode{fs.ModeDir, fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeDevice, fs.ModeSocket} {
		for _, viaOpen := range []bool{false, true} {
			files := fstest.MapFS{"dir/entry": {Mode: mode}, "regular": {}}
			original, err := files.Lstat("dir/entry")
			if err != nil {
				t.Fatal(err)
			}
			info, infoErr := original, error(nil)
			base := &entryFS{FS: files, entries: []fs.DirEntry{inspectedEntry{
				DirEntry: fs.FileInfoToDirEntry(original),
				info:     func() (fs.FileInfo, error) { return info, infoErr },
			}}}
			wrapped := newLinkFS(t, base, eris.BlockSet{})
			readDir := func() ([]fs.DirEntry, error) { return wrapped.ReadDir("dir") }
			if viaOpen {
				file, err := wrapped.Open("dir")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				readDir = func() ([]fs.DirEntry, error) { return file.(fs.ReadDirFile).ReadDir(-1) }
			}
			entries, err := readDir()
			if err != nil || len(entries) != 1 {
				t.Fatalf("ReadDir: %v, %v", entries, err)
			}
			base.opens = nil
			if got, err := entries[0].Info(); got != original || err != nil {
				t.Fatalf("mode %v: original Info: %v, %v", mode, got, err)
			}
			info = nil
			for _, want := range []error{fs.ErrInvalid, fs.ErrPermission} {
				if want == fs.ErrPermission {
					infoErr = want
				}
				got, err := entries[0].Info()
				var pathErr *fs.PathError
				if got != nil || !errors.Is(err, want) || !errors.As(err, &pathErr) ||
					pathErr.Op != "lstat" || pathErr.Path != "dir/entry" {
					t.Errorf("mode %v, via Open %v: Info = %v, %v; want %v with path", mode, viaOpen, got, err, want)
				}
			}
			// A non-regular entry must not be opened even if Info now reports
			// a regular file. The base can change between these calls.
			info, infoErr = files.Stat("regular")
			if got, err := entries[0].Info(); got != info || err != nil {
				t.Fatalf("mode %v: changed Info: %v, %v", mode, got, err)
			}
			if len(base.opens) != 0 {
				t.Fatalf("mode %v: Info opened child files: %v", mode, base.opens)
			}
		}
	}
}
