package http_test

import (
	"context"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"

	"github.com/asciimoth/goeris"
	erishttp "github.com/asciimoth/goeris/http"
)

type exampleRoundTripper func(*nethttp.Request) (*nethttp.Response, error)

func (function exampleRoundTripper) RoundTrip(request *nethttp.Request) (*nethttp.Response, error) {
	return function(request)
}

func ExampleClient() {
	content := []byte("hello")
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	store := eris.NewBlockCapabilityStore(blocks)
	capability, err := store.Encode(context.Background(), content, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		panic(err)
	}
	handler := erishttp.Handler{Content: store}

	// This transport calls the handler in memory. It does not open a socket.
	transport := exampleRoundTripper(func(request *nethttp.Request) (*nethttp.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})
	httpClient := &nethttp.Client{Transport: transport}
	baseURL, err := url.Parse("http://in-memory.invalid")
	if err != nil {
		panic(err)
	}
	client, err := erishttp.NewClient(httpClient, baseURL)
	if err != nil {
		panic(err)
	}

	body, err := client.OpenContent(context.Background(), capability, 0)
	if err != nil {
		panic(err)
	}
	decoded, err := io.ReadAll(body)
	if err != nil {
		panic(err)
	}
	if err := body.Close(); err != nil {
		panic(err)
	}
	fmt.Println(string(decoded))

	// Output: hello
}
