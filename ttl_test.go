package eris

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/asciimoth/goeris/internal/batch"
)

type ttlProbe struct {
	BlockSet
	seen []TTL
	fail bool
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (p *ttlProbe) getBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return nil, ErrMissingBlock, BlockOptions{}
	}
	return GetBlock(ctx, p.BlockSet, ref, ttl, BlockOptions{})
}
func (p *ttlProbe) putBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return Reference{}, ErrMissingBlock
	}
	return PutBlock(ctx, p.BlockSet, block, ttl, BlockOptions{}, expected...)
}

func TestCombinedPreservesTTL(t *testing.T) {
	block := make([]byte, BlockSize1KiB)
	ref, _ := ReferenceOf(block)
	for _, ttl := range []TTL{NoTTL, LocalTTL, 2, 16, 255} {
		for _, wrapped := range []bool{false, true} {
			first := &ttlProbe{fail: true}
			last := &ttlProbe{BlockSet: BlockSet{Blocks: map[Reference]Block{ref: {Data: block}}}}
			var store *CombinedBlockStore
			if wrapped {
				store = NewCombinedBlockStore(struct{ BlockStore }{first}, struct{ BlockStore }{last})
			} else {
				store = NewCombinedBlockStore(first, last)
			}
			if _, err, _ := GetBlock(context.Background(), store, ref, ttl, BlockOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := PutBlock(context.Background(), store, block, ttl, BlockOptions{}, ref); err != nil {
				t.Fatal(err)
			}
			want := []TTL{ttl, ttl}
			if !reflect.DeepEqual(first.seen, want) || !reflect.DeepEqual(last.seen, want) {
				t.Fatalf("ttl %d, wrapped %v: %v, %v", ttl, wrapped, first.seen, last.seen)
			}
		}
	}
}

func TestTTLForward(t *testing.T) {
	for i := 0; i <= 255; i++ {
		ttl := TTL(i)
		got, err := ttl.Forward()
		if ttl == LocalTTL {
			if !errors.Is(err, ErrTTLExpired) {
				t.Fatalf("local TTL: %v", err)
			}
			continue
		}
		want := ttl - 1
		if ttl == NoTTL {
			want = DefaultTTL
		}
		if err != nil || got != want {
			t.Fatalf("%d: got %d, %v; want %d", ttl, got, err, want)
		}
	}
}

// BatchSize reports the maximum number of requests per call.
func (s *ttlProbe) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See BlockSource for error rules.
func (s *ttlProbe) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *ttlProbe) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}
