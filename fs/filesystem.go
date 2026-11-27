package fs

import (
	"bytes"
	"context"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/imagepath"
)

const directoryType EntryType = 255

// FS provides context-aware access to an ERIS-FS image. Use NewIOFS to
// use the standard library file-system interfaces. Nested ERIS-FS indexes are
// loaded when a caller enters them. Each block read receives a copy of the
// options supplied at construction, including reads through Sub.
// Its methods are safe for concurrent use when the capability store supports
// concurrent calls. Each Open call returns a separate file position. Calls on
// the same open directory must be serialized by the caller.
type FS struct {
	root    *node
	prefix  string
	subRoot *node // The directory exposed to consumers of ReadLink.
}

type node struct {
	// block is verified encrypted content for a Mount raw-block file only.
	// Image nodes leave it nil and read through their capability instead.
	block []byte
	// listedImage supplies root metadata for a virtual fs/C directory entry.
	listedImage *Mount
	mu          sync.Mutex
	store       *Store
	name        string
	capability  eris.Capability
	target      string
	// A nested index has nil children until load succeeds. After that the
	// map is immutable, including when the index is empty.
	children    map[string]*node
	ordered     []*node // Set on first listing, after children are immutable.
	loading     chan struct{}
	indexLimit  int64
	size        int64
	kind        EntryType
	sizeKnown   bool
	compression Compression
}

// OpenFS decodes the top-level index and returns a read-only virtual file
// system. It uses ctx only for this initial read and does not retain it.
// Later operations use their own contexts. It does not read regular files.
//
// For repeated operations, use [Store.OpenFS].
func OpenFS(ctx context.Context, capability eris.Capability, content eris.CapabilityStore, options eris.BlockOptions) (*FS, error) {
	return OpenFSWithLimit(ctx, capability, content, DefaultMaxIndexSize, options)
}

// OpenFSWithLimit opens an image whose top-level and nested decoded indexes
// are each at most limit bytes. A non-positive limit is invalid.
// It keeps a copy of options for all later block reads.
//
// For repeated operations, use [Store.OpenFSWithLimit].
func OpenFSWithLimit(ctx context.Context, capability eris.Capability, content eris.CapabilityStore, limit int64, options eris.BlockOptions) (*FS, error) {
	return NewStore(content, options).openFS(ctx, capability, limit)
}

// OpenFS reads the root index. Later operations use their own contexts.
func (s *Store) OpenFS(ctx context.Context, capability eris.Capability) (FileSystem, error) {
	return s.OpenFSWithLimit(ctx, capability, DefaultMaxIndexSize)
}

// OpenFSWithLimit applies limit to the root index and each nested index.
func (s *Store) OpenFSWithLimit(ctx context.Context, capability eris.Capability, limit int64) (FileSystem, error) {
	image, err := s.openFS(ctx, capability, limit)
	if err != nil {
		return nil, err
	}
	return image, nil
}

func (s *Store) openFS(ctx context.Context, capability eris.Capability, limit int64) (*FS, error) {
	index, err := s.DecodeIndexWithLimit(ctx, capability, limit)
	if err != nil {
		return nil, err
	}
	root := buildTree(index, s, ".", limit)
	root.capability = capability
	return &FS{root: root, prefix: "."}, nil
}

// buildTree consumes an index that DecodeIndexWithLimit has already validated.
func buildTree(index Index, s *Store, rootName string, indexLimit int64) *node {
	// In a v2 index, single-component paths come first in wire order.
	// They give a lower bound even for legacy indexes with another order.
	rootEntries := 0
	for rootEntries < len(index.Entries) && len(index.Entries[rootEntries].Path) == 1 {
		rootEntries++
	}
	root := &node{
		name: rootName, kind: directoryType, store: s, children: make(map[string]*node, rootEntries),
		indexLimit: indexLimit,
	}
	var leaves []node
	for i, entry := range index.Entries {
		parent := root
		for _, component := range entry.Path[:len(entry.Path)-1] {
			child := parent.children[component]
			if child == nil {
				child = &node{
					name: component, kind: directoryType, store: s, children: make(map[string]*node),
					indexLimit: indexLimit,
				}
				parent.children[component] = child
			}
			parent = child
		}
		name := entry.Path[len(entry.Path)-1]
		var child *node
		if entry.Type == Subdirectory {
			// A loaded image can retain a whole tree. Give its root an
			// independent allocation so a sibling's metadata cannot retain it.
			child = new(node)
		} else {
			if len(leaves) == 0 {
				leaves = make([]node, min(8, len(index.Entries)-i))
			}
			child = &leaves[0]
			leaves = leaves[1:]
		}
		*child = node{
			name: name, kind: entry.Type, store: s, capability: entry.Capability,
			target: entry.Target, indexLimit: indexLimit,
			size: entry.Size, sizeKnown: entry.Size >= 0, compression: entry.Compression,
		}
		parent.children[name] = child
	}
	return root
}

