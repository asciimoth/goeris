package url

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	eris "github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fetch"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/zpgo"
)

type transport func(*http.Request) (*http.Response, error)

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

type testFetcher struct {
	*Fetcher
	blocks eris.BlockSet
}

// Replace both dependencies when a test injects a capability-store failure.
func (f *testFetcher) setStore(store eris.CapabilityStore) {
	f.config.Store = store
	f.config.Images = erisfs.NewStore(store, eris.BlockOptions{})
}

func setup(t testing.TB, rt transport) *testFetcher {
	t.Helper()
	return setupWithLocks(t, rt, nil)
}

func setupWithLocks(t testing.TB, rt transport, locks fetch.LockDB) *testFetcher {
	t.Helper()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	f, err := New(Config{
		Client: &http.Client{Transport: rt},
		Store:  eris.NewBlockCapabilityStore(blocks),
		Locks:  locks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testFetcher{f, blocks}
}
func source(t testing.TB, desc string) fetch.Source {
	t.Helper()
	s, err := fetch.ParseSource(strings.NewReader(desc))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const description = "type url\nurl https://a.test/data\n"
const checksum = "checksum sha256:c0535e4be2b79ffd93291305436bf889314e4a3faec05ecffcbb7df31ad9e51a\n"

func TestVectors(t *testing.T) {
	for _, v := range []struct{ fields, cap string }{
		{"", "BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M"},
		{"name hello\n", "BIANO4IXQY4XU6EWRTRPBF6SMPKFQ5HUSYMIL2V7WNTBDS5WK7E4E32FKMDUIODQOEM2QUYS37TMRGZ6GCLKOR36VQQMGVU4LFVSR3CUJU"},
	} {
		var hosts []string
		f := setup(t, func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Fatal(r)
			}
			hosts = append(hosts, r.URL.Host)
			if r.URL.Host == "a.test" {
				return response(200, "wrong"), nil
			}
			return response(200, "Hello world!"), nil
		})
		var reports []string
		f.config.Progress = func(root ProgressNode) {
			entry := fmt.Sprintf("%s %d/%d", root.Name, root.Completed, root.Total)
			if root.Child != nil {
				entry += fmt.Sprintf(" > %s %d/%d", root.Child.Name, root.Child.Completed, root.Child.Total)
			}
			reports = append(reports, entry)
		}
		logs := 0
		f.config.Verbose = true
		f.config.Log = func(string) { logs++ }
		got := f.Fetch(t.Context(), source(t, description+"alt ftp://unused.test/foo\nalt http://b.test/x\n"+checksum+v.fields))
		if got.Cap.String() != "urn:eris:"+v.cap || fmt.Sprint(hosts) != "[a.test b.test]" || strings.Join(reports, "\n") != strings.Join([]string{
			"Checking URLs 0/3",
			"Checking URLs 0/3 > Downloading (0 B / 5 B) 0/100",
			"Checking URLs 0/3 > Downloading (5 B / 5 B) 100/100",
			"Checking URLs 1/3",
			"Checking URLs 2/3",
			"Checking URLs 2/3 > Downloading (0 B / 12 B) 0/100",
			"Checking URLs 2/3 > Downloading (12 B / 12 B) 100/100",
			"Checking URLs 3/3",
		}, "\n") || logs != 3 {
			t.Fatal(got, hosts, reports, logs)
		}
	}
}

func TestHTTPMirrorFetch(t *testing.T) {
	checkRequest := func(r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("unexpected method or content coding: %s, %q", r.Method, r.Header.Get("Accept-Encoding"))
		}
	}
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkRequest(r)
		if r.URL.Path != "/body" || r.Referer() != "" {
			t.Error("unexpected redirect path or Referer header")
		}
		_, _ = io.WriteString(w, "Hello world!")
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkRequest(r)
		switch r.URL.Path {
		case "/partial":
			// The bytes match the digest, but the server declared one more byte.
			w.Header().Set("Content-Length", "13")
			_, _ = io.WriteString(w, "Hello world!")
		case "/mismatch":
			_, _ = io.WriteString(w, "wrong")
		case "/redirect":
			http.Redirect(w, r, destination.URL+"/body#ignored", http.StatusTemporaryRedirect)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer origin.Close()
	store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	contentStore := eris.NewBlockCapabilityStore(store)
	tempDir := t.TempDir()
	checkTempFiles := func(want int) {
		t.Helper()
		files, err := os.ReadDir(tempDir)
		if err != nil || len(files) != want {
			t.Fatalf("got %d temporary files, want %d, error %v", len(files), want, err)
		}
	}
	client := destination.Client()
	base := client.Transport
	var paths []string
	client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if len(store.Blocks) != 0 {
			t.Error("blocks published before a complete matching download")
		}
		checkTempFiles(0)
		paths = append(paths, r.URL.Path)
		return base.RoundTrip(r)
	})
	var locks recordingLocks
	handler, err := New(Config{
		Client: client, Store: contentStore, Locks: &locks, MaxBytes: 13, TempStorage: fetch.TempDir(tempDir),
		Progress: func(progress ProgressNode) {
			if progress.Child != nil {
				checkTempFiles(1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	desc := "type url\nurl " + origin.URL + "/partial\nalt " + origin.URL + "/mismatch\nalt " + origin.URL + "/redirect?token=fixture\n" + checksum
	exact := source(t, desc)
	got := handler.Fetch(t.Context(), exact)
	checkTempFiles(0)
	if want := []string{"/partial", "/mismatch", "/redirect", "/body"}; !slices.Equal(paths, want) {
		t.Fatalf("got paths %v, want %v", paths, want)
	}
	if got.Status != "ok" || got.Kind != "blob" {
		t.Fatal(got)
	}
	body, err := contentStore.Decode(t.Context(), got.Cap, eris.BlockOptions{})
	if err != nil || string(body) != "Hello world!" {
		t.Fatalf("decoded body %q, error %v", body, err)
	}
	selected := source(t, "type url\nurl "+origin.URL+"/redirect?token=fixture\n")
	if want := [][32]byte{exact.Hash(), selected.Hash()}; !slices.Equal(locks.writes, want) {
		t.Fatalf("lock writes %x, want %x", locks.writes, want)
	}

	// A separate fetcher can reuse the mirror even when the new primary has
	// never been seen and the new request omits the checksum.
	second, err := New(Config{Client: client, Store: contentStore, Locks: &locks})
	if err != nil {
		t.Fatal(err)
	}
	next := source(t, "type url\nurl "+origin.URL+"/new\nalt "+origin.URL+"/redirect?token=fixture\n")
	hit := second.Fetch(t.Context(), next)
	if hit.Status != "ok" || hit.Kind != got.Kind || hit.Cap != got.Cap || len(paths) != 4 || !slices.Equal(locks.writes[2:], [][32]byte{next.Hash()}) {
		t.Fatalf("mirror hit returned %+v, HTTP requests %v, writes %x", hit, paths, locks.writes)
	}
}

type closeErrorBody struct{ io.Reader }

func (closeErrorBody) Close() error { return errors.New("response body close failed") }

func TestResponseBodyCloseFailureTriesMirror(t *testing.T) {
	var paths []string
	handler := setup(t, func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		resp := response(http.StatusOK, "Hello world!")
		if req.URL.Path == "/data" {
			resp.Body = closeErrorBody{resp.Body}
		}
		return resp, nil
	})
	store := handler.blocks
	var buffers []*testTempData
	handler.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) {
		for _, buffer := range buffers {
			if !buffer.closed {
				t.Fatal("previous temporary buffer is still open")
			}
		}
		if len(store.Blocks) != 0 {
			t.Fatal("blocks published before a candidate completed")
		}
		buffer := new(testTempData)
		buffers = append(buffers, buffer)
		return buffer, nil
	})
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+checksum))
	if got.Status != "ok" || !slices.Equal(paths, []string{"/data", "/next"}) {
		t.Fatalf("got %+v after requests to %v", got, paths)
	}
	for i, buffer := range buffers {
		if !buffer.closed {
			t.Errorf("temporary buffer for candidate %d is still open", i)
		}
	}
}

