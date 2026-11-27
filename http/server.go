package http

import (
	"bytes"
	"errors"
	"io"
	nethttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/httpoptions"
	"github.com/asciimoth/goeris/internal/httpttl"
)

// Handler serves ERIS blocks and decoded content. It processes
// only supported URNs at N2RPath. It delegates all other requests to Next.
// Callers can chain handlers for other URN types on the same endpoint.
//
// A nil Next handler is equivalent to net/http.NotFoundHandler.
// Requests with more than 16 range entries receive HTTP 416 before block I/O.
// Successful responses use a one-year immutable cache policy unless the caller
// has already set Cache-Control on the response writer.
type Handler struct {
	// Blocks serves encrypted blocks by reference.
	Blocks eris.BlockSource
	// Content serves decoded resources by capability.
	Content eris.CapabilityStore
	Next    nethttp.Handler
	// MaxContentSize limits the full decoded resource size, including for HEAD
	// and range requests. Zero disables the limit. A negative value is invalid.
	// Encrypted block requests are not subject to this limit.
	MaxContentSize int64
}

// ServeHTTP implements net/http.Handler.
func (handler Handler) ServeHTTP(writer nethttp.ResponseWriter, request *nethttp.Request) {
	if request.URL.Path != N2RPath || !supportedURN(request.URL.RawQuery) {
		handler.next().ServeHTTP(writer, request)
		return
	}
	if request.Method != nethttp.MethodGet && request.Method != nethttp.MethodHead {
		writer.Header().Set("Allow", nethttp.MethodGet+", "+nethttp.MethodHead)
		writer.WriteHeader(nethttp.StatusMethodNotAllowed)
		return
	}
	// ServeContent limits the sum of range lengths, but that does not limit
	// multipart headers or repeated block fetches for many small ranges.
	if tooManyRanges(request.Header.Values("Range")) {
		writer.WriteHeader(nethttp.StatusRequestedRangeNotSatisfiable)
		return
	}
	blockRequest := strings.HasPrefix(request.URL.RawQuery, eris.ReferenceURNPrefix)
	if (blockRequest && handler.Blocks == nil) || (!blockRequest && handler.Content == nil) {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	ttl, err := httpttl.Parse(request.Header)
	if err != nil {
		writer.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if blockRequest {
		handler.serveBlock(writer, request, ttl)
		return
	}
	handler.serveContent(writer, request, ttl)
}

func (handler Handler) serveBlock(writer nethttp.ResponseWriter, request *nethttp.Request, ttl eris.TTL) {
	ref, err := eris.ParseReferenceURN(request.URL.RawQuery)
	if err != nil {
		writer.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if err := request.Context().Err(); err != nil {
		writeSourceError(writer, err)
		return
	}
	block, err, options := eris.GetBlock(request.Context(), handler.Blocks, ref, ttl, httpoptions.Parse(request.Header, HighPriorityHeader, RootHeader))
	if err != nil {
		writeSourceError(writer, err)
		return
	}
	httpoptions.Set(writer.Header(), HighPriorityHeader, RootHeader, options)
	setResourceHeaders(writer)
	nethttp.ServeContent(writer, request, "", time.Time{}, bytes.NewReader(block))
}

func (handler Handler) serveContent(writer nethttp.ResponseWriter, request *nethttp.Request, ttl eris.TTL) {
	if handler.MaxContentSize < 0 {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	capability, err := eris.ParseURN(request.URL.RawQuery)
	if err != nil {
		writer.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if err := request.Context().Err(); err != nil {
		writeSourceError(writer, err)
		return
	}
	limit := handler.MaxContentSize
	if limit == 0 {
		limit = -1
	}
	reader, err := handler.Content.NewReaderWithLimit(request.Context(), capability, limit, ttl, httpoptions.Parse(request.Header, HighPriorityHeader, RootHeader))
	if err != nil {
		if errors.Is(err, eris.ErrDecodedTooLarge) {
			writer.WriteHeader(nethttp.StatusRequestEntityTooLarge)
			return
		}
		writeSourceError(writer, err)
		return
	}
	if reader == nil {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	response := &checkedResponseWriter{ResponseWriter: writer}
	content := &checkedContent{reader: reader}
	defer func() {
		if response.failed {
			// A multipart producer can still be in a block read. Return so
			// net/http can cancel the original request context, then let Close
			// wait for that read. Never replace the caller's context.
			go content.close()
			return
		}
		content.close()
	}()
	size := reader.Size()
	if size < 0 {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	if limit >= 0 && size > limit {
		writer.WriteHeader(nethttp.StatusRequestEntityTooLarge)
		return
	}
	setResourceHeaders(writer)
	// ServeContent can send seek errors to the client. Seek within the
	// known length locally and request only the selected bytes from the store.
	stream := io.NewSectionReader(content, 0, size)
	request = safeRangeRequest(request, size)
	nethttp.ServeContent(response, request, "", time.Time{}, stream)
	if content.failed.Load() {
		panic(nethttp.ErrAbortHandler)
	}
}

// checkedResponseWriter records write failures, including a panic in Write.
// Unwrap preserves access through net/http.ResponseController.
type checkedResponseWriter struct {
	nethttp.ResponseWriter
	failed bool
}

func (writer *checkedResponseWriter) Write(p []byte) (int, error) {
	failed := writer.failed
	writer.failed = true
	n, err := writer.ResponseWriter.Write(p)
	writer.failed = failed || err != nil || n != len(p)
	return n, err
}

func (writer *checkedResponseWriter) Unwrap() nethttp.ResponseWriter { return writer.ResponseWriter }

// ServeContent does not report copy errors. Abort the response so a failed
// block fetch cannot produce a complete multipart response with missing data.
type checkedContent struct {
	mu     sync.Mutex
	reader eris.CapabilityReader
	// ServeContent reads multipart ranges in a separate goroutine. Record
	// errors here, then abort from the handler goroutine.
	failed atomic.Bool
}

func (c *checkedContent) ReadAt(buffer []byte, offset int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reader == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := c.reader.ReadAt(buffer, offset)
	// The section requests only bytes within Size. ReaderAt may return EOF
	// with a complete final read. A short read means the content is incomplete.
	if n == len(buffer) && (err == nil || err == io.EOF) {
		return n, nil
	}
	c.failed.Store(true)
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return 0, err
}

func (c *checkedContent) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.reader.Close()
	c.reader = nil
}

func (handler Handler) next() nethttp.Handler {
	if handler.Next != nil {
		return handler.Next
	}
	return nethttp.NotFoundHandler()
}

func setResourceHeaders(writer nethttp.ResponseWriter) {
	writer.Header().Set("Content-Type", ContentType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if _, set := writer.Header()["Cache-Control"]; !set {
		writer.Header().Set("Cache-Control", "max-age=31536000, immutable")
	}
}

func writeSourceError(writer nethttp.ResponseWriter, err error) {
	if errors.Is(err, eris.ErrTTLExpired) {
		writer.WriteHeader(nethttp.StatusLoopDetected)
		return
	}
	if errors.Is(err, eris.ErrMissingBlock) {
		writer.WriteHeader(nethttp.StatusNotFound)
		return
	}
	writer.WriteHeader(nethttp.StatusInternalServerError)
}
