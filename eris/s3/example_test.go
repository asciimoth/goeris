package s3_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/asciimoth/eris-tools/eris"
	eriss3 "github.com/asciimoth/eris-tools/eris/s3"
)

type exampleHTTPClient func(*http.Request) (*http.Response, error)

func (function exampleHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func ExampleClient() {
	capability, blocks, err := eris.Encode([]byte("hello"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		panic(err)
	}
	handler := eriss3.Handler{Blocks: blocks, Bucket: "eris"}

	// This executor calls the handler in memory. It does not open a socket.
	httpClient := exampleHTTPClient(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})
	store, err := eriss3.ParseStoreURL("s3://objects.example/eris/")
	if err != nil {
		panic(err)
	}
	client, err := eriss3.NewClient(httpClient, store, nil)
	if err != nil {
		panic(err)
	}
	block, err := client.GetBlockContext(context.Background(), capability.Root.Reference)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(block))

	// Output: 1024
}

func ExampleSignV4() {
	credentials := eriss3.Credentials{AccessKey: "writer", SecretKey: "secret"}
	now := time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC)
	request, err := http.NewRequest(http.MethodPut, "https://objects.example/eris/KEY", nil)
	if err != nil {
		panic(err)
	}
	if err := eriss3.SignV4(request, nil, credentials, "local", now); err != nil {
		panic(err)
	}
	authenticator := eriss3.V4Authenticator{
		Credentials: eriss3.CredentialSourceFunc(func(_ context.Context, accessKey string) (eriss3.Credentials, error) {
			if accessKey != credentials.AccessKey {
				return eriss3.Credentials{}, eriss3.ErrAuthentication
			}
			return credentials, nil
		}),
		Region: "local",
		Clock:  func() time.Time { return now },
	}
	identity, err := authenticator.Authenticate(request, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(identity)

	// Output: writer
}
