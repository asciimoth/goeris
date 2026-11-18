package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
)

func TestPrefetchWarmsCache(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{Root: true})
	r.TTL = 12
	r.Options.HighPriority = true
	c := newCache(t, s, 2048)
	type key struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), key{}, "value"), time.Minute)
	defer cancel()
	requests := []eris.BlockRequest{r}
	c.PrefetchBlock(ctx, requests)
	requests[0] = eris.BlockRequest{}
	waitFor(t, func() bool { return contains(c, r.Reference) })
	block := readOne(t, c, r)
	if block.Options != (eris.BlockOptions{Root: true}) || s.count() != 1 {
		t.Fatal("prefetch did not serve next read")
	}
	s.mu.Lock()
	got, readCtx := s.reads[0], s.contexts[0]
	s.mu.Unlock()
	deadline, _ := ctx.Deadline()
	readDeadline, _ := readCtx.Deadline()
	if got != r || readCtx.Value(key{}) != "value" || readDeadline != deadline {
		t.Fatal("prefetch changed request, values, or deadline")
	}
	// A hit accepts a priority hint without changing stored options or doing I/O.
	r2 := s.add(2, 1024, eris.BlockOptions{})
	readOne(t, c, r2)
	r2.Options.HighPriority = true
	c.PrefetchBlock(ctx, []eris.BlockRequest{r2})
	c.mu.Lock()
	promoted := c.entries[r2.Reference].Value.(*entry).high
	c.mu.Unlock()
	if !promoted || s.count() != 2 {
		t.Fatal("cached hint did not promote entry")
	}
}

func TestPrefetchDiscard(t *testing.T) {
	s := newStore()
	s.limit = 1
	c := newCache(t, s, 1024)
	r := s.add(1, 1024, eris.BlockOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.PrefetchBlock(context.Background(), nil)
	c.PrefetchBlock(ctx, []eris.BlockRequest{r})
	c.PrefetchBlock(context.Background(), []eris.BlockRequest{r, r})
	c.mu.Lock()
	c.PrefetchBlock(context.Background(), []eris.BlockRequest{r})
	c.mu.Unlock()
	_ = c.Close()
	c.PrefetchBlock(context.Background(), []eris.BlockRequest{r})
	if s.count() != 0 {
		t.Fatal("discarded hints reached source")
	}
}

func TestPrefetchPoolQueueAndClose(t *testing.T) {
	s := newStore()
	started := make(chan eris.BlockRequest, 10)
	finished := make(chan struct{}, 10)
	s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
		started <- requests[0]
		<-ctx.Done()
		finished <- struct{}{}
		return nil, ctx.Err()
	}
	c, err := New(s, Config{CapacityBytes: 2048, Workers: 2, QueueSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	c.PrefetchBlock(ctx, []eris.BlockRequest{{Reference: eris.Reference{1}}, {Reference: eris.Reference{2}}})
	receive(t, started)
	receive(t, started)
	// Both workers are blocked in I/O. Only two more requests can be retained.
	c.PrefetchBlock(ctx, []eris.BlockRequest{{Reference: eris.Reference{3}}, {Reference: eris.Reference{4}}, {Reference: eris.Reference{5}}})
	if len(c.queue) != 2 || s.count() != 2 {
		t.Fatal("pool or queue exceeded its limit")
	}
	// Foreground work is independent of the worker pool.
	foregroundCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := c.GetBlock(foregroundCtx, []eris.BlockRequest{{Reference: eris.Reference{6}}})
		done <- err
	}()
	receive(t, started)
	cancel()
	if err := receive(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var closers sync.WaitGroup
	for range 4 {
		closers.Go(func() { _ = c.Close() })
	}
	closed := make(chan struct{})
	go func() { closers.Wait(); close(closed) }()
	receive(t, closed)
	if len(c.queue) != 0 || s.count() != 3 || len(finished) != 3 {
		t.Fatal("Close did not drain queue and stop all workers")
	}
}

func TestPrefetchCancellationAndRetry(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
		if requests[0].Reference == r.Reference {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
		}
		return s.read(ctx, requests)
	}
	c, err := New(s, Config{CapacityBytes: 1024, Workers: 1, QueueSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	c.PrefetchBlock(ctx, []eris.BlockRequest{r})
	receive(t, started)
	// Canceled queued hints must not start a read.
	queuedCtx, queuedCancel := context.WithCancel(context.Background())
	c.PrefetchBlock(queuedCtx, []eris.BlockRequest{{Reference: eris.Reference{9}}})
	queuedCancel()
	cancel()
	waitFor(t, func() bool { return len(c.queue) == 0 })
	if contains(c, r.Reference) {
		t.Fatal("canceled read cached")
	}
	close(release)
	// A missing background block must not stop the worker or become a hit.
	c.PrefetchBlock(context.Background(), []eris.BlockRequest{{}, r})
	receive(t, started)
	waitFor(t, func() bool { return contains(c, r.Reference) })
	if s.count() != 3 {
		t.Fatalf("reads = %d, want 3", s.count())
	}
}

func TestConcurrentMisses(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{})
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.read(ctx, requests)
	}
	c := newCache(t, s, 1024)
	var wg sync.WaitGroup
	for _, high := range []bool{false, true} {
		wg.Go(func() { request := r; request.Options.HighPriority = high; readOne(t, c, request) })
	}
	receive(t, started)
	receive(t, started)
	close(release)
	wg.Wait()
	if c.used != 1024 || len(c.entries) != 1 || !c.entries[r.Reference].Value.(*entry).high {
		t.Fatal("concurrent misses duplicated data or lost priority")
	}
}

func TestReadOverlappingWrite(t *testing.T) {
	for _, test := range []struct {
		name          string
		readFirst     bool
		readEndsFirst bool
	}{
		{"read encloses write", true, false},
		{"write encloses read", false, true},
		{"read starts and ends first", true, true},
		{"write starts and ends first", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newStore()
			r := s.add(1, 1024, eris.BlockOptions{HighPriority: true})
			startedRead := make(chan struct{}, 1)
			releaseRead := make(chan struct{})
			startedWrite := make(chan struct{}, 1)
			releaseWrite := make(chan struct{})
			s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
				blocks, err := s.read(ctx, requests)
				startedRead <- struct{}{}
				select {
				case <-releaseRead:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return blocks, err
			}
			s.update = func(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
				startedWrite <- struct{}{}
				select {
				case <-releaseWrite:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.set.UpdateBlockOptions(ctx, requests)
			}
			c := newCache(t, s, 1024)
			readDone := make(chan error, 1)
			writeDone := make(chan error, 1)
			startRead := func() {
				go func() { _, err := c.GetBlock(t.Context(), []eris.BlockRequest{r}); readDone <- err }()
				receive(t, startedRead)
			}
			startWrite := func() {
				go func() { _, err := c.UpdateBlockOptions(t.Context(), []eris.BlockRequest{r}); writeDone <- err }()
				receive(t, startedWrite)
			}
			if test.readFirst {
				startRead()
				startWrite()
			} else {
				startWrite()
				startRead()
			}
			finishRead := func() {
				close(releaseRead)
				if err := receive(t, readDone); err != nil {
					t.Fatal(err)
				}
				if contains(c, r.Reference) {
					t.Fatal("overlapping read retained stale options")
				}
			}
			finishWrite := func() {
				close(releaseWrite)
				if err := receive(t, writeDone); err != nil {
					t.Fatal(err)
				}
			}
			if test.readEndsFirst {
				finishRead()
				finishWrite()
			} else {
				finishWrite()
				finishRead()
			}
			if got := readOne(t, c, r); got.Options.HighPriority {
				t.Fatal("next read returned stale options")
			}
		})
	}
}

func TestCloseDuringForegroundRead(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{})
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.get = func(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
		started <- struct{}{}
		<-release
		return s.read(ctx, requests)
	}
	c := newCache(t, s, 1024)
	done := make(chan error, 1)
	go func() { _, err := c.GetBlock(context.Background(), []eris.BlockRequest{r}); done <- err }()
	receive(t, started)
	_ = c.Close()
	close(release)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if contains(c, r.Reference) {
		t.Fatal("foreground read cached after Close")
	}
}

