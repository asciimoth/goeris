package fs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	stdfs "io/fs"
	"path"
	"strings"

	"github.com/asciimoth/goeris"
)

// Mount serves the eris-go path layout as a read-only io/fs filesystem.
// An ImageStore supplies image trees at fs/C. A *Store also serves decoded content at C.
// C can be a full capability URN, unprefixed base32, or hexadecimal. A block
// source supplies encrypted blocks at their flat hash and volume.BlockPath.
// Each component is optional. Paths for an absent component match fs.ErrNotExist.
//
// Names resolve on demand. By default the root lists fs when an image store
// is available. [Mount.WithListingCache] enables bounded listings of recently
// accessed capabilities and blocks. Hashes, capabilities, and the reserved fs
// component accept ASCII case variants. Image paths are case-sensitive.
// The image backend supplies link handling and metadata within each tree.
//
// Each operation that enters fs/C opens the image. Use [Mount.Sub] to retain
// the image for repeated access. Mount retains its context for all operations
// and open files. Image operations use the image store's options. Raw-block
// reads use the options supplied to the mount constructor.
//
// Mount supports concurrent calls when its components do. Each open file has
// its own position. The caller must serialize calls on one open directory.
// A FUSE or 9P adapter can serve the mount through its io/fs interfaces.
type Mount struct {
	ctx        context.Context
	images     ImageStore
	content    *Store // supplies decoded files at C
	source     eris.BlockSource
	options    eris.BlockOptions
	indexLimit int64  // Zero uses the image backend's default.
	prefix     string // A Sub view of a virtual directory; empty at the mount root.
	listings   *mountListingCache
}

// NewMount binds ctx and source without I/O. Both must be non-nil.
// It limits each decoded ERIS-FS index to DefaultMaxIndexSize bytes.
// The zero value of Mount is not ready for use.
//
// This compatibility function wraps NewMountWithStore.
func NewMount(ctx context.Context, source eris.BlockSource, options eris.BlockOptions) *Mount {
	if source == nil {
		return &Mount{}
	}
	images := NewStore(eris.NewBlockCapabilityStore(source), options)
	return NewMountWithStore(ctx, images, source, options)
}

// NewMountWithStore exposes fs/C trees from images and
// raw hash paths from source. Either component can be nil. Missing components
// omit their paths from listings and lookups. With both nil, only "." exists.
// A *Store also exposes decoded capability content at C. Options apply to raw
// blocks. Image operations use the image backend's own settings.
func NewMountWithStore(ctx context.Context, images ImageStore, source eris.BlockSource, options eris.BlockOptions) *Mount {
	// A capability-backed image store also supplies the legacy C namespace.
	content, ok := images.(*Store)
	if ok && (content == nil || content.content == nil) {
		images = nil
		content = nil
	}
	return &Mount{ctx: ctx, images: images, content: content, source: source, options: options}
}

// NewMountWithLimit is NewMount with a limit for each decoded index, including
// nested indexes. A non-positive limit returns ErrInvalidLimit. The limit does
// not apply to decoded content at C, encrypted blocks, or image file content.
//
// This compatibility function accepts a single block source for all paths.
func NewMountWithLimit(ctx context.Context, source eris.BlockSource, limit int64, options eris.BlockOptions) (*Mount, error) {
	return NewMount(ctx, source, options).withIndexLimit(limit)
}

// NewMountWithStoreAndLimit sets a positive limit for each decoded index.
func NewMountWithStoreAndLimit(ctx context.Context, images ImageStore, source eris.BlockSource, limit int64, options eris.BlockOptions) (*Mount, error) {
	return NewMountWithStore(ctx, images, source, options).withIndexLimit(limit)
}

// withIndexLimit configures a newly constructed mount before it is shared.
func (mount *Mount) withIndexLimit(limit int64) (*Mount, error) {
	if limit <= 0 {
		return nil, ErrInvalidLimit
	}
	mount.indexLimit = limit
	return mount, nil
}

func (m *Mount) checkedPath(name string) (string, error) {
	if m == nil || m.ctx == nil || !stdfs.ValidPath(name) {
		return "", stdfs.ErrInvalid
	}
	if err := m.ctx.Err(); err != nil {
		return "", err
	}
	return m.fullPath(name), nil
}

type mountResolveOptions uint8

