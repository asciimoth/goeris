package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func TestHandlerHugeContentRanges(t *testing.T) {
	// Fourteen stored blocks describe almost 4 EiB of decoded content.
	blocks := make(eris.BlockSet)
	node := make([]byte, int(eris.BlockSize1KiB))
	node[0] = 0x80
	var pair eris.Pair
	for level := eris.Level(0); level <= 13; level++ {
		block, next, err := eris.EncryptNode(node, eris.Secret{}, level)
		if err != nil {
			t.Fatal(err)
		}
		pair = next
		blocks[pair.Reference] = block
		for offset := 0; offset < len(node); offset += eris.PairSize {
			copy(node[offset:], pair.Reference[:])
			copy(node[offset+32:], pair.Key[:])
		}
	}
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 13, Root: pair}
	const size = int64(1<<62 - 1024)
	for _, test := range []struct {
		byteRange string
		status    int
	}{
		{"bytes=0-10,1024-1030", nethttp.StatusPartialContent},
		{"bytes=0-", nethttp.StatusPartialContent},
		{"bytes=0-,0-,0-", nethttp.StatusOK},
		// Overflow can also wrap back to a positive length.
		{"bytes=0-" + strings.Repeat(",0-", 4), nethttp.StatusOK},
		{"bytes=-4611686018427387904,-4611686018427387904,-4611686018427387904", nethttp.StatusOK},
		{fmt.Sprintf("bytes=0-%d,0-%d,0-%d", size-1, size-1, size-1), nethttp.StatusOK},
		{"bytes=bad,0-", nethttp.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(test.byteRange, func(t *testing.T) {
			request := httptest.NewRequest(nethttp.MethodHead, N2RPath+"?"+capability.String(), nil)
			request.Header.Set("Range", test.byteRange)
			recorder := httptest.NewRecorder()
			Handler{Blocks: blocks}.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status %d; want %d", recorder.Code, test.status)
			}
			if test.status == nethttp.StatusOK || test.status == nethttp.StatusPartialContent {
				length, err := strconv.ParseInt(recorder.Header().Get("Content-Length"), 10, 64)
				if err != nil || length < 0 {
					t.Fatalf("invalid Content-Length: %q", recorder.Header().Get("Content-Length"))
				}
				if test.status == nethttp.StatusOK && length != size {
					t.Fatalf("length %d; want %d", length, size)
				}
			}
			if request.Header.Get("Range") != test.byteRange {
				t.Fatal("handler changed the request headers")
			}
		})
	}
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil)
	request.Header.Set("Range", "bytes=0-10")
	recorder := httptest.NewRecorder()
	Handler{Blocks: blocks}.ServeHTTP(recorder, request)
	want := make([]byte, 11)
	want[0] = 0x80
	if recorder.Code != nethttp.StatusPartialContent || !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("small range of huge content: status %d, body %x", recorder.Code, recorder.Body.Bytes())
	}
}

func TestHandlerRangeLengthOverflowBoundary(t *testing.T) {
	for _, test := range []struct {
		value  string
		status int
	}{
		// The payload fits in int64, but the MIME headers do not.
		{"bytes=0-9223372036854775805,9223372036854775806-", nethttp.StatusOK},
		{"bytes=0-0,9223372036854775806-", nethttp.StatusPartialContent},
		{"bytes=-1,-1", nethttp.StatusPartialContent},
		{"bytes=9223372036854775807-,0-0", nethttp.StatusPartialContent},
		{"bytes=0-,0-,bad", nethttp.StatusRequestedRangeNotSatisfiable},
		{"bytes=0-,0-,-", nethttp.StatusRequestedRangeNotSatisfiable},
		{"bytes=0-,0-,-1-2", nethttp.StatusRequestedRangeNotSatisfiable},
		{"bytes=0-,0-,1-0", nethttp.StatusRequestedRangeNotSatisfiable},
		{"bytes=0-,0-,a-1", nethttp.StatusRequestedRangeNotSatisfiable},
		{"bytes=0-,0-,1-9223372036854775808", nethttp.StatusRequestedRangeNotSatisfiable},
	} {
		t.Run(test.value, func(t *testing.T) {
			request := httptest.NewRequest(nethttp.MethodHead, "http://example.test", nil)
			request.Header.Set("Range", test.value)
			recorder := httptest.NewRecorder()
			setResourceHeaders(recorder)
			content := io.NewSectionReader(bytes.NewReader(nil), 0, math.MaxInt64)
			nethttp.ServeContent(recorder, safeRangeRequest(request, math.MaxInt64), "", time.Time{}, content)
			if recorder.Code != test.status {
				t.Fatalf("status %d; want %d", recorder.Code, test.status)
			}
			if test.status == nethttp.StatusOK && recorder.Header().Get("Content-Length") != "9223372036854775807" {
				t.Fatalf("full response length %q", recorder.Header().Get("Content-Length"))
			}
		})
	}
}

