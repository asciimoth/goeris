package http

import (
	"bytes"
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/asciimoth/goeris"
)

func FuzzResourceURL(f *testing.F) {
	capability, _, err := eris.Encode([]byte("fuzz content"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(capability.String())
	f.Add(capability.Root.Reference.URN())
	f.Add("urn:eris:bad")
	f.Fuzz(func(t *testing.T, urn string) {
		base, _ := url.Parse("memory://example.test/base?old=value#fragment")
		got, err := ResourceURL(base, urn)
		if err != nil {
			return
		}
		if got.Path != N2RPath || got.RawQuery != urn || base.Path != "/base" || !validURN(urn) {
			t.Fatalf("ResourceURL(%q) = %q", urn, got)
		}
	})
}

func FuzzHandler(f *testing.F) {
	capability, blocks, err := eris.Encode([]byte("fuzz content"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(nethttp.MethodGet, N2RPath, capability.String(), "")
	f.Add(nethttp.MethodGet, N2RPath, capability.String(), "bytes=0-1,3-4")
	f.Add(nethttp.MethodHead, N2RPath, capability.Root.Reference.URN(), "bytes=0-")
	f.Add(nethttp.MethodPost, "/other", "urn:other:value", "")
	f.Fuzz(func(t *testing.T, method, path, query, byteRange string) {
		if method == "" || bytes.ContainsAny([]byte(method), " \t\r\n") {
			return
		}
		target := &url.URL{Scheme: "http", Host: "example.test", Path: path, RawQuery: query}
		request := &nethttp.Request{Method: method, URL: target, Header: make(nethttp.Header), Body: nethttp.NoBody}
		request.Header.Set("Range", byteRange)
		recorder := httptest.NewRecorder()
		Handler{Blocks: blocks}.ServeHTTP(recorder, request)
		if recorder.Code < 100 || recorder.Code > 599 {
			t.Fatalf("invalid status %d", recorder.Code)
		}
	})
}

func FuzzClientGetBlock(f *testing.F) {
	capability, blocks, err := eris.Encode([]byte("fuzz content"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		f.Fatal(err)
	}
	valid := blocks[capability.Root.Reference]
	f.Add(ContentType, valid)
	f.Add("text/plain", []byte("bad"))
	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		base, _ := url.Parse("http://example.test")
		client, _ := NewClient(clientFunc(func(*nethttp.Request) (*nethttp.Response, error) {
			return response(200, contentType, bytes.NewReader(body)), nil
		}), base)
		block, err := client.GetBlockContext(context.Background(), capability.Root.Reference)
		if err == nil {
			if !bytes.Equal(block, valid) {
				t.Fatal("GetBlock accepted an incorrect block")
			}
			if _, readErr := io.ReadAll(bytes.NewReader(block)); readErr != nil {
				t.Fatal(readErr)
			}
		}
	})
}
