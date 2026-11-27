package fs

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/imagepath"
	"github.com/asciimoth/goeris/internal/streamio"
)

// MountStore reads the decoded trees of a trusted, immutable ERIS-FS mount.
// The mount handles verification and decompression. It exposes neither the raw
// index bytes nor the limits used to decode them. Each link must stay within
// its containing directory because the mount does not identify nested indexes.
type MountStore struct{ mount string }

// NewMountStore selects the directory containing the capability trees, e.g.
// /eris/fs. With an empty path each OpenFS checks ERIS_FS_MNT, then ERIS_MNT
// with /fs appended. With neither set it tries /n/eris/fs and /eris/fs.
func NewMountStore(path string) *MountStore { return &MountStore{mount: path} }

func (s *MountStore) roots() []string {
	if s.mount != "" {
		return []string{s.mount}
	}
	if root := os.Getenv("ERIS_FS_MNT"); root != "" {
		return []string{root}
	}
	if root := os.Getenv("ERIS_MNT"); root != "" {
		return []string{root + string(filepath.Separator) + "fs"}
	}
	return []string{"/n/eris/fs", "/eris/fs"}
}

// OpenFS opens the tree for the canonical capability URN. With the default
// mount directories, each lookup can fall back to the second mount.
func (s *MountStore) OpenFS(ctx context.Context, cap eris.Capability) (FileSystem, error) {
	return openMountedTrees(ctx, cap, s.roots())
}

func openMountedTrees(ctx context.Context, cap eris.Capability, roots []string) (FileSystem, error) {
	if ctx == nil {
		return nil, stdfs.ErrInvalid
	}
	if err := cap.Validate(); err != nil {
		return nil, err
	}
	return openImages(ctx, roots, nil, func(ctx context.Context, root string) (FileSystem, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := streamio.AbsolutePath(root, cap.String())
		if err != nil {
			return nil, err
		}
		if _, err := mountedRootInfo(name); err != nil {
			return nil, err
		}
		return &mountedImage{root: name, prefix: "."}, nil
	})
}

// OpenFSWithLimit reports ErrUnsupported because the mount enforces its own index limits.
func (s *MountStore) OpenFSWithLimit(_ context.Context, _ eris.Capability, limit int64) (FileSystem, error) {
	if limit <= 0 {
		return nil, ErrInvalidLimit
	}
	return nil, errors.ErrUnsupported
}

// DecodeIndex reports errors.ErrUnsupported. The tree contains decoded entries.
func (s *MountStore) DecodeIndex(context.Context, eris.Capability) (Index, error) {
	return Index{}, errors.ErrUnsupported
}

// DecodeIndexWithLimit checks the limit before reporting errors.ErrUnsupported.
func (s *MountStore) DecodeIndexWithLimit(_ context.Context, _ eris.Capability, limit int64) (Index, error) {
	if limit <= 0 {
		return Index{}, ErrInvalidLimit
	}
	return Index{}, errors.ErrUnsupported
}

// EncodeIndex returns eris.ErrReadOnly.
func (s *MountStore) EncodeIndex(context.Context, Index, eris.Secret) (eris.Capability, error) {
	return eris.Capability{}, eris.ErrReadOnly
}

// NewBuilder returns eris.ErrReadOnly before any input is consumed.
func (s *MountStore) NewBuilder(eris.Secret) (ImageBuilder, error) { return nil, eris.ErrReadOnly }

type mountedImage struct{ root, prefix string }

func mountedRootInfo(name string) (stdfs.FileInfo, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, pathError("open", name, stdfs.ErrInvalid)
	}
	return info, nil
}

