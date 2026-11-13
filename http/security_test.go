package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func TestClientCancelsPartialResponse(t *testing.T) {
	capability, blocks, _ := testData(t)
	for _, decoded := range []bool{false, true} {
		t.Run(map[bool]string{false: "block", true: "content"}[decoded], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{})
			stopped := make(chan struct{})
			server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", ContentType)
				// Send only part of a block, then stall until cancellation.
				_, _ = w.Write(blocks[capability.Root.Reference][:16])
				w.(nethttp.Flusher).Flush()
				close(started)
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
					t.Error("server request context was not canceled")
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client, err := NewClient(server.Client(), base)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				if decoded {
					body, err := client.OpenContent(ctx, capability, 0)
					if err == nil {
						_, err = io.Copy(io.Discard, body)
						_ = body.Close()
					}
					result <- err
					return
				}
				block, err := client.GetBlockContext(ctx, capability.Root.Reference, 0)
				if block != nil {
					result <- errors.New("partial block accepted")
					return
				}
				result <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("response did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("read error = %v; want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read did not stop after cancellation")
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("server request did not stop")
			}
		})
	}
}

func TestHandlerAbortsFailedStream(t *testing.T) {
	capability, blocks, _ := testData(t)
	root, err := eris.DecryptNode(blocks[capability.Root.Reference], capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := eris.ParseInternalNode(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceErr := range []error{eris.ErrMissingBlock, io.EOF, errors.New("private store details")} {
		for _, byteRange := range []string{"", "bytes=0-10", "bytes=0-10,1024-1030"} {
			t.Run(sourceErr.Error()+byteRange, func(t *testing.T) {
				handler := Handler{Blocks: sourceFunc(func(ref eris.Reference) ([]byte, error) {
					if ref == pairs[0].Reference {
						return nil, sourceErr
					}
					return blocks.GetBlock(ref, 0)
				})}
				request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil)
				request.Header.Set("Range", byteRange)
				recorder := httptest.NewRecorder()
				defer func() {
					if got := recover(); got != nethttp.ErrAbortHandler {
						t.Errorf("panic = %v; want ErrAbortHandler", got)
					}
					if strings.Contains(recorder.Body.String(), sourceErr.Error()) {
						t.Error("source error exposed")
					}
				}()
				handler.ServeHTTP(recorder, request)
			})
		}
	}
}

func TestHandlerRejectsQuerySuffixesWithoutSourceAccess(t *testing.T) {
	capability, _, _ := testData(t)
	handler := Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
		t.Fatal("invalid query accessed block source")
		return nil, nil
	})}
	for _, urn := range []string{capability.String(), capability.Root.Reference.URN()} {
		for _, suffix := range []string{"&x=1", "%00", "=", strings.Repeat("A", 1<<20)} {
			request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+urn+suffix, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != nethttp.StatusBadRequest {
				t.Fatalf("status = %d", recorder.Code)
			}
		}
	}
}

func TestHandlerStreamFailureClosesResponse(t *testing.T) {
	capability, blocks, _ := testData(t)
	root, err := eris.DecryptNode(blocks[capability.Root.Reference], capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := eris.ParseInternalNode(root)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Blocks: sourceFunc(func(ref eris.Reference) ([]byte, error) {
		if ref == pairs[0].Reference {
			return nil, eris.ErrMissingBlock
		}
		return blocks.GetBlock(ref, 0)
	})})
	defer server.Close()
	for _, byteRange := range []string{"", "bytes=0-10", "bytes=0-10,1024-1030"} {
		request, err := nethttp.NewRequest(nethttp.MethodGet, server.URL+N2RPath+"?"+capability.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Range", byteRange)
		response, err := server.Client().Do(request)
		if err != nil {
			continue // The server can abort before it flushes the headers.
		}
		_, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr == nil {
			t.Fatalf("range %q: failed stream completed without an error", byteRange)
		}
	}
}