func TestAutoCompressedTreeVector(t *testing.T) {
	handler := setup(t, func(*http.Request) (*http.Response, error) {
		return response(200, strings.Repeat("\x00", 1024)), nil
	})
	got := handler.Fetch(t.Context(), source(t, description+"name file\nerisfs.compression auto\n"))
	const want = "urn:eris:BIACZ5E57FY2IDGF7I34DWENBOVRH3B4SWPEWEO2D3XPPVZOR5MNHD4MKZ43NFF5KRRCCLMMC6RVZHGEZKO7ACCP55TH2QIE5WVUWIJ53M"
	if got.Status != "ok" || got.Kind != "erisfs" || got.Cap.String() != want {
		t.Fatal(got)
	}
}

func TestTreeFileMetadataAndSecret(t *testing.T) {
	const name = `tools\hello`
	const content = "Hello world!"
	secret := eris.Secret{1}
	store := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)})
	handler, err := New(Config{
		Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, content), nil
		})},
		Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	desc := fmt.Sprintf("%sname %s\nexecutable true\neris.secret %x\n", description, name, secret[:])
	got := handler.Fetch(t.Context(), source(t, desc))
	if got.Status != "ok" || got.Kind != "erisfs" {
		t.Fatal(got)
	}
	index, err := erisfs.DecodeIndex(t.Context(), got.Cap, store, eris.BlockOptions{})
	if err != nil || len(index.Entries) != 1 {
		t.Fatalf("got index %+v, error %v", index, err)
	}
	entry := index.Entries[0]
	if !slices.Equal(entry.Path, []string{name}) || entry.Type != erisfs.Executable || entry.Size != int64(len(content)) || entry.Compression != erisfs.Uncompressed {
		t.Fatalf("got entry %+v", entry)
	}
	fileCap, err := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}).EncodeRecommended(t.Context(), []byte(content), secret, eris.BlockOptions{})
	if err != nil || entry.Capability != fileCap {
		t.Fatalf("got file capability %s, want %s, error %v", entry.Capability, fileCap, err)
	}
	decoded, err := store.Decode(t.Context(), entry.Capability, eris.BlockOptions{})
	if err != nil || string(decoded) != content {
		t.Fatalf("got file %q, error %v", decoded, err)
	}
	encodedIndex, err := index.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	rootCap, err := eris.NewBlockCapabilityStore(eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}).EncodeRecommended(t.Context(), encodedIndex, secret, eris.BlockOptions{})
	if err != nil || got.Cap != rootCap {
		t.Fatalf("got root capability %s, want %s, error %v", got.Cap, rootCap, err)
	}
}

