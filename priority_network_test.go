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

func TestHTTPPriorityRoundTrip(t *testing.T) {
	for _, transport := range []string{"http", "s3"} {
		t.Run(transport, func(t *testing.T) {
			ctx := context.Background()
			block := make([]byte, eris.BlockSize1KiB)
			ref, _ := eris.ReferenceOf(block)
			terminal := &priorityProbe{BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
			target := eris.NewCombinedBlockStore(terminal)
			header := erishttp.HighPriorityHeader
			var handler http.Handler = erishttp.Handler{Blocks: target}
			creds := s3.Credentials{AccessKey: "test", SecretKey: "test-secret"}
			if transport == "s3" {
				header = s3.HighPriorityHeader
				handler = s3.Handler{Blocks: target, Bucket: "blocks", AllowPut: true,
					Auth: s3.V4Authenticator{Region: "test", Credentials: s3.CredentialSourceFunc(func(context.Context, string) (s3.Credentials, error) { return creds, nil })}}
			}
			var wantRequest, wantResponse bool
			requests := 0
			wire := ttlHTTPFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				value := request.Header.Get(header)
				if wantRequest && value != "true" || !wantRequest && len(request.Header.Values(header)) != 0 {
					t.Fatalf("request priority: %v", request.Header)
				}
				if transport == "s3" && wantRequest && !strings.Contains(request.Header.Get("Authorization"), strings.ToLower(header)) {
					t.Fatal("priority was not signed")
				}
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				response := recorder.Result()
				if request.Method != http.MethodPut {
					value = response.Header.Get(header)
					if wantResponse && value != "true" || !wantResponse && len(response.Header.Values(header)) != 0 {
						t.Fatalf("response priority: %v", response.Header)
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
			// A lower-priority repeated write must not clear a stored high priority.
			for _, putPriority := range []bool{false, true, false} {
				wantRequest = putPriority
				if _, err := eris.PutBlock(ctx, sink, block, 0, eris.BlockOptions{HighPriority: putPriority}, ref); err != nil {
					t.Fatal(err)
				}
				if terminal.writes[len(terminal.writes)-1].HighPriority != putPriority {
					t.Fatal("put hint was lost")
				}
				wantResponse = wantResponse || putPriority
				for _, readPriority := range []bool{false, true} {
					wantRequest = readPriority
					data, err, options := eris.GetBlock(ctx, client, ref, 0, eris.BlockOptions{HighPriority: readPriority})
					if err != nil || !bytes.Equal(data, block) || options.HighPriority != wantResponse {
						t.Fatalf("get: %v, %v", err, options)
					}
					if terminal.reads[len(terminal.reads)-1].HighPriority != readPriority {
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

func TestHTTPPriorityOnReadError(t *testing.T) {
	for _, transport := range []string{"http", "s3"} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
			header := erishttp.HighPriorityHeader
			if transport == "s3" {
				header = s3.HighPriorityHeader
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
			if _, err, options := eris.GetBlock(context.Background(), client, eris.Reference{}, 0, eris.BlockOptions{HighPriority: true}); err == nil || options.HighPriority {
				t.Fatalf("%s status %d: %v, %v", transport, status, err, options)
			}
		}
	}
}

func TestHTTPDecodedContentPriority(t *testing.T) {
	content := bytes.Repeat([]byte("priority for all content blocks\n"), 200)
	capability, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	for ref, block := range blocks.Blocks {
		block.Options.HighPriority = true
		blocks.Blocks[ref] = block
	}
	for _, high := range []bool{false, true} {
		terminal := &priorityProbe{BlockSet: blocks}
		handler := erishttp.Handler{Blocks: terminal}
		request := httptest.NewRequest(http.MethodGet, "http://example/uri-res/N2R?"+capability.String(), nil)
		if high {
			request.Header.Set(erishttp.HighPriorityHeader, "true")
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
			if options.HighPriority != high {
				t.Fatal("decoded content read lost priority")
			}
		}
		if response.Header().Get(erishttp.HighPriorityHeader) != "" {
			t.Fatal("decoded content response reported aggregate priority")
		}
	}
}
