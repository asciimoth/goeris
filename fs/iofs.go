package fs

import (
	"context"
	stdfs "io/fs"
)

// IOFS binds a context to a FileSystem for use with io/fs. The context applies
// to every operation, including reads on open files and directory entry Info.
// Create a separate wrapper to use a different context. A Sub wrapper retains
// the same context. The caller must supply a non-nil context and file system.
type IOFS struct {
	ctx context.Context
	fs  FileSystem
}

// NewIOFS binds ctx to fileSystem. It performs no I/O.
func NewIOFS(ctx context.Context, fileSystem FileSystem) *IOFS {
	return &IOFS{ctx: ctx, fs: fileSystem}
}

func (f *IOFS) Open(name string) (stdfs.File, error)          { return f.fs.Open(f.ctx, name) }
func (f *IOFS) OpenFile(name string) (ReadSeekCloser, error)  { return f.fs.OpenFile(f.ctx, name) }
func (f *IOFS) ReadFile(name string) ([]byte, error)          { return f.fs.ReadFile(f.ctx, name) }
func (f *IOFS) ReadDir(name string) ([]stdfs.DirEntry, error) { return f.fs.ReadDir(f.ctx, name) }
func (f *IOFS) Stat(name string) (stdfs.FileInfo, error)      { return f.fs.Stat(f.ctx, name) }
func (f *IOFS) Lstat(name string) (stdfs.FileInfo, error)     { return f.fs.Lstat(f.ctx, name) }
func (f *IOFS) ReadLink(name string) (string, error)          { return f.fs.ReadLink(f.ctx, name) }
func (f *IOFS) Sub(name string) (stdfs.FS, error) {
	sub, err := f.fs.Sub(f.ctx, name)
	if err != nil {
		return nil, err
	}
	return NewIOFS(f.ctx, sub), nil
}

var (
	_ stdfs.FS         = (*IOFS)(nil)
	_ stdfs.ReadFileFS = (*IOFS)(nil)
	_ stdfs.ReadDirFS  = (*IOFS)(nil)
	_ stdfs.ReadLinkFS = (*IOFS)(nil)
	_ stdfs.StatFS     = (*IOFS)(nil)
	_ stdfs.SubFS      = (*IOFS)(nil)
)

// Prefetch requests all content below name using the bound context.
// It can block on index reads; see FS.Prefetch.
func (f *IOFS) Prefetch(name string) error { return f.fs.Prefetch(f.ctx, name) }
