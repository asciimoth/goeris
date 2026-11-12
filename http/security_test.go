package http

import (
	"bytes"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
)

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
	if response.StatusCode != nethttp.StatusPartialContent || !bytes.Contains(body, content[:11]) || !bytes.Contains(body, content[1024:1031]) {
		t.Fatalf("invalid multipart response: %d %q", response.StatusCode, body)
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
