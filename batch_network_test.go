package eris_test

import (
	"context"
	"errors"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/coap"
	erishttp "github.com/asciimoth/goeris/http"
	"github.com/asciimoth/goeris/s3"
	"github.com/asciimoth/goeris/volume"
)

func TestStorageBatchBoundaries(t *testing.T) {
	// Zero-value transports would fail on I/O. Boundary checks must not use them.
	for name, source := range map[string]eris.BlockSource{
		"http": &erishttp.Client{}, "coap": &coap.Client{}, "s3": &s3.Client{},
		"volume": &volume.Volume{}, "combined": &eris.CombinedBlockStore{},
	} {
		t.Run(name, func(t *testing.T) {
			if source.BatchSize() != 1 {
				t.Fatal("expected safe batch limit")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got, err := source.GetBlock(ctx, nil)
			if len(got) != 0 || err != nil {
				t.Fatal(got, err)
			}
			got, err = source.GetBlock(ctx, make([]eris.BlockRequest, 2))
			if len(got) != 0 || !errors.Is(err, eris.ErrBatchTooLarge) {
				t.Fatal(got, err)
			}
			got, err = source.GetBlock(ctx, make([]eris.BlockRequest, 1))
			if len(got) != 0 || !errors.Is(err, context.Canceled) {
				t.Fatal(got, err)
			}
			if sink, ok := source.(eris.BlockSink); ok {
				refs, err := sink.PutBlock(ctx, nil)
				if len(refs) != 0 || err != nil {
					t.Fatal(refs, err)
				}
				refs, err = sink.PutBlock(ctx, make([]eris.PutBlockRequest, 2))
				if len(refs) != 0 || !errors.Is(err, eris.ErrBatchTooLarge) {
					t.Fatal(refs, err)
				}
				refs, err = sink.PutBlock(ctx, make([]eris.PutBlockRequest, 1))
				if len(refs) != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal(refs, err)
				}
				updates, err := sink.UpdateBlockOptions(ctx, nil)
				if len(updates) != 0 || err != nil {
					t.Fatal(updates, err)
				}
				updates, err = sink.UpdateBlockOptions(ctx, make([]eris.BlockRequest, 2))
				if len(updates) != 0 || !errors.Is(err, eris.ErrBatchTooLarge) {
					t.Fatal(updates, err)
				}
				updates, err = sink.UpdateBlockOptions(ctx, make([]eris.BlockRequest, 1))
				if len(updates) != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal(updates, err)
				}
			}
		})
	}
}
