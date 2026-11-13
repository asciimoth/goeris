package link

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
)

// DefaultMaxLinkSize limits each encoded link file to 1 MiB.
const DefaultMaxLinkSize int64 = 1 << 20

var (
	// ErrLinkTooLarge means that an encoded link exceeds the configured limit.
	ErrLinkTooLarge = errors.New("eris link: link file too large")
	// ErrSizeMismatch means that content does not match the size in its link.
	ErrSizeMismatch = erisreader.ErrSizeMismatch
)

// FS wraps a file system and resolves ERIS link files through a block source.
// It identifies regular files by their magic bytes, regardless of extension.
// Paths stay unchanged. Resolution stops after one link; MIME and attributes
// do not change how content is read. Use the base FS to read the original links.
//
// Open and metadata operations read base files but do not fetch linked content.
// Sizes are unverified until content is read. Reads check each visited tree
// path and the declared size at the file end. Unvisited blocks are not checked.
// Block reads use eris.NoTTL and require the BlockSource integrity
// contract. There is no context or cancellation parameter in io/fs.FS.
// Open and ReadFile return io.ErrNoProgress after 100 consecutive empty reads
// without an error. A blocked read still requires a timeout in the base FS.
// These methods reject invalid byte counts from base reads with fs.ErrInvalid.
// A nil file or nil metadata returned without an error also gives fs.ErrInvalid.
// An ordinary file's Seek must return offset zero when it is rewound.
//
// Errors from FS methods and resolved files include the requested path. Their
// text hides unknown backend error messages; errors.Is and errors.As retain
// access to the cause. Files returned directly from the base keep their errors.
// See the package documentation before exposing errors to untrusted callers.
//
// Concurrent FS calls require a base and source that support concurrent calls.
// Separate Open calls have separate file positions. Calls on an open base file
// or directory must be serialized unless the base permits concurrent use.
// Symbolic links follow the base FS policy; this wrapper adds no confinement.
type FS struct {
	base        fs.FS
	source      eris.BlockSource
	maxLinkSize int64
}

// NewFS wraps base with the default encoded link size limit.
func NewFS(base fs.FS, source eris.BlockSource) (*FS, error) {
	return NewFSWithLimit(base, source, DefaultMaxLinkSize)
}

// NewFSWithLimit wraps base with a positive encoded link size limit in bytes.
// It performs no I/O. A nil base, nil source, or non-positive limit is invalid.
// The limit applies to link metadata, not to decoded content or ordinary files.
func NewFSWithLimit(base fs.FS, source eris.BlockSource, limit int64) (*FS, error) {
	if base == nil || source == nil || limit <= 0 {
		return nil, fs.ErrInvalid
	}
	return &FS{base: base, source: source, maxLinkSize: limit}, nil
}

// Open opens a base file or the content referenced by an ERIS link file.
// Resolved files also implement io.Seeker and io.ReaderAt. Ordinary files keep
// their optional interfaces if they support seeking; otherwise only fs.File
// is guaranteed. A complete magic prefix followed by an invalid link is an
// error. Short prefixes are ordinary data. Link sizes above MaxInt64 are invalid.
func (fileSystem *FS) Open(name string) (fs.File, error) {
	if err := fileSystem.checkPath(name); err != nil {
		return nil, linkPathError("open", name, err)
	}
	file, err := fileSystem.base.Open(name)
	if err != nil {
		return nil, linkPathError("open", name, err)
	}
	if file == nil {
		return nil, linkPathError("open", name, fs.ErrInvalid)
	}
	result, err := fileSystem.open(file, name)
	if err != nil {
		_ = file.Close()
		return nil, linkPathError("open", name, err)
	}
	if _, resolved := result.(*resolvedFile); resolved {
		if err := file.Close(); err != nil {
			_ = result.Close()
			return nil, linkPathError("open", name, err)
		}
	}
	return result, nil
}

