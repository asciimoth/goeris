package eris

import (
	"context"
	stdfs "io/fs"

	"github.com/asciimoth/goeris/internal/batch"
)

func (s *blockCapabilityStore) Prefetch(ctx context.Context, capability Capability, ttl TTL, options BlockOptions) error {
	return prefetch(ctx, capability, s.source, ttl, options)
}

// prefetch requests all blocks referenced by capability. It reads and checks
// internal nodes to discover references, but never reads content leaves, even
// the final padding leaf. Repeated internal subtrees are scanned only once.
// Calls use ctx, ttl, and options unchanged, with batches of at most 64 hints
// and within source.BatchSize.
//
// This traversal can block on index reads. It returns index and cancellation
// errors, but cannot report failures of the best-effort hints. Success does
// not guarantee that content exists, is valid, or has reached a cache. Call
// Prefetch in a caller-managed goroutine if the traversal must be asynchronous.
func prefetch(ctx context.Context, capability Capability, source BlockSource, ttl TTL, options BlockOptions) error {
	if source == nil || capability.Validate() != nil {
		return ErrInvalidCapability
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reader := &Reader{
		ctx: ctx, source: source, options: options, ttl: ttl, capability: capability,
		maxSize: -1, cache: make(map[decodeNodeID][]Pair),
		summaries: make(map[decodeNodeID]treeSummary), prefetchAll: true,
	}
	PrefetchBlock(ctx, source, capability.Root.Reference, ttl, options)
	_, _, err := reader.inspect(capability.Root, capability.Level, true)
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Prefetch requests all content of this reader without changing its position.
// Unlike PrefetchBlock, it can block while reading internal nodes. ctx controls
// this traversal; the reader's original context still controls normal reads.
// Close does not cancel a traversal that has already started.
func (reader *Reader) Prefetch(ctx context.Context) error {
	reader.mu.Lock()
	if reader.closed {
		reader.mu.Unlock()
		return stdfs.ErrClosed
	}
	capability, source, ttl, options := reader.capability, reader.source, reader.ttl, reader.options
	reader.mu.Unlock()
	return prefetch(ctx, capability, source, ttl, options)
}

// prefetchPairs sends bounded batches. All request storage stays local; sources
// that keep hints after return must copy it as required by BlockSource.
func prefetchPairs(ctx context.Context, source BlockSource, pairs []Pair, ttl TTL, options BlockOptions) {
	limit := batch.Limit(source.BatchSize())
	for len(pairs) > 0 && ctx.Err() == nil {
		count := min(limit, len(pairs))
		requests := make([]BlockRequest, count)
		for i, pair := range pairs[:count] {
			requests[i] = BlockRequest{Reference: pair.Reference, TTL: ttl, Options: options}
		}
		source.PrefetchBlock(ctx, requests)
		pairs = pairs[count:]
	}
}

// preparePrefetch uses a small initial window at the start of content. A
// sequential access doubles it up to 32 leaves; a jump removes speculation.
// Demand hints plus read-ahead cover at most 64 leaves per iteration. The
// policy needs no goroutines and performs no reads to discover extra paths.
func (reader *Reader) preparePrefetch(offset int64) {
	switch {
	case !reader.readStarted && offset == 0:
		reader.readAhead = 4
	case reader.readStarted && offset == reader.readEnd:
		reader.readAhead = min(32, max(4, reader.readAhead*2))
	default:
		reader.readAhead = 0
	}
	reader.readStarted = true
}

// prefetchRange avoids repeating cached-tree walks for small reads in the
// same window. leafPair sends hints when demand reads load a missing index,
// so a previously unknown path does not depend on repeating this walk.
func (reader *Reader) prefetchRange(first, end int64) {
	if first == reader.hintStart && end == reader.hintEnd {
		return
	}
	reader.hintStart, reader.hintEnd = first, end
	reader.hintRange(reader.capability.Root, reader.capability.Level, first, end)
}

// hintRange follows only cached indexes. At a missing index it hints that
// index and stops that path. The next demand read can discover its children.
// This also lets Seek hint its target without doing block I/O.
func (reader *Reader) hintRange(pair Pair, level Level, first, end int64) {
	if first >= end || reader.ctx.Err() != nil {
		return
	}
	pairs, ok := reader.cache[decodeNodeID{pair: pair, level: level}]
	if level == 0 || !ok {
		reader.hintPairs([]Pair{pair})
		return
	}
	span := subtreeLeafCapacity(reader.capability.BlockSize, level-1)
	start := first / span
	stop := min(int64(len(pairs)), (end-1)/span+1)
	if start >= stop {
		return
	}
	if level == 1 {
		reader.hintPairs(pairs[start:stop])
		return
	}
	for i := start; i < stop; i++ {
		base := i * span
		reader.hintRange(pairs[i], level-1, max(0, first-base), min(span, end-base))
	}
}

// hintPairs suppresses repeated hints from cached paths and short reads. Keep
// at most 128 references, independently of content size. Clearing this small
// cache can repeat hints, which is permitted by the best-effort contract.
func (reader *Reader) hintPairs(pairs []Pair) {
	if reader.hinted == nil {
		reader.hinted = make(map[Reference]bool)
	}
	var pending []Pair
	for _, pair := range pairs {
		if reader.hinted[pair.Reference] {
			continue
		}
		if len(reader.hinted) >= 128 {
			clear(reader.hinted)
		}
		reader.hinted[pair.Reference] = true
		pending = append(pending, pair)
	}
	if len(pending) > 0 {
		prefetchPairs(reader.ctx, reader.source, pending, reader.ttl, reader.options)
	}
}