func TestResponseTrailers(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{
		{"X-Checksum", "fixture", ""},
		{"Content-Length", "12", "unavailable"},
		{"Content-Range", "bytes 0-11/12", "unavailable"},
		{"Content-Encoding", "gzip", "unavailable"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(http.TrailerPrefix+tc.key, tc.value)
				_, _ = io.WriteString(w, "Hello world!")
			}))
			defer server.Close()
			store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
			f, err := New(Config{Client: server.Client(), Store: eris.NewBlockCapabilityStore(store)})
			if err != nil {
				t.Fatal(err)
			}
			got := f.Fetch(t.Context(), source(t, "type url\nurl "+server.URL+"\n"+checksum))
			if tc.want == "" {
				if got.Status != "ok" || len(store.Blocks) == 0 {
					t.Fatalf("got %+v with %d stored blocks", got, len(store.Blocks))
				}
			} else if got.Status != "fail" || got.Failure.Code != tc.want || len(store.Blocks) != 0 {
				t.Fatalf("got %+v with %d stored blocks, want %s and no blocks", got, len(store.Blocks), tc.want)
			}
		})
	}
}

func TestResponseFailures(t *testing.T) {
	for _, v := range []struct {
		status              int
		header, value, code string
	}{
		{401, "", "", "denied"}, {403, "", "", "denied"}, {404, "", "", "not-found"}, {410, "", "", "not-found"}, {206, "", "", "unavailable"}, {200, "Content-Range", "", "unavailable"}, {200, "Content-Encoding", "gzip", "unavailable"}, {200, "Content-Encoding", "IDENTITY", ""},
	} {
		f := setup(t, func(*http.Request) (*http.Response, error) {
			r := response(v.status, "")
			if v.header != "" {
				r.Header.Set(v.header, v.value)
			}
			return r, nil
		})
		got := f.Fetch(t.Context(), source(t, description))
		if got.Failure.Code != v.code {
			t.Fatal(v, got)
		}
	}
}

func TestOverlongResponseTriesMirror(t *testing.T) {
	var paths []string
	handler := setup(t, func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/data" {
			resp := response(200, "Body exceeds both the declared and configured lengths")
			resp.ContentLength = 1
			return resp, nil
		}
		return response(200, "Hello world!"), nil
	})
	handler.config.MaxBytes = 12
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"))
	if got.Status != "ok" || !slices.Equal(paths, []string{"/data", "/next"}) {
		t.Fatalf("got %+v after requests to %v", got, paths)
	}
	body, err := handler.config.Store.Decode(t.Context(), got.Cap, eris.BlockOptions{})
	if err != nil || string(body) != "Hello world!" {
		t.Fatalf("decoded body %q, error %v", body, err)
	}
}

func TestStalledResponseTriesMirror(t *testing.T) {
	var paths []string
	reads := 0
	closed := false
	handler := setup(t, func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/data" {
			if !closed {
				t.Fatal("stalled response is still open")
			}
			return response(http.StatusOK, "Hello world!"), nil
		}
		resp := response(http.StatusOK, "")
		resp.ContentLength = -1
		resp.Body = testBody{
			Reader: readerFunc(func([]byte) (int, error) {
				reads++
				if reads > 100 {
					t.Fatal("fetch kept reading a stalled response")
				}
				return 0, nil
			}),
			close: func() { closed = true },
		}
		return resp, nil
	})
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+checksum))
	if got.Status != "ok" || !slices.Equal(paths, []string{"/data", "/next"}) || reads != 100 {
		t.Fatalf("got %+v after requests to %v and %d stalled reads", got, paths, reads)
	}
}

type testBody struct {
	io.Reader
	close func()
}

func (body testBody) Close() error {
	body.close()
	return nil
}

func TestUnknownLengthExceedsByteLimit(t *testing.T) {
	var requests []string
	handler := setup(t, func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		resp := response(200, "Hello world!")
		resp.ContentLength = -1
		return resp, nil
	})
	tempDir := t.TempDir()
	handler.config.TempStorage = fetch.TempDir(tempDir)
	handler.config.MaxBytes = 11
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+checksum))
	if got.Status != "fail" || got.Failure.Code != "limit" || !slices.Equal(requests, []string{"/data"}) || len(handler.blocks.Blocks) != 0 {
		t.Fatalf("got %+v after requests to %v", got, requests)
	}
	if files, err := os.ReadDir(tempDir); err != nil || len(files) != 0 {
		t.Fatalf("got %d temporary files and error %v", len(files), err)
	}
}

