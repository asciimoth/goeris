package fs

import (
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
)

const directoryType EntryType = 255

// FS exposes an ERIS-FS image through the standard library file-system
// interfaces. Nested ERIS-FS indexes are loaded when a caller enters them.
// Its methods are safe for concurrent use when the block source supports
// concurrent calls. Each Open call returns a separate file position. Calls on
// the same open directory must be serialized by the caller.
type FS struct {
	source     eris.BlockSource
	root       *node
	prefix     string
	indexLimit int64
}

type node struct {
	mu          sync.Mutex
	source      eris.BlockSource
	name        string
	kind        EntryType
	capability  eris.Capability
	target      string
	children    map[string]*node
	loaded      bool
	indexLimit  int64
	size        int64
	sizeKnown   bool
	compression Compression
}

// OpenFS decodes the top-level index and returns a read-only virtual file
// system. It does not read the content of regular files.
func OpenFS(capability eris.Capability, source eris.BlockSource) (*FS, error) {
	return OpenFSWithLimit(capability, source, DefaultMaxIndexSize)
}

// OpenFSWithLimit opens an image whose top-level and nested decoded indexes
// are each at most limit bytes. A non-positive limit is invalid.
func OpenFSWithLimit(capability eris.Capability, source eris.BlockSource, limit int64) (*FS, error) {
	index, err := DecodeIndexWithLimit(capability, source, limit)
	if err != nil {
		return nil, err
	}
	root := buildTree(index, source, ".", limit)
	return &FS{source: source, root: root, prefix: ".", indexLimit: limit}, nil
}

// buildTree consumes an index that DecodeIndexWithLimit has already validated.
func buildTree(index Index, source eris.BlockSource, rootName string, indexLimit int64) *node {
	root := &node{
		name: rootName, kind: directoryType, source: source, children: make(map[string]*node),
		loaded: true, indexLimit: indexLimit,
	}
	for _, entry := range index.Entries {
		parent := root
		for _, component := range entry.Path[:len(entry.Path)-1] {
			child := parent.children[component]
			if child == nil {
				child = &node{
					name: component, kind: directoryType, source: source, children: make(map[string]*node),
					loaded: true, indexLimit: indexLimit,
				}
				parent.children[component] = child
			}
			parent = child
		}
		name := entry.Path[len(entry.Path)-1]
		child := &node{
			name: name, kind: entry.Type, source: source, capability: entry.Capability,
			target: entry.Target, loaded: entry.Type != Subdirectory, indexLimit: indexLimit,
			size: entry.Size, sizeKnown: entry.Size >= 0, compression: entry.Compression,
		}
		if entry.Type == Subdirectory {
			child.children = make(map[string]*node)
		}
		parent.children[name] = child
	}
	return root
}

func (item *node) load() error {
	if item.kind != Subdirectory {
		return nil
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.loaded {
		return nil
	}
	index, err := DecodeIndexWithLimit(item.capability, item.source, item.indexLimit)
	if err != nil {
		return err
	}
	root := buildTree(index, item.source, item.name, item.indexLimit)
	item.children = root.children
	item.loaded = true
	return nil
}

func (fileSystem *FS) find(name string, followFinal bool) (*node, error) {
	if fileSystem == nil || fileSystem.root == nil || !stdfs.ValidPath(name) {
		return nil, stdfs.ErrInvalid
	}
	currentPath := name
	if fileSystem.prefix != "." {
		currentPath = path.Join(fileSystem.prefix, name)
		// Sub has already selected a directory. Its "." names that
		// directory, even when the prefix ends in a symlink. Lstat and
		// ReadLink must not expose the prefix's final link as the new root.
		if name == "." {
			followFinal = true
		}
	}
	if currentPath == "." {
		return fileSystem.root, nil
	}
	// Keep one unprocessed string per followed link. Splitting and copying
	// all target components can multiply memory use when a link points to
	// itself followed by a long suffix, even with the 40-link limit.
	pending := []string{currentPath}
	ancestors := []*node{fileSystem.root}
	links := 0
	for len(pending) > 0 {
		current := ancestors[len(ancestors)-1]
		if !current.isDir() {
			return nil, stdfs.ErrNotExist
		}
		last := len(pending) - 1
		component, rest, more := strings.Cut(pending[last], "/")
		if more {
			pending[last] = rest
		} else {
			pending = pending[:last]
		}
		switch component {
		case "", ".":
			continue
		case "..":
			if len(ancestors) == 1 {
				return nil, stdfs.ErrNotExist
			}
			ancestors = ancestors[:len(ancestors)-1]
			continue
		}
		if err := current.load(); err != nil {
			return nil, err
		}
		current.mu.Lock()
		next := current.children[component]
		current.mu.Unlock()
		if next == nil {
			return nil, stdfs.ErrNotExist
		}
		if next.kind != Symlink || (len(pending) == 0 && !followFinal) {
			ancestors = append(ancestors, next)
			continue
		}
		if links == 40 {
			return nil, stdfs.ErrInvalid
		}
		links++
		if next.target == "" || strings.HasPrefix(next.target, "/") {
			return nil, stdfs.ErrNotExist
		}
		// Resolve each target component before processing its parent segments.
		// Cleaning the text first can select a different file through a link.
		pending = append(pending, next.target)
	}
	return ancestors[len(ancestors)-1], nil
}

// Open implements io/fs.FS. Links are resolved only within this image. An
// absolute link or a relative link that escapes the image is not followed.
func (fileSystem *FS) Open(path string) (stdfs.File, error) {
	item, err := fileSystem.find(path, true)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	if item.isDir() {
		if err = item.load(); err != nil {
			return nil, pathError("open", path, err)
		}
		return &directoryFile{node: item}, nil
	}
	if item.compression == Lzip {
		file, err := openLzipFile(item)
		if err != nil {
			return nil, pathError("open", path, err)
		}
		return file, nil
	}
	return fileSystem.openRegular(item, path)
}

// ReadFile implements io/fs.ReadFileFS. It grows the buffer as content is read,
// without allocating from the untrusted size in the index. To limit memory for
// large content, use Open and read through an io.LimitReader instead.
func (fileSystem *FS) ReadFile(name string) ([]byte, error) {
	file, err := fileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return data, pathError("read", name, err)
	}
	return data, nil
}

