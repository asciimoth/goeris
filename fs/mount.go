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

// Mount exposes a BlockSource with the eris-go mount layout. A path C opens
// decoded ERIS content. A path fs/C opens that content as an ERIS-FS tree.
// C can be a full urn:eris: capability, unprefixed base32, or the
// 66 capability bytes in hexadecimal. All paths follow io/fs.ValidPath rules.
//
// By default, the root lists only "fs", and fs lists no entries. Capabilities
// resolve on demand. WithListingCache can enable bounded recent listings.
// For a base32 block hash H, H[:3]/H[3:6]/H[6:9]/H[9:] and H return the encrypted
// block. The split form matches volume.BlockPath. Its three prefix directories
// resolve on demand and do not require a stored block. Their default listings
// are empty.
// Hashes, capabilities, and the reserved fs component accept ASCII case
// variants. Paths inside an ERIS-FS image remain case-sensitive.
// Files and directories are read-only, with zero modification times.
// Links follow the same rules as FS: ReadLink refuses a target that can
// leave its ERIS-FS image. Safe targets are returned without alteration.
//
// Mount retains its context for all operations and open files. It copies the
// block options and passes them to all reads. The optional listing cache holds
// only identifiers and metadata. Each operation that enters fs/C reads its index.
// Use Sub to retain an image for repeated access without reading its index again.
// Mount is safe for concurrent use if the source supports concurrent calls.
// Each open file has its own position. Calls on one open directory must be
// serialized. Mount does not mount a host filesystem or own the block source.
// A FUSE or 9P adapter can use its io/fs interfaces and OpenFile for random access.
type Mount struct {
	ctx        context.Context
	source     eris.BlockSource
	options    eris.BlockOptions
	indexLimit int64
	prefix     string // A Sub view of a virtual directory; empty at the mount root.
	listings   *mountListingCache
}

// NewMount binds ctx and source without I/O. Both must be non-nil.
// It limits each decoded ERIS-FS index to DefaultMaxIndexSize bytes.
// The zero value of Mount is not ready for use.
func NewMount(ctx context.Context, source eris.BlockSource, options eris.BlockOptions) *Mount {
	return &Mount{ctx: ctx, source: source, options: options, indexLimit: DefaultMaxIndexSize}
}

// NewMountWithLimit is NewMount with a limit for each decoded index, including
// nested indexes. A non-positive limit returns ErrInvalidLimit. The limit does
// not apply to decoded content at C, encrypted blocks, or image file content.
func NewMountWithLimit(ctx context.Context, source eris.BlockSource, limit int64, options eris.BlockOptions) (*Mount, error) {
	if limit <= 0 {
		return nil, ErrInvalidLimit
	}
	mount := NewMount(ctx, source, options)
	mount.indexLimit = limit
	return mount, nil
}

// resolve selects an image and a path within it. Virtual directories and
// content files use the same tree lookup and metadata as FS.
func (m *Mount) resolve(name string) (*FS, string, error) {
	if m == nil || m.ctx == nil || m.source == nil || !stdfs.ValidPath(name) {
		return nil, "", stdfs.ErrInvalid
	}
	if err := m.ctx.Err(); err != nil {
		return nil, "", err
	}
	full := m.fullPath(name)
	root := buildTree(Index{}, m.source, ".", m.indexLimit, m.options)
	image := &FS{source: m.source, options: m.options, root: root, prefix: ".", indexLimit: m.indexLimit}
	if full == "." || mountUpper(full) == "FS" {
		dirName := "fs"
		if full != "." {
			dirName = full
		}
		dir := buildTree(Index{}, m.source, dirName, m.indexLimit, m.options)
		root.children[dirName] = dir
		m.addListings(dir, "fs")
		if full == "." {
			m.addListings(root, ".")
		}
		return image, full, nil
	}
	first, rest, tree := strings.Cut(full, "/")
	if mountUpper(first) == "FS" {
		capName, inner, more := strings.Cut(rest, "/")
		capability, err := mountCapability(capName)
		if err != nil {
			return nil, "", err
		}
		image, err := OpenFSWithLimit(m.ctx, capability, m.source, m.indexLimit, m.options)
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
	if ref, directory, ok := mountBlockPath(full); ok {
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
	if tree { // Decoded content is always a file, even when it is an index.
		return nil, "", stdfs.ErrNotExist
	}
	capability, err := mountCapability(first)
	if err != nil {
		return nil, "", err
	}
	root.children[first] = &node{
		name: first, kind: Regular, source: m.source, options: m.options,
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
