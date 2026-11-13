package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
	"github.com/asciimoth/goeris/internal/httpttl"
)

// Handler serves ERIS resources from an abstract block source. It processes
// only supported URNs at N2RPath. It delegates all other requests to Next.
// Callers can chain handlers for other URN types on the same endpoint.
//
// A nil Next handler is equivalent to net/http.NotFoundHandler.
// Requests with more than 16 range entries receive HTTP 416 before block I/O.
// Successful responses use a one-year immutable cache policy unless the caller
// has already set Cache-Control on the response writer.
type Handler struct {
	Blocks eris.BlockSource
	Next   nethttp.Handler
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
	if handler.Blocks == nil {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	ttl, err := httpttl.Parse(request.Header)
	if err != nil {
		writer.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	if strings.HasPrefix(request.URL.RawQuery, eris.ReferenceURNPrefix) {
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
	block, err := requestBlockSource{ctx: request.Context(), source: handler.Blocks}.GetBlock(ref, ttl)
	if err != nil {
		writeSourceError(writer, err)
		return
	}
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
	limit := handler.MaxContentSize
	if limit == 0 {
		limit = -1
	}
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	reader, err := erisreader.NewReaderWithLimit(capability, requestBlockSource{ctx: ctx, source: handler.Blocks}, limit, ttl)
	if err != nil {
		if errors.Is(err, erisreader.ErrTooLarge) {
			writer.WriteHeader(nethttp.StatusRequestEntityTooLarge)
			return
		}
		writeSourceError(writer, err)
		return
	}
	defer func() {
		// ServeContent can return on a failed response write while its
		// multipart goroutine is still reading a block. Cancel that read
		// before Close waits for the reader's lock.
		cancel()
		_ = reader.Close()
	}()
	setResourceHeaders(writer)
	stream := &checkedContent{ReadSeeker: reader}
	request = safeRangeRequest(request, reader.Size())
	nethttp.ServeContent(writer, request, "", time.Time{}, stream)
	if stream.failed.Load() {
		panic(nethttp.ErrAbortHandler)
	}
}

// ServeContent does not report copy errors. Abort the response so a failed
// block fetch cannot produce a complete multipart response with missing data.
type checkedContent struct {
	io.ReadSeeker
	// ServeContent reads multipart ranges in a separate goroutine. Record
	// errors here, then abort from the handler goroutine.
	failed atomic.Bool
}

func (reader *checkedContent) Read(buffer []byte) (int, error) {
	n, err := reader.ReadSeeker.Read(buffer)
	if err != nil && err != io.EOF {
		reader.failed.Store(true)
	}
	return n, err
}

// requestBlockSource adds request cancellation and preserves the block TTL.
type requestBlockSource struct {
	ctx    context.Context
	source eris.BlockSource
}

func (source requestBlockSource) GetBlock(ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	if err := source.ctx.Err(); err != nil {
		return nil, err
	}
	if contextual, ok := source.source.(eris.ContextBlockSource); ok {
		return contextual.GetBlockContext(source.ctx, ref, ttl)
	}
	return source.source.GetBlock(ref, ttl)
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