type shortTempStorage struct{ fetch.TempDir }

func (storage shortTempStorage) Create(ctx context.Context) (fetch.TempData, error) {
	data, err := storage.TempDir.Create(ctx)
	if err != nil {
		return nil, err
	}
	return shortTempData{data}, nil
}

type shortTempData struct{ fetch.TempData }

func (data shortTempData) Write(p []byte) (int, error) {
	return data.TempData.Write(p[:len(p)-1])
}

func TestShortTempWrite(t *testing.T) {
	requests := 0
	rt := transport(func(*http.Request) (*http.Response, error) {
		requests++
		return response(200, "Hello world!"), nil
	})
	store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	tempDir := t.TempDir()
	handler, err := New(Config{
		Client: &http.Client{Transport: rt}, Store: eris.NewBlockCapabilityStore(store), TempStorage: shortTempStorage{fetch.TempDir(tempDir)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"))
	if got.Failure.Code != "internal" || requests != 1 || len(store.Blocks) != 0 {
		t.Fatalf("got %+v after %d requests and %d block writes", got, requests, len(store.Blocks))
	}
	if files, err := os.ReadDir(tempDir); err != nil || len(files) != 0 {
		t.Fatalf("got %d temporary files and error %v", len(files), err)
	}
}

type tempStorageFunc func(context.Context) (fetch.TempData, error)

func (create tempStorageFunc) Create(ctx context.Context) (fetch.TempData, error) {
	return create(ctx)
}

type testTempData struct {
	bytes.Buffer
	open     func() (io.Reader, error)
	closeErr error
	closed   bool
}

func (data *testTempData) Reader() (io.Reader, error) {
	if data.open == nil {
		return bytes.NewReader(data.Bytes()), nil
	}
	return data.open()
}
func (data *testTempData) Close() error {
	data.closed = true
	return data.closeErr
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(p []byte) (int, error) { return read(p) }

func TestTemporaryReadFailuresStopFetch(t *testing.T) {
	integrity := fetch.Failure{Code: "integrity"}
	for _, tc := range []struct {
		name, fields string
		open         func() (io.Reader, error)
	}{
		{"open for verification", checksum, func() (io.Reader, error) { return nil, integrity }},
		{"read for verification", checksum, func() (io.Reader, error) { return iotest.ErrReader(integrity), nil }},
		{"read for encoding", "", func() (io.Reader, error) { return iotest.ErrReader(io.ErrUnexpectedEOF), nil }},
		{"no read progress during encoding", "", func() (io.Reader, error) {
			return readerFunc(func([]byte) (int, error) { return 0, nil }), nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := setup(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, "Hello world!"), nil
			})
			data := &testTempData{open: tc.open}
			handler.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) { return data, nil })
			got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+tc.fields))
			if got.Status != "fail" || got.Failure.Code != "internal" || calls != 1 || !data.closed || len(handler.blocks.Blocks) != 0 {
				t.Fatalf("got %+v after %d requests, temporary data closed: %t", got, calls, data.closed)
			}
		})
	}
}

func TestTemporaryCleanupFailureStopsFetch(t *testing.T) {
	calls := 0
	handler := setup(t, func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, "wrong"), nil
	})
	data := &testTempData{closeErr: errors.New("temporary cleanup failed")}
	handler.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) { return data, nil })
	got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+checksum))
	if got.Status != "fail" || got.Failure.Code != "internal" || calls != 1 || !data.closed || len(handler.blocks.Blocks) != 0 {
		t.Fatalf("got %+v after %d requests, temporary data closed: %t", got, calls, data.closed)
	}
}

func TestCancellationDuringBodyRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	handler := setup(t, func(*http.Request) (*http.Response, error) {
		calls++
		resp := response(200, "Hello world!")
		resp.Body = io.NopCloser(readerFunc(func(p []byte) (int, error) {
			cancel()
			return copy(p, "Hello world!"), io.EOF
		}))
		return resp, nil
	})
	data := &testTempData{}
	handler.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) { return data, nil })
	got := handler.Fetch(ctx, source(t, description+"alt https://b.test/next\n"))
	if got.Status != "fail" || got.Failure.Code != "cancelled" || calls != 1 || !data.closed || data.Len() != 0 || len(handler.blocks.Blocks) != 0 {
		t.Fatalf("got %+v after %d requests with %d buffered bytes, temporary data closed: %t", got, calls, data.Len(), data.closed)
	}
}

