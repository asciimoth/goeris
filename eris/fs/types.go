package fs

import (
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/eris-tools/eris"
)

// EntryType identifies an ERIS-FS index entry.
type EntryType uint8

const (
	// Regular is a non-executable regular file.
	Regular EntryType = iota
	// Executable is an executable regular file.
	Executable
	// Symlink is a symbolic link.
	Symlink
	// Subdirectory is a nested ERIS-FS image.
	Subdirectory
)

var (
	// ErrInvalidIndex means that data is not a canonical ERIS-FS version 1 index.
	ErrInvalidIndex = errors.New("eris-fs: invalid index")
	// ErrIndexTooLarge means that an index exceeds the configured decoded limit.
	ErrIndexTooLarge = errors.New("eris-fs: index is too large")
	// ErrInvalidLimit means that a decoded index limit is not positive.
	ErrInvalidLimit = errors.New("eris-fs: invalid index limit")
	// ErrInvalidPath means that an entry path is not a valid relative path.
	ErrInvalidPath = errors.New("eris-fs: invalid path")
	// ErrPathConflict means that two entries cannot coexist in one tree.
	ErrPathConflict = errors.New("eris-fs: conflicting paths")
	// ErrInvalidEntry means that an entry type or value is not valid.
	ErrInvalidEntry = errors.New("eris-fs: invalid entry")
	// ErrClosed is returned after an opened file is closed.
	ErrClosed = stdfs.ErrClosed
)

// FileSystem is the high-level, read-only ERIS-FS interface. It includes the
// standard library file-system operations, link metadata, and typed access to
// regular files that support seeking and random-access reads.
type FileSystem interface {
	stdfs.FS
	stdfs.ReadDirFS
	stdfs.ReadLinkFS
	stdfs.StatFS
	stdfs.SubFS

	Lstat(name string) (stdfs.FileInfo, error)
	OpenFile(name string) (ReadSeekCloser, error)
}

// Entry describes one item in an Index. Path contains separate path
// components, as required by the ERIS-FS wire format.
type Entry struct {
	Path       []string
	Type       EntryType
	Capability eris.Capability
	Target     string
}

// Index is an ERIS-FS version 1 index. Entry order is not significant;
// MarshalBinary always writes canonical CBOR map order.
type Index struct {
	Entries []Entry
}

// Validate checks all entries and path relationships.
func (index Index) Validate() error {
	entries := slices.Clone(index.Entries)
	for i := range entries {
		entries[i].Path = slices.Clone(entries[i].Path)
		if err := validateEntry(entries[i]); err != nil {
			return err
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return strings.Compare(strings.Join(a.Path, "/"), strings.Join(b.Path, "/"))
	})
	for i := 1; i < len(entries); i++ {
		previous := strings.Join(entries[i-1].Path, "/")
		current := strings.Join(entries[i].Path, "/")
		if current == previous {
			return ErrPathConflict
		}
		if strings.HasPrefix(current, previous+"/") {
			return ErrPathConflict
		}
	}
	return nil
}

func validateEntry(entry Entry) error {
	if len(entry.Path) == 0 {
		return ErrInvalidPath
	}
	for _, component := range entry.Path {
		if component == "" || component == "." || component == ".." || strings.Contains(component, "/") ||
			!stdfs.ValidPath(component) || !utf8.ValidString(component) {
			return ErrInvalidPath
		}
	}
	switch entry.Type {
	case Regular, Executable, Subdirectory:
		if entry.Capability.Validate() != nil || entry.Target != "" {
			return ErrInvalidEntry
		}
	case Symlink:
		if entry.Capability != (eris.Capability{}) || !utf8.ValidString(entry.Target) {
			return ErrInvalidEntry
		}
	default:
		return ErrInvalidEntry
	}
	return nil
}

// ReadSeekCloser is a regular file opened from an ERIS-FS image. ReadAt and
// Seek provide efficient random access to ERIS leaf blocks.
type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	stdfs.File
}
