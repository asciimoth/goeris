package eris_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	erishttp "github.com/asciimoth/goeris/http"
	"github.com/asciimoth/goeris/s3"
)

func TestHTTPRootRoundTrip(t *testing.T) {
	for _, transport := range []string{"http", "s3"} {
		t.Run(transport, func(t *testing.T) {
			ctx := context.Background()
			block := make([]byte, eris.BlockSize1KiB)
			ref, _ := eris.ReferenceOf(block)
			terminal := &priorityProbe{BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
			target := eris.NewCombinedBlockStore(terminal)
			header := erishttp.RootHeader
			var handler http.Handler = erishttp.Handler{Blocks: target}
			creds := s3.Credentials{AccessKey: "test", SecretKey: "test-secret"}
			if transport == "s3" {
				header = s3.RootHeader
				handler = s3.Handler{Blocks: target, Bucket: "blocks", AllowPut: true,
					Auth: s3.V4Authenticator{Region: "test", Credentials: s3.CredentialSourceFunc(func(context.Context, string) (s3.Credentials, error) { return creds, nil })}}
			}
			var wantRequest, wantResponse bool
			requests := 0
			wire := ttlHTTPFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				value := request.Header.Get(header)
				if wantRequest && value != "true" || !wantRequest && len(request.Header.Values(header)) != 0 {
					t.Fatalf("request root: %v", request.Header)
				}
				if transport == "s3" && wantRequest && !strings.Contains(request.Header.Get("Authorization"), strings.ToLower(header)) {
					t.Fatal("root was not signed")
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				response := recorder.Result()
				if request.Method != http.MethodPut {
					value = response.Header.Get(header)
					if wantResponse && value != "true" || !wantResponse && len(response.Header.Values(header)) != 0 {
						t.Fatalf("response root: %v", response.Header)
					}
				}
				return response, nil
			})
			var client eris.BlockSource
			var sink eris.BlockSink = target
			if transport == "http" {
				base, _ := url.Parse("http://example/")
				c, err := erishttp.NewClient(wire, base)
				if err != nil {
					t.Fatal(err)
				}
				client = c
			} else {
				store, _ := s3.ParseStoreURL("s3://example/blocks/")
				c, err := s3.NewClient(wire, store, s3.StaticV4Signer{Credentials: creds, Region: "test"})
				if err != nil {
					t.Fatal(err)
				}
				client, sink = c, c
			}
			// A repeated write with false must not clear a stored root flag.
			for _, putRoot := range []bool{false, true, false} {
				wantRequest = putRoot
				if _, err := eris.PutBlock(ctx, sink, block, 0, eris.BlockOptions{Root: putRoot}, ref); err != nil {
					t.Fatal(err)
				}
				if terminal.writes[len(terminal.writes)-1].Root != putRoot {
					t.Fatal("put hint was lost")
				}
				wantResponse = wantResponse || putRoot
				for _, readRoot := range []bool{false, true} {
					wantRequest = readRoot
					data, err, options := eris.GetBlock(ctx, client, ref, 0, eris.BlockOptions{Root: readRoot})
					if err != nil || !bytes.Equal(data, block) || options.Root != wantResponse {
						t.Fatalf("get: %v, %v", err, options)
					}
					if terminal.reads[len(terminal.reads)-1].Root != readRoot {
						t.Fatal("get hint was lost")
					}
				}
			}
			// HEAD and range responses carry the same stored metadata.
			path := "http://example/uri-res/N2R?" + ref.URN()
			if transport == "s3" {
				path = "http://example/blocks/" + ref.String()
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				request := httptest.NewRequest(method, path, nil)
				request.Header.Set(header, "true")
				if method == http.MethodGet {
					request.Header.Set("Range", "bytes=0-9")
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK && recorder.Code != http.StatusPartialContent || recorder.Header().Get(header) != "true" {
					t.Fatalf("%s: %d, %v", method, recorder.Code, recorder.Header())
				}
			}
			if requests == 0 {
				t.Fatal("no requests")
			}
		})
	}
}

func TestHTTPRootOnReadError(t *testing.T) {
	for _, transport := range []string{"http", "s3"} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
			header := erishttp.RootHeader
			if transport == "s3" {
				header = s3.RootHeader
			}
			wire := ttlHTTPFunc(func(*http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				recorder.Header().Set(header, "true")
				recorder.Header().Set("Content-Type", "application/octet-stream")
				recorder.WriteHeader(status)
				// Even a successful status must fail block verification for this body.
				_, _ = recorder.Write([]byte("invalid block"))
				return recorder.Result(), nil
			})
			var client eris.BlockSource
			if transport == "http" {
				base, _ := url.Parse("http://example/")
				c, err := erishttp.NewClient(wire, base)
				if err != nil {
					t.Fatal(err)
				}
				client = c
			} else {
				store, _ := s3.ParseStoreURL("s3://example/blocks/")
				c, err := s3.NewClient(wire, store, nil)
				if err != nil {
					t.Fatal(err)
				}
				client = c
			}
			if _, err, options := eris.GetBlock(context.Background(), client, eris.Reference{}, 0, eris.BlockOptions{Root: true}); err == nil || options.Root {
				t.Fatalf("%s status %d: %v, %v", transport, status, err, options)
			}
		}
	}
}

func TestHTTPDecodedContentRoot(t *testing.T) {
	content := bytes.Repeat([]byte("root for all content blocks\n"), 200)
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	capability, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), content, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for ref, block := range blocks.Blocks {
		block.Options.Root = true
		blocks.Blocks[ref] = block
	}
	for _, high := range []bool{false, true} {
		terminal := &priorityProbe{BlockSet: blocks}
		handler := erishttp.Handler{Content: eris.NewBlockCapabilityStore(terminal)}
		request := httptest.NewRequest(http.MethodGet, "http://example/uri-res/N2R?"+capability.String(), nil)
		if high {
			request.Header.Set(erishttp.RootHeader, "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), content) {
			t.Fatalf("content response: status=%d", response.Code)
		}
		if len(terminal.reads) < 2 {
			t.Fatal("test did not fetch multiple blocks")
		}
		for _, options := range terminal.reads {
			if options.Root != high {
				t.Fatal("decoded content read lost root")
			}
		}
		if response.Header().Get(erishttp.RootHeader) != "" {
			t.Fatal("decoded content response reported aggregate root")
		}
	}
}
