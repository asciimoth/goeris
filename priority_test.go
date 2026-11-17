package eris_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/volume"
)

func TestBlockSetPriority(t *testing.T) {
	ctx := context.Background()
	block := make([]byte, eris.BlockSize1KiB)
	ref, _ := eris.ReferenceOf(block)
	for _, values := range [][]bool{{false, false}, {false, true, false}, {true, false, true}} {
		store := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		highest := false
		for _, high := range values {
			options := eris.BlockOptions{HighPriority: high}
			if got, err := eris.PutBlock(ctx, store, block, eris.NoTTL, options, ref); err != nil || got != ref {
				t.Fatalf("put: %v, %v", got, err)
			}
			highest = highest || high
			// Read hints must not change stored priority.
			for _, hint := range []bool{false, true} {
				got, err, stored := eris.GetBlock(ctx, store, ref, eris.NoTTL, eris.BlockOptions{HighPriority: hint})
				if err != nil || !bytes.Equal(got, block) || stored.HighPriority != highest {
					t.Fatalf("get: %v, %v", err, stored)
				}
			}
		}
		for _, high := range []bool{false, true, true, false} {
			changed, err := eris.UpdateBlockOptions(ctx, store, ref, eris.LocalTTL, eris.BlockOptions{HighPriority: high})
			if err != nil || changed != (highest != high) {
				t.Fatalf("update: %v, %v", changed, err)
			}
			highest = high
			_, err, stored := eris.GetBlock(ctx, store, ref, eris.NoTTL, eris.BlockOptions{})
			if err != nil || stored.HighPriority != high {
				t.Fatalf("updated get: %v, %v", err, stored)
			}
		}
		if changed, err := eris.UpdateBlockOptions(ctx, store, eris.Reference{}, 0, eris.BlockOptions{HighPriority: true}); err != nil || changed {
			t.Fatalf("missing update: %v, %v", changed, err)
		}
		if _, err := eris.PutBlock(ctx, store, block, 0, eris.BlockOptions{HighPriority: true}, eris.Reference{}); !errors.Is(err, eris.ErrInvalidBlock) {
			t.Fatal(err)
		}
		if store.Blocks[ref].Options.HighPriority {
			t.Fatal("failed put changed priority")
		}
		if changed, err := eris.UpdateBlockOptions(ctx, store, ref, 0, eris.BlockOptions{HighPriority: true}); err != nil || !changed {
			t.Fatalf("raise priority before corrupt read: %v, %v", changed, err)
		}
		store.Blocks[ref].Data[0]++
		if _, err, options := eris.GetBlock(ctx, store, ref, 0, eris.BlockOptions{HighPriority: true}); err == nil || options.HighPriority {
			t.Fatalf("corrupt get: %v, %v", err, options)
		}
	}
}

type priorityProbe struct {
	eris.BlockSet
	reads, writes []eris.BlockOptions
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (s *priorityProbe) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	s.reads = append(s.reads, options)
	return eris.GetBlock(ctx, s.BlockSet, ref, ttl, options)
}
func (s *priorityProbe) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, refs ...eris.Reference) (eris.Reference, error) {
	s.writes = append(s.writes, options)
	return eris.PutBlock(ctx, s.BlockSet, block, ttl, options, refs...)
}

func TestCombinedPriority(t *testing.T) {
	ctx := context.Background()
	high := eris.BlockOptions{HighPriority: true, Root: true}
	terminal := &priorityProbe{BlockSet: eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}
	store := &volume.Chain{CombinedBlockStore: *eris.NewCombinedBlockStore(eris.BlockSet{}, eris.NewCombinedBlockStore(terminal))}
	ref, err := eris.PutBlock(ctx, store, make([]byte, eris.BlockSize1KiB), 0, high)
	if err != nil {
		t.Fatal(err)
	}
	_, err, options := eris.GetBlock(ctx, store, ref, 0, high)
	if err != nil || options != high || terminal.reads[0] != high || terminal.writes[0] != high {
		t.Fatalf("forwarded options: %v, %v", options, err)
	}
	if changed, err := eris.UpdateBlockOptions(ctx, store, ref, 0, eris.BlockOptions{}); err != nil || !changed {
		t.Fatalf("update: %v, %v", changed, err)
	}
	_, err, options = eris.GetBlock(ctx, store, ref, 0, high)
	if err != nil || options != (eris.BlockOptions{}) {
		t.Fatalf("lowered priority: %v, %v", options, err)
	}
	if changed, err := eris.UpdateBlockOptions(ctx, store, ref, 0, high); err != nil || !changed {
		t.Fatalf("raise through middleware: %v, %v", changed, err)
	}
	_, err, options = eris.GetBlock(ctx, store, ref, 0, eris.BlockOptions{})
	if err != nil || options != high {
		t.Fatalf("raised priority: %v, %v", options, err)
	}
}

type prioritySourceFunc func(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) ([]byte, eris.BlockOptions, error)

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (f prioritySourceFunc) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	block, stored, err := f(ctx, ref, ttl, options)
	return block, err, stored
}

func TestCombinedPriorityAfterFailure(t *testing.T) {
	high := eris.BlockOptions{HighPriority: true, Root: true}
	failure := errors.New("failed source")
	block := make([]byte, eris.BlockSize1KiB)
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	failed := prioritySourceFunc(func(_ context.Context, gotRef eris.Reference, _ eris.TTL, options eris.BlockOptions) ([]byte, eris.BlockOptions, error) {
		calls++
		if options != high || gotRef != ref {
			t.Fatal("fallback changed request arguments")
		}
		// Deliberately return data and metadata with an error. Neither may escape.
		return block, high, failure
	})
	success := prioritySourceFunc(func(_ context.Context, gotRef eris.Reference, _ eris.TTL, options eris.BlockOptions) ([]byte, eris.BlockOptions, error) {
		calls++
		if options != high || gotRef != ref {
			t.Fatal("fallback changed request arguments")
		}
		return block, eris.BlockOptions{}, nil
	})
	for _, tc := range []struct {
		name      string
		store     *eris.CombinedBlockStore
		wantErr   error
		wantCalls int
	}{
		{"empty", eris.NewCombinedBlockStore(), eris.ErrNoBlockStorage, 0},
		{"failed", eris.NewCombinedBlockStore(failed), failure, 1},
		{"fallback", eris.NewCombinedBlockStore(failed, success), nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls = 0
			data, err, options := eris.GetBlock(context.Background(), tc.store, ref, 0, high)
			if !errors.Is(err, tc.wantErr) || calls != tc.wantCalls || options != (eris.BlockOptions{}) {
				t.Fatalf("get: %v, options=%v, calls=%d", err, options, calls)
			}
			if tc.wantErr != nil && data != nil || tc.wantErr == nil && !bytes.Equal(data, block) {
				t.Fatal("incorrect block result")
			}
		})
	}
}

// BatchSize reports the maximum number of requests per call.
func (s *priorityProbe) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s *priorityProbe) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *priorityProbe) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s prioritySourceFunc) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s prioritySourceFunc) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}