func TestTimeoutScope(t *testing.T) {
	for _, tc := range []struct {
		name, status, code string
		clientTimeout      bool
		paths              []string
	}{
		{"client timeout tries mirror", "ok", "", true, []string{"/data", "/next"}},
		{"request deadline stops fetch", "fail", "limit", false, []string{"/data"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var paths []string
				client := &http.Client{Transport: transport(func(req *http.Request) (*http.Response, error) {
					paths = append(paths, req.URL.Path)
					if req.URL.Path == "/data" {
						resp := response(http.StatusOK, "Hello world!")
						resp.Body = io.NopCloser(readerFunc(func([]byte) (int, error) {
							<-req.Context().Done()
							return 0, req.Context().Err()
						}))
						return resp, nil
					}
					return response(http.StatusOK, "Hello world!"), nil
				})}
				ctx := t.Context()
				if tc.clientTimeout {
					client.Timeout = time.Second
				} else {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
				handler, err := New(Config{Client: client, Store: eris.NewBlockCapabilityStore(store)})
				if err != nil {
					t.Fatal(err)
				}
				got := handler.Fetch(ctx, source(t, description+"alt https://b.test/next\n"))
				if got.Status != tc.status || got.Failure.Code != tc.code || !slices.Equal(paths, tc.paths) {
					t.Fatalf("got %+v after requests to %v", got, paths)
				}
				if got.Status == "fail" && len(store.Blocks) != 0 {
					t.Fatal("published blocks after an incomplete download")
				}
			})
		})
	}
}

func TestCancellationBeforeChecksum(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := setup(t, func(*http.Request) (*http.Response, error) {
		return response(200, "Hello world!"), nil
	})
	data := &testTempData{open: func() (io.Reader, error) {
		t.Fatal("opened temporary data after cancellation")
		return nil, nil
	}}
	handler.config.TempStorage = tempStorageFunc(func(context.Context) (fetch.TempData, error) { return data, nil })
	handler.config.Progress = func(node ProgressNode) {
		if node.Completed == 100 {
			cancel()
		}
	}
	got := handler.Fetch(ctx, source(t, description+checksum))
	if got.Status != "fail" || got.Failure.Code != "cancelled" || !data.closed || len(handler.blocks.Blocks) != 0 {
		t.Fatalf("got %+v, temporary data closed: %t", got, data.closed)
	}
}

func TestFailures(t *testing.T) {
	calls := 0
	f := setup(t, func(*http.Request) (*http.Response, error) { calls++; return response(200, "wrong"), nil })
	for _, v := range []struct {
		desc, code string
		calls      int
	}{
		{"type git\nurl https://a.test", "unsupported-type", 0},
		{description + "alt https://a.test/#fragment", "invalid-source", 0},
		{"type url\nurl file:///C:/data", "unsupported-scheme", 0},
		{description + checksum + "alt file:///C:/missing", "integrity", 1},
	} {
		calls = 0
		got := f.Fetch(t.Context(), source(t, v.desc))
		if got.Failure.Code != v.code || calls != v.calls {
			t.Fatal(v, got, calls)
		}
	}
	for _, fields := range []string{"", "name a"} {
		calls = 0
		f.setStore(eris.NewBlockCapabilityStore(brokenStore{}))
		desc := description + "alt https://b.test/next\n" + fields
		if got := f.Fetch(t.Context(), source(t, desc)); got.Failure.Code != "store" || calls != 1 {
			t.Fatalf("got %+v after %d attempts", got, calls)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Time{})
	defer stop()
	f.config.Progress = func(ProgressNode) { t.Error("unexpected progress for stopped fetch") }
	for ctx, code := range map[context.Context]string{ctx: "cancelled", expired: "limit"} {
		if got := f.Fetch(ctx, source(t, description+"alt https://b.test/next\n")); got.Failure.Code != code {
			t.Fatal(got)
		}
	}
	f.config.Progress = nil
	f.config.MaxBytes = 1
	if got := f.Fetch(t.Context(), source(t, description)); got.Failure.Code != "limit" {
		t.Fatal(got)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("accepted missing dependencies")
	}
}

type brokenStore struct{ eris.BlockSet }

func (brokenStore) PutBlock(context.Context, []eris.PutBlockRequest) ([]eris.Reference, error) {
	return nil, errors.New("store offline")
}

type deniedStore struct{ eris.BlockSet }

func (deniedStore) PutBlock(context.Context, []eris.PutBlockRequest) ([]eris.Reference, error) {
	return nil, fmt.Errorf("write policy: %w", fetch.Failure{Code: "denied"})
}

func TestDeniedMirrorSelection(t *testing.T) {
	for _, tc := range []struct {
		name, fields, status, code string
		primaryStatus, requests    int
		store                      eris.BlockSource
	}{
		{"retrieval", "", "ok", "", 403, 2, eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}},
		{"blob publication", "", "fail", "denied", 200, 1, deniedStore{}},
		{"tree publication", "name file\n", "fail", "denied", 200, 1, deniedStore{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler, err := New(Config{
				Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.Host == "a.test" {
						return response(tc.primaryStatus, "Hello world!"), nil
					}
					return response(200, "Hello world!"), nil
				})},
				Store: eris.NewBlockCapabilityStore(tc.store),
			})
			if err != nil {
				t.Fatal(err)
			}
			got := handler.Fetch(t.Context(), source(t, description+"alt https://b.test/next\n"+tc.fields))
			if got.Status != tc.status || got.Failure.Code != tc.code || calls != tc.requests {
				t.Fatalf("got %+v after %d requests", got, calls)
			}
			if got.Status == "fail" && got.Cap.String() != "" {
				t.Fatal("publication failure returned a usable capability")
			}
		})
	}
}

