package eris

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestCombinedCapabilityStorePrefetch(t *testing.T) {
	c := pathTestCapability(t)
	ctx := t.Context()
	want := BlockRequest{Reference: c.Root.Reference, TTL: LocalTTL, Options: BlockOptions{HighPriority: true}}
	first, last := &prefetchProbe{}, &prefetchProbe{}
	missing := NewMountCapabilityStore(t.TempDir())
	store := NewCombinedCapabilityStore(NewBlockCapabilityStore(first), missing, NewBlockCapabilityStore(last), missing)
	if err := store.Prefetch(ctx, c, want.TTL, want.Options); err != nil {
		t.Fatal(err)
	}
	for i, probe := range []*prefetchProbe{first, last} {
		if len(probe.hints) != 1 || !slices.Equal(probe.hints[0], []BlockRequest{want}) || probe.contexts[0] != ctx {
			t.Errorf("block store %d received unexpected prefetch requests: %v", i, probe.hints)
		}
	}
}

type failingStreamStore struct {
	CapabilityStore
	err   error
	read  bool
	calls int
}

func (s *failingStreamStore) fail(r io.Reader) (Capability, error) {
	s.calls++
	if s.read {
		var buf [1]byte
		_, _ = r.Read(buf[:])
	}
	return Capability{}, s.err
}

func (s *failingStreamStore) EncodeContent(_ context.Context, r io.Reader, _ Secret, _ BlockSize, _ BlockOptions) (Capability, error) {
	return s.fail(r)
}

func (s *failingStreamStore) EncodeContentRecommended(_ context.Context, r io.Reader, _ Secret, _ BlockOptions) (Capability, error) {
	return s.fail(r)
}

func TestCombinedCapabilityStoreStreamWrite(t *testing.T) {
	for _, method := range []string{"EncodeContent", "EncodeContentRecommended"} {
		for _, tc := range []struct {
			name, content string
			read          bool
			remaining     int
		}{
			{"rejected before read", "abc", false, 3},
			{"consumed byte", "abc", true, 2},
			{"read at EOF", "", true, 0},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				src := strings.NewReader(tc.content)
				first := &failingStreamStore{read: tc.read, err: errors.New("first")}
				last := &failingStreamStore{err: errors.New("last")}
				store := NewCombinedCapabilityStore(nil, first, last)
				var err error
				if method == "EncodeContent" {
					_, err = store.EncodeContent(t.Context(), src, Secret{}, BlockSize1KiB, BlockOptions{})
				} else {
					_, err = store.EncodeContentRecommended(t.Context(), src, Secret{}, BlockOptions{})
				}
				want, lastCalls := last.err, 1
				if tc.read {
					want, lastCalls = first.err, 0
				}
				if err != want || first.calls != 1 || last.calls != lastCalls || src.Len() != tc.remaining {
					t.Fatalf("error = %v, calls = (%d, %d), remaining bytes = %d", err, first.calls, last.calls, src.Len())
				}
			})
		}
	}
}

type mutatingByteStore struct {
	CapabilityStore
	err error
}

func (s mutatingByteStore) Encode(_ context.Context, content []byte, _ Secret, _ BlockSize, _ BlockOptions) (Capability, error) {
	content[0]++
	return Capability{}, s.err
}

func (s mutatingByteStore) EncodeRecommended(ctx context.Context, content []byte, secret Secret, options BlockOptions) (Capability, error) {
	return s.Encode(ctx, content, secret, BlockSize1KiB, options)
}

func TestCombinedCapabilityStoreStopsAfterByteMutation(t *testing.T) {
	for _, method := range []string{"Encode", "EncodeRecommended"} {
		t.Run(method, func(t *testing.T) {
			src := []byte("abc")
			first := mutatingByteStore{err: errors.New("failed after mutation")}
			blocks := BlockSet{Blocks: make(map[Reference]Block)}
			store := NewCombinedCapabilityStore(first, NewBlockCapabilityStore(blocks))
			var err error
			if method == "Encode" {
				_, err = store.Encode(t.Context(), src, Secret{}, BlockSize1KiB, BlockOptions{})
			} else {
				_, err = store.EncodeRecommended(t.Context(), src, Secret{}, BlockOptions{})
			}
			if string(src) != "bbc" || err != first.err || len(blocks.Blocks) != 0 {
				t.Fatalf("input = %q, error = %v, stored blocks = %d", src, err, len(blocks.Blocks))
			}
		})
	}
}

func TestCombinedCapabilityStoreStreamAfterReadOnlyStore(t *testing.T) {
	dst := NewBlockCapabilityStore(BlockSet{Blocks: make(map[Reference]Block)})
	last := &failingStreamStore{}
	store := NewCombinedCapabilityStore(NewMountCapabilityStore(""), dst, last)
	input := struct{ io.Reader }{strings.NewReader("hello")}
	c, err := store.EncodeContentRecommended(t.Context(), input, Secret{}, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := dst.Decode(t.Context(), c, BlockOptions{})
	if err != nil || string(data) != "hello" || last.calls != 0 {
		t.Fatalf("decoded = %q, error = %v, later calls = %d", data, err, last.calls)
	}
}
