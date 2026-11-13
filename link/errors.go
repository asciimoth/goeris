package link

import (
	"context"
	"io"
	"io/fs"
	"syscall"

	"github.com/asciimoth/goeris"
)

// Keep backend details out of normal error text. A cause can contain private
// paths, URLs, credentials, or content. Unwrap retains it for explicit checks.
type linkOperationError struct {
	cause error
}

func (err *linkOperationError) Unwrap() error { return err.cause }

func (err *linkOperationError) Error() string {
	cause := err.cause
	// Inspect only known wrappers. Bound this walk because an invalid backend
	// error can contain a cycle. Never call an unknown Error or Unwrap method.
	for range 16 {
		switch wrapped := cause.(type) {
		case *fs.PathError:
			if wrapped == nil {
				return "eris link: operation failed"
			}
			cause = wrapped.Err
			continue
		case *linkOperationError:
			if wrapped == nil {
				return "eris link: operation failed"
			}
			cause = wrapped.cause
			continue
		}
		if knownLinkError(cause) {
			return cause.Error()
		}
		break
	}
	return "eris link: operation failed"
}

// These fixed messages contain no backend or input data. Wrapped and joined
// errors remain available through errors.Is and errors.As.
func knownLinkError(err error) bool {
	if _, ok := err.(syscall.Errno); ok {
		return true
	}
	switch err {
	case fs.ErrInvalid, fs.ErrPermission, fs.ErrExist, fs.ErrNotExist, fs.ErrClosed,
		io.EOF, io.ErrUnexpectedEOF, io.ErrNoProgress, io.ErrShortBuffer, io.ErrShortWrite,
		context.Canceled, context.DeadlineExceeded,
		ErrInvalidLink, ErrInvalidValue, ErrLinkTooLarge, ErrSizeMismatch,
		eris.ErrInvalidBlockSize, eris.ErrInvalidBlock, eris.ErrMissingBlock,
		eris.ErrInvalidPadding, eris.ErrInvalidNode, eris.ErrInvalidCapability,
		eris.ErrInvalidRootKey:
		return true
	}
	return false
}
