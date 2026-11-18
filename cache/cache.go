// Package cache provides bounded in-memory caching for encrypted ERIS blocks.
// It owns a fixed pool of workers for best-effort prefetch requests.
package cache

import (
	"container/list"
	"context"
	"errors"
	"io/fs"
	"sync"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
)

// Config sets fixed limits for a Cache. All fields must be positive.
type Config struct {
	// CapacityBytes limits retained block data. It does not include map and
	// queue overhead, returned copies, or reads in progress. A block larger
	// than this limit can be read but is not cached.
	CapacityBytes int
	// Workers is the number of goroutines used for background reads.
	Workers int
	// QueueSize is the maximum number of waiting block requests, excluding
	// active workers. A full queue discards new hints without waiting.
	QueueSize int
}

// Cache wraps a block source. Use New to construct it, and call Close to stop
// its workers. Do not copy a Cache. Its zero value is closed.
//
// Reads verify and copy blocks before caching them. Each result owns its data.
// Eviction removes the least recently used regular block first, then the least
// recently used high-priority block. A new regular block cannot displace high-
// priority blocks. If it cannot fit beside them, no entries are removed.
// Stored HighPriority or a read/prefetch HighPriority hint
// protects an entry until eviction or invalidation. Hints never change the
// returned storage options. Root does not affect eviction.
//
// If the source also implements eris.BlockSink, writes and option updates pass
// through and invalidate the affected cache entry, even on failure. The next
// read loads the actual stored options. Otherwise writes return
// eris.ErrNoBlockStorage. Changes made directly to the source are not tracked;
// use this wrapper for all option changes that must be visible through it.
//
// Calls are safe concurrently if the source supports concurrent calls. No cache
// lock is held during storage I/O. Concurrent misses can fetch the same block.
// Foreground calls pass context, TTL, and options unchanged. Prefetch workers
// use a child context that also cancels on Close; values and deadlines survive.
// TTL is a network hop limit, not a cache expiry time. Cache hits need no hop.
type Cache struct {
	source  eris.BlockSource
	sink    eris.BlockSink
	limit   int
	size    int
	queue   chan job
	stop    context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	entries map[eris.Reference]*list.Element
	regular list.List // Most recently used first in each class.
	high    list.List
	used    int
	// A read overlapping a write must not retain stale storage options.
	// A global generation avoids an unbounded map of per-reference versions.
	version uint64
	writes  int
}

type entry struct {
	ref   eris.Reference
	block eris.Block
	high  bool
}

type job struct {
	ctx     context.Context
	request eris.BlockRequest
}

var _ eris.BlockStore = (*Cache)(nil)

// New starts the worker pool without reading storage. A nil source or a
// nonpositive limit returns fs.ErrInvalid. A typed nil source must support
// method calls. BatchSize is captured here; source limits must remain stable.
func New(source eris.BlockSource, config Config) (*Cache, error) {
	if source == nil || config.CapacityBytes <= 0 || config.Workers <= 0 || config.QueueSize <= 0 {
		return nil, fs.ErrInvalid
	}
	stop, cancel := context.WithCancel(context.Background())
	c := &Cache{
		source: source, limit: source.BatchSize(), size: config.CapacityBytes,
		queue: make(chan job, config.QueueSize), stop: stop, cancel: cancel,
		entries: make(map[eris.Reference]*list.Element),
	}
	if c.limit < 0 {
		c.limit = 1
	}
	c.sink, _ = source.(eris.BlockSink)
	c.workers.Add(config.Workers)
	for range config.Workers {
		go c.worker()
	}
	return c, nil
}

// BatchSize returns the source's batch limit. Zero permits any batch size.
func (c *Cache) BatchSize() int { return c.limit }

