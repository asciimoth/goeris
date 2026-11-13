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
// For untrusted requests, set Handler.MaxContentSize and configure server
// timeouts, header limits, and concurrency limits. A zero MaxContentSize allows
// unlimited decoded content. The handler scans the tree to find its size, even
// for HEAD and range requests. Requests with more than 16 range entries receive
// HTTP 416 before any block reads. Block sources must verify blocks as required by
// eris.BlockSource. Sources that implement eris.ContextBlockSource can stop
// pending reads when a request is canceled or a response write fails. Sources
// without context support must bound their own read time.
// For very large resources, the handler ignores ranges if their combined size
// could overflow the response length. Small ranges remain available.
//
// If a block read fails after response headers are sent, Handler panics with
// net/http.ErrAbortHandler to abort the response. Middleware must preserve this
// behavior. Errors before streaming starts produce an HTTP error status.
//
// Successful responses allow caching for one year by default. An outer handler
// can set Cache-Control (for example, "no-store" for sensitive decoded content)
// before calling Handler to replace this default.
//
// Configure client timeouts and redirect policy in the supplied HTTPClient.
// OpenContent returns unverified bytes and has no response size limit. An ERIS
// capability contains a decryption key; remote decoding sends that key in the
// URL query. Use a trusted resolver and protect request URLs in access logs.
// Redirects can send the key to another server in the URL or Referer header.
// Client wraps request errors to omit URLs and transport text from their Error
// output. GetBlock and OpenContent also wrap body errors. Resolve returns the
// original response body. Its errors and all unwrapped errors can still contain
// keys and untrusted server text.
//
// [ERIS over HTTP recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/http/index.md
package http
