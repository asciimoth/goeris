// Package prefetchtest provides a delayed in-memory block store for tests.
package prefetchtest

import (
	"context"
	"sync"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
)

// Store delays the first unhinted read of a block. Completed reads and accepted
// hints make later reads immediate. Hints model an ideal cache: accepting one
// warms the block immediately without I/O or a worker. This makes read-ahead
// tests deterministic. Delay applies only to cold GetBlock calls.
// Methods are safe for concurrent use. A busy store discards hints instead
// of waiting for its lock. Use New to construct a Store.
type Store struct {
	mu     sync.Mutex
	blocks eris.BlockSet
	warm   map[eris.Reference]bool
	delay  time.Duration
	limit  int
	stats  Stats
}

// Stats records accepted hints, attempted reads, and cold read attempts.
// Requests retain their order and include the original TTL and options.
type Stats struct {
	Hints []eris.BlockRequest
	Reads []eris.BlockRequest
	Cold  int
}

// New copies blocks so tests can change the input without changing the store.
// A negative limit is treated as one, like the shared batch helpers.
func New(blocks eris.BlockSet, delay time.Duration, limit int) *Store {
	copySet := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	for ref, block := range blocks.Blocks {
		block.Data = append([]byte(nil), block.Data...)
		copySet.Blocks[ref] = block
	}
	if limit < 0 {
		limit = 1
	}
	return &Store{blocks: copySet, delay: delay, limit: limit, warm: make(map[eris.Reference]bool)}
}

func (s *Store) BatchSize() int { return s.limit }

func (s *Store) PrefetchBlock(ctx context.Context, requests []eris.BlockRequest) {
	if len(requests) == 0 || s.limit > 0 && len(requests) > s.limit || ctx.Err() != nil {
		return
	}
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	s.stats.Hints = append(s.stats.Hints, requests...)
	for _, request := range requests {
		s.warm[request.Reference] = true
	}
}

func (s *Store) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.limit, eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		s.mu.Lock()
		cold := !s.warm[r.Reference]
		s.stats.Reads = append(s.stats.Reads, r)
		if cold {
			s.stats.Cold++
		}
		s.mu.Unlock()
		if cold && s.delay > 0 {
			timer := time.NewTimer(s.delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return eris.Block{}, ctx.Err()
			case <-timer.C:
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		blocks, err := s.blocks.GetBlock(ctx, []eris.BlockRequest{r})
		if err != nil {
			return eris.Block{}, err
		}
		s.warm[r.Reference] = true
		return blocks[0], nil
	})
}

func (s *Store) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	if s.limit > 0 && len(requests) > s.limit {
		return nil, eris.ErrBatchTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocks.PutBlock(ctx, requests)
}

func (s *Store) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	if s.limit > 0 && len(requests) > s.limit {
		return nil, eris.ErrBatchTooLarge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocks.UpdateBlockOptions(ctx, requests)
}

// Snapshot returns a copy of the counters and request logs.
func (s *Store) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Hints: append([]eris.BlockRequest(nil), s.stats.Hints...), Reads: append([]eris.BlockRequest(nil), s.stats.Reads...), Cold: s.stats.Cold}
}

var _ eris.BlockStore = (*Store)(nil)