// Close cancels background reads, discards queued hints and cached data, and
// waits for all workers. It is safe to call repeatedly or concurrently. The
// source is not closed. It must honor cancellation for Close to finish promptly.
// Foreground operations already in progress may finish; Close does not wait
// for them. Later nonempty operations return fs.ErrClosed. Empty batches remain
// no-ops, and oversized batches still return eris.ErrBatchTooLarge.
func (c *Cache) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		if c.cancel != nil {
			c.cancel()
			close(c.queue)
		}
		clear(c.entries)
		c.regular.Init()
		c.high.Init()
		c.used = 0
	}
	c.mu.Unlock()
	c.workers.Wait()
	return nil
}

// GetBlock returns cached blocks or reads them from the source. Missing blocks
// have zero slots and do not stop later requests. Other errors stop the batch.
// Failed reads are not cached. Cancellation is checked before each request,
// including cache hits.
func (c *Cache) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, c.limit, eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		return c.get(ctx, r)
	})
}

func (c *Cache) get(ctx context.Context, r eris.BlockRequest) (eris.Block, error) {
	c.mu.Lock()
	if c.closed || c.source == nil {
		c.mu.Unlock()
		return eris.Block{}, fs.ErrClosed
	}
	if element := c.entries[r.Reference]; element != nil {
		cached := element.Value.(*entry)
		c.touch(element, r.Options.HighPriority)
		block := clone(cached.block)
		c.mu.Unlock()
		return block, nil
	}
	version, idle := c.version, c.writes == 0
	c.mu.Unlock()

	blocks, err := c.source.GetBlock(ctx, []eris.BlockRequest{r})
	if len(blocks) > 1 || err == nil && len(blocks) != 1 {
		return eris.Block{}, eris.ErrInvalidBatchResult
	}
	if err != nil {
		return eris.Block{}, err
	}
	block := blocks[0]
	if err := eris.VerifyBlock(block.Data, r.Reference); err != nil {
		return eris.Block{}, err
	}
	c.mu.Lock()
	if !c.closed && idle && c.writes == 0 && c.version == version {
		c.remember(r.Reference, block, r.Options.HighPriority)
	}
	c.mu.Unlock()
	return clone(block), nil
}

// PrefetchBlock copies each accepted request into the bounded queue. It never
// waits for storage I/O, queue space, or the cache lock. Empty, canceled,
// oversized, closed, or busy calls are discarded. Cached blocks only refresh
// recency and priority. Workers perform GetBlock calls; they do not forward
// PrefetchBlock hints. Background errors are discarded and can be retried by
// a later hint or foreground read. Accepted hints do not promise retention.
func (c *Cache) PrefetchBlock(ctx context.Context, requests []eris.BlockRequest) {
	if len(requests) == 0 || c.limit > 0 && len(requests) > c.limit || ctx.Err() != nil {
		return
	}
	if !c.mu.TryLock() {
		return
	}
	defer c.mu.Unlock()
	if c.closed || c.source == nil {
		return
	}
	for _, request := range requests {
		if ctx.Err() != nil {
			return
		}
		if element := c.entries[request.Reference]; element != nil {
			c.touch(element, request.Options.HighPriority)
			continue
		}
		select {
		case c.queue <- job{ctx, request}:
		default:
			return
		}
	}
}

func (c *Cache) worker() {
	defer c.workers.Done()
	for job := range c.queue {
		if c.stop.Err() != nil || job.ctx.Err() != nil {
			continue
		}
		ctx, cancel := context.WithCancel(job.ctx)
		stop := context.AfterFunc(c.stop, cancel)
		_, _ = c.GetBlock(ctx, []eris.BlockRequest{job.request})
		stop()
		cancel()
	}
}

