package fs

import (
	"context"
	"io"

	"github.com/asciimoth/goeris"
)

// ImageStore opens read-only image views and creates builders. Each builder
// owns its writes from the first file through Finish. A backend can report
// eris.ErrReadOnly at builder creation or when its storage rejects a write.
// WithLimit methods must enforce the byte limit or report an error.
type ImageStore interface {
	OpenFS(ctx context.Context, root eris.Capability) (FileSystem, error)
	OpenFSWithLimit(ctx context.Context, root eris.Capability, limit int64) (FileSystem, error)
	DecodeIndex(ctx context.Context, root eris.Capability) (Index, error)
	DecodeIndexWithLimit(ctx context.Context, root eris.Capability, limit int64) (Index, error)
	EncodeIndex(ctx context.Context, index Index, secret eris.Secret) (eris.Capability, error)
	NewBuilder(secret eris.Secret) (ImageBuilder, error)
}

// ImageBuilder builds one image. See Builder for the file formats and
// metadata accepted by each operation. Calls must be sequential.
type ImageBuilder interface {
	CheckPath(path string) error
	AddFile(ctx context.Context, path string, content io.Reader, executable bool) error
	AddCompressedFile(ctx context.Context, path string, content io.Reader, executable bool) error
	AddLzipFile(ctx context.Context, path string, content io.Reader, size int64, executable bool) error
	AddAutoFile(ctx context.Context, path string, content io.Reader, executable bool) error
	AddSymlink(path, target string) error
	AddSubdirectory(path string, root eris.Capability) error
	Index() Index
	Finish(ctx context.Context) (eris.Capability, error)
}

// Store reads and builds ERIS-FS images by capability. Each image operation
// uses its caller's context and the options supplied to NewStore.
type Store struct {
	content eris.CapabilityStore
	options eris.BlockOptions
}

// NewStore binds an ERIS capability store to the options for image reads and
// writes. A read-only capability store can open images and decode indexes.
// A nil capability store disables image operations.
func NewStore(content eris.CapabilityStore, options eris.BlockOptions) *Store {
	return &Store{content: content, options: options}
}
