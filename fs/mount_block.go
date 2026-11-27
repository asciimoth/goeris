package fs

import (
	"bytes"
	"context"
	stdfs "io/fs"
	"strings"
	"sync"

	"github.com/asciimoth/goeris"
)

// mountUpper folds only ASCII letters. Unicode case equivalents are not part
// of either base32 or hexadecimal and must not become valid hash names.
func mountUpper(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}, name)
}

func mountUpperByte(ch byte) byte {
	if ch >= 'a' && ch <= 'z' {
		return ch - ('a' - 'A')
	}
	return ch
}

// mountBlockPath recognizes flat hashes, the fixed 3/3/3/43 volume layout,
// and its virtual prefix directories. It rejects other splits, padding, and
// nonzero unused base32 bits. The public Reference parser remains strict.
func mountBlockPath(name string) (ref eris.Reference, directory, ok bool) {
	switch len(name) {
	case eris.ReferenceTextSize:
		ref, err := eris.ParseReference(mountUpper(name))
		return ref, false, err == nil
	case 3, 7, 11, eris.ReferenceTextSize + 3: // A volume prefix or a complete path.
	default:
		return ref, false, false
	}

	var hash [eris.ReferenceTextSize]byte
	n := 0
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i == 3 || i == 7 || i == 11 {
			if ch != '/' {
				return ref, false, false
			}
			continue
		}
		ch = mountUpperByte(ch)
		if (ch < 'A' || ch > 'Z') && (ch < '2' || ch > '7') {
			return ref, false, false
		}
		hash[n] = ch
		n++
	}
	if n < len(hash) {
		return ref, true, true
	}
	ref, err := eris.ParseReference(string(hash[:]))
	return ref, false, err == nil
}

// mountBlockFile holds one verified encrypted block. It never decrypts it.
// Like eris.Reader, it checks cancellation even for cached reads, permits
// concurrent calls, and gives each open handle an independent position.
type mountBlockFile struct {
	mu     sync.Mutex
	ctx    context.Context
	info   *node
	reader *bytes.Reader
	closed bool
}

func newMountBlockFile(ctx context.Context, item *node) *mountBlockFile {
	return &mountBlockFile{ctx: ctx, info: item, reader: bytes.NewReader(item.block)}
}

func (f *mountBlockFile) check() error {
	if f.closed {
		return stdfs.ErrClosed
	}
	return f.ctx.Err()
}

func (f *mountBlockFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return 0, err
	}
	return f.reader.Read(p)
}

func (f *mountBlockFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return 0, err
	}
	return f.reader.ReadAt(p, off)
}

func (f *mountBlockFile) Seek(off int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return 0, err
	}
	return f.reader.Seek(off, whence)
}

func (f *mountBlockFile) Stat() (stdfs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.check(); err != nil {
		return nil, err
	}
	return f.info, nil
}

func (f *mountBlockFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.reader = nil
	f.info = nil
	f.ctx = nil
	return nil
}

var _ ReadSeekCloser = (*mountBlockFile)(nil)
