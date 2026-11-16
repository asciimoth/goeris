package eris

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type ttlProbe struct {
	BlockSet
	seen []TTL
	fail bool
}

func (p *ttlProbe) GetBlock(ctx context.Context, ref Reference, ttl TTL) ([]byte, error) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return nil, ErrMissingBlock
	}
	return p.BlockSet.GetBlock(ctx, ref, ttl)
}
func (p *ttlProbe) PutBlock(ctx context.Context, block []byte, ttl TTL, expected ...Reference) (Reference, error) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return Reference{}, ErrMissingBlock
	}
	return p.BlockSet.PutBlock(ctx, block, ttl, expected...)
}

func TestCombinedPreservesTTL(t *testing.T) {
	block := make([]byte, BlockSize1KiB)
	ref, _ := ReferenceOf(block)
	for _, ttl := range []TTL{NoTTL, LocalTTL, 2, 16, 255} {
		for _, wrapped := range []bool{false, true} {
			first := &ttlProbe{fail: true}
			last := &ttlProbe{BlockSet: BlockSet{ref: block}}
			var store *CombinedBlockStore
			if wrapped {
				store = NewCombinedBlockStore(struct{ BlockStore }{first}, struct{ BlockStore }{last})
			} else {
				store = NewCombinedBlockStore(first, last)
			}
			if _, err := store.GetBlock(context.Background(), ref, ttl); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutBlock(context.Background(), block, ttl, ref); err != nil {
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
