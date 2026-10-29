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
	"time"

	"github.com/asciimoth/eris-tools/eris"
)

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type signerFunc func(*http.Request, []byte) error

func (function signerFunc) Sign(request *http.Request, payload []byte) error {
	return function(request, payload)
}

func testBlock(t *testing.T) (eris.Reference, []byte) {
	t.Helper()
	block, pair, err := eris.EncryptNode(make([]byte, eris.BlockSize1KiB), eris.Secret{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return pair.Reference, block
}

func testStore(t *testing.T) StoreURL {
	t.Helper()
	store, err := ParseStoreURL("s3://s3.example/blocks/")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func response(status int, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(body),
		Header:     make(http.Header),
	}
}

func TestClientRequests(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	signed := 0
	client, err := NewClient(httpClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	}), testStore(t), signerFunc(func(request *http.Request, payload []byte) error {
		signed++
		if request.Method == http.MethodPut &&
			(request.Header.Get("x-amz-acl") != "public-read" || !bytes.Equal(payload, block)) {
			t.Error("incorrect signed PUT")
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	put, err := client.NewRequest(context.Background(), http.MethodPut, ref, block)
	if err != nil || put.URL.String() != "https://s3.example/blocks/"+ref.String() || signed != 1 {
		t.Fatalf("PUT request = %v, %v, signed %d", put, err, signed)
	}
	get, err := client.NewRequest(context.Background(), http.MethodGet, ref, nil)
	if err != nil || signed != 2 || get.Method != http.MethodGet {
		t.Fatalf("GET request = %v, %v, signed %d", get, err, signed)
	}
}

func TestClientPublicReadIsUnsigned(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	client, err := NewClient(httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("x-amz-date") != "" {
			t.Error("public read contains authentication headers")
		}
		return response(http.StatusOK, bytes.NewReader(block)), nil
	}), testStore(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.GetBlock(ref)
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("GetBlock() = %x, %v", got, err)
	}
}

func TestClientEndToEnd(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	blocks := make(eris.BlockSet)
	credentials := Credentials{AccessKey: "writer", SecretKey: "secret", SessionToken: "token"}
	clock := func() time.Time { return testTime }
	auth := V4Authenticator{
		Credentials: CredentialSourceFunc(func(_ context.Context, accessKey string) (Credentials, error) {
			if accessKey != credentials.AccessKey {
				return Credentials{}, ErrAuthentication
			}
			return credentials, nil
		}),
		Region: "test-1",
		Clock:  clock,
	}
	handler := Handler{Blocks: blocks, Bucket: "blocks", Auth: auth}
	executor := httpClientFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})
	client, _ := NewClient(executor, testStore(t), StaticV4Signer{Credentials: credentials, Region: "test-1", Clock: clock})
	if err := client.PutBlock(ref, block); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetBlock(ref)
	if err != nil || !bytes.Equal(got, block) {
		t.Fatalf("GetBlock() = %x, %v", got, err)
	}
}

func TestClientErrors(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	store := testStore(t)
	if _, err := NewClient(nil, store, nil); !errors.Is(err, ErrNilHTTPClient) {
		t.Errorf("nil HTTP client: %v", err)
	}
	if _, err := NewClient(httpClientFunc(nil), StoreURL{}, nil); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("invalid store: %v", err)
	}
	client, _ := NewClient(httpClientFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, bytes.NewReader(block)), nil
	}), store, nil)
	if _, err := client.NewRequest(context.Background(), http.MethodPut, ref, block); !errors.Is(err, ErrAuthentication) {
		t.Errorf("unsigned PUT: %v", err)
	}
	if _, err := client.NewRequest(context.Background(), http.MethodPost, ref, nil); !errors.Is(err, ErrUnsupportedMethod) {
		t.Errorf("POST error = %v", err)
	}
	if _, err := client.NewRequest(context.Background(), http.MethodGet, ref, block); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("GET body: %v", err)
	}
	bad := append([]byte(nil), block...)
	bad[0]++
	signed, _ := NewClient(httpClientFunc(nil), store, signerFunc(func(*http.Request, []byte) error { return nil }))
	if _, err := signed.NewRequest(context.Background(), http.MethodPut, ref, bad); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Errorf("bad PUT block: %v", err)
	}
	sentinel := errors.New("sign")
	signed.signer = signerFunc(func(*http.Request, []byte) error { return sentinel })
	if _, err := signed.NewRequest(context.Background(), http.MethodGet, ref, nil); !errors.Is(err, sentinel) {
		t.Errorf("signing error: %v", err)
	}
	var nilClient *Client
	if _, err := nilClient.NewRequest(context.Background(), http.MethodGet, ref, nil); !errors.Is(err, ErrNilHTTPClient) {
		t.Errorf("nil client: %v", err)
	}
	if _, err := nilClient.GetBlockContext(context.Background(), ref); !errors.Is(err, ErrNilHTTPClient) {
		t.Errorf("nil client read: %v", err)
	}
	client.store = StoreURL{}
	if _, err := client.NewRequest(context.Background(), http.MethodGet, ref, nil); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("damaged client store: %v", err)
	}
	client.store = store
	// A nil context is invalid input and must reach the standard constructor.
	if _, err := client.NewRequest(nil, http.MethodGet, ref, nil); err == nil { //nolint:staticcheck
		t.Error("nil context accepted")
	}
	if got := (&ResponseError{StatusCode: 500, Status: "500 Internal Server Error"}).Error(); got != "eris s3: response status 500 Internal Server Error" {
		t.Errorf("ResponseError.Error() = %q", got)
	}
}

