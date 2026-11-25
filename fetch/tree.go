package fetch

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/internal/streamio"
)

// Tree builds a single flat v2 index. Use virtual slash-separated paths on
// every platform. Add leaves only. Directories are implicit. Operations on a
// Tree must be sequential. For a blob use eris.EncodeContentRecommended with
// the secret from Options.
type Tree struct {
	builder  *erisfs.Builder
	compress bool
}

// NewTree applies the validated output settings to every file and the index.
// The sink must acknowledge all writes before the handler reports success.
func (o Options) NewTree(sink eris.BlockSink, blocks eris.BlockOptions) (*Tree, error) {
	if o.Kind != "erisfs" {
		return nil, Failure{"unsupported-option", "erisfs.use"}
	}
	if o.Compression != "none" && o.Compression != "lzip-v1" {
		return nil, Failure{"unsupported-option", "erisfs.compression"}
	}
	if sink == nil {
		return nil, erisfs.ErrInvalidEntry
	}
	return &Tree{builder: erisfs.NewBuilder(sink, o.Secret, blocks), compress: o.Compression == "lzip-v1"}, nil
}

// AddFile stores the complete stream. With lzip-v1 it compresses every file
// of at least 1024 bytes. A read error leaves the file out of the index.
func (t *Tree) AddFile(ctx context.Context, path string, r io.Reader, executable bool) error {
	if strings.IndexByte(path, 0) >= 0 {
		return erisfs.ErrInvalidPath
	}
	if r == nil {
		return erisfs.ErrInvalidEntry
	}
	if t.compress {
		if err := t.builder.CheckPath(path); err != nil {
			return err
		}
		var prefix [1024]byte
		n, err := streamio.ReadFull(ctx, r, prefix[:])
		if err != nil && err != io.EOF {
			return err
		}
		var content io.Reader = bytes.NewReader(prefix[:n])
		if err != io.EOF {
			content = io.MultiReader(content, r)
		}
		if n == len(prefix) {
			return t.builder.AddCompressedFile(ctx, path, content, executable)
		}
		r = content
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