func TestConcurrentOperations(t *testing.T) {
	s := newStore()
	requests := make([]eris.BlockRequest, 8)
	for i := range requests {
		requests[i] = s.add(byte(i), 1024, eris.BlockOptions{})
	}
	c := newCache(t, s, 4096)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			for i := range 50 {
				r := requests[(i+worker)%len(requests)]
				r.Options.HighPriority = i%2 == 0
				c.PrefetchBlock(context.Background(), []eris.BlockRequest{r})
				block := readOne(t, c, r)
				if _, err := c.PutBlock(context.Background(), []eris.PutBlockRequest{{Data: block.Data, Options: r.Options}}); err != nil {
					t.Error(err)
				}
				if _, err := c.UpdateBlockOptions(context.Background(), []eris.BlockRequest{r}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used > c.size || c.writes != 0 {
		t.Fatal("cache accounting failed")
	}
}

func TestCloseDuringWrite(t *testing.T) {
	s := newStore()
	r := s.add(1, 1024, eris.BlockOptions{})
	c := newCache(t, s, 1024)
	block := readOne(t, c, r)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.put = func(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.set.PutBlock(ctx, requests)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.PutBlock(t.Context(), []eris.PutBlockRequest{{Data: block.Data}})
		done <- err
	}()
	receive(t, started)
	_ = c.Close()
	close(release)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if c.used != 0 || len(c.entries) != 0 || c.writes != 0 {
		t.Fatal("write changed cache state after Close")
	}
	if blocks, err := s.read(t.Context(), []eris.BlockRequest{r}); err != nil || len(blocks) != 1 {
		t.Fatal("Close stopped an active foreground write or closed storage")
	}
}
