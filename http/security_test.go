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
					body, err := client.OpenContent(ctx, capability)
					if err == nil {
						_, err = io.Copy(io.Discard, body)
						_ = body.Close()
					}
					result <- err
					return
				}
				block, err := client.GetBlockContext(ctx, capability.Root.Reference)
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
					return blocks.GetBlock(ref)
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
		return blocks.GetBlock(ref)
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

func TestResponseErrorDoesNotExposeServerText(t *testing.T) {
	err := &ResponseError{StatusCode: 503, Status: "503 \x1b[2Jprivate server details"}
	if got := err.Error(); got != "eris http: response status 503 (Service Unavailable)" {
		t.Fatalf("unsafe error text: %q", got)
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
		block, err := client.GetBlock(ref)
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
		block, err := client.GetBlock(eris.Reference{})
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
	block, err := client.GetBlock(eris.Reference{})
	if block != nil || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("block size %d, error %v", len(block), err)
	}
}