func (fileSystem *FS) openRegular(item *node, path string) (ReadSeekCloser, error) {
	var reader *erisreader.Reader
	var err error
	item.mu.Lock()
	size, known := item.size, item.sizeKnown
	item.mu.Unlock()
	if known {
		reader, err = erisreader.NewReaderWithSize(item.capability, fileSystem.source, size)
	} else {
		reader, err = erisreader.NewReader(item.capability, fileSystem.source)
	}
	if err != nil {
		return nil, pathError("open", path, err)
	}
	item.setSize(reader.Size())
	return &regularFile{node: item, Reader: reader}, nil
}

// OpenFile opens a regular file and returns its random-access interface. It
// follows links within this image. A directory returns fs.ErrInvalid and a
// compressed file returns ErrNotSeekable; use Open to read compressed files.
func (fileSystem *FS) OpenFile(name string) (ReadSeekCloser, error) {
	item, err := fileSystem.find(name, true)
	if err != nil {
		return nil, pathError("open", name, err)
	}
	if item.isDir() {
		return nil, pathError("open", name, stdfs.ErrInvalid)
	}
	if item.compression == Lzip {
		return nil, pathError("open", name, ErrNotSeekable)
	}
	return fileSystem.openRegular(item, name)
}

// Stat implements io/fs.StatFS. It follows links within this image.
func (fileSystem *FS) Stat(path string) (stdfs.FileInfo, error) {
	item, err := fileSystem.find(path, true)
	if err != nil {
		return nil, pathError("stat", path, err)
	}
	info, err := item.info()
	if err != nil {
		return nil, pathError("stat", path, err)
	}
	return info, nil
}

// Lstat implements io/fs.ReadLinkFS without following the final link.
func (fileSystem *FS) Lstat(path string) (stdfs.FileInfo, error) {
	item, err := fileSystem.find(path, false)
	if err != nil {
		return nil, pathError("lstat", path, err)
	}
	info, err := item.info()
	if err != nil {
		return nil, pathError("lstat", path, err)
	}
	return info, nil
}

// ReadDir implements io/fs.ReadDirFS.
func (fileSystem *FS) ReadDir(path string) ([]stdfs.DirEntry, error) {
	item, err := fileSystem.find(path, true)
	if err != nil {
		return nil, pathError("readdir", path, err)
	}
	if err = item.load(); err != nil {
		return nil, pathError("readdir", path, err)
	}
	if !item.isDir() {
		return nil, pathError("readdir", path, stdfs.ErrInvalid)
	}
	return item.dirEntries(), nil
}

// ReadLink implements io/fs.ReadLinkFS. The returned target is not cleaned or
// restricted because ERIS-FS permits targets outside the image.
func (fileSystem *FS) ReadLink(path string) (string, error) {
	item, err := fileSystem.find(path, false)
	if err != nil {
		return "", pathError("readlink", path, err)
	}
	if item.kind != Symlink {
		return "", pathError("readlink", path, stdfs.ErrInvalid)
	}
	return item.target, nil
}

