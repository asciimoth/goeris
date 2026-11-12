package fs

import (
	"errors"
	"io"
	stdfs "io/fs"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
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
	// ErrInvalidIndex means that data is not a supported deterministic ERIS-FS index.
	ErrInvalidIndex = errors.New("eris-fs: invalid index")
	// ErrIndexTooLarge means that an index exceeds the configured decoded limit.
	ErrIndexTooLarge = erisreader.ErrTooLarge
	// ErrInvalidLimit means that a decoded index limit is not positive.
	ErrInvalidLimit = errors.New("eris-fs: invalid index limit")
	// ErrInvalidPath means that an entry path is not a valid relative path.
	ErrInvalidPath = errors.New("eris-fs: invalid path")
	// ErrPathConflict means that two entries cannot coexist in one tree.
	ErrPathConflict = errors.New("eris-fs: conflicting paths")
	// ErrInvalidEntry means that an entry type or value is not valid.
	ErrInvalidEntry = errors.New("eris-fs: invalid entry")
	// ErrUnknownSize means that a v1 file needs a size before it can be written as v2.
	ErrUnknownSize = errors.New("eris-fs: file size is unknown")
	// ErrSizeMismatch means that file content does not match its declared size.
	ErrSizeMismatch = erisreader.ErrSizeMismatch
	// ErrNotSeekable means that a compressed file does not support random access.
	ErrNotSeekable = errors.New("eris-fs: compressed file is not seekable")
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
	// Target is a symlink target with slash-separated components. The codec
	// stores these components as a CBOR array and preserves empty components.
	Target string
	// Size is the uncompressed file length. Zero denotes an empty file.
	// Parsed v1 files have Size -1. Set their size before encoding them as v2.
	// Symlinks and subdirectories must have Size zero.
	Size int64
	// Compression applies only to regular and executable files.
	Compression Compression
}

// Compression identifies how file content is stored.
type Compression uint8

const (
	// Uncompressed files support efficient random access.
	Uncompressed Compression = iota
	// Lzip files store a Lzip stream and are read sequentially.
	Lzip
)

// Index is an ERIS-FS index. Entry order is not significant;
// MarshalBinary always writes canonical CBOR map order.
type Index struct {
	Entries []Entry
}

// Validate checks all entries and path relationships. It permits Size -1 on
// uncompressed files read from v1; MarshalBinary requires known file sizes.
func (index Index) Validate() error {
	entries := slices.Clone(index.Entries)
	for _, entry := range index.Entries {
		if err := validateEntry(entry); err != nil {
			return err
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		for i := 0; i < min(len(a.Path), len(b.Path)); i++ {
			if order := strings.Compare(a.Path[i], b.Path[i]); order != 0 {
				return order
			}
		}
		return len(a.Path) - len(b.Path)
	})
	for i := 1; i < len(entries); i++ {
		previous := entries[i-1].Path
		current := entries[i].Path
		if len(previous) <= len(current) && slices.Equal(previous, current[:len(previous)]) {
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
		if !validPathComponent(component) {
			return ErrInvalidPath
		}
	}
	switch entry.Type {
	case Regular, Executable:
		if entry.Capability.Validate() != nil || entry.Target != "" {
			return ErrInvalidEntry
		}
		if entry.Size < -1 || entry.Compression > Lzip || (entry.Size == -1 && entry.Compression != Uncompressed) {
			return ErrInvalidEntry
		}
	case Subdirectory:
		if entry.Capability.Validate() != nil || entry.Target != "" || entry.Size != 0 || entry.Compression != Uncompressed {
			return ErrInvalidEntry
		}
	case Symlink:
		if entry.Capability != (eris.Capability{}) || !utf8.ValidString(entry.Target) || entry.Size != 0 || entry.Compression != Uncompressed {
			return ErrInvalidEntry
		}
	default:
		return ErrInvalidEntry
	}
	return nil
}

func validPathComponent(component string) bool {
	// ValidPath also rejects empty strings, parent segments, and invalid UTF-8.
	return component != "." && !strings.Contains(component, "/") && stdfs.ValidPath(component)
}

// ReadSeekCloser is an uncompressed file opened from an ERIS-FS image. ReadAt and
// Seek provide efficient random access to ERIS leaf blocks.
type ReadSeekCloser interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	stdfs.File
}
