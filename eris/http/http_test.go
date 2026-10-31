package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/asciimoth/eris-tools/eris"
)

type clientFunc func(*nethttp.Request) (*nethttp.Response, error)

func (function clientFunc) Do(request *nethttp.Request) (*nethttp.Response, error) {
	return function(request)
}

type sourceFunc func(eris.Reference) ([]byte, error)

func (function sourceFunc) GetBlock(ref eris.Reference) ([]byte, error) {
	return function(ref)
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type trackingBody struct {
	io.Reader
	closed bool
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

func testData(t *testing.T) (eris.Capability, eris.BlockSet, []byte) {
	t.Helper()
	content := bytes.Repeat([]byte("ERIS over an injected HTTP transport. "), 40)
	capability, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	return capability, blocks, content
}

type contextSource struct {
	blocks       eris.BlockSet
	wantContext  context.Context
	contextCalls int
	plainCalls   int
}

type requestContextKey struct{}

func (source *contextSource) GetBlock(ref eris.Reference) ([]byte, error) {
	source.plainCalls++
	return source.blocks.GetBlock(ref)
}

func (source *contextSource) GetBlockContext(ctx context.Context, ref eris.Reference) ([]byte, error) {
	source.contextCalls++
	if ctx != source.wantContext {
		return nil, errors.New("incorrect request context")
	}
	return source.blocks.GetBlock(ref)
}

func TestResourceURL(t *testing.T) {
	t.Parallel()
	capability, _, _ := testData(t)
	base, _ := url.Parse("memory://resolver.example/old/path?old=query#fragment")
	got, err := ResourceURL(base, capability.String())
	if err != nil {
		t.Fatal(err)
	}
	want := "memory://resolver.example" + N2RPath + "?" + capability.String()
	if got.String() != want {
		t.Fatalf("ResourceURL() = %q, want %q", got, want)
	}
	if base.String() != "memory://resolver.example/old/path?old=query#fragment" {
		t.Fatal("ResourceURL changed base")
	}
	if _, err := ResourceURL(nil, capability.String()); !errors.Is(err, ErrNilBaseURL) {
		t.Fatalf("ResourceURL(nil) error = %v", err)
	}
	for _, urn := range []string{"", "urn:other:value", "urn:eris:bad", "urn:blake2b:bad"} {
		if _, err := ResourceURL(base, urn); !errors.Is(err, ErrInvalidURN) {
			t.Errorf("ResourceURL(%q) error = %v", urn, err)
		}
	}
}

func TestClientConstructionAndRequest(t *testing.T) {
	t.Parallel()
	capability, _, _ := testData(t)
	base, _ := url.Parse("http://example.test/base")
	executor := clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		return nil, errors.New("unused")
	})
	if _, err := NewClient(nil, base); !errors.Is(err, ErrNilHTTPClient) {
		t.Fatalf("NewClient(nil) error = %v", err)
	}
	if _, err := NewClient(executor, nil); !errors.Is(err, ErrNilBaseURL) {
		t.Fatalf("NewClient(nil URL) error = %v", err)
	}
	client, err := NewClient(executor, base)
	if err != nil {
		t.Fatal(err)
	}
	base.Host = "changed.test"
	request, err := client.NewRequest(context.Background(), capability.String())
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != nethttp.MethodGet || request.URL.Host != "example.test" || request.URL.Path != N2RPath {
		t.Fatalf("NewRequest() = %s %s", request.Method, request.URL)
	}
	if _, err := client.NewRequest(context.Background(), "urn:eris:bad"); !errors.Is(err, ErrInvalidURN) {
		t.Fatalf("NewRequest(invalid) error = %v", err)
	}
	if _, err := (*Client)(nil).NewRequest(context.Background(), capability.String()); !errors.Is(err, ErrNilHTTPClient) {
		t.Fatalf("nil Client.NewRequest error = %v", err)
	}
}

