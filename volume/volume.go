// Package volume stores encrypted ERIS blocks in a local directory and reads
// ERIS CBOR files. The root path must exist before Open is called. Writers use
// three levels of three-character Base32 prefixes. Readers also accept flat
// block files in directories. A regular file root is read-only.
// OpenChain combines a semicolon-separated path list in left-to-right order.
// An empty list selects the platform defaults returned by DefaultChainPaths.
// See specs/eris-volume.md for the storage format.
package volume

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/asciimoth/goeris"
)

// ErrNotDirectory means that a write was requested for a regular file root.
var ErrNotDirectory = errors.New("eris-volume: root is not a directory")

// Volume is a local block store. Use Open to create a usable Volume.
// Block operations support concurrent calls, including calls through separate
// handles to the same directory. Close only after all operations have finished.
// Volume is a terminal store: it ignores TTL and does not expire blocks.
type Volume struct {
	root    *os.Root
	archive *os.File
}

// Open opens an existing directory or regular file. It does not create the root
// or scan its contents. A file is opened read-only and parsed as ERIS CBOR on
// each GetBlock call. The caller must close the returned Volume.
func Open(name string) (*Volume, error) {
	// Reject unsupported types before opening them.
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		root, err := os.OpenRoot(directoryOpenPath(name))
		if err != nil {
			return nil, err
		}
		return &Volume{root: root}, nil
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	file, err := os.OpenFile(name, os.O_RDONLY|readOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return &Volume{archive: file}, nil
}

// Close releases the directory or file handle. It does not remove stored blocks.
func (v *Volume) Close() error {
	if v == nil || v.root == nil && v.archive == nil {
		return fs.ErrClosed
	}
	// Close cannot overlap block operations. Clear both handles so later calls
	// report ErrClosed before checking block data or the root type.
	defer func() {
		v.root = nil
		v.archive = nil
	}()
	if v.archive != nil {
		return v.archive.Close()
	}
	return v.root.Close()
}

// BlockPath returns the canonical path relative to the volume root, using
// the host path separator. It does not access the file system.
func BlockPath(ref eris.Reference) string {
	name := ref.String()
	return filepath.Join(name[:3], name[3:6], name[6:9], name[9:])
}

// GetBlock reads and verifies a block. It tries the flat path only when the
// canonical path is absent. For a file root, it searches the ERIS CBOR contents.
// Missing blocks match eris.ErrMissingBlock.
func (v *Volume) GetBlock(ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	return v.GetBlockContext(context.Background(), ref, ttl)
}

// GetBlockContext is GetBlock with cancellation checks between file operations.
// Cancellation cannot interrupt a file-system call that is already in progress.
func (v *Volume) GetBlockContext(ctx context.Context, ref eris.Reference, _ eris.TTL) ([]byte, error) {
	if err := v.check(ctx); err != nil {
		return nil, err
	}
	if v.archive != nil {
		return v.readCBOR(ctx, ref)
	}
	name := BlockPath(ref)
	block, err := v.read(ctx, name, ref)
	if errors.Is(err, fs.ErrNotExist) {
		if err := v.checkAbsentParents(ctx, name); err != nil {
			return nil, err
		}
		block, err = v.read(ctx, ref.String(), ref)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.Join(eris.ErrMissingBlock, err)
	}
	return block, err
}

// Windows can report a non-directory parent as fs.ErrNotExist. Check the
// parents before flat fallback so a blocked canonical path is not bypassed.
func (v *Volume) checkAbsentParents(ctx context.Context, name string) error {
	for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := v.root.Stat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &os.PathError{Op: "stat", Path: parent, Err: fs.ErrInvalid}
		}
		return nil
	}
	return nil
}

func (v *Volume) read(ctx context.Context, name string, ref eris.Reference) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Check before opening so an existing FIFO or device is not read as a block.
	info, err := v.root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, eris.ErrInvalidBlock
	}
	file, err := v.root.OpenFile(name, os.O_RDONLY|readOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, eris.ErrInvalidBlock
	}
	block, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, int64(eris.BlockSize32KiB)+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err
	}
	return block, nil
}

// PutBlock verifies and stores a block at its canonical path. An existing
// file at that path is replaced. Flat files are left in place.
// A regular file root returns ErrNotDirectory and is never changed.
// It syncs the block before rename and the directories afterward where supported.
// A directory-sync error can be returned after the complete block is visible.
func (v *Volume) PutBlock(block []byte, ttl eris.TTL, expected ...eris.Reference) (eris.Reference, error) {
	return v.PutBlockContext(context.Background(), block, ttl, expected...)
}

// PutBlockContext is PutBlock with cancellation checks between file operations.
// Cancellation cannot interrupt a file-system call that is already in progress.
// Once the final rename starts, the block can be stored despite cancellation.
func (v *Volume) PutBlockContext(ctx context.Context, block []byte, _ eris.TTL, expected ...eris.Reference) (eris.Reference, error) {
	if err := v.check(ctx); err != nil {
		return eris.Reference{}, err
	}
	if v.archive != nil {
		if _, err := v.archive.Stat(); err != nil {
			return eris.Reference{}, err
		}
		return eris.Reference{}, &os.PathError{Op: "putblock", Path: v.archive.Name(), Err: ErrNotDirectory}
	}
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, errors.Join(eris.ErrInvalidBlock, err)
	}
	if len(expected) > 1 || len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	name := BlockPath(ref)
	if err := v.root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return eris.Reference{}, err
	}
	// Keep the leaf directory open so temporary-file cleanup and publication
	// use the same directory even if an ancestor is moved.
	dir, err := v.root.OpenRoot(directoryOpenPath(filepath.Dir(name)))
	if err != nil {
		return eris.Reference{}, err
	}
	defer func() { _ = dir.Close() }()
	if err := write(ctx, dir, filepath.Base(name), block); err != nil {
		return eris.Reference{}, err
	}
	// Sync the leaf through its pinned handle, then its parents up to the root.
	// Include existing parents: another writer may have just created them.
	// Do not cancel after publication; finish the durability work if possible.
	if err := syncDirectory(dir, "."); err != nil {
		return eris.Reference{}, err
	}
	for parent := filepath.Dir(filepath.Dir(name)); ; parent = filepath.Dir(parent) {
		if err := syncDirectory(v.root, parent); err != nil {
			return eris.Reference{}, err
		}
		if parent == "." {
			break
		}
	}
	return ref, nil
}

func write(ctx context.Context, dir *os.Root, name string, block []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var file *os.File
	var temporary string
	for {
		temporary = ".eris-tmp-" + rand.Text()
		var err error
		file, err = dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	defer func() { _ = dir.Remove(temporary) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(block); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return dir.Rename(temporary, name)
}

func (v *Volume) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.root == nil && v.archive == nil {
		return fs.ErrClosed
	}
	return nil
}

var (
	_ eris.BlockStore         = (*Volume)(nil)
	_ eris.ContextBlockSource = (*Volume)(nil)
	_ eris.ContextBlockSink   = (*Volume)(nil)
	_ io.Closer               = (*Volume)(nil)
)
