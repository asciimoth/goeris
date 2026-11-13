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

	"github.com/asciimoth/goeris"
)

type sourceFunc func(eris.Reference) ([]byte, error)

func (function sourceFunc) GetBlock(ref eris.Reference) ([]byte, error) { return function(ref) }

type storeFunc struct {
	get func(eris.Reference) ([]byte, error)
	put func(eris.Reference, []byte) error
}

type wrongReferenceStore struct{ eris.BlockSet }

func (wrongReferenceStore) PutBlock(_ []byte, expected ...eris.Reference) (eris.Reference, error) {
	ref := expected[0]
	ref[0] ^= 1
	return ref, nil
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

func (store *contextStore) PutBlockContext(ctx context.Context, block []byte, expected ...eris.Reference) (eris.Reference, error) {
	if ctx == nil || len(expected) != 1 || expected[0] != store.ref {
		return eris.Reference{}, errors.New("incorrect context write")
	}
	store.writes++
	store.block = append([]byte(nil), block...)
	return store.ref, nil
}

func (store storeFunc) GetBlock(ref eris.Reference) ([]byte, error) { return store.get(ref) }
func (store storeFunc) PutBlock(block []byte, expected ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, err
	}
	if len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	return ref, store.put(ref, block)
}

func TestHandlerReads(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := eris.BlockSet{ref: block}
	handler := Handler{Blocks: blocks, Bucket: "blocks"}
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

func TestHandlerRejectsInvalidStoredBlock(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	bad := append([]byte(nil), block...)
	bad[0] ^= 1
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		handler := Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
			return bad, nil
		}), Bucket: "blocks"}
		request := httptest.NewRequest(method, "https://example/blocks/"+ref.String(), nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError || recorder.Header().Get("Cache-Control") != "" {
			t.Errorf("%s returned status %d, headers %v", method, recorder.Code, recorder.Header())
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
		{"corrupt", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrInvalidBlock }), Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
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
		{"read only by default", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", Auth: allow}, base, 405},
		{"no auth", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true}, base, 401},
		{"bad auth", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: deny}, base, 403},
		{"denied", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow, Authorize: func(_ context.Context, _, _, _ string, _ eris.Reference) bool { return false }}, base, 403},
		{"bad acl", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request { request := base(); request.Header.Del("x-amz-acl"); return request }, 400},
		{"duplicate acl", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request { request := base(); request.Header.Add("x-amz-acl", "private"); return request }, 400},
		{"bad block", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, eris.BlockSize1KiB)))
			return request
		}, 400},
		{"too large", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, int(eris.BlockSize32KiB)+1)))
			return request
		}, 413},
		{"body error", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(errorReader{sentinel})
			return request
		}, 400},
		{"missing body", Handler{Blocks: eris.BlockSet{}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = nil
			return request
		}, 400},
		{"source only", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }), Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
		{"sink error", Handler{Blocks: storeFunc{get: func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }, put: func(eris.Reference, []byte) error { return sentinel }}, Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
		{"sink reports wrong reference", Handler{Blocks: wrongReferenceStore{make(eris.BlockSet)}, Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
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

func TestHandlerDefaultRejectsPutBeforeAuthentication(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := make(eris.BlockSet)
	authCalled := false
	handler := Handler{
		Blocks: blocks,
		Bucket: "blocks",
		Auth: RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) {
			authCalled = true
			return "writer", nil
		}),
	}
	request := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(block))
	request.Header.Set("x-amz-acl", "public-read")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" || authCalled {
		t.Fatalf("default PUT = status %d, Allow %q, auth called %t", recorder.Code, recorder.Header().Get("Allow"), authCalled)
	}
	if _, err := blocks.GetBlock(ref); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("default PUT stored block: %v", err)
	}
}

func TestHandlerAllowHeader(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	for _, test := range []struct {
		name     string
		allowPut bool
		allow    string
	}{
		{"read only", false, "GET, HEAD"},
		{"writable", true, "GET, HEAD, PUT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := Handler{Blocks: eris.BlockSet{ref: block}, Bucket: "blocks", AllowPut: test.allowPut}
			request := httptest.NewRequest(http.MethodDelete, "https://example/blocks/"+ref.String(), nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != test.allow {
				t.Fatalf("DELETE = status %d, Allow %q; want %q", recorder.Code, recorder.Header().Get("Allow"), test.allow)
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
		Blocks:   blocks,
		Bucket:   "blocks",
		AllowPut: true,
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

func TestHandlerRejectsInvalidBlockBeforeSink(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	store := &contextStore{ref: ref}
	handler := Handler{
		Blocks: store, Bucket: "blocks", AllowPut: true,
		Auth: RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) {
			return "writer", nil
		}),
	}
	bad := append([]byte(nil), block...)
	bad[0] ^= 1
	request := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(bad))
	request.Header.Set("x-amz-acl", "public-read")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || store.writes != 0 {
		t.Fatalf("invalid PUT returned status %d and called sink %d times", recorder.Code, store.writes)
	}
}

func TestHandlerContextStore(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	store := &contextStore{block: block, ref: ref}
	auth := RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) { return "writer", nil })
	handler := Handler{Blocks: store, Bucket: "blocks", AllowPut: true, Auth: auth}

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
		handler := Handler{Blocks: eris.BlockSet{ref: block}, Bucket: "blocks"}
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