const (
	mountMetadata   mountResolveOptions = 0
	mountList       mountResolveOptions = 1 << 0
	mountBlockBytes mountResolveOptions = 1 << 1
)

// resolve selects an image and a path within it. Virtual directories and
// content files use the same tree lookup and metadata as FS.
func (m *Mount) resolve(name string, options mountResolveOptions) (FileSystem, string, error) {
	full, err := m.checkedPath(name)
	if err != nil {
		return nil, "", err
	}
	return m.resolvePath(full, options)
}

func (m *Mount) resolvePath(full string, options mountResolveOptions) (FileSystem, string, error) {
	virtualDirectory := func(base string) *FS {
		root := &node{name: base, kind: directoryType}
		if options&mountList != 0 {
			root.children = make(map[string]*node)
			if full == "." && m.images != nil {
				root.children["fs"] = &node{name: "fs", kind: directoryType}
			}
			m.addListings(root, full)
		}
		return &FS{root: root, prefix: "."}
	}
	if full == "." {
		return virtualDirectory("."), ".", nil
	}
	first, rest, tree := strings.Cut(full, "/")
	if isMountFS(first) {
		if m.images == nil {
			return nil, "", stdfs.ErrNotExist
		}
		if !tree {
			return virtualDirectory(first), ".", nil
		}
		capName, inner, more := strings.Cut(rest, "/")
		capability, err := mountCapability(capName)
		if err != nil {
			return nil, "", err
		}
		view, err := m.openImage(m.ctx, capability)
		if err != nil {
			return nil, "", err
		}
		m.rememberCapability(capability, -1, true)
		// The image root has the name used by the caller, including aliases.
		if !more {
			inner = "."
			view = namedImageRoot{FileSystem: view, name: capName}
		}
		return view, inner, nil
	}
	if ref, directory, ok := mountBlockPath(full); ok && m.source != nil {
		base := path.Base(full)
		if directory {
			return virtualDirectory(base), ".", nil
		}
		data, err := m.loadBlock(ref)
		if err != nil {
			return nil, "", err
		}
		child := &node{
			name: base, kind: Regular, size: int64(len(data)), sizeKnown: true,
		}
		if options&mountBlockBytes != 0 {
			child.block = bytes.Clone(data)
		}
		root := &node{name: ".", kind: directoryType, children: map[string]*node{base: child}}
		return &FS{root: root, prefix: "."}, base, nil
	}
	if tree || m.content == nil { // Decoded content is always a file, even when it is an index.
		return nil, "", stdfs.ErrNotExist
	}
	capability, err := mountCapability(first)
	if err != nil {
		return nil, "", err
	}
	root := buildTree(Index{}, m.content, ".", m.indexLimit)
	root.children[first] = &node{
		name: first, kind: Regular, store: m.content,
		capability: capability,
	}
	return &FS{root: root, prefix: "."}, first, nil
}

// loadBlock verifies the block. Callers that expose its bytes keep a copy.
func (m *Mount) loadBlock(ref eris.Reference) ([]byte, error) {
	data, err, _ := eris.GetBlock(m.ctx, m.source, ref, eris.NoTTL, m.options)
	if err != nil {
		return nil, err
	}
	if err := eris.VerifyBlock(data, ref); err != nil {
		return nil, err
	}
	m.rememberBlock(ref, int64(len(data)))
	return data, nil
}

func (m *Mount) openImage(ctx context.Context, cap eris.Capability) (FileSystem, error) {
	if m.indexLimit > 0 {
		return m.images.OpenFSWithLimit(ctx, cap, m.indexLimit)
	}
	return m.images.OpenFS(ctx, cap)
}

// fullPath preserves caller spelling for metadata and never cleans image paths.
func (m *Mount) fullPath(name string) string {
	if m.prefix == "" {
		return name
	}
	if name == "." {
		return m.prefix
	}
	return m.prefix + "/" + name
}

func isMountFS(name string) bool {
	return len(name) == 2 && strings.EqualFold(name, "fs")
}

