package s3

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidStoreURL means that a store URL is not canonical.
	ErrInvalidStoreURL = errors.New("eris s3: invalid store URL")
	// ErrNilHTTPClient means that NewClient received no HTTP client.
	ErrNilHTTPClient = errors.New("eris s3: nil HTTP client")
	// ErrInvalidCredentials means that SigV4 credentials or a region are not valid.
	ErrInvalidCredentials = errors.New("eris s3: invalid credentials")
	// ErrInvalidKey means that an object key is not a canonical ERIS reference.
	ErrInvalidKey = errors.New("eris s3: invalid object key")
	// ErrInvalidRequest means that a request cannot be canonicalized or signed.
	ErrInvalidRequest = errors.New("eris s3: invalid request")
	// ErrAuthentication means that request authentication failed.
	ErrAuthentication = errors.New("eris s3: authentication failed")
	// ErrResponseTooLarge means that an object is larger than an ERIS block.
	ErrResponseTooLarge = errors.New("eris s3: response is too large")
	// ErrUnsupportedMethod means that a client request method is not supported.
	ErrUnsupportedMethod = errors.New("eris s3: unsupported method")
	// ErrReadOnly means that a write was sent to a read-only handler.
	ErrReadOnly = errors.New("eris s3: handler is read-only")
)

// ResponseError reports a non-successful object-storage response.
type ResponseError struct {
	StatusCode int
	Status     string
}

// Error implements error.
func (err *ResponseError) Error() string {
	return fmt.Sprintf("eris s3: response status %s", err.Status)
}
