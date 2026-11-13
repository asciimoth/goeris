package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asciimoth/goeris"
)

// RequestAuthenticator authenticates a complete request body and returns an
// identity. Implementations must be safe for concurrent use.
type RequestAuthenticator interface {
	Authenticate(request *http.Request, payload []byte) (identity string, err error)
}

// RequestAuthenticatorFunc adapts a function to RequestAuthenticator.
type RequestAuthenticatorFunc func(*http.Request, []byte) (string, error)

// Authenticate implements RequestAuthenticator.
func (function RequestAuthenticatorFunc) Authenticate(request *http.Request, payload []byte) (string, error) {
	return function(request, payload)
}

// Authorizer decides whether identity can use method for a block. A nil
// Authorizer permits every authenticated write.
type Authorizer func(ctx context.Context, identity, method, bucket string, ref eris.Reference) bool

// Handler serves one fixed bucket. GET and HEAD are public. PUT requires Auth,
// public-read, and a Blocks value that also implements eris.BlockSink or
// eris.ContextBlockSink. PUT is disabled unless AllowPut is true.
// GET and HEAD requests with more than 16 range entries receive HTTP 416
// before storage access.
type Handler struct {
	Blocks eris.BlockSource
	Bucket string
	// AllowPut enables authenticated PUT requests. The zero value rejects PUT.
	AllowPut  bool
	Auth      RequestAuthenticator
	Authorize Authorizer
}

// ServeHTTP implements http.Handler.
func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler.Blocks == nil || !validBucket(handler.Bucket) {
		writeS3Error(writer, http.StatusInternalServerError, "InternalError", "The handler configuration is invalid.")
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		writeS3Error(writer, http.StatusBadRequest, "InvalidRequest", "Queries are not supported.")
		return
	}
	ref, ok := handler.reference(request.URL.Path)
	if !ok {
		writeS3Error(writer, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handler.get(writer, request, ref)
	case http.MethodPut:
		if !handler.AllowPut {
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			writeS3Error(writer, http.StatusMethodNotAllowed, "MethodNotAllowed", "The handler is read-only.")
			return
		}
		handler.put(writer, request, ref)
	default:
		allow := http.MethodGet + ", " + http.MethodHead
		if handler.AllowPut {
			allow += ", " + http.MethodPut
		}
		writer.Header().Set("Allow", allow)
		writeS3Error(writer, http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed.")
	}
}

func (handler Handler) reference(path string) (eris.Reference, bool) {
	prefix := "/" + handler.Bucket + "/"
	if !strings.HasPrefix(path, prefix) || strings.Contains(path[len(prefix):], "/") {
		return eris.Reference{}, false
	}
	ref, err := eris.ParseReference(path[len(prefix):])
	return ref, err == nil
}

func (handler Handler) get(writer http.ResponseWriter, request *http.Request, ref eris.Reference) {
	// ServeContent limits the sum of range lengths, but many small ranges
	// can still produce a large response through multipart headers.
	ranges := 0
	for _, value := range request.Header.Values("Range") {
		ranges += strings.Count(value, ",") + 1
		if ranges > 16 {
			writeS3Error(writer, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "Too many ranges were requested.")
			return
		}
	}
	block, err := handler.getBlock(request.Context(), ref)
	if err != nil {
		if errors.Is(err, eris.ErrMissingBlock) {
			writeS3Error(writer, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		} else {
			writeS3Error(writer, http.StatusInternalServerError, "InternalError", "The store could not read the block.")
		}
		return
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		writeS3Error(writer, http.StatusInternalServerError, "InternalError", "The store returned an invalid block.")
		return
	}
	writer.Header().Set("Content-Type", ContentType)
	writer.Header().Set("Cache-Control", "max-age=31536000, immutable")
	http.ServeContent(writer, request, ref.String(), time.Time{}, bytes.NewReader(block))
}

func (handler Handler) put(writer http.ResponseWriter, request *http.Request, ref eris.Reference) {
	if handler.Auth == nil {
		writeS3Error(writer, http.StatusUnauthorized, "AccessDenied", "Write authentication is required.")
		return
	}
	if request.Body == nil {
		writeS3Error(writer, http.StatusBadRequest, "InvalidRequest", "The request body is missing.")
		return
	}
	maxSize := int64(eris.BlockSize32KiB)
	if request.ContentLength > maxSize {
		writeS3Error(writer, http.StatusRequestEntityTooLarge, "EntityTooLarge", "The block is too large.")
		return
	}
	limit := maxSize + 1
	block, err := io.ReadAll(io.LimitReader(request.Body, limit))
	if err != nil {
		writeS3Error(writer, http.StatusBadRequest, "InvalidRequest", "The request body could not be read.")
		return
	}
	if int64(len(block)) == limit {
		writeS3Error(writer, http.StatusRequestEntityTooLarge, "EntityTooLarge", "The block is too large.")
		return
	}
	identity, err := handler.Auth.Authenticate(request, block)
	if err != nil {
		writeS3Error(writer, http.StatusForbidden, "SignatureDoesNotMatch", "The request signature is invalid.")
		return
	}
	if handler.Authorize != nil && !handler.Authorize(request.Context(), identity, request.Method, handler.Bucket, ref) {
		writeS3Error(writer, http.StatusForbidden, "AccessDenied", "The identity cannot write this block.")
		return
	}
	if !publicReadACL(request.Header) {
		writeS3Error(writer, http.StatusBadRequest, "InvalidArgument", "x-amz-acl must be public-read.")
		return
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		writeS3Error(writer, http.StatusBadRequest, "BadDigest", "The block does not match its key.")
		return
	}
	_, sinkOK := handler.Blocks.(eris.BlockSink)
	_, contextSinkOK := handler.Blocks.(eris.ContextBlockSink)
	if !sinkOK && !contextSinkOK {
		writeS3Error(writer, http.StatusInternalServerError, "InternalError", "The store is not writable.")
		return
	}
	if err := handler.putBlock(request.Context(), ref, block); err != nil {
		if errors.Is(err, eris.ErrInvalidBlock) {
			writeS3Error(writer, http.StatusBadRequest, "BadDigest", "The block does not match its key.")
		} else {
			writeS3Error(writer, http.StatusInternalServerError, "InternalError", "The store could not write the block.")
		}
		return
	}
	writer.WriteHeader(http.StatusOK)
}

func publicReadACL(headers http.Header) bool {
	found := false
	for name, values := range headers {
		if !strings.EqualFold(name, "x-amz-acl") {
			continue
		}
		if found || len(values) != 1 || values[0] != PublicReadACL {
			return false
		}
		found = true
	}
	return found
}

func (handler Handler) getBlock(ctx context.Context, ref eris.Reference) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source, ok := handler.Blocks.(eris.ContextBlockSource); ok {
		return source.GetBlockContext(ctx, ref)
	}
	return handler.Blocks.GetBlock(ref)
}

func (handler Handler) putBlock(ctx context.Context, ref eris.Reference, block []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stored eris.Reference
	var err error
	if contextual, ok := handler.Blocks.(eris.ContextBlockSink); ok {
		stored, err = contextual.PutBlockContext(ctx, block, ref)
	} else {
		stored, err = handler.Blocks.(eris.BlockSink).PutBlock(block, ref)
	}
	if err != nil {
		return err
	}
	if stored != ref {
		return errors.New("eris s3: block sink returned an unexpected reference")
	}
	return nil
}

type errorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeS3Error(writer http.ResponseWriter, status int, code, message string) {
	data, _ := xml.Marshal(errorResponse{Code: code, Message: message})
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}
