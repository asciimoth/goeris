// Package http implements ERIS block and content retrieval over HTTP.
//
// It implements the /uri-res/N2R endpoint described by the [ERIS over HTTP
// recommendation].
//
// The package does not create an HTTP client or server. Client uses a
// caller-supplied HTTPClient. Handler is an http.Handler that callers can
// register in their own server. Both components support custom transports,
// including in-memory transports and Unix sockets.
//
// [ERIS over HTTP recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/http/index.md
package http