func TestClientResponseErrors(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	sentinel := errors.New("transport")
	tests := []struct {
		name string
		do   httpClientFunc
		want error
	}{
		{"transport", func(*http.Request) (*http.Response, error) { return nil, sentinel }, sentinel},
		{"missing", func(*http.Request) (*http.Response, error) { return response(404, strings.NewReader("")), nil }, eris.ErrMissingBlock},
		{"status", func(*http.Request) (*http.Response, error) { return response(500, strings.NewReader("")), nil }, &ResponseError{}},
		{"large", func(*http.Request) (*http.Response, error) {
			return response(200, bytes.NewReader(make([]byte, int(eris.BlockSize32KiB)+1))), nil
		}, ErrResponseTooLarge},
		{"invalid", func(*http.Request) (*http.Response, error) {
			return response(200, bytes.NewReader(make([]byte, eris.BlockSize1KiB))), nil
		}, eris.ErrInvalidBlock},
		{"read", func(*http.Request) (*http.Response, error) { return response(200, errorReader{sentinel}), nil }, sentinel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := NewClient(test.do, testStore(t), nil)
			_, err := client.GetBlock(ref)
			var responseErr *ResponseError
			if _, ok := test.want.(*ResponseError); ok {
				if !errors.As(err, &responseErr) || responseErr.StatusCode != 500 {
					t.Fatalf("GetBlock() = %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("GetBlock() = %v, want %v", err, test.want)
			}
		})
	}
	client, _ := NewClient(httpClientFunc(func(*http.Request) (*http.Response, error) {
		return response(200, bytes.NewReader(block)), nil
	}), testStore(t), nil)
	if got, err := client.GetBlockContext(context.Background(), ref); err != nil || !bytes.Equal(got, block) {
		t.Fatalf("valid GetBlockContext = %x, %v", got, err)
	}
}

func TestClientPutResponses(t *testing.T) {
	t.Parallel()
	ref, block := testBlock(t)
	sentinel := errors.New("failure")
	tests := []struct {
		do   httpClientFunc
		want error
	}{
		{func(*http.Request) (*http.Response, error) { return nil, sentinel }, sentinel},
		{func(*http.Request) (*http.Response, error) { return response(403, strings.NewReader("denied")), nil }, &ResponseError{}},
		{func(*http.Request) (*http.Response, error) { return response(200, errorReader{sentinel}), nil }, sentinel},
		{func(*http.Request) (*http.Response, error) { return response(200, strings.NewReader("ok")), nil }, nil},
	}
	for index, test := range tests {
		client, _ := NewClient(test.do, testStore(t), signerFunc(func(*http.Request, []byte) error { return nil }))
		err := client.PutBlockContext(context.Background(), ref, block)
		if test.want == nil && err != nil {
			t.Errorf("case %d: %v", index, err)
		} else if test.want != nil {
			var responseErr *ResponseError
			if _, ok := test.want.(*ResponseError); ok {
				if !errors.As(err, &responseErr) {
					t.Errorf("case %d: %v", index, err)
				}
			} else if !errors.Is(err, test.want) {
				t.Errorf("case %d: %v", index, err)
			}
		}
	}
	unsigned, _ := NewClient(httpClientFunc(nil), testStore(t), nil)
	if err := unsigned.PutBlockContext(context.Background(), ref, block); !errors.Is(err, ErrAuthentication) {
		t.Errorf("request construction error = %v", err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
