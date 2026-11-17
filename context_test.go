package eris

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/asciimoth/goeris/internal/batch"
)

type checkedContextStore struct {
	BlockSet
	t             *testing.T
	want          context.Context
	reads, writes int
	cancel        context.CancelFunc
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (s *checkedContextStore) getBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	s.t.Helper()
	if ctx != s.want {
		s.t.Fatal("read context changed")
	}
	s.reads++
	return GetBlock(ctx, s.BlockSet, ref, ttl, BlockOptions{})
}
func (s *checkedContextStore) putBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, refs ...Reference) (Reference, error) {
	s.t.Helper()
	if ctx != s.want {
		s.t.Fatal("write context changed")
	}
	s.writes++
	if s.cancel != nil {
		s.cancel()
	}
	return PutBlock(ctx, s.BlockSet, block, ttl, BlockOptions{}, refs...)
}

func TestStreamingContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := bytes.Repeat([]byte("context"), 6000)
	store := &checkedContextStore{BlockSet: BlockSet{Blocks: map[Reference]Block{}}, t: t, want: ctx}
	cap, err := EncodeContent(ctx, bytes.NewReader(data), Secret{}, BlockSize1KiB, store, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if store.writes < 40 {
		t.Fatal("test did not write internal and leaf blocks")
	}
	reader, err := NewReader(ctx, cap, store, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read: %v", err)
	}
	sized, err := NewReaderWithSize(ctx, cap, store, int64(len(data)), BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sized.Close() }()
	if _, err := sized.ReadAt(make([]byte, 1), 0); err != nil {
		t.Fatal(err)
	}
	reads := store.reads
	cancel()
	// The first leaf is cached. Cancellation must still stop the read.
	for _, r := range []*Reader{reader, sized} {
		if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled read: %v", err)
		}
	}
	if store.reads != reads {
		t.Fatal("storage called after cancellation")
	}
}

func TestEncodeStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &checkedContextStore{BlockSet: BlockSet{Blocks: map[Reference]Block{}}, t: t, want: ctx, cancel: cancel}
	_, err := EncodeContent(ctx, bytes.NewReader(make([]byte, 3000)), Secret{}, BlockSize1KiB, store, BlockOptions{})
	if !errors.Is(err, context.Canceled) || store.writes != 1 {
		t.Fatalf("error %v, writes %d", err, store.writes)
	}
	_, err = EncodeContentRecommended(ctx, bytes.NewReader([]byte("data")), Secret{}, store, BlockOptions{})
	if !errors.Is(err, context.Canceled) || store.writes != 1 {
		t.Fatalf("error %v, writes %d", err, store.writes)
	}
}

type cancelingInput struct {
	cancel context.CancelFunc
	reads  int
}

func (input *cancelingInput) Read(p []byte) (int, error) {
	input.reads++
	if input.reads > 1 {
		return 0, errors.New("input read after cancellation")
	}
	input.cancel()
	p[0] = 'x'
	return 1, nil
}

func TestEncodeCancellationDuringInput(t *testing.T) {
	for name, encode := range map[string]func(context.Context, io.Reader, BlockSink) (Capability, error){
		"explicit size": func(ctx context.Context, input io.Reader, sink BlockSink) (Capability, error) {
			return EncodeContent(ctx, input, Secret{}, BlockSize1KiB, sink, BlockOptions{})
		},
		"size sample": func(ctx context.Context, input io.Reader, sink BlockSink) (Capability, error) {
			return EncodeContentRecommended(ctx, input, Secret{}, sink, BlockOptions{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := &cancelingInput{cancel: cancel}
			blocks := BlockSet{Blocks: map[Reference]Block{}}
			_, err := encode(ctx, input, blocks)
			if !errors.Is(err, context.Canceled) || input.reads != 1 || len(blocks.Blocks) != 0 {
				t.Fatalf("error %v, input reads %d, blocks %d", err, input.reads, len(blocks.Blocks))
			}
		})
	}
}

// BatchSize reports the maximum number of requests per call.
func (s *checkedContextStore) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See BlockSource for error rules.
func (s *checkedContextStore) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *checkedContextStore) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}
