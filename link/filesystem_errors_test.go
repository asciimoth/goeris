package link_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/link"
)

type privateError struct{ detail string }

func (err *privateError) Error() string { return err.detail }

func TestFSErrorTextHidesBackendDetails(t *testing.T) {
	secret := &privateError{detail: "private-token-and-content"}
	privatePath := "/private/backend/path"
	for _, cause := range []error{
		secret,
		&fs.PathError{Op: "backend", Path: privatePath, Err: secret},
		fmt.Errorf("private URL %s: %w", privatePath, secret),
		errors.Join(fs.ErrPermission, secret),
	} {
		for _, operation := range []string{"open", "stat", "lstat", "readdir", "read", "readat"} {
			t.Run(operation, func(t *testing.T) {
				base := openFSFunc(func(string) (fs.File, error) { return nil, cause })
				wrapped := newLinkFS(t, base, eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}})
				var err error
				switch operation {
				case "open":
					_, err = wrapped.Open(context.Background(), "public")
				case "stat":
					_, err = wrapped.Stat(context.Background(), "public")
				case "lstat":
					_, err = wrapped.Lstat(context.Background(), "public")
				case "readdir":
					_, err = wrapped.ReadDir(context.Background(), "public")
				case "read", "readat":
					encoded, _ := encodeFSLink(t, []byte("content"))
					wrapped = newLinkFS(t, fstest.MapFS{"public": {Data: encoded}}, &countingSource{err: cause})
					if operation == "read" {
						_, err = wrapped.ReadFile(context.Background(), "public")
					} else {
						file, openErr := wrapped.Open(context.Background(), "public")
						if openErr != nil {
							t.Fatal(openErr)
						}
						defer func() { _ = file.Close() }()
						_, err = file.(io.ReaderAt).ReadAt(make([]byte, 1), 0)
					}
				}
				if err == nil {
					t.Fatal("missing backend error")
				}
				for _, format := range []string{"%s", "%v", "%+v"} {
					message := fmt.Sprintf(format, err)
					if strings.Contains(message, secret.detail) || strings.Contains(message, privatePath) {
						t.Fatalf("backend details in error: %s", message)
					}
					if !strings.Contains(message, operation+" public:") {
						t.Fatalf("missing public context: %s", message)
					}
				}
				var got *privateError
				if !errors.Is(err, cause) || !errors.As(err, &got) || got != secret {
					t.Fatal("lost backend error identity or type")
				}
			})
		}
	}
}

type panicMessageError struct{}

func (*panicMessageError) Error() string { panic("backend Error must not run") }

func TestFSErrorTextDoesNotCallUnknownMethods(t *testing.T) {
	cycle := &fs.PathError{Op: "private", Path: "/private"}
	cycle.Err = cycle
	for _, cause := range []error{&panicMessageError{}, (*fs.PathError)(nil), cycle} {
		base := openFSFunc(func(string) (fs.File, error) { return nil, cause })
		_, err := newLinkFS(t, base, eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}).Open(context.Background(), "public")
		if err == nil || err.Error() != "open public: eris link: operation failed" {
			t.Fatal("unsafe backend error text")
		}
	}
}

func TestFSErrorTextKeepsKnownFailures(t *testing.T) {
	for _, cause := range []error{fs.ErrInvalid, fs.ErrPermission, fs.ErrNotExist, io.ErrNoProgress, link.ErrInvalidLink, link.ErrSizeMismatch, eris.ErrMissingBlock, syscall.EACCES, syscall.ENOENT, syscall.EEXIST} {
		base := openFSFunc(func(string) (fs.File, error) {
			return nil, &fs.PathError{Op: "backend", Path: "/private", Err: cause}
		})
		_, err := newLinkFS(t, base, eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}).Open(context.Background(), "public")
		if err == nil || err.Error() != "open public: "+cause.Error() || !errors.Is(err, cause) {
			t.Fatalf("known failure: %v", err)
		}
		if errors.Is(cause, fs.ErrNotExist) && !os.IsNotExist(err) ||
			errors.Is(cause, fs.ErrPermission) && !os.IsPermission(err) ||
			errors.Is(cause, fs.ErrExist) && !os.IsExist(err) {
			t.Fatalf("legacy os error check failed: %v", err)
		}
	}
}

type sizedReaderStore struct {
	eris.CapabilityStore
	reader eris.CapabilityReader
}

func (s sizedReaderStore) NewReaderWithSize(context.Context, eris.Capability, int64, eris.BlockOptions) (eris.CapabilityReader, error) {
	return s.reader, nil
}

type testContentReader struct {
	*strings.Reader
	closed   bool
	closeErr error
}

func (r *testContentReader) Close() error {
	r.closed = true
	return r.closeErr
}

func TestResolvedCloseError(t *testing.T) {
	data, _ := encodeFSLink(t, []byte("file content"))
	cause := &privateError{detail: "private storage path"}
	store := sizedReaderStore{reader: &testContentReader{Reader: strings.NewReader("file content"), closeErr: cause}}
	image, err := link.NewFS(fstest.MapFS{"file": {Data: data}}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := image.Open(t.Context(), "file")
	if err != nil {
		t.Fatal(err)
	}
	err = file.Close()
	var pathError *fs.PathError
	if !errors.As(err, &pathError) || pathError.Path != "file" || pathError.Op != "close" ||
		!errors.Is(err, cause) || strings.Contains(err.Error(), cause.detail) {
		t.Fatalf("unexpected close error: %v", err)
	}
	if _, err := file.Stat(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("Stat after Close: %v", err)
	}
	if _, err := file.Read(make([]byte, 1)); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("Read after Close: %v", err)
	}
	if _, err := file.(io.ReaderAt).ReadAt(make([]byte, 1), 0); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("ReadAt after Close: %v", err)
	}
	if _, err := file.(io.Seeker).Seek(0, io.SeekStart); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("Seek after Close: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestOpenRejectsInvalidStoreReader(t *testing.T) {
	data, _ := encodeFSLink(t, []byte("file content"))
	base := fstest.MapFS{"file": {Data: data}}
	short := &testContentReader{Reader: strings.NewReader("file")}
	for _, tc := range []struct {
		reader eris.CapabilityReader
		want   error
	}{{nil, fs.ErrInvalid}, {short, link.ErrSizeMismatch}} {
		image, err := link.NewFS(base, sizedReaderStore{reader: tc.reader}, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if file, err := image.Open(t.Context(), "file"); file != nil || !errors.Is(err, tc.want) {
			t.Fatalf("Open returned %T and %v, want %v", file, err, tc.want)
		}
	}
	if !short.closed {
		t.Error("Open did not close the rejected reader")
	}
}