func TestRedirects(t *testing.T) {
	for _, v := range []struct {
		location, code string
		requests       int
	}{
		{"/next#fragment", "", 2}, {"/data", "unavailable", 1},
		{"https://A.test:0443/data", "unavailable", 1}, {"https://a.test:444/data", "", 2},
		{"http://a.test/x", "unavailable", 1}, {"https://user@b.test/x", "unavailable", 1},
		{"file:///tmp", "unavailable", 1}, {"/denied", "denied", 1}, {"/hop", "unavailable", 11},
	} {
		calls := 0
		rt := transport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Fragment != "" {
				t.Fatal(r.URL)
			}
			if r.URL.Path == "/next" || r.URL.Port() == "444" {
				return response(200, ""), nil
			}
			resp := response(302, "")
			loc := v.location
			if loc == "/hop" {
				loc += fmt.Sprint(calls)
			}
			resp.Header.Set("Location", loc)
			return resp, nil
		})
		f, err := New(Config{Store: eris.NewBlockCapabilityStore(brokenStore{}), Client: &http.Client{Transport: rt, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if r.URL.Path == "/denied" {
				return fmt.Errorf("blocked by destination policy: %w", fetch.Failure{Code: "denied"})
			}
			return nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		body, code := f.download(t.Context(), "https://a.test/data", nil)
		if body != nil {
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if code != v.code || calls != v.requests {
			t.Fatal(v, code, calls)
		}
	}
}

func TestRedirectRejectsSameRequestTarget(t *testing.T) {
	requests := 0
	handler := setup(t, func(*http.Request) (*http.Response, error) {
		requests++
		if requests > 1 {
			return response(200, "unexpected second request"), nil
		}
		resp := response(307, "")
		resp.Header.Set("Location", "https://A.test/?fixture#ignored")
		return resp, nil
	})
	got := handler.Fetch(t.Context(), source(t, "type url\nurl https://a.test?fixture\n"))
	if got.Status != "fail" || got.Failure.Code != "unavailable" || requests != 1 {
		t.Fatalf("got %+v after %d requests", got, requests)
	}
}

func FuzzBody(f *testing.F) {
	for _, v := range []struct {
		s        string
		n, limit int64
	}{{"", 0, 0}, {"abc", -1, 0}, {"abc", 3, 3}, {"abc", 2, 0}, {"abc", 4, 0}, {"abc", -1, 2}, {"abc", 1, 2}, {"abc", 2, 2}, {strings.Repeat("x", 32769), 32769, 0}} {
		f.Add([]byte(v.s), v.n, v.limit)
	}
	f.Fuzz(func(t *testing.T, data []byte, n, limit int64) {
		if n < -1 || limit < 0 {
			return
		}
		fetcher := setup(t, nil)
		fetcher.config.MaxBytes = limit
		var got bytes.Buffer
		code := fetcher.read(t.Context(), bytes.NewReader(data), n, &got, nil)
		size := int64(len(data))
		want := ""
		switch {
		case limit > 0 && size > limit && (n < 0 || limit <= n):
			want = "limit"
		case n >= 0 && size != n:
			want = "unavailable"
		}
		if code != want {
			t.Fatalf("body size %d, declared length %d, limit %d: got %q, want %q", size, n, limit, code, want)
		}
		if code == "" && !bytes.Equal(got.Bytes(), data) {
			t.Fatal("complete body changed")
		}
	})
}

// A sequence describes complete bodies, missing resources and interrupted
// downloads. Check where retrieval stops and which locks it uses.
func FuzzMirrorSelection(f *testing.F) {
	for _, seq := range [][]byte{{0}, {1, 2}, {3, 0, 1}, {2, 3}, {3, 2}, {1, 0}} {
		f.Add(seq, true)
		f.Add(seq, false)
	}
	f.Fuzz(func(t *testing.T, seq []byte, checked bool) {
		if len(seq) == 0 || len(seq) > 32 {
			return
		}
		const mirror = "https://b.test/resource"
		desc := description + strings.Repeat("alt "+mirror+"\n", len(seq)-1)
		if checked {
			desc += checksum
		}
		want := ""
		attempts := 0
		mismatch := false
		for i, b := range seq {
			attempts = i + 1
			outcome := b % 4
			if outcome == 0 || outcome == 1 && !checked {
				want = ""
				break
			}
			mismatch = mismatch || outcome == 1
			want = [...]string{"", "integrity", "not-found", "unavailable"}[outcome]
		}
		if want != "" && mismatch {
			want = "integrity"
		}
		calls := 0
		var db recordingLocks
		handler := setupWithLocks(t, func(*http.Request) (*http.Response, error) {
			b := seq[calls]
			calls++
			switch b % 4 {
			case 0:
				return response(200, "Hello world!"), nil
			case 1:
				return response(200, "bad"), nil
			case 2:
				return response(410, ""), nil
			default:
				r := response(200, "Hello world!")
				r.ContentLength++
				return r, nil
			}
		}, &db)
		src := source(t, desc)
		got := handler.Fetch(t.Context(), src)
		status := "fail"
		if want == "" {
			status = "ok"
		}
		if calls != attempts || got.Status != status || got.Failure.Code != want {
			t.Fatal(seq, got, calls)
		}
		exact := src.Hash()
		primary := source(t, description).Hash()
		alternate := source(t, "type url\nurl "+mirror+"\n").Hash()
		reads := [][32]byte{exact}
		if !checked && len(seq) > 1 {
			reads = append(reads, primary, alternate)
		}
		var writes [][32]byte
		if status == "ok" {
			selected := primary
			if attempts > 1 {
				selected = alternate
			}
			writes = append(writes, exact)
			if selected != exact {
				writes = append(writes, selected)
			}
		}
		if !slices.Equal(db.reads, reads) || !slices.Equal(db.writes, writes) {
			t.Fatalf("lock reads %x, want %x, writes %x, want %x", db.reads, reads, db.writes, writes)
		}
	})
}

func FuzzRedirect(f *testing.F) {
	for _, loc := range []string{"next#f", "http://b.test", "https://b.test", "/data", "ftp://b.test", "//user@host", "//b.test:65536", "%00", "%xx"} {
		f.Add(loc)
	}
	f.Fuzz(func(t *testing.T, loc string) {
		calls := 0
		handler := setup(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				resp := response(307, "")
				resp.Header.Set("Location", loc)
				return resp, nil
			}
			if r.URL.Scheme != "https" || !fetch.ValidHTTPURI(r.URL.String()) || r.URL.String() == "https://a.test/data" {
				t.Fatal(r.URL)
			}
			return response(200, "done"), nil
		})
		body, code := handler.download(t.Context(), "https://a.test/data", nil)
		if code == "" {
			defer func() { _ = body.Close() }()
			r, err := body.Reader()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			if calls != 2 || err != nil || string(data) != "done" {
				t.Fatal(calls, string(data), err)
			}
		}
	})
}

func TestInterruptedDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := setup(t, func(*http.Request) (*http.Response, error) { cancel(); return nil, context.Canceled })
	if got := handler.Fetch(ctx, source(t, description)); got.Failure.Code != "cancelled" {
		t.Fatal(got)
	}
}

type cancellingStore struct {
	eris.BlockSet
	cancel context.CancelFunc
}

func (s cancellingStore) PutBlock(context.Context, []eris.PutBlockRequest) ([]eris.Reference, error) {
	s.cancel()
	return nil, context.Canceled
}

func TestInterruptedStore(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := setup(t, func(*http.Request) (*http.Response, error) { return response(200, ""), nil })
	handler.setStore(eris.NewBlockCapabilityStore(cancellingStore{cancel: cancel}))
	if got := handler.Fetch(ctx, source(t, description)); got.Failure.Code != "cancelled" {
		t.Fatal(got)
	}
}

func TestUnrepresentableHTTPHost(t *testing.T) {
	// The source grammar accepts percent-encoded host bytes. net/http cannot
	// construct a request for a percent-encoded ASCII letter in the host.
	handler := setup(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil })
	got := handler.Fetch(t.Context(), source(t, "type url\nurl https://%61.test/"))
	if got.Failure.Code != "unavailable" {
		t.Fatal(got)
	}
}

func TestSingleDownloadProgress(t *testing.T) {
	for _, tc := range []struct {
		length int64
		status int
		want   []ProgressNode
	}{
		{10000, 200, []ProgressNode{
			{Name: "Downloading (0 B / 10.0 KB)", Total: 100},
			{Name: "Downloading (10.0 KB / 10.0 KB)", Completed: 100, Total: 100},
		}},
		{-1, 200, []ProgressNode{{Name: "Downloading 0 B"}, {Name: "Downloading 10.0 KB"}}},
		{0, 404, nil},
	} {
		var reports []ProgressNode
		handler := setup(t, func(*http.Request) (*http.Response, error) {
			if len(reports) != 0 {
				t.Fatal("download progress before response")
			}
			resp := response(tc.status, strings.Repeat("x", 10000))
			resp.ContentLength = tc.length
			return resp, nil
		})
		handler.config.Progress = func(root ProgressNode) { reports = append(reports, root) }
		handler.Fetch(t.Context(), source(t, description))
		if !slices.Equal(reports, tc.want) {
			t.Fatalf("length %d status %d: got %+v, want %+v", tc.length, tc.status, reports, tc.want)
		}
	}
}