func (item *node) load(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.kind != Subdirectory {
		return nil
	}
	for {
		item.mu.Lock()
		if item.children != nil {
			item.mu.Unlock()
			return ctx.Err()
		}
		if item.loading == nil {
			item.loading = make(chan struct{})
			item.mu.Unlock()
			break
		}
		loading := item.loading
		item.mu.Unlock()
		// Each caller has its own context. Waiting for a shared index must
		// not require the caller that started the load to finish first.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-loading:
		}
	}
	defer func() {
		item.mu.Lock()
		close(item.loading)
		item.loading = nil
		item.mu.Unlock()
	}()
	index, err := item.store.DecodeIndexWithLimit(ctx, item.capability, item.indexLimit)
	if err != nil {
		return err
	}
	root := buildTree(index, item.store, item.name, item.indexLimit)
	item.mu.Lock()
	item.children = root.children
	item.mu.Unlock()
	return nil
}

func (fileSystem *FS) find(ctx context.Context, name string, followFinal bool) (*node, error) {
	mode := imagepath.KeepFinal
	if followFinal {
		mode = imagepath.Follow
	}
	item, _, err := fileSystem.lookup(ctx, name, mode)
	return item, err
}

// lookup resolves a path from the root of the selected image or view.
func (fileSystem *FS) lookup(ctx context.Context, name string, mode imagepath.Mode) (*node, string, error) {
	if ctx == nil {
		return nil, "", stdfs.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if fileSystem == nil || fileSystem.root == nil || !stdfs.ValidPath(name) {
		return nil, "", stdfs.ErrInvalid
	}
	currentPath := name
	if fileSystem.prefix != "." {
		currentPath = path.Join(fileSystem.prefix, name)
	}
	return imagepath.Resolve(fileSystem.root, currentPath, mode, func(dir *node, name string) (*node, imagepath.Kind, string, error) {
		if err := dir.load(ctx); err != nil {
			return nil, 0, "", err
		}
		next := dir.children[name]
		if next == nil {
			return nil, 0, "", imagepath.ErrMissingEntry
		}
		switch next.kind {
		case directoryType:
			return next, imagepath.Directory, "", nil
		case Subdirectory:
			return next, imagepath.ImageRoot, "", nil
		case Symlink:
			return next, imagepath.Link, next.target, nil
		default:
			return next, imagepath.File, "", nil
		}
	})
}

// Open opens a file. The returned file uses ctx for all later block reads.
// Links are resolved only within this image. An absolute link or a relative
// link that escapes the image is not followed.
func (fileSystem *FS) Open(ctx context.Context, path string) (stdfs.File, error) {
	item, err := fileSystem.find(ctx, path, true)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	if item.isDir() {
		if err = item.load(ctx); err != nil {
			return nil, pathError("open", path, err)
		}
		return &directoryFile{ctx: ctx, node: item}, nil
	}
	if item.compression == Lzip {
		file, err := openLzipFile(ctx, item)
		if err != nil {
			return nil, pathError("open", path, err)
		}
		return file, nil
	}
	return item.openRegular(ctx, path)
}

// ReadFile reads the complete file. It uses a bounded size hint for the initial
// buffer. To limit memory for large content, use Open with an io.LimitReader.
func (fileSystem *FS) ReadFile(ctx context.Context, name string) ([]byte, error) {
	file, err := fileSystem.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := readImageFile(file)
	if err != nil {
		return data, pathError("read", name, err)
	}
	return data, nil
}

func readImageFile(file stdfs.File) ([]byte, error) {
	var size int64
	switch f := file.(type) {
	case *regularFile:
		size = f.node.Size()
	case *lzipFile:
		size = f.node.size
	}
	// The size can come from an untrusted index. Limit the initial allocation
	// and keep room for the read that verifies EOF or reports a size mismatch.
	if size < bytes.MinRead || size > 32<<10 {
		return io.ReadAll(file)
	}
	buffer := bytes.NewBuffer(make([]byte, 0, int(size)+bytes.MinRead))
	_, err := buffer.ReadFrom(file)
	return buffer.Bytes(), err
}

func (item *node) openRegular(ctx context.Context, path string) (ReadSeekCloser, error) {
	if item.block != nil {
		return newMountBlockFile(ctx, item), nil
	}
	reader, err := item.openRegularReader(ctx)
	if err != nil {
		return nil, pathError("open", path, err)
	}
	return &regularFile{ctx: ctx, node: item, CapabilityReader: reader}, nil
}

func (item *node) openRegularReader(ctx context.Context) (eris.CapabilityReader, error) {
	var reader eris.CapabilityReader
	var err error
	if size := item.Size(); size >= 0 {
		reader, err = item.store.content.NewReaderWithSize(ctx, item.capability, size, item.store.options)
	} else {
		reader, err = item.store.content.NewReader(ctx, item.capability, item.store.options)
	}
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, stdfs.ErrInvalid
	}
	if err := item.recordSize(reader.Size()); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

// OpenFile opens a regular file and returns its random-access interface. It
// follows links within this image. A directory returns fs.ErrInvalid and a
// compressed file returns ErrNotSeekable; use Open to read compressed files.
// The returned file uses ctx for all later block reads.
func (fileSystem *FS) OpenFile(ctx context.Context, name string) (ReadSeekCloser, error) {
	item, err := fileSystem.find(ctx, name, true)
	if err != nil {
		return nil, pathError("open", name, err)
	}
	if item.isDir() {
		return nil, pathError("open", name, stdfs.ErrInvalid)
	}
	if item.compression == Lzip {
		return nil, pathError("open", name, ErrNotSeekable)
	}
	return item.openRegular(ctx, name)
}

// Stat returns file metadata. It follows links within this image.
func (fileSystem *FS) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	item, err := fileSystem.find(ctx, path, true)
	if err != nil {
		return nil, pathError("stat", path, err)
	}
	info, err := item.info(ctx)
	if err != nil {
		return nil, pathError("stat", path, err)
	}
	return info, nil
}

