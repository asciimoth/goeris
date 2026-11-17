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
	"github.com/asciimoth/goeris/internal/batch"
)

type sourceFunc func(eris.Reference) ([]byte, error)

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (function sourceFunc) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	blockResult, blockErr := function(ref)
	return blockResult, blockErr, eris.BlockOptions{}
}

type storeFunc struct {
	get func(eris.Reference) ([]byte, error)
	put func(eris.Reference, []byte) error
}

type wrongReferenceStore struct{ eris.BlockSet }

func (wrongReferenceStore) putBlock(ctx context.Context, _ []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
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

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (store *contextStore) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	if ctx == nil || ref != store.ref {
		return nil, errors.New("incorrect context read"), eris.BlockOptions{}
	}
	store.reads++
	return append([]byte(nil), store.block...), nil, eris.BlockOptions{}
}

func (store *contextStore) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	if ctx == nil || len(expected) != 1 || expected[0] != store.ref {
		return eris.Reference{}, errors.New("incorrect context write")
	}
	store.writes++
	store.block = append([]byte(nil), block...)
	return store.ref, nil
}

func (store *contextStore) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (store storeFunc) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	blockResult, blockErr := store.get(ref)
	return blockResult, blockErr, eris.BlockOptions{}
}
func (store storeFunc) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, err
	}
	if len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	return ref, store.put(ref, block)
}

func (store storeFunc) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

func TestHandlerReads(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block}}}
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

func TestHandlerRangeLimit(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, test := range []struct {
			name   string
			values []string
			status int
			reads  int
		}{
			{"single", []string{"bytes=0-0"}, http.StatusPartialContent, 1},
			{"at limit", []string{"bytes=" + strings.Repeat("0-0,", 15) + "0-0"}, http.StatusPartialContent, 1},
			{"over limit", []string{"bytes=" + strings.Repeat("0-0,", 16) + "0-0"}, http.StatusRequestedRangeNotSatisfiable, 0},
			{"empty ranges", []string{"bytes=" + strings.Repeat("-0,", 1023) + "-0"}, http.StatusRequestedRangeNotSatisfiable, 0},
			{"multiple fields", []string{"bytes=" + strings.Repeat("0-0,", 15) + "0-0", "bytes=0-0"}, http.StatusRequestedRangeNotSatisfiable, 0},
		} {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				reads := 0
				handler := Handler{Bucket: "blocks", Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
					reads++
					return block, nil
				})}
				request := httptest.NewRequest(method, "https://example/blocks/"+ref.String(), nil)
				request.Header["Range"] = test.values
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if recorder.Code != test.status || reads != test.reads {
					t.Fatalf("status %d, reads %d; want status %d, reads %d", recorder.Code, reads, test.status, test.reads)
				}
			})
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
		{"bad path", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/other/key", 404},
		{"bad key", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/blocks/bad", 404},
		{"extra path", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/blocks/a/b", 404},
		{"query", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String() + "?acl", 400},
		{"empty query marker", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String() + "?", 400},
		{"nil store", Handler{Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"missing", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 404},
		{"source error", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, sentinel }), Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"corrupt", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrInvalidBlock }), Bucket: "blocks"}, http.MethodGet, "/blocks/" + ref.String(), 500},
		{"method", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block}}}, Bucket: "blocks"}, http.MethodDelete, "/blocks/" + ref.String(), 405},
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
		{"read only by default", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", Auth: allow}, base, 405},
		{"no auth", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true}, base, 401},
		{"bad auth", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: deny}, base, 403},
		{"denied", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow, Authorize: func(_ context.Context, _, _, _ string, _ eris.Reference) bool { return false }}, base, 403},
		{"bad acl", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request { request := base(); request.Header.Del("x-amz-acl"); return request }, 400},
		{"duplicate acl", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request { request := base(); request.Header.Add("x-amz-acl", "private"); return request }, 400},
		{"case variant acl", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Header["x-amz-acl"] = []string{"private"}
			return request
		}, 400},
		{"bad block", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, eris.BlockSize1KiB)))
			return request
		}, 400},
		{"too large", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(bytes.NewReader(make([]byte, int(eris.BlockSize32KiB)+1)))
			return request
		}, 413},
		{"declared too large", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.ContentLength = int64(eris.BlockSize32KiB) + 1
			request.Body = io.NopCloser(errorReader{sentinel})
			return request
		}, 413},
		{"body error", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = io.NopCloser(errorReader{sentinel})
			return request
		}, 400},
		{"missing body", Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}, Bucket: "blocks", AllowPut: true, Auth: allow}, func() *http.Request {
			request := base()
			request.Body = nil
			return request
		}, 400},
		{"source only", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }), Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
		{"sink error", Handler{Blocks: storeFunc{get: func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }, put: func(eris.Reference, []byte) error { return sentinel }}, Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
		{"sink reports wrong reference", Handler{Blocks: wrongReferenceStore{eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}, Bucket: "blocks", AllowPut: true, Auth: allow}, base, 500},
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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
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
	if _, err, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{}); !errors.Is(err, eris.ErrMissingBlock) {
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
			handler := Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block}}}, Bucket: "blocks", AllowPut: test.allowPut}
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
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
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
	stored, err, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
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
		handler := Handler{Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block}}}, Bucket: "blocks"}
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

func TestHandlerCanceledStorage(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	for _, contextual := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
			ctx, cancel := context.WithCancel(context.Background())
			contextualStore := &contextStore{block: block, ref: ref}
			var store eris.BlockSource = storeFunc{
				get: func(eris.Reference) ([]byte, error) {
					t.Fatal("canceled request reached storage read")
					return nil, nil
				},
				put: func(eris.Reference, []byte) error {
					t.Fatal("canceled request reached storage write")
					return nil
				},
			}
			if contextual {
				store = contextualStore
			}
			handler := Handler{
				Blocks: store, Bucket: "blocks", AllowPut: true,
				Auth: RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) {
					// Cancellation can occur after the body has been read.
					cancel()
					return "writer", nil
				}),
			}
			request := httptest.NewRequest(method, "https://example/blocks/"+ref.String(), bytes.NewReader(block)).WithContext(ctx)
			request.Header.Set("x-amz-acl", "public-read")
			if method != http.MethodPut {
				cancel()
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			cancel()
			if recorder.Code != http.StatusInternalServerError || contextualStore.reads != 0 || contextualStore.writes != 0 {
				t.Fatalf("contextual=%t method=%s: status %d, reads %d, writes %d", contextual, method,
					recorder.Code, contextualStore.reads, contextualStore.writes)
			}
		}
	}
}

// BatchSize reports the maximum number of requests per call.
func (s sourceFunc) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s sourceFunc) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// BatchSize reports the maximum number of requests per call.
func (s wrongReferenceStore) BatchSize() int { return 1 }

// PutBlock processes requests in order and returns the completed prefix.
func (s wrongReferenceStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s *contextStore) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s *contextStore) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *contextStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s *contextStore) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s storeFunc) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s storeFunc) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s storeFunc) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s storeFunc) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}