func TestProgressReportsShortReads(t *testing.T) {
	var reports []uint64
	reads := 0
	store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	handler, err := New(Config{
		Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, "Hello world!")
			body := strings.NewReader("Hello world!")
			resp.Body = io.NopCloser(readerFunc(func(p []byte) (int, error) {
				reads++
				if reads == 2 && !slices.Equal(reports, []uint64{0, 50}) {
					t.Errorf("got progress %v before second read, want [0 50]", reports)
				}
				return body.Read(p[:min(len(p), 6)])
			}))
			return resp, nil
		})},
		Store: eris.NewBlockCapabilityStore(store),
		Progress: func(node ProgressNode) {
			reports = append(reports, node.Completed)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := handler.Fetch(t.Context(), source(t, description+checksum))
	if got.Status != "ok" || !slices.Equal(reports, []uint64{0, 50, 100}) {
		t.Fatalf("got %+v with progress %v", got, reports)
	}
}

func TestDownloadProgressCounts(t *testing.T) {
	for _, tc := range []struct {
		size   uint64
		length int64
		want   ProgressNode
	}{
		{0, 0, ProgressNode{Name: "Downloading (0 B / 0 B)", Completed: 100, Total: 100}},
		{10000000, 1000000000, ProgressNode{Name: "Downloading (10.0 MB / 1.0 GB)", Completed: 1, Total: 100}},
		{1 << 62, 1<<62 + 1, ProgressNode{Name: "Downloading (4.6 EB / 4.6 EB)", Completed: 99, Total: 100}},
	} {
		if got := downloadProgress(tc.size, tc.length); got != tc.want {
			t.Fatalf("got %+v, want %+v", got, tc.want)
		}
	}
}

func TestNodeProgress(t *testing.T) {
	t.Setenv("ZIG_PROGRESS", "")
	if err := os.Unsetenv("ZIG_PROGRESS"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	reporter, err := zpgo.New(zpgo.Options{
		Manual:   true,
		Renderer: &zpgo.RendererOptions{Writer: &output},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reporter.Close() })
	report := NodeProgress(reporter.Root())
	state := ProgressNode{Name: "Checking URLs", Completed: 1, Total: 3}
	state.Child = &ProgressNode{Name: "Downloading", Completed: 20, Total: 100}
	report(state)
	state.Child.Completed = 50
	report(state)
	if err := reporter.Flush(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\r\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "1/3") || !strings.Contains(lines[1], "Downloading") || !strings.HasSuffix(lines[1], "50/100") {
		t.Fatalf("active download: %q", output.String())
	}
	output.Reset()
	state.Child = nil
	// The snapshot count takes precedence over the reporter's implicit increment.
	report(state)
	if err := reporter.Flush(); err != nil {
		t.Fatal(err)
	}
	if frame := strings.TrimSpace(output.String()); !strings.HasSuffix(frame, "1/3") || strings.Contains(frame, "Downloading") {
		t.Fatalf("after ending download: %q", output.String())
	}
}

func FuzzDownloadProgress(f *testing.F) {
	f.Add(uint64(0), int64(0))
	f.Add(uint64(2048), int64(-1))
	f.Add(uint64(1<<62), int64(1<<62+1))
	f.Fuzz(func(t *testing.T, size uint64, length int64) {
		if length < 0 {
			got := downloadProgress(size, length)
			if got.Completed != 0 || got.Total != 0 || !strings.HasPrefix(got.Name, "Downloading ") {
				t.Fatal(got)
			}
			return
		}
		size = min(size, uint64(length))
		got := downloadProgress(size, length)
		want := uint64(100)
		if length > 0 {
			var numerator big.Int
			numerator.SetUint64(size)
			numerator.Mul(&numerator, big.NewInt(100))
			want = numerator.Quo(&numerator, big.NewInt(length)).Uint64()
		}
		if got.Completed != want || got.Total != 100 {
			t.Fatalf("%d of %d bytes: got %+v, want %d%%", size, length, got, want)
		}
	})
}

func TestReadOnlyImageStore(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	handler, err := New(Config{
		Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return response(200, "hello"), nil })},
		Store:  eris.NewBlockCapabilityStore(blocks),
		Images: erisfs.NewMountStore(t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := handler.Fetch(t.Context(), source(t, description+"name hello\n"))
	if got.Failure.Code != "store" || len(blocks.Blocks) != 0 {
		t.Fatal(got)
	}
	got = handler.Fetch(t.Context(), source(t, description))
	if got.Status != "ok" || got.Kind != "blob" {
		t.Fatal(got)
	}
}

func TestSeparateImageStore(t *testing.T) {
	ctx := t.Context()
	blobs := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	treeBlocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	images := erisfs.NewStore(eris.NewBlockCapabilityStore(treeBlocks), eris.BlockOptions{})
	handler, err := New(Config{
		Client: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return response(200, "hello"), nil })},
		Store:  eris.NewBlockCapabilityStore(blobs),
		Images: images,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := handler.Fetch(ctx, source(t, description+"name greeting.txt\n"))
	if result.Status != "ok" || result.Kind != "erisfs" || len(blobs.Blocks) != 0 {
		t.Fatalf("tree result = %+v, blocks in blob store = %d", result, len(blobs.Blocks))
	}
	tree, err := images.OpenFS(ctx, result.Cap)
	if err != nil {
		t.Fatal(err)
	}
	data, err := tree.ReadFile(ctx, "greeting.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("fetched tree file = %q, %v", data, err)
	}
}
