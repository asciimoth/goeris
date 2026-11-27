package fs

import (
	"context"
	"errors"
	"fmt"
)

// ResolvePath returns the absolute host path to the content of an uncompressed
// file, using its capability in the backing store. It follows links within the
// image as [FS.Open] does. The host file can have a different name and mode.
// The error matches [ErrNoHostPath] for directories, compressed files, and
// stores that return [errors.ErrUnsupported] for the lookup. On failure,
// ResolvePath returns an empty string and an [io/fs.PathError] for name.
func (fileSystem *FS) ResolvePath(ctx context.Context, name string) (string, error) {
	item, err := fileSystem.find(ctx, name, true)
	if err != nil {
		return "", pathError("resolvepath", name, err)
	}
	if item.isDir() || item.compression != Uncompressed {
		return "", pathError("resolvepath", name, ErrNoHostPath)
	}
	resolved, err := item.store.content.ResolvePath(ctx, item.capability)
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			err = fmt.Errorf("%w: %w", ErrNoHostPath, err)
		}
		return "", pathError("resolvepath", name, err)
	}
	return resolved, nil
}