func mountCapability(name string) (eris.Capability, error) {
	const prefix = eris.CapabilityURNPrefix
	switch len(name) {
	case eris.CapabilityTextSize:
		if !strings.EqualFold(name[:len(prefix)], prefix) {
			return eris.Capability{}, stdfs.ErrNotExist
		}
		name = name[len(prefix):]
	case eris.CapabilityTextSize - len(prefix):
		// The caller supplied the base32 payload.
	case 2 * eris.CapabilitySize:
		var data [eris.CapabilitySize]byte
		if _, err := hex.Decode(data[:], []byte(name)); err != nil {
			return eris.Capability{}, stdfs.ErrNotExist
		}
		cap, err := eris.ParseCapability(data[:])
		if err != nil {
			return eris.Capability{}, stdfs.ErrNotExist
		}
		return cap, nil
	default:
		return eris.Capability{}, stdfs.ErrNotExist
	}

	// Normalize into a fixed buffer. The public parser checks the base32
	// alphabet and rejects nonzero padding bits.
	var urn [eris.CapabilityTextSize]byte
	copy(urn[:], prefix)
	for i := range len(name) {
		urn[len(prefix)+i] = mountUpperByte(name[i])
	}
	var cap eris.Capability
	if err := cap.UnmarshalText(urn[:]); err != nil {
		return eris.Capability{}, stdfs.ErrNotExist
	}
	return cap, nil
}

// mountError keeps the public path and operation, while preserving source
// errors. Missing blocks also match fs.ErrNotExist for standard fs callers.
func mountError(op, name string, err error) error {
	if err == nil {
		return nil
	}
	// A PathError can come directly from a block source. Keep it in the chain
	// so callers can inspect the storage path as well as this public path.
	if errors.Is(err, eris.ErrMissingBlock) && !errors.Is(err, stdfs.ErrNotExist) {
		err = errors.Join(stdfs.ErrNotExist, err)
	}
	return pathError(op, name, err)
}

// Open opens a file or directory. The image backend determines whether
// a compressed file also supports random access.
func (m *Mount) Open(name string) (stdfs.File, error) {
	image, inner, err := m.resolve(name, mountList|mountBlockBytes)
	if err != nil {
		return nil, mountError("open", name, err)
	}
	file, err := image.Open(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, file.Stat)
	}
	return file, mountError("open", name, err)
}

// OpenFile opens a regular file for Read, ReadAt, and Seek. A capability-
// backed image returns ErrNotSeekable for compressed files.
func (m *Mount) OpenFile(name string) (ReadSeekCloser, error) {
	image, inner, err := m.resolve(name, mountBlockBytes)
	if err != nil {
		return nil, mountError("open", name, err)
	}
	file, err := image.OpenFile(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, file.Stat)
	}
	return file, mountError("open", name, err)
}

// ReadFile reads a complete file. It has no content size limit. Use Open with
// io.LimitReader when the caller needs a limit on the output size.
func (m *Mount) ReadFile(name string) ([]byte, error) {
	full, err := m.checkedPath(name)
	if err != nil {
		return nil, mountError("readfile", name, err)
	}
	if m.source != nil {
		if ref, directory, ok := mountBlockPath(full); ok && !directory {
			data, err := m.loadBlock(ref)
			if err != nil {
				return nil, mountError("readfile", name, err)
			}
			data = bytes.Clone(data)
			if err := m.ctx.Err(); err != nil {
				return nil, mountError("readfile", name, err)
			}
			return data, nil
		}
	}
	// At C the listing records a successful open even if a later read fails.
	// At fs/C the backend must receive the complete read so it controls fallback.
	if m.listings != nil {
		if _, err := mountCapability(full); err == nil {
			file, err := m.Open(name)
			if err != nil {
				return nil, mountError("readfile", name, err)
			}
			defer func() { _ = file.Close() }()
			data, err := readImageFile(file)
			return data, mountError("readfile", name, err)
		}
	}
	image, inner, err := m.resolvePath(full, mountMetadata)
	if err != nil {
		return nil, mountError("readfile", name, err)
	}
	data, err := image.ReadFile(m.ctx, inner)
	return data, mountError("readfile", name, err)
}

// ReadDir returns sorted directory entries. Entry.Info retains the context.
func (m *Mount) ReadDir(name string) ([]stdfs.DirEntry, error) {
	image, inner, err := m.resolve(name, mountList)
	if err != nil {
		return nil, mountError("readdir", name, err)
	}
	entries, err := image.ReadDir(m.ctx, inner)
	return entries, mountError("readdir", name, err)
}