// PutBlock writes through to the source and invalidates cached copies. It
// verifies each block and Expected reference before sending that request.
// The source controls stored options, including merging flags on repeated puts.
func (c *Cache) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, c.limit, func(r eris.PutBlockRequest) (eris.Reference, error) {
		ref, err := eris.ReferenceOf(r.Data)
		if err != nil {
			err = errors.Join(eris.ErrInvalidBlock, err)
		} else if r.Expected != nil && *r.Expected != ref {
			err = eris.ErrInvalidBlock
		}
		if err := c.beginWrite(ref, err); err != nil {
			return eris.Reference{}, err
		}
		defer c.endWrite(ref)
		refs, err := c.sink.PutBlock(ctx, []eris.PutBlockRequest{r})
		if len(refs) > 1 || err == nil && len(refs) != 1 {
			return eris.Reference{}, eris.ErrInvalidBatchResult
		}
		if err != nil {
			return eris.Reference{}, err
		}
		if refs[0] != ref {
			return eris.Reference{}, eris.ErrInvalidBlock
		}
		return ref, nil
	})
}

// UpdateBlockOptions passes each update through and invalidates the affected
// entry, including when the source returns false or an error.
func (c *Cache) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, c.limit, func(r eris.BlockRequest) (bool, error) {
		if err := c.beginWrite(r.Reference, nil); err != nil {
			return false, err
		}
		defer c.endWrite(r.Reference)
		results, err := c.sink.UpdateBlockOptions(ctx, []eris.BlockRequest{r})
		if len(results) > 1 || err == nil && len(results) != 1 {
			return false, eris.ErrInvalidBatchResult
		}
		if err != nil {
			return false, err
		}
		return results[0], nil
	})
}

// beginWrite checks storage state before a local validation error. Invalid
// requests do not invalidate entries. Hashing stays outside the cache lock.
func (c *Cache) beginWrite(ref eris.Reference, validationErr error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.source == nil {
		return fs.ErrClosed
	}
	if c.sink == nil {
		return eris.ErrNoBlockStorage
	}
	if validationErr != nil {
		return validationErr
	}
	c.version++
	c.writes++
	c.invalidate(ref)
	return nil
}

func (c *Cache) endWrite(ref eris.Reference) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version++
	c.writes--
	c.invalidate(ref)
}

// The helpers below require mu. Each entry belongs to exactly one list.
func (c *Cache) order(high bool) *list.List {
	if high {
		return &c.high
	}
	return &c.regular
}

func (c *Cache) touch(element *list.Element, high bool) {
	cached := element.Value.(*entry)
	if high && !cached.high {
		c.regular.Remove(element)
		cached.high = true
		c.entries[cached.ref] = c.high.PushFront(cached)
		return
	}
	c.order(cached.high).MoveToFront(element)
}

func (c *Cache) remember(ref eris.Reference, block eris.Block, high bool) {
	if len(block.Data) > c.size {
		return
	}
	high = high || block.Options.HighPriority
	if element := c.entries[ref]; element != nil {
		c.touch(element, high)
		return
	}
	if !high {
		// Check admission before eviction: a block that cannot fit beside
		// protected entries must not discard useful regular entries.
		available := c.size - c.used
		for e := c.regular.Back(); available < len(block.Data) && e != nil; e = e.Prev() {
			available += len(e.Value.(*entry).block.Data)
		}
		if available < len(block.Data) {
			return
		}
	}
	// Evict before insertion to keep accounting within the configured limit.
	for len(block.Data) > c.size-c.used {
		oldest := c.regular.Back()
		if oldest == nil {
			oldest = c.high.Back()
		}
		c.invalidate(oldest.Value.(*entry).ref)
	}
	cached := &entry{ref: ref, block: clone(block), high: high}
	c.entries[ref] = c.order(high).PushFront(cached)
	c.used += len(block.Data)
}

func (c *Cache) invalidate(ref eris.Reference) {
	if element := c.entries[ref]; element != nil {
		cached := element.Value.(*entry)
		c.used -= len(cached.block.Data)
		c.order(cached.high).Remove(element)
		delete(c.entries, ref)
	}
}

func clone(block eris.Block) eris.Block {
	block.Data = append([]byte(nil), block.Data...)
	return block
}
