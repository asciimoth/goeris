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

// Keep large strings out of shared allocations. A reference to any part of
// a batch keeps the entire batch and the strings it points to alive.
const maxBatchedTextBytes = 1024

// FS provides context-aware access to an ERIS-FS image. Use NewIOFS to
// use the standard library file-system interfaces. Nested ERIS-FS indexes are
// loaded when a caller enters them. Each block read receives a copy of the
// options supplied at construction, including reads through Sub.
// Its methods are safe for concurrent use when the capability store supports
// concurrent calls. Each Open call returns a separate file position. Calls on
// the same open directory must be serialized by the caller.
type FS struct {
	root    *node  // The index that contains the selected directory.
	prefix  string // The path to the selected directory within that index.
	subRoot *node  // The directory exposed to consumers of ReadLink.
}

type node struct {
	// block is verified encrypted content for a Mount raw-block file only.
	// Image nodes leave it nil and read through their capability instead.
	block []byte
	// listedImage supplies root metadata for a virtual fs/C directory entry.
	listedImage *mountImageSource
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
	encodedSize atomic.Int64 // Bitwise complement of the file size. Zero means unknown.
	kind        EntryType
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
		if entry.Type == Subdirectory || len(name)+len(entry.Target) > maxBatchedTextBytes {
			// Isolate large strings and nested trees from sibling handles.
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
			compression: entry.Compression,
		}
		child.initSize(entry.Size)
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
	children, err := item.decodeChildren(ctx)
	if err != nil {
		return err
	}
	item.mu.Lock()
	item.children = children
	item.mu.Unlock()
	return nil
}

func (item *node) decodeChildren(ctx context.Context) (map[string]*node, error) {
	index, err := item.store.DecodeIndexWithLimit(ctx, item.capability, item.indexLimit)
	if err != nil {
		return nil, err
	}
	return buildTree(index, item.store, item.name, item.indexLimit).children, nil
}

func (item *node) loadChildren(ctx context.Context) (map[string]*node, error) {
	if err := item.load(ctx); err != nil {
		return nil, err
	}
	return item.children, nil
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
	return fileSystem.lookupWithChildren(ctx, name, mode, (*node).loadChildren)
}

func (fileSystem *FS) lookupWithChildren(ctx context.Context, name string, mode imagepath.Mode, loadChildren func(*node, context.Context) (map[string]*node, error)) (*node, string, error) {
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
		children, err := loadChildren(dir, ctx)
		if err != nil {
			return nil, 0, "", err
		}
		next := children[name]
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
		size = f.Size()
	case *lzipFile:
		size = f.info.size
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
	file := &regularFile{size: item.Size()}
	file.state.Store(&regularFileState{ctx: ctx, node: item, reader: reader})
	return file, nil
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
	root := fileSystem.root
	// Lookup has loaded every directory on the canonical path. Links within
	// the innermost image cannot reach the enclosing indexes.
	for dir, rest := root, prefix; rest != "." && rest != ""; {
		component, tail, _ := strings.Cut(rest, "/")
		dir = dir.children[component]
		if dir.kind == Subdirectory {
			root, prefix = dir, tail
		}
		rest = tail
	}
	switch {
	case prefix == "":
		prefix = "."
	case prefix != "." && !strings.Contains(prefix, "/"):
		// A one-component canonical path can still share the caller's input.
		prefix = item.name
	case root != fileSystem.root:
		prefix = strings.Clone(prefix)
	}
	return &FS{root: root, prefix: prefix, subRoot: item}, nil
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
	if len(entries) == 0 {
		return entries
	}
	// Bound the memory retained by one entry. Use separate batches so entries
	// with complete metadata cannot keep a sibling's backend alive.
	var ready []dirEntry
	var pending []dirEntryLookup
	var readyContext *dirEntryContext
	if ctx.Done() != nil {
		readyContext = &dirEntryContext{ctx}
	}
	lookupContext := readyContext
	for i, child := range children {
		info := child.metadata()
		entry := dirEntry{imageFileInfo: info, ctx: readyContext}
		if child.listedImage != nil || info.size < 0 {
			if lookupContext == nil {
				lookupContext = &dirEntryContext{ctx}
			}
			if child.listedImage == nil {
				// Keep each lazy file independent so it can release its
				// store after resolving the size.
				lazy := &legacyDirEntry{imageFileInfo: info}
				lazy.ctx.Store(lookupContext)
				lazy.lookup.Store(child)
				entries[i] = lazy
				continue
			}
			entry.ctx = lookupContext
			if len(info.name) > maxBatchedTextBytes {
				entries[i] = &dirEntryLookup{dirEntry: entry, lookup: child}
				continue
			}
			if len(pending) == 0 {
				pending = make([]dirEntryLookup, min(16, len(children)-i))
			}
			pending[0] = dirEntryLookup{dirEntry: entry, lookup: child}
			entries[i] = &pending[0]
			pending = pending[1:]
			continue
		}
		if len(info.name) > maxBatchedTextBytes {
			entries[i] = &dirEntry{imageFileInfo: info, ctx: readyContext}
			continue
		}
		if len(ready) == 0 {
			ready = make([]dirEntry, min(16, len(children)-i))
		}
		ready[0] = entry
		entries[i] = &ready[0]
		ready = ready[1:]
	}
	return entries
}