// Stat returns metadata and follows links within their ERIS-FS image.
func (m *Mount) Stat(name string) (stdfs.FileInfo, error) {
	image, inner, err := m.resolve(name, mountMetadata)
	if err != nil {
		return nil, mountError("stat", name, err)
	}
	info, err := image.Stat(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, func() (stdfs.FileInfo, error) { return info, nil })
	}
	return info, mountError("stat", name, err)
}

// Lstat returns metadata without following the final symbolic link.
func (m *Mount) Lstat(name string) (stdfs.FileInfo, error) {
	image, inner, err := m.resolve(name, mountMetadata)
	if err != nil {
		return nil, mountError("lstat", name, err)
	}
	info, err := image.Lstat(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, func() (stdfs.FileInfo, error) { return info, nil })
	}
	return info, mountError("lstat", name, err)
}

// ReadLink returns a link target only when it cannot leave the image.
func (m *Mount) ReadLink(name string) (string, error) {
	image, inner, err := m.resolve(name, mountMetadata)
	if err != nil {
		return "", mountError("readlink", name, err)
	}
	target, err := image.ReadLink(m.ctx, inner)
	return target, mountError("readlink", name, err)
}

// Sub selects a directory and retains the context and block options. A view
// of a virtual directory still resolves names on demand. Within an image,
// the selected backend controls link lookup. ReadLink also checks that the
// target stays within the selected directory.
func (m *Mount) Sub(name string) (stdfs.FS, error) {
	image, inner, err := m.resolve(name, mountMetadata)
	if err != nil {
		return nil, mountError("sub", name, err)
	}
	if name == "." {
		return m, nil
	}
	full := m.fullPath(name)
	_, directory, blockPath := mountBlockPath(full)
	if isMountFS(full) || (blockPath && directory) {
		sub := *m
		sub.prefix = full
		return &sub, nil
	}
	sub, err := image.Sub(m.ctx, inner)
	if err != nil {
		return nil, mountError("sub", name, err)
	}
	return NewIOFS(m.ctx, sub), nil
}

var (
	_ stdfs.FS         = (*Mount)(nil)
	_ stdfs.ReadFileFS = (*Mount)(nil)
	_ stdfs.ReadDirFS  = (*Mount)(nil)
	_ stdfs.ReadLinkFS = (*Mount)(nil)
	_ stdfs.StatFS     = (*Mount)(nil)
	_ stdfs.SubFS      = (*Mount)(nil)
)

// namedImageRoot gives the image directory the capability spelling used in
// the mount lookup. The backend may share its root with other callers.
type namedImageRoot struct {
	FileSystem
	name string
}
type namedInfo struct {
	stdfs.FileInfo
	name string
}

func (i namedInfo) Name() string { return i.name }
func (m namedImageRoot) Stat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	i, err := m.FileSystem.Stat(ctx, p)
	if err == nil && p == "." {
		return namedInfo{i, m.name}, nil
	}
	return i, err
}
func (m namedImageRoot) Lstat(ctx context.Context, p string) (stdfs.FileInfo, error) {
	info, err := m.FileSystem.Lstat(ctx, p)
	if err == nil && p == "." {
		return namedInfo{info, m.name}, nil
	}
	return info, err
}
func (m namedImageRoot) Open(ctx context.Context, p string) (stdfs.File, error) {
	if p != "." {
		return m.FileSystem.Open(ctx, p)
	}
	file, err := m.FileSystem.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return namedDirectory{File: file, name: m.name}, nil
}
func (m namedImageRoot) Sub(ctx context.Context, p string) (FileSystem, error) {
	sub, err := m.FileSystem.Sub(ctx, p)
	if err == nil && p == "." {
		return namedImageRoot{FileSystem: sub, name: m.name}, nil
	}
	return sub, err
}

// Preserve the backend's directory handle and metadata, including Close errors.
type namedDirectory struct {
	stdfs.File
	name string
}

func (d namedDirectory) Stat() (stdfs.FileInfo, error) {
	info, err := d.File.Stat()
	if err != nil {
		return nil, err
	}
	return namedInfo{info, d.name}, nil
}

func (d namedDirectory) ReadDir(n int) ([]stdfs.DirEntry, error) {
	directory, ok := d.File.(stdfs.ReadDirFile)
	if !ok {
		return nil, errors.ErrUnsupported
	}
	return directory.ReadDir(n)
}