// Lstat returns metadata without following the final link.
func (fileSystem *FS) Lstat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	item, err := fileSystem.find(ctx, path, false)
	if err != nil {
		return nil, pathError("lstat", path, err)
	}
	info, err := item.info(ctx)
	if err != nil {
		return nil, pathError("lstat", path, err)
	}
	return info, nil
}

// ReadDir returns directory entries. Entry.Info uses ctx for block reads.
func (fileSystem *FS) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	item, err := fileSystem.find(ctx, path, true)
	if err != nil {
		return nil, pathError("readdir", path, err)
	}
	if err = item.load(ctx); err != nil {
		return nil, pathError("readdir", path, err)
	}
	if !item.isDir() {
		return nil, pathError("readdir", path, stdfs.ErrInvalid)
	}
	return bindDirEntries(ctx, item.sortedChildren()), nil
}

// ReadLink reads a symbolic link only when following it cannot leave this
// image and the exposed view. It preserves the target text. A dangling link
// can still be read if all of its parent steps have been resolved.
// A cycle or a chain past the expansion limit returns fs.ErrInvalid because
// safety cannot be proved. An escaping or absolute link returns fs.ErrNotExist,
// including when a client could escape by cleaning the target before lookup.
func (fileSystem *FS) ReadLink(ctx context.Context, path string) (string, error) {
	if fileSystem != nil && fileSystem.subRoot != nil {
		// A native adapter can export a Sub view as its own mount.
		fileSystem = &FS{root: fileSystem.subRoot, prefix: "."}
	}
	_, target, err := fileSystem.lookup(ctx, path, imagepath.Export)
	if err == nil {
		err = imagepath.CheckNative(path)
	}
	if err != nil {
		return "", pathError("readlink", path, err)
	}
	return target, nil
}

// Sub selects a directory. Links in the result can resolve to paths outside
// the selected directory but remain inside the same image. ReadLink only
// exports targets that also stay inside the selected directory.
func (fileSystem *FS) Sub(ctx context.Context, name string) (FileSystem, error) {
	// Save the resolved path so later lookups start with a fresh link limit.
	item, prefix, err := fileSystem.lookup(ctx, name, imagepath.Canonical)
	if err != nil {
		return nil, pathError("sub", name, err)
	}
	if err = item.load(ctx); err != nil {
		return nil, pathError("sub", name, err)
	}
	if !item.isDir() {
		return nil, pathError("sub", name, stdfs.ErrInvalid)
	}
	return &FS{root: fileSystem.root, prefix: prefix, subRoot: item}, nil
}

func (dir *node) sortedChildren() []*node {
	dir.mu.Lock()
	defer dir.mu.Unlock()
	if dir.ordered == nil {
		children := make([]*node, 0, len(dir.children))
		for _, child := range dir.children {
			children = append(children, child)
		}
		slices.SortFunc(children, func(a, b *node) int { return strings.Compare(a.name, b.name) })
		dir.ordered = children
	}
	return dir.ordered
}

