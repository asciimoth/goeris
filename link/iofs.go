package link

import (
	"context"
	"io/fs"
)

// IOFS binds a context to a link FS for use with io/fs. The context applies to
// linked content reads, including reads after Open returns. It cannot stop I/O
// in the base fs.FS. Create a separate wrapper to use a different context.
type IOFS struct {
	ctx context.Context
	fs  *FS
}

// NewIOFS binds ctx to fileSystem without I/O. Both arguments must be non-nil.
func NewIOFS(ctx context.Context, fileSystem *FS) *IOFS {
	return &IOFS{ctx: ctx, fs: fileSystem}
}

func (f *IOFS) Open(name string) (fs.File, error)          { return f.fs.Open(f.ctx, name) }
func (f *IOFS) ReadFile(name string) ([]byte, error)       { return f.fs.ReadFile(f.ctx, name) }
func (f *IOFS) ReadDir(name string) ([]fs.DirEntry, error) { return f.fs.ReadDir(f.ctx, name) }
func (f *IOFS) Stat(name string) (fs.FileInfo, error)      { return f.fs.Stat(f.ctx, name) }
func (f *IOFS) Lstat(name string) (fs.FileInfo, error)     { return f.fs.Lstat(f.ctx, name) }
func (f *IOFS) ReadLink(name string) (string, error)       { return f.fs.ReadLink(f.ctx, name) }

var (
	_ fs.FS         = (*IOFS)(nil)
	_ fs.ReadFileFS = (*IOFS)(nil)
	_ fs.ReadDirFS  = (*IOFS)(nil)
	_ fs.ReadLinkFS = (*IOFS)(nil)
	_ fs.StatFS     = (*IOFS)(nil)
)
