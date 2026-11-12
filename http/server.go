package http

import (
	"bytes"
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/erisreader"
)

// Handler serves ERIS resources from an abstract block source. It processes
// only supported URNs at N2RPath. It delegates all other requests to Next.
// Callers can chain handlers for other URN types on the same endpoint.
//
// A nil Next handler is equivalent to net/http.NotFoundHandler.
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
	if handler.Blocks == nil {
		writer.WriteHeader(nethttp.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(request.URL.RawQuery, "urn:blake2b:") {
		handler.serveBlock(writer, request)
		return
	}
	handler.serveContent(writer, request)
}

func (handler Handler) serveBlock(writer nethttp.ResponseWriter, request *nethttp.Request) {
	ref, err := eris.ParseReferenceURN(request.URL.RawQuery)
	if err != nil {
		writer.WriteHeader(nethttp.StatusBadRequest)
		return
	}
	block, err := requestBlockSource{ctx: request.Context(), source: handler.Blocks}.GetBlock(ref)
	if err != nil {
		writeSourceError(writer, err)
		return
	}
	setResourceHeaders(writer)
	nethttp.ServeContent(writer, request, "", time.Time{}, bytes.NewReader(block))
}

func (handler Handler) serveContent(writer nethttp.ResponseWriter, request *nethttp.Request) {
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
	reader, err := erisreader.NewReaderWithLimit(capability, requestBlockSource{ctx: request.Context(), source: handler.Blocks}, limit)
	if err != nil {
		if errors.Is(err, erisreader.ErrTooLarge) {
			writer.WriteHeader(nethttp.StatusRequestEntityTooLarge)
			return
		}
		writeSourceError(writer, err)
		return
	}
	defer func() { _ = reader.Close() }()
	setResourceHeaders(writer)
	nethttp.ServeContent(writer, request, "", time.Time{}, reader)
}

type requestBlockSource struct {
	ctx    context.Context
	source eris.BlockSource
}

func (source requestBlockSource) GetBlock(ref eris.Reference) ([]byte, error) {
	if err := source.ctx.Err(); err != nil {
		return nil, err
	}
	if contextual, ok := source.source.(eris.ContextBlockSource); ok {
		return contextual.GetBlockContext(source.ctx, ref)
	}
	return source.source.GetBlock(ref)
}

func (handler Handler) next() nethttp.Handler {
	if handler.Next != nil {
		return handler.Next
	}
	return nethttp.NotFoundHandler()
}

func setResourceHeaders(writer nethttp.ResponseWriter) {
	writer.Header().Set("Content-Type", ContentType)
	writer.Header().Set("Cache-Control", "max-age=31536000, immutable")
}

func writeSourceError(writer nethttp.ResponseWriter, err error) {
	if errors.Is(err, eris.ErrMissingBlock) {
		writer.WriteHeader(nethttp.StatusNotFound)
		return
	}
	writer.WriteHeader(nethttp.StatusInternalServerError)
}
