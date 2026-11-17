package eris_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	eris "github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/coap"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/s3"
	"github.com/asciimoth/goeris/volume"
)

type updateSink struct {
	eris.BlockSink
	update func(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error)
}

func (s updateSink) updateBlockOptions(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) (bool, error) {
	return s.update(ctx, ref, ttl, options)
}

func TestUpdateBlockOptionsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref := eris.Reference{1}
	blocks := eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: []byte{2}}}}
	for name, sink := range map[string]eris.BlockSink{
		"blocks":     blocks,
		"nil blocks": eris.BlockSet{},
		"volume":     &volume.Volume{},
		"coap":       &coap.Client{},
		"s3":         &s3.Client{},
	} {
		t.Run(name, func(t *testing.T) {
			for _, ctx := range []context.Context{context.Background(), ctx} {
				for _, ref := range []eris.Reference{ref, {}} {
					updated, err := eris.UpdateBlockOptions(ctx, sink, ref, eris.LocalTTL, eris.BlockOptions{})
					if updated || !errors.Is(err, ctx.Err()) {
						t.Fatalf("UpdateBlockOptions = %v, %v", updated, err)
					}
				}
			}
		})
	}
	if len(blocks.Blocks) != 1 || len(blocks.Blocks[ref].Data) != 1 || blocks.Blocks[ref].Data[0] != 2 {
		t.Fatal("block data changed")
	}
}

func TestCombinedUpdateBlockOptions(t *testing.T) {
	failure := errors.New("update failed")
	for _, tc := range []struct {
		name    string
		results []bool
		errs    []error
		cancel  bool
		want    bool
		wantErr error
		calls   int
		reports []int
	}{
		{name: "empty", wantErr: eris.ErrNoBlockStorage},
		{name: "canceled", cancel: true, wantErr: context.Canceled},
		{name: "updated", results: []bool{true, true}, errs: []error{nil, nil}, want: true, calls: 1},
		{name: "unsupported continues", results: []bool{false, true, true}, errs: []error{nil, nil, nil}, want: true, calls: 2},
		{name: "all unsupported", results: []bool{false, false}, errs: []error{nil, nil}, calls: 2},
		{name: "error then unsupported", results: []bool{false, false, false}, errs: []error{failure, nil, nil}, calls: 3, reports: []int{1}},
		{name: "unsupported then error", results: []bool{false, true}, errs: []error{nil, failure}, wantErr: failure, calls: 2},
		{name: "unsupported then retry", results: []bool{false, false, true}, errs: []error{nil, failure, nil}, want: true, calls: 3, reports: []int{2}},
		{name: "retry", results: []bool{false, true}, errs: []error{failure, nil}, want: true, calls: 2, reports: []int{1}},
		{name: "discard failed result", results: []bool{true}, errs: []error{failure}, wantErr: failure, calls: 1},
		{name: "all fail", results: []bool{true, true}, errs: []error{failure, failure}, wantErr: failure, calls: 2, reports: []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			ref := eris.Reference{3}
			calls := 0
			var reports []int
			entries := []any{struct{}{}}
			for i := range tc.results {
				entries = append(entries, updateSink{update: func(gotCtx context.Context, gotRef eris.Reference, ttl eris.TTL, options eris.BlockOptions) (bool, error) {
					if gotCtx != ctx || gotRef != ref || ttl != 7 || options != (eris.BlockOptions{}) {
						t.Fatal("arguments changed")
					}
					if calls != i {
						t.Fatal("incorrect attempt order")
					}
					calls++
					return tc.results[i], tc.errs[i]
				}})
			}
			store := eris.NewCombinedBlockStore(entries...)
			store.OnError = func(index int, err error) {
				reports = append(reports, index)
				if err != failure {
					t.Fatalf("OnError = %d, %v", index, err)
				}
			}
			got, err := eris.UpdateBlockOptions(ctx, store, ref, 7, eris.BlockOptions{})
			if got != tc.want || err != tc.wantErr || calls != tc.calls || !slices.Equal(reports, tc.reports) {
				t.Fatalf("got (%v, %v), %d calls, %v reports; want (%v, %v), %d calls, %v reports", got, err, calls, reports, tc.want, tc.wantErr, tc.calls, tc.reports)
			}
		})
	}
}

func TestCombinedUpdateBlockOptionsCancellation(t *testing.T) {
	failure := errors.New("update failed")
	for _, cancelFromCallback := range []bool{false, true} {
		for _, firstErr := range []error{nil, failure} {
			if cancelFromCallback && firstErr == nil {
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			reports := 0
			first := updateSink{update: func(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
				if !cancelFromCallback {
					cancel()
				}
				return false, firstErr
			}}
			second := updateSink{update: func(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
				t.Fatal("called next sink after cancellation")
				return true, nil
			}}
			store := eris.NewCombinedBlockStore(struct{ eris.BlockSource }{eris.BlockSet{}}, first, nil, second)
			store.OnError = func(index int, err error) {
				reports++
				if index != 1 || err != firstErr {
					t.Fatalf("OnError = %d, %v", index, err)
				}
				if cancelFromCallback {
					cancel()
				}
			}
			updated, err := eris.UpdateBlockOptions(ctx, store, eris.Reference{}, eris.NoTTL, eris.BlockOptions{})
			cancel()
			wantReports := 0
			if firstErr != nil {
				wantReports = 1
			}
			if updated || err != context.Canceled || reports != wantReports {
				t.Fatalf("UpdateBlockOptions = %v, %v; reports = %d, want %d", updated, err, reports, wantReports)
			}
		}
	}
}

func TestNestedUpdateBlockOptionsTTL(t *testing.T) {
	for _, ttl := range []eris.TTL{eris.NoTTL, eris.LocalTTL, 7, 255} {
		ctx := context.Background()
		ref := eris.Reference{5}
		calls := 0
		sink := updateSink{update: func(gotCtx context.Context, gotRef eris.Reference, gotTTL eris.TTL, _ eris.BlockOptions) (bool, error) {
			calls++
			if gotCtx != ctx || gotRef != ref || gotTTL != ttl {
				t.Fatal("nested store changed arguments")
			}
			return true, nil
		}}
		inner := eris.NewCombinedBlockStore(eris.BlockSet{}, sink)
		chain := &volume.Chain{CombinedBlockStore: *eris.NewCombinedBlockStore(eris.BlockSet{}, inner)}
		updated, err := eris.UpdateBlockOptions(ctx, chain, ref, ttl, eris.BlockOptions{})
		if !updated || err != nil || calls != 1 {
			t.Fatalf("UpdateBlockOptions = %v, %v; calls = %d", updated, err, calls)
		}
	}
}

// BatchSize reports the maximum number of requests per call.
func (s updateSink) BatchSize() int { return 1 }

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s updateSink) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}
