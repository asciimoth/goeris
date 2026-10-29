package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asciimoth/eris-tools/eris"
)

type sourceFunc func(eris.Reference) ([]byte, error)

func (function sourceFunc) GetBlock(ref eris.Reference) ([]byte, error) { return function(ref) }

type storeFunc struct {
	get func(eris.Reference) ([]byte, error)
	put func(eris.Reference, []byte) error
}

type contextStore struct {
	block  []byte
	ref    eris.Reference
	reads  int
	writes int
}

func (store *contextStore) GetBlock(eris.Reference) ([]byte, error) {
	return nil, errors.New("non-context read used")
}

func (store *contextStore) GetBlockContext(ctx context.Context, ref eris.Reference) ([]byte, error) {
	if ctx == nil || ref != store.ref {
		return nil, errors.New("incorrect context read")
	}
	store.reads++
	return append([]byte(nil), store.block...), nil
}

func (store *contextStore) PutBlockContext(ctx context.Context, ref eris.Reference, block []byte) error {
	if ctx == nil || ref != store.ref {
		return errors.New("incorrect context write")
	}
	store.writes++
	store.block = append([]byte(nil), block...)
	return nil
}

func (store storeFunc) GetBlock(ref eris.Reference) ([]byte, error) { return store.get(ref) }
func (store storeFunc) PutBlock(ref eris.Reference, block []byte) error {
	return store.put(ref, block)
}

func TestHandlerReads(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := eris.BlockSet{ref: block}
	handler := Handler{Blocks: blocks, Bucket: "blocks", ReadOnly: true}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "https://s3.example/blocks/"+ref.String(), nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		result := recorder.Result()
		_ = result.Body.Close()
		if result.StatusCode != 200 || result.Header.Get("Content-Type") != "application/octet-stream" ||
			result.Header.Get("Cache-Control") != "max-age=31536000, immutable" {
			t.Errorf("%s response = %d, %v", method, result.StatusCode, result.Header)
		}
		if method == http.MethodGet && !bytes.Equal(recorder.Body.Bytes(), block) {
			t.Errorf("GET body does not match")
		}
	}
}

func TestHandlerRoutingAndReadErrors(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	sentinel := errors.New("source")
	tests := []struct {
		name    string
		handler Handler
		method  string
		path    string
		status  int
	}{
		{"bad path", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, http.MethodGet, "/other/key", 404},
		{"bad key", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, http.MethodGet, "/blocks/bad", 404},
		{"extra path", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, http.MethodGet, "/blocks/a/b", 404},
		{"query", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String() + "?acl", 400},
		{"nil store", Handler{Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"missing", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 404},
		{"source error", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, sentinel }), Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"corrupt", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return make([]byte, eris.BlockSize1KiB), nil }), Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"method", Handler{Blocks: eris.BlockSet{ref: block}, Bucket: "blocks"}, http.MethodDelete, "/blocks/" + ref.String(), 405},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler.ServeHTTP(recorder, httptest.NewRequest(test.method, "https://example"+test.path, nil))
			if recorder.Code != test.status || recorder.Header().Get("Content-Type") != "application/xml" {
				t.Fatalf("response = %d, %v, %s", recorder.Code, recorder.Header(), recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "<Error>") {
				t.Fatal("response is not an S3 XML error")
			}
		})
	}
}

func TestHandlerWriteChecks(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	allow := RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) { return "writer", nil })
	deny := RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) { return "", ErrAuthentication })
	sentinel := errors.New("sink")
	base := func() *http.Request {
		request := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(block))
		request.Header.Set("x-amz-acl", "public-read")
		return request
	}
	tests := []struct {
		name    string
		handler Handler
		request func() *http.Request
		status  int
	}{
		{"read only", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", ReadOnly: true}, base, 405},
		{"no auth", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks"}, base, 401},
		{"bad auth", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: deny}, base, 403},
		{"denied", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow, Authorize: func(_ context.Context, _, _, _ string, _ eris.Reference) bool { return false }}, base, 403},
		{"bad acl", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, func() *http.Request { request := base(); request.Header.Del("x-amz-acl"); return request }, 400},
		{"bad block", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, eris.BlockSize1KiB)))
			return request
		}, 400},
		{"too large", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, int(eris.BlockSize32KiB)+1)))
			return request
		}, 413},
		{"body error", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(errorReader{sentinel})
			return request
		}, 400},
		{"missing body", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, func() *http.Request {
			request := base()
			request.Body = nil
			return request
		}, 400},
		{"source only", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }), Bucket: "blocks", Auth: allow}, base, 500},
		{"sink error", Handler{Blocks: storeFunc{get: func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }, put: func(eris.Reference, []byte) error { return sentinel }}, Bucket: "blocks", Auth: allow}, base, 500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler.ServeHTTP(recorder, test.request())
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestHandlerAuthenticatedWrite(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := make(eris.BlockSet)
	authorized := false
	handler := Handler{
		Blocks: blocks,
		Bucket: "blocks",
		Auth: RequestAuthenticatorFunc(func(_ *http.Request, payload []byte) (string, error) {
			if !bytes.Equal(payload, block) {
				t.Error("authenticator received incorrect payload")
			}
			return "writer", nil
		}),
		Authorize: func(_ context.Context, identity, method, bucket string, got eris.Reference) bool {
			authorized = identity == "writer" && method == http.MethodPut && bucket == "blocks" && got == ref
			return authorized
		},
	}
	request := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(block))
	request.Header.Set("x-amz-acl", "public-read")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != 200 || !authorized {
		t.Fatalf("response = %d, authorized %t: %s", recorder.Code, authorized, recorder.Body.String())
	}
	stored, err := blocks.GetBlock(ref)
	if err != nil || !bytes.Equal(stored, block) {
		t.Fatalf("stored block = %x, %v", stored, err)
	}
}

func TestHandlerContextStore(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	store := &contextStore{block: block, ref: ref}
	auth := RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) { return "writer", nil })
	handler := Handler{Blocks: store, Bucket: "blocks", Auth: auth}

	get := httptest.NewRequest(http.MethodGet, "https://example/blocks/"+ref.String(), nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK || store.reads != 1 {
		t.Fatalf("context read = status %d, calls %d", getRecorder.Code, store.reads)
	}

	put := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(block))
	put.Header.Set("x-amz-acl", "public-read")
	putRecorder := httptest.NewRecorder()
	handler.ServeHTTP(putRecorder, put)
	if putRecorder.Code != http.StatusOK || store.writes != 1 {
		t.Fatalf("context write = status %d, calls %d", putRecorder.Code, store.writes)
	}
}

func FuzzHandlerPath(f *testing.F) {
	block, pair, err := eris.EncryptNode(make([]byte, eris.BlockSize1KiB), eris.Secret{}, 0)
	if err != nil {
		f.Fatal(err)
	}
	ref := pair.Reference
	f.Add(http.MethodGet, "/blocks/"+ref.String())
	f.Add(http.MethodPut, "/blocks/bad")
	f.Fuzz(func(t *testing.T, method, path string) {
		handler := Handler{Blocks: eris.BlockSet{ref: block}, Bucket: "blocks", ReadOnly: true}
		request := httptest.NewRequest(http.MethodGet, "https://example/", nil)
		request.Method = method
		request.URL.Path = path
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code < 100 || recorder.Code > 599 {
			t.Fatalf("invalid status %d", recorder.Code)
		}
	})
}