// initSize sets the index size before the node is shared. A size of -1 is unknown.
func (node *node) initSize(size int64) { node.encodedSize.Store(^size) }

func (node *node) Size() int64 {
	if node.kind == Symlink {
		return int64(len(node.target))
	}
	if node.kind != Regular && node.kind != Executable {
		return 0
	}
	return ^node.encodedSize.Load()
}

// Keep the index size, or the first size learned for a version 1 file.
func (node *node) recordSize(size int64) error {
	if size < 0 {
		return eris.ErrInvalidSize
	}
	encoded := ^size
	current := node.encodedSize.Load()
	if current == 0 {
		node.encodedSize.CompareAndSwap(0, encoded)
		current = node.encodedSize.Load()
	}
	if current != encoded {
		return ErrSizeMismatch
	}
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
	info := node.metadata()
	if info.size < 0 {
		reader, err := node.openRegularReader(ctx)
		if err != nil {
			return nil, err
		}
		_ = reader.Close()
		info.size = node.Size()
	}
	return info, nil
}

// A metadata snapshot can outlive its image or open file. Keep only the
// fields that FileInfo needs so it cannot pin a tree, store, or raw block.
type imageFileInfo struct {
	name string
	size int64
	kind EntryType
}

func (node *node) metadata() imageFileInfo {
	return imageFileInfo{name: node.name, size: node.Size(), kind: node.kind}
}

func (info imageFileInfo) Name() string { return info.name }
func (info imageFileInfo) Size() int64  { return info.size }

