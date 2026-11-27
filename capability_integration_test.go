package eris_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	erishttp "github.com/asciimoth/goeris/http"
	"github.com/asciimoth/goeris/link"
)

// Serve one decoded file through a mount with no encrypted blocks available.
// Both callers must use the supplied store, including its size checks.
func mountedContent(t testing.TB, data []byte) (eris.CapabilityStore, eris.Capability) {
	t.Helper()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	capability, err := eris.NewBlockCapabilityStore(blocks).EncodeRecommended(t.Context(), data, eris.Secret{}, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	name, err := filepath.Localize(capability.String())
	if err != nil {
		t.Skipf("host cannot represent a capability file name: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return eris.NewMountCapabilityStore(dir), capability
}

func checkMountedLink(t *testing.T, store eris.CapabilityStore, capability eris.Capability, data []byte, declared uint64) {
	t.Helper()
	encoded, err := (link.Link{Capability: capability, Size: declared}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	image, err := link.NewFS(fstest.MapFS{"file": {Data: encoded}}, store, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := image.ReadFile(t.Context(), "file")
	if declared != uint64(len(data)) {
		if !errors.Is(err, eris.ErrSizeMismatch) {
			t.Fatalf("link size %d: got %v, want ErrSizeMismatch", declared, err)
		}
	} else if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("link returned %d bytes, error %v", len(got), err)
	}
}

func checkMountedHTTP(t *testing.T, store eris.CapabilityStore, capability eris.Capability, data []byte, limit int64) {
	t.Helper()
	handler := erishttp.Handler{Content: store, MaxContentSize: limit}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, erishttp.N2RPath+"?"+capability.String(), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if limit > 0 && int64(len(data)) > limit {
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("HTTP limit %d: got status %d, want 413", limit, response.Code)
		}
	} else if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), data) {
		t.Fatalf("HTTP returned status %d and %d bytes", response.Code, response.Body.Len())
	}
}

func TestMountedContentConsumers(t *testing.T) {
	data := []byte("decoded content")
	store, capability := mountedContent(t, data)
	size := len(data)
	for _, declared := range []int{size - 1, size, size + 1} {
		checkMountedLink(t, store, capability, data, uint64(declared))
	}
	for _, limit := range []int{0, size - 1, size, size + 1} {
		checkMountedHTTP(t, store, capability, data, int64(limit))
	}
}

func FuzzMountedContentConsumers(f *testing.F) {
	f.Add([]byte{}, int8(0), uint16(0))
	f.Add([]byte("content"), int8(0), uint16(6))
	f.Add([]byte("content"), int8(-1), uint16(7))
	f.Add(make([]byte, 1025), int8(1), uint16(1026))
	f.Fuzz(func(t *testing.T, data []byte, delta int8, limit uint16) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		store, capability := mountedContent(t, data)
		declared := max(0, len(data)+int(delta))
		checkMountedLink(t, store, capability, data, uint64(declared))
		checkMountedHTTP(t, store, capability, data, int64(limit))
	})
}
