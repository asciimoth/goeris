package prefetchtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), []byte("data"), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	ref := cap.Root.Reference
	req := []eris.BlockRequest{{Reference: ref, TTL: 3, Options: eris.BlockOptions{Root: true}}}
	s := New(blocks, time.Millisecond, 1)
	if _, err := s.GetBlock(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBlock(ctx, req); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Cold != 1 {
		t.Fatal("warm reads delayed")
	}
	s.PrefetchBlock(ctx, req)
	req[0].TTL = 7
	snap := s.Snapshot()
	if snap.Hints[0].TTL != 3 {
		t.Fatal("caller request retained")
	}
	snap.Hints[0].TTL = 9
	snap.Reads[0].TTL = 9
	if s.Snapshot().Hints[0].TTL != 3 || s.Snapshot().Reads[0].TTL != 3 {
		t.Fatal("snapshot aliases store")
	}
	delete(blocks.Blocks, ref)
	if _, err := s.GetBlock(ctx, req); err != nil {
		t.Fatal("input blocks aliased", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	s.PrefetchBlock(canceled, req)
	s.PrefetchBlock(ctx, nil)
	s.PrefetchBlock(ctx, append(req, req...))
	if len(s.Snapshot().Hints) != 1 {
		t.Fatal("invalid hints accepted")
	}
	if _, err := s.GetBlock(ctx, append(req, req...)); !errors.Is(err, eris.ErrBatchTooLarge) {
		t.Fatal(err)
	}
	if _, err := s.GetBlock(ctx, []eris.BlockRequest{{}}); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
	if _, err := s.GetBlock(canceled, req); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if New(eris.BlockSet{}, 0, -1).BatchSize() != 1 {
		t.Fatal("negative limit")
	}
}

func TestColdCancellationAndNonblockingHint(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), nil, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	s := New(blocks, time.Hour, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	req := []eris.BlockRequest{{Reference: cap.Root.Reference}}
	if _, err := s.GetBlock(ctx, req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.PrefetchBlock(context.Background(), req); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hint waited for cold delay")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := s.GetBlock(ctx2, req); err != nil {
		t.Fatal("hint did not warm block", err)
	}
}

func TestStoreWritesAndConcurrency(t *testing.T) {
	s := New(eris.BlockSet{}, 0, 1)
	ctx := context.Background()
	requests := []eris.PutBlockRequest{{Data: make([]byte, 1024)}}
	refs, err := s.PutBlock(ctx, requests)
	if err != nil {
		t.Fatal(err)
	}
	req := []eris.BlockRequest{{Reference: refs[0], Options: eris.BlockOptions{HighPriority: true}}}
	changed, err := s.UpdateBlockOptions(ctx, req)
	if err != nil || !changed[0] {
		t.Fatal(changed, err)
	}
	if _, err = s.PutBlock(ctx, append(requests, requests...)); !errors.Is(err, eris.ErrBatchTooLarge) {
		t.Fatal(err)
	}
	if _, err = s.UpdateBlockOptions(ctx, append(req, req...)); !errors.Is(err, eris.ErrBatchTooLarge) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			s.PrefetchBlock(ctx, req)
			_, _ = s.GetBlock(ctx, req)
			_ = s.Snapshot()
			_, _ = s.PutBlock(ctx, requests)
			_, _ = s.UpdateBlockOptions(ctx, req)
		})
	}
	wg.Wait()
}

func TestPrefetchDoesNotWaitForStoreLock(t *testing.T) {
	s := New(eris.BlockSet{}, 0, 1)
	s.mu.Lock()
	done := make(chan struct{})
	go func() {
		s.PrefetchBlock(context.Background(), []eris.BlockRequest{{Reference: eris.Reference{1}}})
		close(done)
	}()
	select {
	case <-done:
		s.mu.Unlock()
	case <-time.After(time.Second):
		s.mu.Unlock()
		<-done
		t.Fatal("prefetch waited for the store lock")
	}
	if len(s.Snapshot().Hints) != 0 {
		t.Fatal("busy store accepted a hint")
	}
}
