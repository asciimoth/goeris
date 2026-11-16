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
	"github.com/asciimoth/goeris/link"
)

type exactContextSource struct {
	eris.BlockSource
	want  context.Context
	t     *testing.T
	reads int
}

func (s *exactContextSource) GetBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL) ([]byte, error) {
	if ctx != s.want {
		s.t.Fatal("link read context changed")
	}
	s.reads++
	return s.BlockSource.GetBlock(ctx, ref, ttl)
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
