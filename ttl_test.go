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

func (p *ttlProbe) GetBlock(ref Reference, ttl TTL) ([]byte, error) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return nil, ErrMissingBlock
	}
	return p.BlockSet.GetBlock(ref, ttl)
}
func (p *ttlProbe) PutBlock(block []byte, ttl TTL, expected ...Reference) (Reference, error) {
	p.seen = append(p.seen, ttl)
	if p.fail {
		return Reference{}, ErrMissingBlock
	}
	return p.BlockSet.PutBlock(block, ttl, expected...)
}
func (p *ttlProbe) GetBlockContext(_ context.Context, ref Reference, ttl TTL) ([]byte, error) {
	return p.GetBlock(ref, ttl)
}
func (p *ttlProbe) PutBlockContext(_ context.Context, block []byte, ttl TTL, expected ...Reference) (Reference, error) {
	return p.PutBlock(block, ttl, expected...)
}

func TestCombinedPreservesTTL(t *testing.T) {
	block := make([]byte, BlockSize1KiB)
	ref, _ := ReferenceOf(block)
	for _, ttl := range []TTL{NoTTL, LocalTTL, 2, 16, 255} {
		for _, plain := range []bool{false, true} {
			first := &ttlProbe{fail: true}
			last := &ttlProbe{BlockSet: BlockSet{ref: block}}
			var store *CombinedBlockStore
			if plain {
				store = NewCombinedBlockStore(struct{ BlockStore }{first}, struct{ BlockStore }{last})
			} else {
				store = NewCombinedBlockStore(first, last)
			}
			if _, err := store.GetBlock(ref, ttl); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutBlock(block, ttl, ref); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetBlockContext(context.Background(), ref, ttl); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutBlockContext(context.Background(), block, ttl, ref); err != nil {
				t.Fatal(err)
			}
			want := []TTL{ttl, ttl, ttl, ttl}
			if !reflect.DeepEqual(first.seen, want) || !reflect.DeepEqual(last.seen, want) {
				t.Fatalf("ttl %d, plain %v: %v, %v", ttl, plain, first.seen, last.seen)
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