func (info imageFileInfo) Mode() stdfs.FileMode {
	switch info.kind {
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

func (info imageFileInfo) Type() stdfs.FileMode { return info.Mode().Type() }
func (info imageFileInfo) ModTime() time.Time   { return time.Time{} }
func (info imageFileInfo) IsDir() bool          { return isDirectory(info.kind) }
func (info imageFileInfo) Sys() any             { return nil }

type dirEntryContext struct{ context.Context }

type dirEntry struct {
	imageFileInfo
	ctx *dirEntryContext
}

func (entry *dirEntry) Info() (stdfs.FileInfo, error) {
	if entry.ctx != nil {
		if err := entry.ctx.Err(); err != nil {
			return nil, err
		}
	}
	return entry.imageFileInfo, nil
}

type dirEntryLookup struct {
	dirEntry
	lookup *node // Root metadata from another backend.
}

func (entry *dirEntryLookup) Info() (stdfs.FileInfo, error) {
	return entry.lookup.info(entry.ctx.Context)
}

type legacyDirEntry struct {
	imageFileInfo
	ctx    atomic.Pointer[dirEntryContext]
	lookup atomic.Pointer[node]
	size   atomic.Int64
}

func (entry *legacyDirEntry) Info() (stdfs.FileInfo, error) {
	if ctx := entry.ctx.Load(); ctx != nil {
		if child := entry.lookup.Load(); child != nil {
			info, err := child.info(ctx.Context)
			if err == nil {
				entry.size.Store(info.Size())
				entry.lookup.Store(nil)
				// A context without cancellation is only needed for backend
				// reads. Publish the size and clear the lookup before dropping it.
				if ctx.Done() == nil {
					entry.ctx.Store(nil)
				}
			}
			return info, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	info := entry.imageFileInfo
	info.size = entry.size.Load()
	return info, nil
}

func (node *node) isDir() bool { return isDirectory(node.kind) }

func isDirectory(kind EntryType) bool { return kind == directoryType || kind == Subdirectory }

type regularFile struct {
	state atomic.Pointer[regularFileState]
	size  int64
}

// An operation holds its own reference until it returns. Close can detach
// the state without serializing operations that the reader permits concurrently.
type regularFileState struct {
	ctx    context.Context
	node   *node
	reader eris.CapabilityReader
}

func (file *regularFile) Close() error {
	if state := file.state.Swap(nil); state != nil {
		return state.reader.Close()
	}
	return nil
}

func (file *regularFile) Read(p []byte) (int, error) {
	state := file.state.Load()
	if state == nil {
		return 0, ErrClosed
	}
	return state.reader.Read(p)
}

func (file *regularFile) ReadAt(p []byte, offset int64) (int, error) {
	state := file.state.Load()
	if state == nil {
		return 0, ErrClosed
	}
	return state.reader.ReadAt(p, offset)
}

func (file *regularFile) Seek(offset int64, whence int) (int64, error) {
	state := file.state.Load()
	if state == nil {
		return 0, ErrClosed
	}
	if err := state.ctx.Err(); err != nil {
		return 0, err
	}
	return state.reader.Seek(offset, whence)
}

func (file *regularFile) Size() int64 { return file.size }

// IsClosed and CacheSize preserve diagnostics previously provided by the
// embedded block reader. An opaque capability reader reports no cache count.
func (file *regularFile) IsClosed() bool { return file.state.Load() == nil }

func (file *regularFile) CacheSize() int {
	if state := file.state.Load(); state != nil {
		if reader, ok := state.reader.(interface{ CacheSize() int }); ok {
			return reader.CacheSize()
		}
	}
	return 0
}

func (file *regularFile) Stat() (stdfs.FileInfo, error) {
	state := file.state.Load()
	if state == nil {
		return nil, ErrClosed
	}
	return state.node.info(state.ctx)
}

type directoryFile struct {
	ctx     context.Context
	node    *node
	entries []*node
	info    imageFileInfo
	offset  int
	closed  bool
}

func (file *directoryFile) Stat() (stdfs.FileInfo, error) {
	if file.closed {
		return nil, ErrClosed
	}
	if file.node != nil {
		return file.node.info(file.ctx)
	}
	if err := file.ctx.Err(); err != nil {
		return nil, err
	}
	return file.info, nil
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
	ctx := file.ctx
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file.entries == nil && file.node != nil {
		file.entries = file.node.sortedChildren()
	}
	entries := file.entries
	start := file.offset
	end := len(entries)
	if count > 0 {
		end = start + min(count, len(entries)-start)
	}
	file.offset = end
	if end == len(entries) && file.node != nil {
		// The position cannot be reset. Keep only the directory's metadata.
		file.info = file.node.metadata()
		file.node = nil
		file.entries = nil
		file.offset = 0
		if ctx.Done() == nil {
			file.ctx = context.Background()
		}
	}
	if count > 0 && start == end {
		return nil, io.EOF
	}
	return bindDirEntries(ctx, entries[start:end]), nil
}

func (file *directoryFile) Close() error {
	file.closed = true
	file.entries = nil
	file.node = nil
	file.info = imageFileInfo{}
	file.ctx = nil
	return nil
}

func pathError(operation, path string, err error) error {
	return &stdfs.PathError{Op: operation, Path: strings.Clone(path), Err: err}
}

var (
	_ FileSystem        = (*FS)(nil)
	_ stdfs.ReadDirFile = (*directoryFile)(nil)
)