// Use the capability directory as the host root, including for Sub views.
func withMountedRoot[T any](ctx context.Context, m *mountedImage, op, name string, call func(*os.Root, string) (T, error)) (T, error) {
	var zero T
	if err := checkContext(ctx); err != nil {
		return zero, pathError(op, name, err)
	}
	local, err := filepath.Localize(name)
	if err != nil {
		return zero, pathError(op, name, stdfs.ErrInvalid)
	}
	dir, err := mountedRootInfo(m.root)
	if err != nil {
		return zero, pathError(op, name, err)
	}
	// Keep the final dot so the open requires a directory. Check its identity
	// before use in case the capability entry changed after Lstat.
	r, err := os.OpenRoot(m.root + string(filepath.Separator) + ".")
	if err != nil {
		return zero, pathError(op, name, err)
	}
	defer func() { _ = r.Close() }()
	opened, err := r.Stat(".")
	if err != nil {
		return zero, pathError(op, name, err)
	}
	if !os.SameFile(dir, opened) {
		return zero, pathError(op, name, stdfs.ErrInvalid)
	}
	// Both components use native path syntax. Localize validated the caller input.
	result, err := call(r, filepath.Join(m.prefix, local))
	if err != nil {
		return result, pathError(op, name, err)
	}
	return result, nil
}

func mountedChildPath(dir, component string) (string, error) {
	// A Windows lookup can trim dots and spaces from a component.
	if filepath.Separator == '\\' && (strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ")) {
		return "", stdfs.ErrInvalid
	}
	local, err := filepath.Localize(component)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, local), nil
}

// An expanded tree has no markers for nested indexes. Each directory can be
// an image root. Resolve each link within the directory that contains it.
func resolveMountedLinks(ctx context.Context, r *os.Root, p string, mode imagepath.Mode) (string, string, error) {
	return imagepath.Resolve(".", filepath.ToSlash(p), mode, func(dir, component string) (string, imagepath.Kind, string, error) {
		if err := ctx.Err(); err != nil {
			return "", 0, "", err
		}
		next, err := mountedChildPath(dir, component)
		if err != nil {
			return "", 0, "", err
		}
		info, err := r.Lstat(next)
		if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return "", 0, "", imagepath.ErrMissingEntry
		}
		if err != nil {
			return "", 0, "", err
		}
		if info.Mode()&stdfs.ModeSymlink != 0 {
			link, err := r.Readlink(next)
			if mode != imagepath.Export {
				link = filepath.ToSlash(link)
			}
			return next, imagepath.Link, link, err
		}
		if filepath.Separator == '\\' && info.Mode()&stdfs.ModeIrregular != 0 {
			// A Windows junction can redirect lookup like a symlink.
			return "", 0, "", stdfs.ErrInvalid
		}
		if info.IsDir() {
			return next, imagepath.ImageRoot, "", nil
		}
		return next, imagepath.File, "", nil
	})
}

func mountedCall[T any](ctx context.Context, m *mountedImage, op, name string, call func(*os.Root, string) (T, error)) (T, error) {
	return withMountedRoot(ctx, m, op, name, func(r *os.Root, p string) (T, error) {
		resolved, _, err := resolveMountedLinks(ctx, r, p, imagepath.Follow)
		if err != nil {
			var zero T
			return zero, err
		}
		return call(r, resolved)
	})
}

func (m *mountedImage) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return mountedCall(ctx, m, "stat", name, (*os.Root).Stat)
}

func (m *mountedImage) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	if name == "." {
		return mountedCall(ctx, m, "lstat", name, (*os.Root).Stat)
	}
	return withMountedRoot(ctx, m, "lstat", name, func(r *os.Root, p string) (stdfs.FileInfo, error) {
		dir, _, err := resolveMountedLinks(ctx, r, filepath.Dir(p), imagepath.Follow)
		if err != nil {
			return nil, err
		}
		child, err := mountedChildPath(dir, filepath.Base(p))
		if err != nil {
			return nil, err
		}
		return r.Lstat(child)
	})
}

func (m *mountedImage) ReadLink(ctx context.Context, name string) (string, error) {
	return withMountedRoot(ctx, m, "readlink", name, func(r *os.Root, p string) (string, error) {
		if name == "." {
			return "", stdfs.ErrInvalid
		}
		if err := imagepath.CheckNative(name); err != nil {
			return "", err
		}
		_, target, err := resolveMountedLinks(ctx, r, p, imagepath.Export)
		return target, err
	})
}