func TestHandlerBlocksAndChaining(t *testing.T) {
	t.Parallel()
	capability, blocks, _ := testData(t)
	ref := capability.Root.Reference
	nextCalls := 0
	next := nethttp.HandlerFunc(func(writer nethttp.ResponseWriter, _ *nethttp.Request) {
		nextCalls++
		writer.WriteHeader(299)
	})
	handler := Handler{Blocks: blocks, Next: next}

	for _, target := range []string{"/other?" + ref.URN(), N2RPath + "?urn:other:value", N2RPath} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(nethttp.MethodGet, target, nil))
		if recorder.Code != 299 {
			t.Errorf("ServeHTTP(%q) status = %d", target, recorder.Code)
		}
	}
	if nextCalls != 3 {
		t.Fatalf("next handler calls = %d", nextCalls)
	}

	for _, method := range []string{nethttp.MethodGet, nethttp.MethodHead} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, N2RPath+"?"+ref.URN(), nil))
		result := recorder.Result()
		if result.StatusCode != nethttp.StatusOK || result.Header.Get("Content-Type") != ContentType ||
			result.Header.Get("Cache-Control") != "max-age=31536000, immutable" ||
			result.ContentLength != int64(len(blocks[ref])) {
			t.Errorf("%s block response = status %d, headers %#v", method, result.StatusCode, result.Header)
		}
		if method == nethttp.MethodGet && !bytes.Equal(recorder.Body.Bytes(), blocks[ref]) {
			t.Error("GET returned an incorrect block")
		}
		if method == nethttp.MethodHead && recorder.Body.Len() != 0 {
			t.Error("HEAD returned a body")
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+ref.URN(), nil)
	request.Header.Set("Range", "bytes=1-3")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != nethttp.StatusPartialContent || !bytes.Equal(recorder.Body.Bytes(), blocks[ref][1:4]) {
		t.Fatalf("range response = %d %x", recorder.Code, recorder.Body.Bytes())
	}
}

func TestHandlerContent(t *testing.T) {
	t.Parallel()
	capability, blocks, content := testData(t)
	handler := Handler{Blocks: blocks}
	for _, method := range []string{nethttp.MethodGet, nethttp.MethodHead} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, N2RPath+"?"+capability.String(), nil))
		result := recorder.Result()
		if result.StatusCode != nethttp.StatusOK || result.ContentLength != int64(len(content)) ||
			result.Header.Get("Content-Type") != ContentType {
			t.Errorf("%s content response = status %d, length %d", method, result.StatusCode, result.ContentLength)
		}
		if method == nethttp.MethodGet && !bytes.Equal(recorder.Body.Bytes(), content) {
			t.Errorf("GET content = %q", recorder.Body.Bytes())
		}
	}
}

func TestHandlerForwardsRequestContext(t *testing.T) {
	t.Parallel()
	capability, blocks, content := testData(t)
	ctx := context.WithValue(context.Background(), requestContextKey{}, "request")
	source := &contextSource{blocks: blocks, wantContext: ctx}
	handler := Handler{Blocks: source}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil).WithContext(ctx)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != nethttp.StatusOK || !bytes.Equal(recorder.Body.Bytes(), content) {
		t.Fatalf("response = %d, %q", recorder.Code, recorder.Body.Bytes())
	}
	if source.contextCalls == 0 || source.plainCalls != 0 {
		t.Fatalf("context calls = %d, plain calls = %d", source.contextCalls, source.plainCalls)
	}
}

func TestHandlerErrors(t *testing.T) {
	t.Parallel()
	capability, blocks, _ := testData(t)
	ref := capability.Root.Reference
	tests := []struct {
		name    string
		handler Handler
		method  string
		urn     string
		status  int
	}{
		{"default next", Handler{}, nethttp.MethodGet, "urn:other:value", nethttp.StatusNotFound},
		{"method", Handler{Blocks: blocks}, nethttp.MethodPost, ref.URN(), nethttp.StatusMethodNotAllowed},
		{"put rejected", Handler{Blocks: blocks}, nethttp.MethodPut, ref.URN(), nethttp.StatusMethodNotAllowed},
		{"nil source", Handler{}, nethttp.MethodGet, ref.URN(), nethttp.StatusInternalServerError},
		{"bad block URN", Handler{Blocks: blocks}, nethttp.MethodGet, "urn:blake2b:bad", nethttp.StatusBadRequest},
		{"bad capability URN", Handler{Blocks: blocks}, nethttp.MethodGet, "urn:eris:bad", nethttp.StatusBadRequest},
		{"missing block", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock })}, nethttp.MethodGet, ref.URN(), nethttp.StatusNotFound},
		{"wrapped missing", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
			return nil, errors.Join(errors.New("store"), eris.ErrMissingBlock)
		})}, nethttp.MethodGet, ref.URN(), nethttp.StatusNotFound},
		{"source failure", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, errors.New("store") })}, nethttp.MethodGet, ref.URN(), nethttp.StatusInternalServerError},
		{"corrupt block", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return make([]byte, eris.BlockSize1KiB), nil })}, nethttp.MethodGet, ref.URN(), nethttp.StatusInternalServerError},
		{"missing content", Handler{Blocks: eris.BlockSet{}}, nethttp.MethodGet, capability.String(), nethttp.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler.ServeHTTP(recorder, httptest.NewRequest(test.method, N2RPath+"?"+test.urn, nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if test.status == nethttp.StatusMethodNotAllowed && recorder.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", recorder.Header().Get("Allow"))
			}
		})
	}
}

