package eris_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/coap"
	erishttp "github.com/asciimoth/goeris/http"
	"github.com/asciimoth/goeris/s3"
	"github.com/asciimoth/goeris/volume"
)

func TestTerminalPrefetchNoop(t *testing.T) {
	// Nil handles cannot perform network or filesystem work. No-op hints must
	// return without consulting them, even for invalid or canceled requests.
	stores := []eris.BlockSource{eris.BlockSet{}, (*volume.Volume)(nil), (*erishttp.Client)(nil), (*coap.Client)(nil), (*s3.Client)(nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requests := []eris.BlockRequest{{TTL: 3, Options: eris.BlockOptions{HighPriority: true}}, {TTL: 5}}
	want := append([]eris.BlockRequest(nil), requests...)
	for _, store := range stores {
		store.PrefetchBlock(context.Background(), requests)
		store.PrefetchBlock(ctx, requests)
		store.PrefetchBlock(ctx, nil)
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatal("terminal changed requests")
	}
}
