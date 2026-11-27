package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"testing"

	"github.com/asciimoth/goeris/internal/streamio"
)

func TestCapabilityReaderConstructors(t *testing.T) {
	data := []byte("reader content")
	source := newTestStore()
	c, err := source.EncodeRecommended(t.Context(), data, Secret{}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	chain := NewCombinedCapabilityStore(NewBlockCapabilityStore(BlockSet{}), source)
	ctx := t.Context()
	opts := BlockOptions{HighPriority: true}
	for _, tc := range []struct {
		name string
		open func() (CapabilityReader, error)
		want error
	}{
		{"fallback", func() (CapabilityReader, error) { return chain.NewReader(ctx, c, opts) }, nil},
		{"exact limit", func() (CapabilityReader, error) {
			return chain.NewReaderWithLimit(ctx, c, int64(len(data)), LocalTTL, opts)
		}, nil},
		{"below limit", func() (CapabilityReader, error) {
			return chain.NewReaderWithLimit(ctx, c, int64(len(data)-1), LocalTTL, opts)
		}, ErrDecodedTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := tc.open()
			if tc.want != nil {
				if r != nil || !errors.Is(err, tc.want) {
					t.Fatalf("open = %v, %v, want %v", r, err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("read = %q, %v", got, err)
			}
		})
	}

	// A sized reader can open on the empty first store. Its read error must
	// reach the caller even though the second store has the content.
	r, err := chain.NewReaderWithSize(ctx, c, int64(len(data)), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if n, err := r.Read(make([]byte, len(data))); n != 0 || !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("sized read from empty first store returned %d bytes, %v", n, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, store := range []CapabilityStore{source, NewMountCapabilityStore(t.TempDir())} {
		for _, tc := range []struct {
			ctx  context.Context
			c    Capability
			size int64
			want error
		}{
			{canceled, c, int64(len(data)), context.Canceled},
			{ctx, Capability{}, 0, ErrInvalidCapability},
			{ctx, c, -1, ErrInvalidSize},
		} {
			if r, err := store.NewReaderWithSize(tc.ctx, tc.c, tc.size, opts); r != nil || !errors.Is(err, tc.want) {
				t.Errorf("%T sized reader = %v, %v, want %v", store, r, err, tc.want)
			}
		}
		if got, err := store.Decode(ctx, Capability{}, opts); got != nil || !errors.Is(err, ErrInvalidCapability) {
			t.Errorf("%T invalid decode = %q, %v", store, got, err)
		}
	}
}

func TestMountReaderCancellation(t *testing.T) {
	c := pathTestCapability(t)
	mount := t.TempDir()
	writePathTestContent(t, mount, c)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, err := NewMountCapabilityStore(mount).NewReader(ctx, c, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	cancel()
	buf := make([]byte, 1)
	if n, err := r.Read(buf); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("Read = %d, %v", n, err)
	}
	if n, err := r.ReadAt(buf, 0); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("ReadAt = %d, %v", n, err)
	}
}

func TestMountFileChangedBeforeOpen(t *testing.T) {
	c := pathTestCapability(t)
	for _, tc := range []struct {
		name string
		want error
	}{
		{"removed", ErrMissingBlock}, {"directory", fs.ErrInvalid}, {"stat failed", fs.ErrClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mount := t.TempDir()
			writePathTestContent(t, mount, c)
			var opened *os.File
			r, err := openMountReader(t.Context(), c, []string{mount}, func(path string, flags int, mode fs.FileMode) (*os.File, error) {
				if flags != streamio.ReadFileFlags {
					t.Fatalf("mount open flags = %d, want %d", flags, streamio.ReadFileFlags)
				}
				if tc.name != "stat failed" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if tc.name == "directory" {
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
				}
				f, err := os.OpenFile(path, flags, mode)
				if err == nil {
					opened = f
					t.Cleanup(func() { _ = f.Close() })
					if tc.name == "stat failed" {
						if err := f.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				return f, err
			})
			if r != nil || !errors.Is(err, tc.want) {
				t.Fatalf("open = %v, %v, want %v", r, err, tc.want)
			}
			if opened != nil {
				if _, err := opened.Stat(); !errors.Is(err, fs.ErrClosed) {
					t.Fatalf("rejected file still open: %v", err)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("missing file lost filesystem error: %v", err)
			}
		})
	}
}

func TestMountReaderFallsBackAfterOpenFailure(t *testing.T) {
	c := pathTestCapability(t)
	roots := []string{t.TempDir(), t.TempDir()}
	first := writePathTestContent(t, roots[0], c)
	second := writePathTestContent(t, roots[1], c)
	var attempts []string
	r, err := openMountReader(t.Context(), c, roots, func(path string, flags int, mode fs.FileMode) (*os.File, error) {
		attempts = append(attempts, path)
		if path == first {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		return os.OpenFile(path, flags, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if !slices.Equal(attempts, []string{first, second}) {
		t.Fatalf("open attempts = %q", attempts)
	}
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "Hello, ERIS!\n" {
		t.Fatalf("read from second mount = %q, %v", data, err)
	}
}

// openedReaderStore supplies a reader after its opening size has been recorded.
type openedReaderStore struct {
	CapabilityStore
	reader CapabilityReader
}

func (s openedReaderStore) NewReaderWithLimit(context.Context, Capability, int64, TTL, BlockOptions) (CapabilityReader, error) {
	return s.reader, nil
}

func TestDecodeFileChangedAfterOpen(t *testing.T) {
	c := pathTestCapability(t)
	for _, tc := range []struct {
		name string
		want error
	}{{"truncated", ErrSizeMismatch}, {"read canceled", context.Canceled}, {"grew", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			mount := t.TempDir()
			path := writePathTestContent(t, mount, c)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := NewMountCapabilityStore(mount).NewReader(ctx, c, BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			switch tc.name {
			case "truncated":
				err = os.Truncate(path, r.Size()-1)
			case "grew":
				err = os.Truncate(path, r.Size()+1)
			case "read canceled":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := decodeCapability(t.Context(), openedReaderStore{reader: r}, c, r.Size(), BlockOptions{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("decode error = %v, want %v", err, tc.want)
			}
			if err == nil && string(data) != "Hello, ERIS!\n" || err != nil && data != nil {
				t.Fatalf("decoded data = %q", data)
			}
			if _, err := r.Seek(0, io.SeekStart); !errors.Is(err, fs.ErrClosed) {
				t.Fatalf("reader still open: %v", err)
			}
		})
	}
}

func TestMountReaderKeepsOpeningSize(t *testing.T) {
	c := pathTestCapability(t)
	mount := t.TempDir()
	path := writePathTestContent(t, mount, c)
	store := NewMountCapabilityStore(mount)
	const original = "Hello, ERIS!\n"
	size := int64(len(original))
	r, err := store.NewReaderWithLimit(t.Context(), c, size, NoTTL, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err := os.WriteFile(path, []byte(original+"more"), 0600); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if n, err := r.ReadAt(buf, size-1); n != 1 || err != io.EOF || buf[0] != '\n' {
		t.Errorf("ReadAt across opening size = %d, %v, %q", n, err, buf)
	}
	if data, err := io.ReadAll(r); err != nil || string(data) != original {
		t.Errorf("Read = %q, %v", data, err)
	}
	if end, err := r.Seek(0, io.SeekEnd); err != nil || end != size {
		t.Errorf("SeekEnd = %d, %v", end, err)
	}
}

func TestCapabilityReaderErrors(t *testing.T) {
	contentStore := newTestStore()
	c, err := contentStore.EncodeRecommended(t.Context(), []byte("Hello, ERIS!\n"), Secret{}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"blocks", "mount"} {
		t.Run(backend, func(t *testing.T) {
			store := contentStore
			if backend == "mount" {
				mount := t.TempDir()
				writePathTestContent(t, mount, c)
				store = NewMountCapabilityStore(mount)
			}
			r, err := store.NewReader(t.Context(), c, BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			if _, err := r.Seek(-1, io.SeekStart); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("%T seek: %v", store, err)
			}
			buf := make([]byte, 1)
			if _, err := r.ReadAt(buf, -1); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("%T read at negative offset: %v", store, err)
			}
			for _, pos := range []int64{r.Size(), r.Size() + 1} {
				if _, err := r.Seek(pos, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				if n, err := r.Read(nil); n != 0 || err != nil {
					t.Errorf("empty read at %d = %d, %v", pos, n, err)
				}
				if n, err := r.ReadAt(nil, pos); n != 0 || err != nil {
					t.Errorf("empty ReadAt at %d = %d, %v", pos, n, err)
				}
				if n, err := r.Read(buf); n != 0 || err != io.EOF {
					t.Errorf("read at %d = %d, %v", pos, n, err)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("%T repeated close: %v", store, err)
			}
			if _, err := r.Read(buf); !errors.Is(err, fs.ErrClosed) {
				t.Errorf("%T read after close: %v", store, err)
			}
			if _, err := r.ReadAt(buf, 0); !errors.Is(err, fs.ErrClosed) {
				t.Errorf("%T read at after close: %v", store, err)
			}
		})
	}
}