func TestHandlerMultipartContent(t *testing.T) {
	capability, blocks, content := testData(t)
	server := httptest.NewServer(Handler{Blocks: blocks})
	defer server.Close()
	request, err := nethttp.NewRequest(nethttp.MethodGet, server.URL+N2RPath+"?"+capability.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-10,1024-1030")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != nethttp.StatusPartialContent || response.ContentLength != int64(len(body)) {
		t.Fatalf("invalid multipart response: status %d, length %d, bytes %d", response.StatusCode, response.ContentLength, len(body))
	}
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/byteranges" || params["boundary"] == "" {
		t.Fatalf("invalid multipart content type: %q", response.Header.Get("Content-Type"))
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for i, want := range [][]byte{content[:11], content[1024:1031]} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(part)
		if err != nil || !bytes.Equal(got, want) || part.Header.Get("Content-Type") != ContentType {
			t.Fatalf("part %d: bytes %q, headers %v, error %v", i, got, part.Header, err)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("multipart ending = %v; want EOF", err)
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
}

func TestHandlerPreservesCachePolicy(t *testing.T) {
	capability, blocks, _ := testData(t)
	for _, urn := range []string{capability.String(), capability.Root.Reference.URN()} {
		for _, method := range []string{nethttp.MethodGet, nethttp.MethodHead} {
			for _, byteRange := range []string{"", "bytes=0-10", "bytes=0-10,20-30"} {
				request := httptest.NewRequest(method, N2RPath+"?"+urn, nil)
				request.Header.Set("Range", byteRange)
				recorder := httptest.NewRecorder()
				// An outer handler can restrict caching of sensitive content.
				recorder.Header().Add("Cache-Control", "private")
				recorder.Header().Add("Cache-Control", "no-store")
				Handler{Blocks: blocks}.ServeHTTP(recorder, request)
				wantStatus := nethttp.StatusOK
				if byteRange != "" {
					wantStatus = nethttp.StatusPartialContent
				}
				if recorder.Code != wantStatus {
					t.Fatalf("%s range %q: status %d", method, byteRange, recorder.Code)
				}
				if got := strings.Join(recorder.Result().Header.Values("Cache-Control"), ", "); got != "private, no-store" {
					t.Fatalf("%s range %q: cache policy changed to %q", method, byteRange, got)
				}
			}
		}
	}
}

func TestResponseErrorDoesNotExposeServerText(t *testing.T) {
	err := &ResponseError{StatusCode: 503, Status: "503 \x1b[2Jprivate server details"}
	if got := err.Error(); got != "eris http: response status 503 (Service Unavailable)" {
		t.Fatalf("unsafe error text: %q", got)
	}
}

func TestClientBodyErrorsDoNotExposeServerText(t *testing.T) {
	capability, _, _ := testData(t)
	base, _ := url.Parse("http://example.test")
	bodyErr := errors.New("private server text\x1b[2J")
	for _, decoded := range []bool{false, true} {
		body := &failingBody{err: bodyErr}
		client, err := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			res := response(nethttp.StatusOK, ContentType, nil)
			res.Body = body
			return res, nil
		}), base)
		if err != nil {
			t.Fatal(err)
		}
		check := func(err error) {
			t.Helper()
			if !errors.Is(err, bodyErr) || strings.Contains(err.Error(), bodyErr.Error()) {
				t.Errorf("decoded %v: unsafe body error: %v", decoded, err)
			}
		}
		if decoded {
			content, err := client.OpenContent(context.Background(), capability, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(content)
			check(err)
			check(content.Close())
		} else {
			block, err := client.GetBlock(capability.Root.Reference, 0)
			if block != nil {
				t.Fatal("accepted a failed block read")
			}
			check(err)
		}
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	}
}

type failingBody struct {
	err    error
	closed bool
}

func TestClientRejectsMalformedTrailerWithoutExposingText(t *testing.T) {
	capability, _, _ := testData(t)
	const privateText = "private-server-details"
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		conn, writer, err := w.(nethttp.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = writer.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nx\r\n0\r\n" + privateText + "\r\n\r\n")
		_ = writer.Flush()
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	client, err := NewClient(httpClient, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, decoded := range []bool{false, true} {
		if decoded {
			body, openErr := client.OpenContent(context.Background(), capability, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			_, err = io.ReadAll(body)
			_ = body.Close()
		} else {
			_, err = client.GetBlock(capability.Root.Reference, 0)
		}
		if err == nil || strings.Contains(err.Error(), privateText) {
			t.Fatalf("decoded %v: unsafe trailer error: %v", decoded, err)
		}
		cause := errors.Unwrap(err)
		if cause == nil || !strings.Contains(cause.Error(), privateText) {
			t.Fatalf("decoded %v: original trailer error is unavailable: %v", decoded, cause)
		}
	}
}

func (body *failingBody) Read([]byte) (int, error) { return 0, body.err }
func (body *failingBody) Close() error {
	body.closed = true
	return body.err
}

func TestClientRequestErrorsDoNotExposeURLs(t *testing.T) {
	capability, _, _ := testData(t)
	transportErr := errors.New("untrusted transport details\x1b[2J")
	base, _ := url.Parse("http://user:password@example.test")
	client, err := NewClient(&nethttp.Client{Transport: roundTripFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return nil, transportErr
	})}, base)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Resolve(context.Background(), capability.String(), 0)
	if err == nil || err.Error() != "eris http: request failed" {
		t.Fatalf("unsafe request error: %v", err)
	}
	var urlErr *url.Error
	if !errors.Is(err, transportErr) || !errors.As(err, &urlErr) {
		t.Fatal("request error lost its cause")
	}
	// URL parse errors also contain the full query, including the key.
	client, err = NewClient(nethttp.DefaultClient, &url.URL{Scheme: "http", Host: "example.test:bad"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.NewRequest(context.Background(), capability.String(), 0)
	if err == nil || err.Error() != "eris http: request failed" || !errors.As(err, &urlErr) {
		t.Fatalf("unsafe URL parse error: %v", err)
	}
}

func TestClientRejectsUnexpectedSuccessStatus(t *testing.T) {
	capability, blocks, _ := testData(t)
	ref := capability.Root.Reference
	base, _ := url.Parse("http://example.test")
	for _, status := range []int{201, 202, 204, 206, 299} {
		body := &trackingBody{Reader: bytes.NewReader(blocks[ref])}
		client, err := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			res := response(status, ContentType, nil)
			res.Body = body
			return res, nil
		}), base)
		if err != nil {
			t.Fatal(err)
		}
		block, err := client.GetBlock(ref, 0)
		var statusErr *ResponseError
		if block != nil || !errors.As(err, &statusErr) || statusErr.StatusCode != status {
			t.Fatalf("status %d: block %x, error %v", status, block, err)
		}
		if !body.closed {
			t.Fatalf("status %d: response body was not closed", status)
		}
	}
}

