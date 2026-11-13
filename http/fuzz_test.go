package http

import (
	"bytes"
	"context"
	"io"
	"math"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func FuzzHugeContentRanges(f *testing.F) {
	for _, value := range []string{
		"bytes=0-,0-,0-", "bytes=-9223372036854775807,-9223372036854775807",
		"bytes=0-10,1024-1030", "bytes=0-,bad", "bytes=0-9223372036854775806,0-0",
	} {
		f.Add(value, int64(math.MaxInt64))
	}
	f.Fuzz(func(t *testing.T, value string, size int64) {
		if size < 0 || tooManyRanges([]string{value}) {
			return
		}
		request := httptest.NewRequest(nethttp.MethodHead, "http://example.test", nil)
		request.Header.Set("Range", value)
		recorder := httptest.NewRecorder()
		setResourceHeaders(recorder)
		// HEAD only needs the size. No huge buffer or tree is needed.
		content := io.NewSectionReader(bytes.NewReader(nil), 0, size)
		nethttp.ServeContent(recorder, safeRangeRequest(request, size), "", time.Time{}, content)
		if recorder.Code == nethttp.StatusOK || recorder.Code == nethttp.StatusPartialContent {
			length, err := strconv.ParseInt(recorder.Header().Get("Content-Length"), 10, 64)
			if err != nil || length < 0 {
				t.Fatalf("invalid response length: %q", recorder.Header().Get("Content-Length"))
			}
		}
		if request.Header.Get("Range") != value {
			t.Fatal("changed request headers")
		}
	})
}

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
	f.Add(nethttp.MethodPost, N2RPath, capability.String(), "")
	f.Add(nethttp.MethodGet, N2RPath, capability.String()+"&x=1", "")
	f.Add(nethttp.MethodGet, N2RPath, "urn:blake2b:bad", "")
	f.Add(nethttp.MethodGet, N2RPath, capability.String(), "bytes=9223372036854775808-")
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
		if recorder.Code == nethttp.StatusOK || recorder.Code == nethttp.StatusPartialContent {
			if path != N2RPath || !validURN(query) || (method != nethttp.MethodGet && method != nethttp.MethodHead) {
				t.Fatal("invalid request returned a resource")
			}
			if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("resource response does not disable content sniffing")
			}
			if method == nethttp.MethodHead && recorder.Body.Len() != 0 {
				t.Fatal("HEAD response contains a body")
			}
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
		block, err := client.GetBlockContext(context.Background(), capability.Root.Reference, 0)
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
