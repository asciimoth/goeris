package fetch

import (
	"context"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
)

// Tree builds a single flat v2 index. Use virtual slash-separated paths on
// every platform. Add leaves only. Directories are implicit. Operations on a
// Tree must be sequential. For a blob, use [eris.CapabilityStore.EncodeContentRecommended]
// with the secret from Options.
type Tree struct {
	builder         *erisfs.Builder
	autoCompression bool
}

// NewTree applies the validated output settings to every file and the index.
// The store must acknowledge all writes before the handler reports success.
func (o Options) NewTree(store eris.CapabilityStore, options eris.BlockOptions) (*Tree, error) {
	if o.Kind != "erisfs" {
		return nil, Failure{"unsupported-option", "erisfs.use"}
	}
	if o.Compression != "none" && o.Compression != "auto" {
		return nil, Failure{"unsupported-option", "erisfs.compression"}
	}
	if store == nil {
		return nil, erisfs.ErrInvalidEntry
	}
	return &Tree{builder: erisfs.NewBuilder(store, o.Secret, options), autoCompression: o.Compression == "auto"}, nil
}

// AddFile stores the complete stream. In auto mode it applies ERIS-FS-AUTO-1.
// A read error leaves the file out of the index.
func (t *Tree) AddFile(ctx context.Context, path string, r io.Reader, executable bool) error {
	if strings.IndexByte(path, 0) >= 0 {
		return erisfs.ErrInvalidPath
	}
	if t.autoCompression {
		return t.builder.AddAutoFile(ctx, path, r, executable)
	}
	return t.builder.AddFile(ctx, path, r, executable)
}

// AddSymlink stores the target literally, including empty components.
func (t *Tree) AddSymlink(path, target string) error {
	if strings.IndexByte(path, 0) >= 0 {
		return erisfs.ErrInvalidPath
	}
	if strings.IndexByte(target, 0) >= 0 {
		return erisfs.ErrInvalidEntry
	}
	return t.builder.AddSymlink(path, target)
}

// Finish stores the complete index and returns its root capability.
func (t *Tree) Finish(ctx context.Context) (eris.Capability, error) { return t.builder.Finish(ctx) }
