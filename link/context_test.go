package link_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/link"
)

type exactContextSource struct {
	eris.BlockSource
	want  context.Context
	t     *testing.T
	reads int
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (s *exactContextSource) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	if ctx != s.want {
		s.t.Fatal("link read context changed")
	}
	s.reads++
	return eris.GetBlock(ctx, s.BlockSource, ref, ttl, eris.BlockOptions{})
}

func TestFSContextAndWrapper(t *testing.T) {
	data := patternedPayload(9000)
	encoded, blocks := encodeFSLink(t, data)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &exactContextSource{BlockSource: blocks, want: ctx, t: t}
	image := newLinkFS(t, fstest.MapFS{"dir/file": {Data: encoded}}, source)
	wrapped := link.NewIOFS(ctx, image)
	sub, err := fs.Sub(wrapped, "dir")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(sub, "file")
	if err != nil || !bytes.Equal(got, data) || source.reads == 0 {
		t.Fatalf("wrapped read: %v", err)
	}
	file, err := image.Open(ctx, "dir/file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	before := source.reads
	cancel()
	if _, err := io.ReadAll(file); !errors.Is(err, context.Canceled) {
		t.Fatalf("late read: %v", err)
	}
	if _, err := wrapped.ReadFile("dir/file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wrapped read after cancel: %v", err)
	}
	if source.reads != before {
		t.Fatal("storage called after cancellation")
	}
}

// BatchSize reports the maximum number of requests per call.
func (s *exactContextSource) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s *exactContextSource) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}
