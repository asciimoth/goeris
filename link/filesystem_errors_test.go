package link_test

import (
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
				wrapped := newLinkFS(t, base, eris.BlockSet{})
				var err error
				switch operation {
				case "open":
					_, err = wrapped.Open("public")
				case "stat":
					_, err = wrapped.Stat("public")
				case "lstat":
					_, err = wrapped.Lstat("public")
				case "readdir":
					_, err = wrapped.ReadDir("public")
				case "read", "readat":
					encoded, _ := encodeFSLink(t, []byte("content"))
					wrapped = newLinkFS(t, fstest.MapFS{"public": {Data: encoded}}, &countingSource{err: cause})
					if operation == "read" {
						_, err = wrapped.ReadFile("public")
					} else {
						file, openErr := wrapped.Open("public")
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
		_, err := newLinkFS(t, base, eris.BlockSet{}).Open("public")
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
		_, err := newLinkFS(t, base, eris.BlockSet{}).Open("public")
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