func (fileSystem *FS) open(file fs.File, name string) (fs.File, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fs.ErrInvalid
	}
	if info.IsDir() {
		if dir, ok := file.(fs.ReadDirFile); ok {
			return &linkDirectory{ReadDirFile: dir, fileSystem: fileSystem, name: name}, nil
		}
		return file, nil
	}
	if !info.Mode().IsRegular() {
		return file, nil
	}
	// ReadFull would hide errors returned with a complete prefix, and would
	// make a short EOF indistinguishable from a source ErrUnexpectedEOF.
	// Check the base reader before LimitReader adjusts its remaining count.
	baseReader := &progressReader{Reader: file}
	var remainder io.Reader = baseReader
	prefix := make([]byte, HeaderSize)
	read := 0
	for read < len(prefix) {
		n, readErr := baseReader.Read(prefix[read:])
		read += n
		if readErr != nil {
			if readErr != io.EOF {
				return nil, readErr
			}
			// EOF can arrive with the final bytes. Do not read the base
			// again when replaying this prefix or parsing a short link.
			remainder = bytes.NewReader(nil)
			break
		}
	}
	prefix = prefix[:read]
	if !HasHeader(prefix) {
		if seeker, ok := file.(io.Seeker); ok {
			position, err := seeker.Seek(0, io.SeekStart)
			if err != nil {
				return nil, err
			}
			if position != 0 {
				return nil, fs.ErrInvalid
			}
			return file, nil
		}
		return &prefixFile{File: file, reader: io.MultiReader(bytes.NewReader(prefix), remainder), name: name}, nil
	}
	if fileSystem.maxLinkSize < int64(HeaderSize) {
		return nil, ErrLinkTooLarge
	}
	// Read one extra byte to detect overflow without trusting FileInfo.Size.
	// Include the prefix in this buffer to avoid another copy of the body.
	encoded, err := io.ReadAll(io.MultiReader(bytes.NewReader(prefix),
		io.LimitReader(remainder, fileSystem.maxLinkSize-int64(HeaderSize)+1)))
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > fileSystem.maxLinkSize {
		return nil, ErrLinkTooLarge
	}
	link, err := parseLink(encoded)
	if err != nil {
		return nil, err
	}
	if link.size > math.MaxInt64 {
		return nil, ErrInvalidLink
	}
	reader, err := erisreader.NewReaderWithSize(link.capability, fileSystem.source, int64(link.size))
	if err != nil {
		return nil, err
	}
	return &resolvedFile{reader: reader, info: resolvedInfo{FileInfo: info, size: int64(link.size)}, name: name}, nil
}

// Stat returns base metadata with the declared content size for a link file.
// Name, mode, modification time, and Sys remain those of the base file.
// Detection requires read access to regular files, even for ordinary files.
// If base implements fs.StatFS, non-regular files are not opened.
func (fileSystem *FS) Stat(name string) (fs.FileInfo, error) {
	if err := fileSystem.checkPath(name); err != nil {
		return nil, linkPathError("stat", name, err)
	}
	if base, ok := fileSystem.base.(fs.StatFS); ok {
		info, err := base.Stat(name)
		if err != nil {
			return nil, linkPathError("stat", name, err)
		}
		if info == nil {
			return nil, linkPathError("stat", name, fs.ErrInvalid)
		}
		// Opening a pipe or device can block or have side effects. Only
		// regular files need to be read to detect ERIS links.
		if !info.Mode().IsRegular() {
			return info, nil
		}
	}
	file, err := fileSystem.Open(name)
	if err != nil {
		return nil, linkPathError("stat", name, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, linkPathError("stat", name, err)
	}
	if info == nil {
		return nil, linkPathError("stat", name, fs.ErrInvalid)
	}
	return info, nil
}

// Lstat reports a symbolic link itself when base implements fs.ReadLinkFS.
// Regular ERIS link files report their declared content size, as in Stat.
// If base does not implement fs.ReadLinkFS, Lstat falls back to Stat.
// As with Open, this does not protect against concurrent path changes.
func (fileSystem *FS) Lstat(name string) (fs.FileInfo, error) {
	if err := fileSystem.checkPath(name); err != nil {
		return nil, linkPathError("lstat", name, err)
	}
	if base, ok := fileSystem.base.(fs.ReadLinkFS); ok {
		info, err := base.Lstat(name)
		if err != nil {
			return nil, linkPathError("lstat", name, err)
		}
		if info == nil {
			return nil, linkPathError("lstat", name, fs.ErrInvalid)
		}
		if !info.Mode().IsRegular() {
			return info, nil
		}
	}
	info, err := fileSystem.Stat(name)
	return info, linkPathError("lstat", name, err)
}

// ReadLink returns the base symbolic link target without following it.
// The target is unchanged and can be absolute or contain parent components.
// It returns fs.ErrInvalid if base does not implement fs.ReadLinkFS.
func (fileSystem *FS) ReadLink(name string) (string, error) {
	if err := fileSystem.checkPath(name); err != nil {
		return "", linkPathError("readlink", name, err)
	}
	target, err := fs.ReadLink(fileSystem.base, name)
	if err != nil {
		return "", linkPathError("readlink", name, err)
	}
	return target, nil
}

// ReadFile reads resolved content without allocating from an untrusted size.
// It reads all content into memory. Use Open and io.LimitReader for a byte limit.
func (fileSystem *FS) ReadFile(name string) ([]byte, error) {
	file, err := fileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := readAllProgress(file)
	return data, linkPathError("read", name, err)
}

// ReadDir returns entries sorted by name. Entry.Info resolves regular link
// metadata on demand. Listing names alone does not open the child files.
// Entry.Info preserves base metadata errors and does not open entries that
// the base reports as symbolic links or other non-regular files.
// Entry.Info returns fs.ErrInvalid if the base returns nil metadata without
// an error, for any entry type.
// Nil entries or invalid entry names from the base FS return fs.ErrInvalid
// and no entries. Returned entry names and types are fixed at the time of the
// read, even if the base later changes its entries.
func (fileSystem *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := fileSystem.checkPath(name); err != nil {
		return nil, linkPathError("readdir", name, err)
	}
	entries, err := fileSystem.readDir(name)
	entries, wrapErr := fileSystem.wrapEntries(name, entries)
	if wrapErr != nil {
		err = errors.Join(err, wrapErr)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, linkPathError("readdir", name, err)
}

func (fileSystem *FS) readDir(name string) ([]fs.DirEntry, error) {
	if base, ok := fileSystem.base.(fs.ReadDirFS); ok {
		return base.ReadDir(name)
	}
	file, err := fileSystem.base.Open(name)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fs.ErrInvalid
	}
	defer func() { _ = file.Close() }()
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, fs.ErrInvalid
	}
	// fs.ReadDir sorts before returning. Validate the entries first so a
	// nil entry from the base cannot cause a panic during that sort.
	return dir.ReadDir(-1)
}