func TestClientGetBlock(t *testing.T) {
	t.Parallel()
	capability, blocks, _ := testData(t)
	ref := capability.Root.Reference
	handler := Handler{Blocks: blocks}
	httpClient := &nethttp.Client{Transport: roundTripFunc(func(request *nethttp.Request) (*nethttp.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	base, _ := url.Parse("http://in-memory.invalid")
	client, _ := NewClient(httpClient, base)
	got, err := client.GetBlockContext(context.Background(), ref)
	if err != nil || !bytes.Equal(got, blocks[ref]) {
		t.Fatalf("GetBlockContext() = %x, %v", got, err)
	}
	got, err = client.GetBlock(ref)
	if err != nil || !bytes.Equal(got, blocks[ref]) {
		t.Fatalf("GetBlock() = %x, %v", got, err)
	}
}

func TestClientBlockSizeBoundaryAndClose(t *testing.T) {
	t.Parallel()
	capability, blocks, err := eris.Encode([]byte("large block"), eris.Secret{}, eris.BlockSize32KiB)
	if err != nil {
		t.Fatal(err)
	}
	ref := capability.Root.Reference
	body := &trackingBody{Reader: bytes.NewReader(blocks[ref])}
	base, _ := url.Parse("http://example.test")
	client, _ := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		response := response(nethttp.StatusOK, ContentType, strings.NewReader(""))
		response.Body = body
		return response, nil
	}), base)
	got, err := client.GetBlock(ref)
	if err != nil || !bytes.Equal(got, blocks[ref]) {
		t.Fatalf("GetBlock() = %x, %v", got, err)
	}
	if !body.closed {
		t.Fatal("GetBlock did not close the response body")
	}
}

type roundTripFunc func(*nethttp.Request) (*nethttp.Response, error)

func (function roundTripFunc) RoundTrip(request *nethttp.Request) (*nethttp.Response, error) {
	return function(request)
}

func response(status int, contentType string, body io.Reader) *nethttp.Response {
	header := make(nethttp.Header)
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &nethttp.Response{
		StatusCode: status,
		Status:     nethttp.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(body),
	}
}

