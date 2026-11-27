package eris

import (
	"context"
	"io"
	"io/fs"
	"math"
	"os"
	"sync"

	"github.com/asciimoth/goeris/internal/streamio"
)

type mountCapabilityStore struct{ root string }

// NewMountCapabilityStore reads decoded files from an ERIS mount. An empty
// root uses ERIS_MNT. If that variable is empty, it tries /n/eris, then /eris.
// ResolvePath looks up the canonical capability URN as a regular file under
// the selected root. It follows symbolic links. The mount verifies content.
// A failed lookup matches ErrMissingBlock only if every candidate is absent.
func NewMountCapabilityStore(root string) CapabilityStore { return &mountCapabilityStore{root} }

// mountFile receives reads within the length recorded at open. An early
// end of file means that the content no longer has that length.
type mountFile struct{ *os.File }

func (f mountFile) ReadAt(p []byte, offset int64) (int, error) {
	n, err := f.File.ReadAt(p, offset)
	if err == io.EOF {
		return n, ErrSizeMismatch
	}
	return n, err
}

type mountReader struct {
	mu      sync.Mutex
	file    *os.File
	ctx     context.Context
	section *io.SectionReader
}

func (r *mountReader) Size() int64 { return r.section.Size() }

func (r *mountReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, fs.ErrClosed
	}
	pos, err := r.section.Seek(offset, whence)
	if err != nil {
		return 0, fs.ErrInvalid
	}
	return pos, nil
}

func (r *mountReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *mountReader) readError() error {
	if r.file == nil {
		return fs.ErrClosed
	}
	return r.ctx.Err()
}

func (r *mountReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readError(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	return r.section.Read(p)
}

func (r *mountReader) ReadAt(p []byte, offset int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.readError(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, fs.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	return r.section.ReadAt(p, offset)
}

func (s *mountCapabilityStore) Encode(ctx context.Context, content []byte, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	return Capability{}, ErrReadOnly
}

func (s *mountCapabilityStore) EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error) {
	return Capability{}, ErrReadOnly
}

func (s *mountCapabilityStore) EncodeContent(ctx context.Context, content io.Reader, secret Secret, size BlockSize, options BlockOptions) (Capability, error) {
	return Capability{}, ErrReadOnly
}

func (s *mountCapabilityStore) EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, options BlockOptions) (Capability, error) {
	return Capability{}, ErrReadOnly
}

func (s *mountCapabilityStore) EncodeContentWithOptions(ctx context.Context, content io.Reader, secret Secret, options EncodeOptions) (Capability, error) {
	return Capability{}, ErrReadOnly
}

func (s *mountCapabilityStore) Decode(ctx context.Context, capability Capability, options BlockOptions) ([]byte, error) {
	return s.DecodeWithLimit(ctx, capability, math.MaxInt, options)
}

func (s *mountCapabilityStore) DecodeWithLimit(ctx context.Context, capability Capability, limit int64, options BlockOptions) ([]byte, error) {
	return decodeCapability(ctx, s, capability, limit, options)
}

func (s *mountCapabilityStore) NewReader(ctx context.Context, capability Capability, options BlockOptions) (CapabilityReader, error) {
	return s.NewReaderWithLimit(ctx, capability, -1, NoTTL, options)
}

func (s *mountCapabilityStore) NewReaderWithSize(ctx context.Context, capability Capability, size int64, options BlockOptions) (CapabilityReader, error) {
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, ErrInvalidSize
	}
	r, err := s.NewReader(ctx, capability, options)
	if err != nil {
		return nil, err
	}
	if r.Size() != size {
		_ = r.Close()
		return nil, ErrSizeMismatch
	}
	return r, nil
}

func (s *mountCapabilityStore) NewReaderWithLimit(ctx context.Context, capability Capability, limit int64, ttl TTL, options BlockOptions) (CapabilityReader, error) {
	r, err := openMountReader(ctx, capability, mountRoots(s.root), os.OpenFile)
	if err != nil {
		return nil, err
	}
	if limit >= 0 && r.Size() > limit {
		_ = r.Close()
		return nil, ErrDecodedTooLarge
	}
	return r, nil
}

// Check the opened file again because the mount entry can change after lookup.
func openMountReader(ctx context.Context, capability Capability, roots []string, open func(string, int, fs.FileMode) (*os.File, error)) (CapabilityReader, error) {
	return findMountEntry(capability, roots, func(path string) (CapabilityReader, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkMountFile(path, os.Stat); err != nil {
			return nil, err
		}
		return openMountFile(ctx, path, open)
	})
}

func openMountFile(ctx context.Context, path string, open func(string, int, fs.FileMode) (*os.File, error)) (CapabilityReader, error) {
	f, err := open(path, streamio.ReadFileFlags, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fs.ErrInvalid
	}
	return &mountReader{file: f, ctx: ctx, section: io.NewSectionReader(mountFile{f}, 0, info.Size())}, nil
}

func (s *mountCapabilityStore) Prefetch(ctx context.Context, capability Capability, ttl TTL, options BlockOptions) error {
	_, err := s.ResolvePath(ctx, capability)
	return err
}

func (s *mountCapabilityStore) ResolvePath(ctx context.Context, capability Capability) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return resolveMountPath(capability, s.root, os.Stat)
}