func (fileSystem *FS) checkPath(name string) error {
	if fileSystem == nil || fileSystem.base == nil || !fs.ValidPath(name) {
		return fs.ErrInvalid
	}
	return nil
}

func (fileSystem *FS) wrapEntries(name string, entries []fs.DirEntry) ([]fs.DirEntry, error) {
	result := make([]fs.DirEntry, len(entries))
	for i, entry := range entries {
		if entry == nil {
			return nil, fs.ErrInvalid
		}
		// Check each component before path.Join can clean away traversal.
		child := entry.Name()
		if !fs.ValidPath(child) || child == "." || strings.Contains(child, "/") {
			return nil, fs.ErrInvalid
		}
		result[i] = &linkDirEntry{
			DirEntry: entry, fileSystem: fileSystem, name: path.Join(name, child),
			child: child, mode: entry.Type(),
		}
	}
	return result, nil
}

func readAllProgress(reader io.Reader) ([]byte, error) {
	return io.ReadAll(&progressReader{Reader: reader})
}

type progressReader struct {
	io.Reader
	empty int
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	if n < 0 || n > len(buffer) {
		// Do not let a broken base reader cause slice bounds panics or
		// corrupt the byte count used to enforce the link size limit.
		return 0, errors.Join(fs.ErrInvalid, err)
	}
	if n == 0 && err == nil && len(buffer) > 0 {
		reader.empty++
		if reader.empty >= 100 {
			return 0, io.ErrNoProgress
		}
	} else {
		reader.empty = 0
	}
	return n, err
}

type prefixFile struct {
	fs.File
	reader io.Reader
	name   string
	closed bool
}

func (file *prefixFile) Read(buffer []byte) (int, error) {
	if file.closed {
		return 0, linkPathError("read", file.name, fs.ErrClosed)
	}
	n, err := file.reader.Read(buffer)
	return n, linkReadError("read", file.name, err)
}

func (file *prefixFile) Stat() (fs.FileInfo, error) {
	if file.closed {
		return nil, linkPathError("stat", file.name, fs.ErrClosed)
	}
	info, err := file.File.Stat()
	if err != nil {
		return nil, linkPathError("stat", file.name, err)
	}
	if info == nil {
		return nil, linkPathError("stat", file.name, fs.ErrInvalid)
	}
	return info, nil
}

