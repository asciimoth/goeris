package s3

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func TestAuthenticationRejectsUnsignedTrailers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		trailer    string
		undeclared bool
	}{
		{"metadata", "X-Amz-Meta-Label", false},
		{"acl", "X-Amz-Acl", false},
		{"checksum", "X-Checksum", false},
		{"undeclared metadata", "X-Amz-Meta-Label", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte("body")
			request, err := http.NewRequest(http.MethodPut, "https://example/key", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			if err := SignV4(request, payload, testCredentials, "region", testTime); err != nil {
				t.Fatal(err)
			}
			// Add a trailer after signing, then use the HTTP parser to expose it
			// in Request.Trailer instead of Request.Header.
			request.ContentLength = -1
			request.TransferEncoding = []string{"chunked"}
			request.Trailer = http.Header{test.trailer: []string{"unsigned"}}
			var wire bytes.Buffer
			if err := request.Write(&wire); err != nil {
				t.Fatal(err)
			}
			data := wire.Bytes()
			if test.undeclared {
				data = bytes.ReplaceAll(data, []byte("Trailer: "+test.trailer+"\r\n"), nil)
			}
			received, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = received.Body.Close() }()
			body, err := io.ReadAll(received.Body)
			if err != nil || !bytes.Equal(body, payload) || received.Trailer.Get(test.trailer) != "unsigned" {
				t.Fatalf("read body = %q, trailers = %v, error = %v", body, received.Trailer, err)
			}
			auth := V4Authenticator{
				Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
					t.Error("unsigned trailers reached credential lookup")
					return testCredentials, nil
				}),
				Region: "region", Clock: func() time.Time { return testTime },
			}
			if _, err := auth.Authenticate(received, body); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("unsigned trailer authenticated: %v", err)
			}
		})
	}
}

func TestSigningRejectsTrailers(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*http.Request){
		func(request *http.Request) { request.Trailer = http.Header{"X-Checksum": nil} },
		func(request *http.Request) { request.Trailer = http.Header{"X-Checksum": []string{"value"}} },
		func(request *http.Request) { request.Header.Set("Trailer", "X-Checksum") },
		func(request *http.Request) { request.Header["trailer"] = []string{"X-Checksum"} },
	} {
		request, err := http.NewRequest(http.MethodPut, "https://example/key", nil)
		if err != nil {
			t.Fatal(err)
		}
		mutate(request)
		if _, _, err := CanonicalRequest(request, sha256Hex(nil)); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("canonical request with trailers: %v", err)
		}
		if err := SignV4(request, nil, testCredentials, "region", testTime); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("signed request with trailers: %v", err)
		}
	}
}

// endlessBody exposes a source that never reaches EOF. Reads must stop at the
// block limit even when a peer omits Content-Length or sends a false value.
type endlessBody struct {
	read   int
	closed bool
}

func (body *endlessBody) Read(data []byte) (int, error) {
	// Stop a broken test from allocating without a bound.
	if body.read >= 2*int(eris.BlockSize32KiB) {
		return 0, errors.New("test body read budget exceeded")
	}
	clear(data)
	body.read += len(data)
	return len(data), nil
}

func (body *endlessBody) Close() error {
	body.closed = true
	return nil
}

func TestHandlerBoundsBodyBeforeAuthentication(t *testing.T) {
	t.Parallel()
	ref, _ := testBlock(t)
	for _, length := range []int64{-1, int64(eris.BlockSize1KiB), int64(eris.BlockSize32KiB) + 1} {
		body := &endlessBody{}
		store := &contextStore{ref: ref}
		handler := Handler{
			Blocks: store, Bucket: "blocks", AllowPut: true,
			Auth: RequestAuthenticatorFunc(func(*http.Request, []byte) (string, error) {
				t.Error("oversized body reached authentication")
				return "writer", nil
			}),
		}
		request := httptest.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), body)
		request.ContentLength = length
		request.Header.Set("X-Amz-Acl", "public-read")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		wantRead := int(eris.BlockSize32KiB) + 1
		if length > int64(eris.BlockSize32KiB) {
			wantRead = 0
		}
		if recorder.Code != http.StatusRequestEntityTooLarge || body.read != wantRead || store.writes != 0 {
			t.Fatalf("length %d: status %d, read %d, writes %d", length, recorder.Code, body.read, store.writes)
		}
	}
}

func TestClientBoundsResponseBodies(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		body := &endlessBody{}
		client, err := NewClient(httpClientFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: body, ContentLength: -1}, nil
		}), testStore(t), signerFunc(func(*http.Request, []byte) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		if method == http.MethodGet {
			_, err = client.GetBlock(ref)
		} else {
			_, err = client.PutBlock(block)
		}
		if !errors.Is(err, ErrResponseTooLarge) || body.read != int(eris.BlockSize32KiB)+1 || !body.closed {
			t.Fatalf("%s: error %v, read %d, closed %t", method, err, body.read, body.closed)
		}
	}
}

func TestHandlerSignedChunkedPut(t *testing.T) {
	t.Parallel()
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		block := make([]byte, size)
		ref, err := eris.ReferenceOf(block)
		if err != nil {
			t.Fatal(err)
		}
		store := &contextStore{ref: ref}
		handler := Handler{
			Blocks: store, Bucket: "blocks", AllowPut: true,
			Auth: V4Authenticator{
				Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
					return testCredentials, nil
				}),
				Region: "region", Clock: func() time.Time { return testTime },
			},
		}
		request, err := http.NewRequest(http.MethodPut, "https://example/blocks/"+ref.String(), bytes.NewReader(block))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Amz-Acl", "public-read")
		request.ContentLength = -1
		request.TransferEncoding = []string{"chunked"}
		if err := SignV4(request, block, testCredentials, "region", testTime); err != nil {
			t.Fatal(err)
		}
		var wire bytes.Buffer
		if err := request.Write(&wire); err != nil {
			t.Fatal(err)
		}
		received, err := http.ReadRequest(bufio.NewReader(&wire))
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, received)
		_ = received.Body.Close()
		if recorder.Code != http.StatusOK || store.writes != 1 || !bytes.Equal(store.block, block) {
			t.Fatalf("size %d: status %d, writes %d", size, recorder.Code, store.writes)
		}
	}
}