func TestClientGetBlockErrors(t *testing.T) {
	t.Parallel()
	capability, blocks, _ := testData(t)
	ref := capability.Root.Reference
	valid := blocks[ref]
	readErr := errors.New("read failed")
	requestErr := errors.New("request failed")
	tests := []struct {
		name string
		do   clientFunc
		want error
	}{
		{"request", func(*nethttp.Request) (*nethttp.Response, error) { return nil, requestErr }, requestErr},
		{"missing", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(404, "", strings.NewReader("")), nil
		}, eris.ErrMissingBlock},
		{"status", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(503, "", strings.NewReader("")), nil
		}, &ResponseError{}},
		{"no media type", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, "", bytes.NewReader(valid)), nil
		}, ErrUnexpectedContentType},
		{"bad media type", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, "text/plain", bytes.NewReader(valid)), nil
		}, ErrUnexpectedContentType},
		{"malformed media type", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, `application/octet-stream; bad`, bytes.NewReader(valid)), nil
		}, ErrUnexpectedContentType},
		{"read", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, ContentType, errorReader{readErr}), nil
		}, readErr},
		{"too large", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, ContentType, bytes.NewReader(make([]byte, int(eris.BlockSize32KiB)+1))), nil
		}, ErrResponseTooLarge},
		{"bad block", func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, ContentType, bytes.NewReader(make([]byte, eris.BlockSize1KiB))), nil
		}, eris.ErrInvalidBlock},
	}
	base, _ := url.Parse("http://example.test")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := NewClient(test.do, base)
			got, err := client.GetBlockContext(context.Background(), ref)
			if got != nil {
				t.Fatalf("block = %x", got)
			}
			var statusError *ResponseError
			if _, ok := test.want.(*ResponseError); ok {
				if !errors.As(err, &statusError) || statusError.StatusCode != 503 || statusError.Error() == "" {
					t.Fatalf("error = %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClientOpenContent(t *testing.T) {
	t.Parallel()
	capability, _, content := testData(t)
	base, _ := url.Parse("http://example.test")
	t.Run("success", func(t *testing.T) {
		client, _ := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, ContentType, bytes.NewReader(content)), nil
		}), base)
		body, err := client.OpenContent(context.Background(), capability)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(body)
		closeErr := body.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(got, content) {
			t.Fatalf("content = %q, read = %v, close = %v", got, readErr, closeErr)
		}
	})
	tests := []struct {
		name       string
		capability eris.Capability
		do         clientFunc
		want       error
	}{
		{"invalid capability", eris.Capability{}, func(*nethttp.Request) (*nethttp.Response, error) { t.Fatal("unexpected request"); return nil, nil }, ErrInvalidURN},
		{"request", capability, func(*nethttp.Request) (*nethttp.Response, error) { return nil, io.ErrUnexpectedEOF }, io.ErrUnexpectedEOF},
		{"missing", capability, func(*nethttp.Request) (*nethttp.Response, error) {
			return response(404, "", strings.NewReader("")), nil
		}, eris.ErrMissingBlock},
		{"status", capability, func(*nethttp.Request) (*nethttp.Response, error) {
			return response(500, "", strings.NewReader("")), nil
		}, &ResponseError{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := NewClient(test.do, base)
			body, err := client.OpenContent(context.Background(), test.capability)
			if body != nil {
				t.Fatal("OpenContent returned a body")
			}
			var statusError *ResponseError
			if _, ok := test.want.(*ResponseError); ok {
				if !errors.As(err, &statusError) || statusError.StatusCode != 500 {
					t.Fatalf("error = %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestClientContentBodyOwnership(t *testing.T) {
	t.Parallel()
	capability, _, content := testData(t)
	base, _ := url.Parse("http://example.test")

	successBody := &trackingBody{Reader: bytes.NewReader(content)}
	client, _ := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
		response := response(nethttp.StatusOK, ContentType, strings.NewReader(""))
		response.Body = successBody
		return response, nil
	}), base)
	body, err := client.OpenContent(context.Background(), capability)
	if err != nil || body != successBody || successBody.closed {
		t.Fatalf("OpenContent() = %#v, %v; closed = %v", body, err, successBody.closed)
	}
	if err := body.Close(); err != nil || !successBody.closed {
		t.Fatalf("Close() = %v; closed = %v", err, successBody.closed)
	}

	for _, status := range []int{nethttp.StatusNotFound, nethttp.StatusInternalServerError} {
		errorBody := &trackingBody{Reader: strings.NewReader("error")}
		client, _ := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			response := response(status, "text/plain", strings.NewReader(""))
			response.Body = errorBody
			return response, nil
		}), base)
		if body, err := client.OpenContent(context.Background(), capability); body != nil || err == nil {
			t.Fatalf("OpenContent(status %d) = %#v, %v", status, body, err)
		}
		if !errorBody.closed {
			t.Fatalf("OpenContent(status %d) did not close the response body", status)
		}
	}
}

func TestClientResolve(t *testing.T) {
	t.Parallel()
	capability, _, _ := testData(t)
	base, _ := url.Parse("http://example.test")
	want := response(299, "", strings.NewReader("body"))
	client, _ := NewClient(clientFunc(func(request *nethttp.Request) (*nethttp.Response, error) {
		if request.URL.RawQuery != capability.String() {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		return want, nil
	}), base)
	got, err := client.Resolve(context.Background(), capability.String())
	if err != nil || got != want {
		t.Fatalf("Resolve() = %#v, %v", got, err)
	}
	if got, err := client.Resolve(context.Background(), "urn:eris:bad"); got != nil || !errors.Is(err, ErrInvalidURN) {
		t.Fatalf("Resolve(invalid) = %#v, %v", got, err)
	}
}

func TestCloneURLUserInformation(t *testing.T) {
	t.Parallel()
	base, _ := url.Parse("http://user:pass@example.test")
	clone := cloneURL(base)
	if !reflect.DeepEqual(clone, base) || clone == base || clone.User == base.User {
		t.Fatalf("cloneURL() = %#v", clone)
	}
}