// Sub implements io/fs.SubFS. Links in the result can resolve to paths outside
// the selected directory but remain inside the same image.
func (fileSystem *FS) Sub(name string) (stdfs.FS, error) {
	item, err := fileSystem.find(name, true)
	if err != nil {
		return nil, pathError("sub", name, err)
	}
	if err = item.load(); err != nil {
		return nil, pathError("sub", name, err)
	}
	if !item.isDir() {
		return nil, pathError("sub", name, stdfs.ErrInvalid)
	}
	prefix := name
	if fileSystem.prefix != "." {
		prefix = path.Join(fileSystem.prefix, name)
	}
	return &FS{
		source: fileSystem.source, root: fileSystem.root, prefix: prefix,
		indexLimit: fileSystem.indexLimit,
	}, nil
}

func (node *node) dirEntries() []stdfs.DirEntry {
	node.mu.Lock()
	defer node.mu.Unlock()
	entries := make([]stdfs.DirEntry, 0, len(node.children))
	for _, child := range node.children {
		entries = append(entries, child)
	}
	slices.SortFunc(entries, func(a, b stdfs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries
}

func (node *node) Name() string { return node.name }

func (node *node) Size() int64 {
	if node.kind == Symlink {
		return int64(len(node.target))
	}
	if node.kind != Regular && node.kind != Executable {
		return 0
	}
	size, err := node.contentSize()
	if err != nil {
		return -1
	}
	return size
}

func (node *node) contentSize() (int64, error) {
	node.mu.Lock()
	if node.sizeKnown {
		size := node.size
		node.mu.Unlock()
		return size, nil
	}
	node.mu.Unlock()
	reader, err := erisreader.NewReader(node.capability, node.source)
	if err != nil {
		return 0, err
	}
	size := reader.Size()
	node.setSize(size)
	return size, nil
}

func (node *node) setSize(size int64) {
	node.mu.Lock()
	node.size = size
	node.sizeKnown = true
	node.mu.Unlock()
}

func (node *node) info() (stdfs.FileInfo, error) {
	if node.kind == Regular || node.kind == Executable {
		if _, err := node.contentSize(); err != nil {
			return nil, err
		}
	}
	return node, nil
}

func (node *node) Mode() stdfs.FileMode {
	switch node.kind {
	case directoryType, Subdirectory:
		return stdfs.ModeDir | 0o555
	case Executable:
		return 0o555
	case Symlink:
		return stdfs.ModeSymlink | 0o555
	default:
		return 0o444
	}
}

func (node *node) Type() stdfs.FileMode          { return node.Mode().Type() }
func (node *node) ModTime() time.Time            { return time.Time{} }
func (node *node) IsDir() bool                   { return node.isDir() }
func (node *node) Sys() any                      { return nil }
func (node *node) Info() (stdfs.FileInfo, error) { return node.info() }
func (node *node) isDir() bool                   { return node.kind == directoryType || node.kind == Subdirectory }

type regularFile struct {
	node *node
	*erisreader.Reader
}

func (file *regularFile) Stat() (stdfs.FileInfo, error) {
	if file.IsClosed() {
		return nil, ErrClosed
	}
	return file.node.info()
}

type directoryFile struct {
	node    *node
	entries []stdfs.DirEntry
	offset  int
	closed  bool
}

func (file *directoryFile) Stat() (stdfs.FileInfo, error) {
	if file.closed {
		return nil, ErrClosed
	}
	return file.node.info()
}

func (file *directoryFile) Read([]byte) (int, error) {
	if file.closed {
		return 0, ErrClosed
	}
	return 0, stdfs.ErrInvalid
}

func (file *directoryFile) ReadDir(count int) ([]stdfs.DirEntry, error) {
	if file.closed {
		return nil, ErrClosed
	}
	if file.entries == nil {
		file.entries = file.node.dirEntries()
	}
	entries := file.entries
	if file.offset == len(entries) && count > 0 {
		return nil, io.EOF
	}
	end := len(entries)
	if count > 0 {
		end = file.offset + min(count, len(entries)-file.offset)
	}
	// Do not let append on the returned slice overwrite unread entries.
	result := entries[file.offset:end:end]
	file.offset = end
	return result, nil
}

func (file *directoryFile) Close() error {
	file.closed = true
	file.entries = nil
	return nil
}

func pathError(operation, path string, err error) error {
	return &stdfs.PathError{Op: operation, Path: path, Err: err}
}

var (
	_ FileSystem        = (*FS)(nil)
	_ stdfs.FS          = (*FS)(nil)
	_ stdfs.ReadFileFS  = (*FS)(nil)
	_ stdfs.ReadDirFS   = (*FS)(nil)
	_ stdfs.ReadLinkFS  = (*FS)(nil)
	_ stdfs.StatFS      = (*FS)(nil)
	_ stdfs.SubFS       = (*FS)(nil)
	_ stdfs.ReadDirFile = (*directoryFile)(nil)
)