func bindDirEntries(ctx context.Context, children []*node) []stdfs.DirEntry {
	entries := make([]stdfs.DirEntry, len(children))
	// Keep allocation count low for large listings. Bound each batch so
	// retaining one DirEntry only keeps a small group of wrappers alive.
	for start := 0; start < len(children); {
		batch := make([]dirEntry, min(16, len(children)-start))
		for i := range batch {
			batch[i] = dirEntry{node: children[start+i], ctx: ctx}
			entries[start+i] = &batch[i]
		}
		start += len(batch)
	}
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
	node.mu.Lock()
	defer node.mu.Unlock()
	if !node.sizeKnown {
		return -1
	}
	return node.size
}

// Keep the index size, or the first size learned for a version 1 file.
func (node *node) recordSize(size int64) error {
	node.mu.Lock()
	defer node.mu.Unlock()
	if size < 0 {
		return eris.ErrInvalidSize
	}
	if node.sizeKnown && node.size != size {
		return ErrSizeMismatch
	}
	node.size = size
	node.sizeKnown = true
	return nil
}

func (node *node) info(ctx context.Context) (stdfs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if node.listedImage != nil {
		view, err := node.listedImage.openImage(ctx, node.capability)
		if err != nil {
			return nil, err
		}
		return namedImageRoot{FileSystem: view, name: node.name}.Stat(ctx, ".")
	}
	if node.Size() < 0 {
		reader, err := node.openRegularReader(ctx)
		if err != nil {
			return nil, err
		}
		_ = reader.Close()
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

func (node *node) Type() stdfs.FileMode { return node.Mode().Type() }
func (node *node) ModTime() time.Time   { return time.Time{} }
func (node *node) IsDir() bool          { return node.isDir() }
func (node *node) Sys() any             { return nil }

type dirEntry struct {
	*node
	ctx context.Context
}

func (entry dirEntry) Info() (stdfs.FileInfo, error) { return entry.info(entry.ctx) }
func (node *node) isDir() bool                       { return node.kind == directoryType || node.kind == Subdirectory }

type regularFile struct {
	ctx  context.Context
	node *node
	eris.CapabilityReader
	closed atomic.Bool
}

func (file *regularFile) Close() error {
	file.closed.Store(true)
	return file.CapabilityReader.Close()
}

func (file *regularFile) Seek(offset int64, whence int) (int64, error) {
	if file.IsClosed() {
		return 0, ErrClosed
	}
	if err := file.ctx.Err(); err != nil {
		return 0, err
	}
	return file.CapabilityReader.Seek(offset, whence)
}

// IsClosed and CacheSize preserve diagnostics previously provided by the
// embedded block reader. An opaque capability reader reports no cache count.
func (file *regularFile) IsClosed() bool { return file.closed.Load() }

func (file *regularFile) CacheSize() int {
	if reader, ok := file.CapabilityReader.(interface{ CacheSize() int }); ok {
		return reader.CacheSize()
	}
	return 0
}

func (file *regularFile) Stat() (stdfs.FileInfo, error) {
	if file.IsClosed() {
		return nil, ErrClosed
	}
	return file.node.info(file.ctx)
}

type directoryFile struct {
	ctx     context.Context
	node    *node
	entries []*node
	offset  int
	closed  bool
}

func (file *directoryFile) Stat() (stdfs.FileInfo, error) {
	if file.closed {
		return nil, ErrClosed
	}
	return file.node.info(file.ctx)
}

func (file *directoryFile) Read([]byte) (int, error) {
	if file.closed {
		return 0, ErrClosed
	}
	if err := file.ctx.Err(); err != nil {
		return 0, err
	}
	return 0, stdfs.ErrInvalid
}

func (file *directoryFile) ReadDir(count int) ([]stdfs.DirEntry, error) {
	if file.closed {
		return nil, ErrClosed
	}
	if err := file.ctx.Err(); err != nil {
		return nil, err
	}
	if file.entries == nil {
		file.entries = file.node.sortedChildren()
	}
	entries := file.entries
	if file.offset == len(entries) && count > 0 {
		return nil, io.EOF
	}
	end := len(entries)
	if count > 0 {
		end = file.offset + min(count, len(entries)-file.offset)
	}
	result := bindDirEntries(file.ctx, entries[file.offset:end])
	file.offset = end
	return result, nil
}

func (file *directoryFile) Close() error {
	file.closed = true
	file.entries = nil
	file.node = nil
	file.ctx = nil
	return nil
}

func pathError(operation, path string, err error) error {
	return &stdfs.PathError{Op: operation, Path: path, Err: err}
}

var (
	_ FileSystem        = (*FS)(nil)
	_ stdfs.ReadDirFile = (*directoryFile)(nil)
)