func TestHandlerLimitsRangeCount(t *testing.T) {
	capability, blocks, _ := testData(t)
	for _, urn := range []string{capability.String(), capability.Root.Reference.URN()} {
		for _, method := range []string{nethttp.MethodGet, nethttp.MethodHead} {
			for _, count := range []int{1, 16, 17, 1024} {
				calls := 0
				handler := Handler{Blocks: sourceFunc(func(ref eris.Reference) ([]byte, error) {
					calls++
					return blocks.GetBlock(ref)
				})}
				request := httptest.NewRequest(method, N2RPath+"?"+urn, nil)
				request.Header.Set("Range", "bytes=0-0"+strings.Repeat(",0-0", count-1))
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				if count <= 16 {
					if recorder.Code != nethttp.StatusPartialContent {
						t.Fatalf("%s count %d: status %d", method, count, recorder.Code)
					}
				} else if recorder.Code != nethttp.StatusRequestedRangeNotSatisfiable || calls != 0 || recorder.Body.Len() != 0 {
					t.Fatalf("%s count %d: status %d, source calls %d, body bytes %d", method, count, recorder.Code, calls, recorder.Body.Len())
				}
			}
		}
	}
}

func TestHandlerCountsRangesAcrossHeaderFields(t *testing.T) {
	capability, _, _ := testData(t)
	handler := Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
		t.Fatal("excessive ranges accessed the block source")
		return nil, nil
	})}
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil)
	for range 2 {
		request.Header.Add("Range", "bytes=0-0"+strings.Repeat(",0-0", 8))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != nethttp.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status %d; want 416", recorder.Code)
	}
}

func TestHandlerContentLimit(t *testing.T) {
	capability, blocks, content := testData(t)
	for _, method := range []string{nethttp.MethodGet, nethttp.MethodHead} {
		for _, limit := range []int64{-1, 0, int64(len(content) - 1), int64(len(content)), int64(len(content) + 1)} {
			request := httptest.NewRequest(method, N2RPath+"?"+capability.String(), nil)
			response := httptest.NewRecorder()
			handler := Handler{Blocks: blocks, MaxContentSize: limit}
			handler.ServeHTTP(response, request)
			want := nethttp.StatusOK
			if limit < 0 {
				want = nethttp.StatusInternalServerError
			} else if limit > 0 && limit < int64(len(content)) {
				want = nethttp.StatusRequestEntityTooLarge
			}
			if response.Code != want {
				t.Fatalf("%s limit %d: status %d; want %d", method, limit, response.Code, want)
			}
			if want != nethttp.StatusOK || method == nethttp.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatal("unexpected content in response")
				}
			} else if !bytes.Equal(response.Body.Bytes(), content) {
				t.Fatal("content changed")
			}
		}
	}
}

func TestHandlerContentLimitAllowsEmptyContentAndBlockReads(t *testing.T) {
	capability, blocks, err := eris.Encode(nil, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	for _, urn := range []string{capability.String(), capability.Root.Reference.URN()} {
		request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+urn, nil)
		response := httptest.NewRecorder()
		Handler{Blocks: blocks, MaxContentSize: 1}.ServeHTTP(response, request)
		want := []byte(nil)
		if urn == capability.Root.Reference.URN() {
			want = blocks[capability.Root.Reference]
		}
		if response.Code != nethttp.StatusOK || !bytes.Equal(response.Body.Bytes(), want) {
			t.Fatalf("%s: status %d, body %d bytes", urn, response.Code, response.Body.Len())
		}
	}
}

func TestHandlerLimitRejectsRepeatedTreeBeforeExpansion(t *testing.T) {
	// Each parent repeats its child 16 times. A small block set can describe
	// a huge decoded resource. The limit must stop the initial tree scan.
	blocks := make(eris.BlockSet)
	node := make([]byte, int(eris.BlockSize1KiB))
	node[0] = 0x80
	block, pair, err := eris.EncryptNode(node, eris.Secret{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocks[pair.Reference] = block
	for level := eris.Level(1); level <= 8; level++ {
		for offset := 0; offset < len(node); offset += eris.PairSize {
			copy(node[offset:], pair.Reference[:])
			copy(node[offset+32:], pair.Key[:])
		}
		block, pair, err = eris.EncryptNode(node, eris.Secret{}, level)
		if err != nil {
			t.Fatal(err)
		}
		blocks[pair.Reference] = block
	}
	capability := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 8, Root: pair}
	calls := 0
	handler := Handler{MaxContentSize: 1024, Blocks: sourceFunc(func(ref eris.Reference) ([]byte, error) {
		calls++
		return blocks.GetBlock(ref)
	})}
	request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil)
	request.Header.Set("Range", "bytes=0-0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != nethttp.StatusRequestEntityTooLarge || response.Body.Len() != 0 || calls > 8 {
		t.Fatalf("status %d, body %d bytes, block calls %d", response.Code, response.Body.Len(), calls)
	}
}

func TestHandlerStopsCanceledTreeScan(t *testing.T) {
	capability, blocks, _ := testData(t)
	for _, canceledBeforeStart := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceledBeforeStart {
			cancel()
		}
		calls := 0
		handler := Handler{Blocks: sourceFunc(func(ref eris.Reference) ([]byte, error) {
			calls++
			cancel()
			return blocks.GetBlock(ref)
		})}
		request := httptest.NewRequest(nethttp.MethodGet, N2RPath+"?"+capability.String(), nil).WithContext(ctx)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		cancel()
		wantCalls := 1
		if canceledBeforeStart {
			wantCalls = 0
		}
		if calls != wantCalls || response.Code != nethttp.StatusInternalServerError || response.Body.Len() != 0 {
			t.Fatalf("canceled before start %v: calls %d, status %d, body %d bytes", canceledBeforeStart, calls, response.Code, response.Body.Len())
		}
	}
}
