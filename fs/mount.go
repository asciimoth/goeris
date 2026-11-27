package fs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	stdfs "io/fs"
	"path"
	"strings"

	"github.com/asciimoth/goeris"
)

// Mount serves the eris-go path layout as a read-only io/fs filesystem.
// An image store supplies decoded content at C and image trees at fs/C.
// C can be a full capability URN, unprefixed base32, or hexadecimal. A block
// source supplies encrypted blocks at their flat hash and volume.BlockPath.
// Each component is optional. Paths for an absent component match fs.ErrNotExist.
//
// Names resolve on demand. By default the root lists fs when an image store
// is available. [Mount.WithListingCache] enables bounded listings of recently
// accessed capabilities and blocks. Hashes, capabilities, and the reserved fs
// component accept ASCII case variants. Image paths are case-sensitive.
// Links follow the same rules as [FS]. Modification times are zero.
//
// Each operation that enters fs/C reads its index. Use [Mount.Sub] to retain
// the image for repeated access. Mount retains its context for all operations
// and open files. Image operations use the image store's options. Raw-block
// reads use the options supplied to the mount constructor.
//
// Mount supports concurrent calls when its components do. Each open file has
// its own position. The caller must serialize calls on one open directory.
// A FUSE or 9P adapter can serve the mount through its io/fs interfaces.
type Mount struct {
	ctx        context.Context
	images     *Store
	source     eris.BlockSource
	options    eris.BlockOptions
	indexLimit int64
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

// NewMountWithStore exposes decoded content and fs/C trees from images and
// raw hash paths from source. Either component can be nil. Missing components
// omit their paths from listings and lookups. With both nil, only "." exists.
// Options apply to raw blocks. Image operations use the Store's own options.
func NewMountWithStore(ctx context.Context, images *Store, source eris.BlockSource, options eris.BlockOptions) *Mount {
	if images != nil && images.content == nil {
		images = nil
	}
	return &Mount{ctx: ctx, images: images, source: source, options: options, indexLimit: DefaultMaxIndexSize}
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
func NewMountWithStoreAndLimit(ctx context.Context, images *Store, source eris.BlockSource, limit int64, options eris.BlockOptions) (*Mount, error) {
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

// resolve selects an image and a path within it. Virtual directories and
// content files use the same tree lookup and metadata as FS.
func (m *Mount) resolve(name string) (*FS, string, error) {
	if m == nil || m.ctx == nil || !stdfs.ValidPath(name) {
		return nil, "", stdfs.ErrInvalid
	}
	if err := m.ctx.Err(); err != nil {
		return nil, "", err
	}
	full := m.fullPath(name)
	root := buildTree(Index{}, m.images, ".", m.indexLimit)
	image := &FS{root: root, prefix: "."}
	if full == "." {
		if m.images != nil {
			root.children["fs"] = buildTree(Index{}, m.images, "fs", m.indexLimit)
		}
		m.addListings(root, full)
		return image, ".", nil
	}
	first, rest, tree := strings.Cut(full, "/")
	if mountUpper(first) == "FS" {
		if m.images == nil {
			return nil, "", stdfs.ErrNotExist
		}
		if !tree {
			root.name = first
			m.addListings(root, full)
			return image, ".", nil
		}
		capName, inner, more := strings.Cut(rest, "/")
		capability, err := mountCapability(capName)
		if err != nil {
			return nil, "", err
		}
		image, err := m.images.OpenFSWithLimit(m.ctx, capability, m.indexLimit)
		if err != nil {
			return nil, "", err
		}
		m.rememberCapability(capability, -1, true)
		// The image root has the name used by the caller, including aliases.
		image.root.name = capName
		if !more {
			inner = "."
		}
		return image, inner, nil
	}
	if ref, directory, ok := mountBlockPath(full); ok && m.source != nil {
		base := path.Base(full)
		if directory {
			root.name = base
			m.addListings(root, full)
			return image, ".", nil
		}
		data, err, _ := eris.GetBlock(m.ctx, m.source, ref, eris.NoTTL, m.options)
		if err != nil {
			return nil, "", err
		}
		if err := eris.VerifyBlock(data, ref); err != nil {
			return nil, "", err
		}
		m.rememberBlock(ref, int64(len(data)))
		root.children[base] = &node{
			name: base, kind: Regular, size: int64(len(data)), sizeKnown: true,
			block: bytes.Clone(data),
		}
		return image, base, nil
	}
	if tree || m.images == nil { // Decoded content is always a file, even when it is an index.
		return nil, "", stdfs.ErrNotExist
	}
	capability, err := mountCapability(first)
	if err != nil {
		return nil, "", err
	}
	root.children[first] = &node{
		name: first, kind: Regular, store: m.images,
		capability: capability,
	}
	return image, first, nil
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

func mountCapability(name string) (eris.Capability, error) {
	name = mountUpper(name)
	var capability eris.Capability
	var err error
	switch len(name) {
	case eris.CapabilityTextSize:
		if strings.HasPrefix(name, "URN:ERIS:") {
			capability, err = eris.ParseURN(eris.CapabilityURNPrefix + name[len(eris.CapabilityURNPrefix):])
		} else {
			err = eris.ErrInvalidCapability
		}
	case eris.CapabilityTextSize - len(eris.CapabilityURNPrefix):
		capability, err = eris.ParseURN(eris.CapabilityURNPrefix + name)
	case 2 * eris.CapabilitySize:
		var data []byte
		data, err = hex.DecodeString(name)
		if err == nil {
			capability, err = eris.ParseCapability(data)
		}
	default:
		err = eris.ErrInvalidCapability
	}
	if err != nil {
		return eris.Capability{}, stdfs.ErrNotExist
	}
	return capability, nil
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

// Open opens a file or directory. Uncompressed files also implement
// ReadSeekCloser. Compressed image files support sequential reads only.
func (m *Mount) Open(name string) (stdfs.File, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("open", name, err)
	}
	file, err := image.Open(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, image, inner)
	}
	return file, mountError("open", name, err)
}

// OpenFile opens an uncompressed file for Read, ReadAt, and Seek.
// Directories return fs.ErrInvalid; compressed files return ErrNotSeekable.
func (m *Mount) OpenFile(name string) (ReadSeekCloser, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("open", name, err)
	}
	file, err := image.OpenFile(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, image, inner)
	}
	return file, mountError("open", name, err)
}

// ReadFile reads a complete file. It has no content size limit. Use Open with
// io.LimitReader when the caller needs a limit on the output size.
func (m *Mount) ReadFile(name string) ([]byte, error) {
	file, err := m.Open(name)
	if err != nil {
		return nil, mountError("readfile", name, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	return data, mountError("readfile", name, err)
}

// ReadDir returns sorted directory entries. Entry.Info retains the context.
func (m *Mount) ReadDir(name string) ([]stdfs.DirEntry, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("readdir", name, err)
	}
	entries, err := image.ReadDir(m.ctx, inner)
	return entries, mountError("readdir", name, err)
}

// Stat returns metadata and follows links within their ERIS-FS image.
func (m *Mount) Stat(name string) (stdfs.FileInfo, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("stat", name, err)
	}
	info, err := image.Stat(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, image, inner)
	}
	return info, mountError("stat", name, err)
}

// Lstat returns metadata without following the final symbolic link.
func (m *Mount) Lstat(name string) (stdfs.FileInfo, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("lstat", name, err)
	}
	info, err := image.Lstat(m.ctx, inner)
	if err == nil {
		m.rememberContent(name, image, inner)
	}
	return info, mountError("lstat", name, err)
}

// ReadLink returns a link target only when it cannot leave the image.
func (m *Mount) ReadLink(name string) (string, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return "", mountError("readlink", name, err)
	}
	target, err := image.ReadLink(m.ctx, inner)
	return target, mountError("readlink", name, err)
}

// Sub selects a directory and retains the context and block options. A view
// of a virtual directory still resolves names on demand. A view within an
// image retains that image; links can leave the directory but not the image.
func (m *Mount) Sub(name string) (stdfs.FS, error) {
	image, inner, err := m.resolve(name)
	if err != nil {
		return nil, mountError("sub", name, err)
	}
	if name == "." {
		return m, nil
	}
	full := m.fullPath(name)
	_, directory, blockPath := mountBlockPath(full)
	if mountUpper(full) == "FS" || (blockPath && directory) {
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