func (m *mountedImage) ResolvePath(ctx context.Context, name string) (string, error) {
	return mountedCall(ctx, m, "resolvepath", name, func(r *os.Root, p string) (string, error) {
		info, err := r.Stat(p)
		if err != nil {
			return "", err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return "", stdfs.ErrInvalid
		}
		return streamio.AbsolutePath(m.root, p)
	})
}

func (m *mountedImage) Sub(ctx context.Context, name string) (FileSystem, error) {
	return mountedCall(ctx, m, "sub", name, func(r *os.Root, p string) (FileSystem, error) {
		info, err := r.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, stdfs.ErrInvalid
		}
		return &mountedImage{root: m.root, prefix: p}, nil
	})
}

// A mount has no separate prefetch protocol. Visit the directory tree and
// look up each regular file so the mount can load its metadata on demand.
func (m *mountedImage) Prefetch(ctx context.Context, name string) error {
	_, err := mountedCall(ctx, m, "prefetch", name, func(root *os.Root, local string) (struct{}, error) {
		return struct{}{}, prefetchMountedTree(ctx, root.FS(), filepath.ToSlash(local))
	})
	return err
}

func prefetchMountedTree(ctx context.Context, tree stdfs.FS, name string) error {
	return stdfs.WalkDir(tree, name, func(path string, entry stdfs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// WalkDir already looked up the starting entry. It follows a link at
		// that path, but skips links encountered inside the selected directory.
		if path != name && entry.Type().IsRegular() {
			_, err = stdfs.Stat(tree, path)
		}
		return err
	})
}

func (m *mountedImage) open(ctx context.Context, name string, directories bool) (*mountedFile, error) {
	file, err := mountedCall(ctx, m, "open", name, func(r *os.Root, p string) (*os.File, error) {
		info, err := r.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() && (!directories || !info.IsDir()) {
			return nil, stdfs.ErrInvalid
		}
		return r.Open(p)
	})
	if err != nil {
		return nil, err
	}
	return &mountedFile{file: file, ctx: ctx}, nil
}

func (m *mountedImage) Open(ctx context.Context, name string) (stdfs.File, error) {
	f, err := m.open(ctx, name, true)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (m *mountedImage) OpenFile(ctx context.Context, name string) (ReadSeekCloser, error) {
	f, err := m.open(ctx, name, false)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (m *mountedImage) ReadFile(ctx context.Context, name string) ([]byte, error) {
	f, err := m.OpenFile(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

func (m *mountedImage) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return mountedCall(ctx, m, "readdir", name, func(r *os.Root, p string) ([]stdfs.DirEntry, error) {
		info, err := r.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, stdfs.ErrInvalid
		}
		entries, err := stdfs.ReadDir(r.FS(), filepath.ToSlash(p))
		return bindMountedEntries(ctx, entries), err
	})
}

type mountedDirEntry struct {
	stdfs.DirEntry
	ctx context.Context
}

func (entry mountedDirEntry) String() string { return stdfs.FormatDirEntry(entry) }

func (entry mountedDirEntry) Info() (stdfs.FileInfo, error) {
	if err := entry.ctx.Err(); err != nil {
		return nil, err
	}
	return entry.DirEntry.Info()
}

func bindMountedEntries(ctx context.Context, entries []stdfs.DirEntry) []stdfs.DirEntry {
	if ctx.Done() == nil {
		return entries
	}
	for i, entry := range entries {
		entries[i] = mountedDirEntry{DirEntry: entry, ctx: ctx}
	}
	return entries
}

// Expose only read operations. An os.File would also expose Chmod and Chown.
type mountedFile struct {
	file *os.File
	ctx  context.Context
}

func (f *mountedFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Read(p)
}

func (f *mountedFile) ReadAt(p []byte, off int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.ReadAt(p, off)
}

func (f *mountedFile) ReadDir(n int) ([]stdfs.DirEntry, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := f.file.ReadDir(n)
	return bindMountedEntries(f.ctx, entries), err
}

func (f *mountedFile) Seek(off int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.file.Seek(off, whence)
}

func (f *mountedFile) Stat() (stdfs.FileInfo, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	return f.file.Stat()
}

func (f *mountedFile) Close() error { return f.file.Close() }