func (file *prefixFile) Close() error {
	if file.closed {
		return linkPathError("close", file.name, fs.ErrClosed)
	}
	file.closed = true
	file.reader = nil
	return linkPathError("close", file.name, file.File.Close())
}

type resolvedInfo struct {
	fs.FileInfo
	size int64
}

func (info resolvedInfo) Size() int64 { return info.size }

type resolvedFile struct {
	reader *erisreader.Reader
	info   resolvedInfo
	name   string
}

func (file *resolvedFile) Stat() (fs.FileInfo, error) {
	if file.reader.IsClosed() {
		return nil, linkPathError("stat", file.name, fs.ErrClosed)
	}
	return file.info, nil
}

func (file *resolvedFile) Read(buffer []byte) (int, error) {
	n, err := file.reader.Read(buffer)
	return n, linkReadError("read", file.name, err)
}

func (file *resolvedFile) ReadAt(buffer []byte, offset int64) (int, error) {
	n, err := file.reader.ReadAt(buffer, offset)
	return n, linkReadError("readat", file.name, err)
}

func (file *resolvedFile) Seek(offset int64, whence int) (int64, error) {
	position, err := file.reader.Seek(offset, whence)
	return position, linkPathError("seek", file.name, err)
}

func (file *resolvedFile) Close() error { return file.reader.Close() }

type linkDirectory struct {
	fs.ReadDirFile
	fileSystem *FS
	name       string
}

func (file *linkDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := file.ReadDirFile.ReadDir(n)
	entries, wrapErr := file.fileSystem.wrapEntries(file.name, entries)
	if wrapErr != nil {
		err = errors.Join(err, wrapErr)
	}
	return entries, linkReadError("readdir", file.name, err)
}

type linkDirEntry struct {
	fs.DirEntry
	fileSystem *FS
	name       string
	child      string
	mode       fs.FileMode
}

// Keep the checked name and type stable if the base reuses or changes entries.
// WalkDir and other callers use these methods to construct paths.
func (entry *linkDirEntry) Name() string      { return entry.child }
func (entry *linkDirEntry) Type() fs.FileMode { return entry.mode }
func (entry *linkDirEntry) IsDir() bool       { return entry.mode.IsDir() }

func (entry *linkDirEntry) Info() (fs.FileInfo, error) {
	// DirEntry.Info can inspect a symbolic link even when the base FS does
	// not implement ReadLinkFS. Do not discard that check or its errors.
	info, err := entry.DirEntry.Info()
	if err != nil {
		return nil, linkPathError("lstat", entry.name, err)
	}
	if info == nil {
		return nil, linkPathError("lstat", entry.name, fs.ErrInvalid)
	}
	if !entry.mode.IsRegular() || !info.Mode().IsRegular() {
		return info, nil
	}
	// The entry may have been replaced since ReadDir. Retain the base's
	// ability to inspect a symbolic link without following its target.
	return entry.fileSystem.Lstat(entry.name)
}

func linkReadError(op, name string, err error) error {
	if err == io.EOF {
		return err
	}
	return linkPathError(op, name, err)
}

func linkPathError(op, name string, err error) error {
	if err == nil {
		return nil
	}
	if pathErr, ok := err.(*fs.PathError); ok && pathErr != nil {
		if cause, ok := pathErr.Err.(*linkOperationError); ok {
			return &fs.PathError{Op: op, Path: name, Err: cause}
		}
		if knownLinkError(pathErr.Err) {
			err = pathErr.Err
		}
	}
	// Keep fixed errors directly under PathError for legacy os.Is* checks.
	if !knownLinkError(err) {
		err = &linkOperationError{cause: err}
	}
	return &fs.PathError{Op: op, Path: name, Err: err}
}

var (
	_ fs.FS          = (*FS)(nil)
	_ fs.StatFS      = (*FS)(nil)
	_ fs.ReadFileFS  = (*FS)(nil)
	_ fs.ReadDirFS   = (*FS)(nil)
	_ fs.ReadLinkFS  = (*FS)(nil)
	_ fs.ReadDirFile = (*linkDirectory)(nil)
	_ fs.File        = (*resolvedFile)(nil)
	_ io.ReaderAt    = (*resolvedFile)(nil)
	_ io.Seeker      = (*resolvedFile)(nil)
)