// endlessBody models a peer that continues to send data without an EOF.
type endlessBody struct {
	read   int
	closed bool
}

func (body *endlessBody) Read(buffer []byte) (int, error) {
	clear(buffer)
	body.read += len(buffer)
	return len(buffer), nil
}

func (body *endlessBody) Close() error {
	body.closed = true
	return nil
}

func TestClientBoundsBlockResponseReads(t *testing.T) {
	base, _ := url.Parse("http://example.test")
	maximum := int64(eris.BlockSize32KiB)
	for _, length := range []int64{-1, 0, 1, maximum, maximum + 1, math.MaxInt64} {
		body := new(endlessBody)
		client, err := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			res := response(nethttp.StatusOK, ContentType, nil)
			res.ContentLength = length
			res.Body = body
			return res, nil
		}), base)
		if err != nil {
			t.Fatal(err)
		}
		block, err := client.GetBlock(eris.Reference{}, 0)
		if block != nil || !errors.Is(err, ErrResponseTooLarge) || !body.closed {
			t.Fatalf("length %d: block size %d, error %v, closed %v", length, len(block), err, body.closed)
		}
		wantRead := int(maximum) + 1
		if length > maximum {
			wantRead = 0
		}
		if body.read != wantRead {
			t.Fatalf("length %d: read %d bytes; want %d", length, body.read, wantRead)
		}
	}
}

func TestClientBoundsDecompressedBlockResponse(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(make([]byte, int(eris.BlockSize32KiB)+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Error("request does not enable gzip decoding")
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client, err := NewClient(server.Client(), base)
	if err != nil {
		t.Fatal(err)
	}
	block, err := client.GetBlock(eris.Reference{}, 0)
	if block != nil || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("block size %d, error %v", len(block), err)
	}
}

type contextSourceFunc func(context.Context, eris.Reference) ([]byte, error)

func (source contextSourceFunc) GetBlock(ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	return source(context.Background(), ref)
}

func (source contextSourceFunc) GetBlockContext(ctx context.Context, ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	return source(ctx, ref)
}

type failedStreamWriter struct {
	*httptest.ResponseRecorder
	started <-chan struct{}
	ctx     context.Context
}

func (writer failedStreamWriter) Write([]byte) (int, error) {
	// The multipart producer can start the block read after the pipe has
	// delivered the part header, while the response write is still pending.
	select {
	case <-writer.started:
	case <-writer.ctx.Done():
	}
	return 0, io.ErrClosedPipe
}

func TestHandlerCancelsMultipartReadAfterWriteFailure(t *testing.T) {
	capability, blocks, _ := testData(t)
	root, err := eris.DecryptNode(blocks[capability.Root.Reference], capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := eris.ParseInternalNode(root)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	handler := Handler{Blocks: contextSourceFunc(func(ctx context.Context, ref eris.Reference) ([]byte, error) {
		if ref == pairs[0].Reference {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return blocks.GetBlock(ref, 0)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil).WithContext(ctx)
	request.Header.Set("Range", "bytes=0-10,1024-1030")
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		handler.ServeHTTP(failedStreamWriter{httptest.NewRecorder(), started, ctx}, request)
	}()
	select {
	case result := <-done:
		if result != nil && result != nethttp.ErrAbortHandler {
			t.Fatalf("unexpected panic: %v", result)
		}
		if ctx.Err() != nil {
			t.Fatal("handler canceled the caller's context")
		}
	case <-time.After(5 * time.Second):
		cancel() // Release the blocked read before reporting the failure.
		<-done
		t.Fatal("handler did not cancel the pending read after a response write failed")
	}
}
