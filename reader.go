package eris

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"sync"

	"github.com/asciimoth/goeris/internal/batch"
)

type treeSummary struct {
	leaves int64
	last   Pair
}

// Reader decodes one ERIS byte sequence and supports random access. Operations
// on one reader are safe for concurrent use. At most 64 internal nodes and the
// most recent leaf are cached. Reads fetch at most 64 requested leaves per batch
// (at most 2 MiB of encrypted block data). Use NewReaderWithSize to avoid scanning
// the tree on open. Create a Reader with one of the NewReader functions; its zero value
// is not ready for use. The source must verify block references as required by
// BlockSource. Every block read uses the context supplied at construction,
// unchanged. Each block read receives a copy of the construction options.
// Cancellation stops later reads, including reads from the cache.
type Reader struct {
	ctx         context.Context
	mu          sync.Mutex
	source      BlockSource
	options     BlockOptions
	ttl         TTL
	capability  Capability
	size        int64
	position    int64
	leaves      int64
	maxSize     int64
	cache       map[decodeNodeID][]Pair
	summaries   map[decodeNodeID]treeSummary
	closed      bool
	sized       bool
	lastChecked bool
	leafID      Pair
	leafData    []byte
}

// NewReader opens an ERIS byte sequence from an abstract block source. It
// scans internal nodes and the final leaf to obtain the decoded length and
// check the tree shape. The scan keeps one temporary summary per distinct
// internal node. Other leaf blocks are fetched when read. Use
// NewReaderWithSize when the decoded length is already known. Reads use NoTTL.
func NewReader(ctx context.Context, capability Capability, source BlockSource, options BlockOptions) (*Reader, error) {
	return NewReaderWithLimit(ctx, capability, source, -1, NoTTL, options)
}

// NewReaderWithSize opens content with a known decoded size, without block I/O.
// It checks tree shape on each visited path and padding when the end is read.
// Unvisited blocks are not checked. Opening and Size do not validate content.
// Its cache holds at most 64 internal nodes and one leaf, independent of content
// size. Reads use NoTTL. A negative size returns ErrInvalidSize; content that
// does not match size returns ErrSizeMismatch when the mismatch is detected.
func NewReaderWithSize(ctx context.Context, capability Capability, source BlockSource, size int64, options BlockOptions) (*Reader, error) {
	if source == nil || capability.Validate() != nil {
		return nil, ErrInvalidCapability
	}
	if size < 0 {
		return nil, ErrInvalidSize
	}
	leaves := size/int64(capability.BlockSize) + 1
	level := Level(0)
	for subtreeLeafCapacity(capability.BlockSize, level) < leaves {
		level++
	}
	if level != capability.Level {
		return nil, ErrSizeMismatch
	}
	return &Reader{
		ctx: ctx, source: source, options: options, capability: capability, size: size, leaves: leaves,
		maxSize: -1, sized: true, cache: make(map[decodeNodeID][]Pair),
	}, nil
}

// NewReaderWithLimit scans the tree and rejects decoded content above maxSize.
// A negative maxSize disables the size limit. Every block read uses ttl unchanged.
// Content above the limit returns ErrDecodedTooLarge.
func NewReaderWithLimit(ctx context.Context, capability Capability, source BlockSource, maxSize int64, ttl TTL, options BlockOptions) (*Reader, error) {
	if source == nil || capability.Validate() != nil {
		return nil, ErrInvalidCapability
	}
	reader := &Reader{
		ctx: ctx, source: source, options: options, capability: capability, maxSize: maxSize, ttl: ttl,
		cache: make(map[decodeNodeID][]Pair), summaries: make(map[decodeNodeID]treeSummary),
	}
	leaves, last, err := reader.inspect(capability.Root, capability.Level, true)
	if err != nil {
		return nil, err
	}
	// Summaries prevent repeated subtrees from expanding the scan work. They
	// are not needed for reads and must not keep the full tree in memory.
	reader.summaries = nil
	lastNode, err := reader.decrypt(last, 0)
	if err != nil {
		return nil, err
	}
	lastLength, err := paddedContentLength(lastNode)
	if err != nil {
		return nil, err
	}
	blockSize := int64(capability.BlockSize)
	if leaves-1 > (math.MaxInt64-int64(lastLength))/blockSize {
		return nil, ErrInvalidTree
	}
	reader.leaves = leaves
	reader.size = (leaves-1)*blockSize + int64(lastLength)
	if maxSize >= 0 && reader.size > maxSize {
		return nil, ErrDecodedTooLarge
	}
	return reader, nil
}

// Size returns the decoded content size.
func (reader *Reader) Size() int64 {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.size
}

// IsClosed reports whether Close has been called.
func (reader *Reader) IsClosed() bool {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.closed
}

// CacheSize returns the number of cached internal nodes.
func (reader *Reader) CacheSize() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return len(reader.cache)
}

// Read implements io.Reader.
func (reader *Reader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, stdfs.ErrClosed
	}
	n, err := reader.readAt(buffer, reader.position)
	reader.position += int64(n)
	return n, err
}

// ReadAt implements io.ReaderAt. It does not change the seek position.
func (reader *Reader) ReadAt(buffer []byte, offset int64) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, stdfs.ErrClosed
	}
	return reader.readAt(buffer, offset)
}

func (reader *Reader) readAt(buffer []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, stdfs.ErrInvalid
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if offset >= reader.size {
		if err := reader.checkLast(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	requested := len(buffer)
	if int64(requested) > reader.size-offset {
		requested = int(reader.size - offset)
	}
	written := 0
	blockSize := int64(reader.capability.BlockSize)
	for written < requested {
		if err := reader.ctx.Err(); err != nil {
			return written, err
		}
		// Batch only leaves needed by this read. Resolve internal paths through
		// the existing bounded cache and keep no additional leaves between calls.
		count := min(batch.Limit(reader.source.BatchSize()), 1+(int(offset%blockSize)+requested-written-1)/int(blockSize))
		if count > 1 {
			n, err := reader.readBatch(buffer[written:requested], offset, count)
			written += n
			offset += int64(n)
			if err != nil {
				return written, err
			}
			continue
		}
		leafIndex := offset / blockSize
		within := int(offset % blockSize)
		pair, err := reader.leafPair(leafIndex)
		if err != nil {
			return written, err
		}
		node, err := reader.leaf(pair)
		if err != nil {
			return written, err
		}
		if reader.sized && leafIndex == reader.leaves-1 {
			if err := reader.checkPadding(node); err != nil {
				return written, err
			}
		}
		amount := min(requested-written, len(node)-within)
		copy(buffer[written:written+amount], node[within:within+amount])
		written += amount
		offset += int64(amount)
	}
	if offset == reader.size {
		if err := reader.checkLast(); err != nil {
			return written, err
		}
	}
	if written < len(buffer) {
		return written, io.EOF
	}
	return written, nil
}

// readBatch returns all bytes completed before a path, fetch, or decode error.
// A storage failure must never look like the end of the decoded stream.
func (reader *Reader) readBatch(buffer []byte, offset int64, count int) (int, error) {
	pairs := make([]Pair, 0, count)
	requests := make([]BlockRequest, 0, count)
	blockSize := int64(reader.capability.BlockSize)
	first := offset / blockSize
	var pathErr error
	for i := range count {
		if pathErr = reader.ctx.Err(); pathErr != nil {
			break
		}
		var pair Pair
		pair, pathErr = reader.leafPair(first + int64(i))
		if pathErr != nil {
			break
		}
		pairs = append(pairs, pair)
		requests = append(requests, BlockRequest{Reference: pair.Reference, TTL: reader.ttl, Options: reader.options})
	}
	blocks, err := reader.source.GetBlock(reader.ctx, requests)
	if len(blocks) > len(requests) || err == nil && len(blocks) != len(requests) {
		return 0, ErrInvalidBatchResult
	}
	written := 0
	for i, block := range blocks {
		if cancelErr := reader.ctx.Err(); cancelErr != nil {
			return written, cancelErr
		}
		// Storage can return later leaves after a missing block. A content
		// reader must stop at that gap; it cannot concatenate those leaves.
		if block.Data == nil && errors.Is(err, ErrMissingBlock) {
			return written, err
		}
		if len(block.Data) != int(blockSize) {
			return written, ErrInvalidBlockSize
		}
		// The capability and the size check above guarantee a valid length.
		node, _ := DecryptNodeFromSource(block.Data, pairs[i].Key, 0)
		if reader.sized && first+int64(i) == reader.leaves-1 {
			if paddingErr := reader.checkPadding(node); paddingErr != nil {
				return written, paddingErr
			}
		}
		reader.leafID, reader.leafData = pairs[i], node
		within := int(offset % blockSize)
		amount := min(len(buffer)-written, len(node)-within)
		copy(buffer[written:written+amount], node[within:within+amount])
		written += amount
		offset += int64(amount)
	}
	if err == nil {
		err = pathErr
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return written, err
}

// Seek implements io.Seeker.
func (reader *Reader) Seek(offset int64, whence int) (int64, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, stdfs.ErrClosed
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = reader.position
	case io.SeekEnd:
		base = reader.size
	default:
		return reader.position, stdfs.ErrInvalid
	}
	if (offset > 0 && base > math.MaxInt64-offset) || (offset < 0 && base < math.MinInt64-offset) || base+offset < 0 {
		return reader.position, stdfs.ErrInvalid
	}
	reader.position = base + offset
	return reader.position, nil
}

// Close releases references held by the reader. It does not close the block
// source. Close is idempotent.
func (reader *Reader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.closed = true
	reader.cache = nil
	reader.summaries = nil
	reader.source = nil
	reader.ctx = nil
	reader.leafData = nil
	return nil
}

func (reader *Reader) inspect(pair Pair, level Level, root bool) (int64, Pair, error) {
	if level == 0 {
		return 1, pair, nil
	}
	id := decodeNodeID{pair: pair, level: level}
	if !root {
		if summary, ok := reader.summaries[id]; ok {
			return summary.leaves, summary.last, nil
		}
	}
	pairs, err := reader.internal(pair, level, root)
	if err != nil {
		return 0, Pair{}, err
	}
	if root && len(pairs) < 2 {
		return 0, Pair{}, ErrInvalidNode
	}
	capacity := subtreeLeafCapacity(reader.capability.BlockSize, level-1)
	var total int64
	var last Pair
	for i, child := range pairs {
		count, candidate, inspectErr := reader.inspect(child, level-1, false)
		if inspectErr != nil {
			return 0, Pair{}, inspectErr
		}
		if i < len(pairs)-1 && count != capacity {
			return 0, Pair{}, ErrInvalidNode
		}
		if count > math.MaxInt64-total {
			return 0, Pair{}, ErrInvalidNode
		}
		total += count
		if reader.maxSize >= 0 && total > reader.maxSize/int64(reader.capability.BlockSize)+1 {
			return 0, Pair{}, ErrDecodedTooLarge
		}
		last = candidate
	}
	if !root {
		reader.summaries[id] = treeSummary{leaves: total, last: last}
	}
	return total, last, nil
}

func (reader *Reader) internal(pair Pair, level Level, root bool) ([]Pair, error) {
	id := decodeNodeID{pair: pair, level: level}
	if pairs, ok := reader.cache[id]; ok {
		return pairs, nil
	}
	node, err := reader.decrypt(pair, level)
	if err != nil {
		return nil, err
	}
	if root {
		digest := blake2bSum(node)
		if subtle.ConstantTimeCompare(digest[:], pair.Key[:]) != 1 {
			return nil, ErrInvalidRootKey
		}
	}
	pairs, err := ParseInternalNode(node)
	if err != nil {
		return nil, err
	}
	if len(reader.cache) >= 64 {
		// Clear a bounded cache instead of retaining an entire large tree.
		clear(reader.cache)
	}
	reader.cache[id] = pairs
	return pairs, nil
}

func (reader *Reader) leaf(pair Pair) ([]byte, error) {
	if reader.leafData != nil && reader.leafID == pair {
		return reader.leafData, nil
	}
	data, err := reader.decrypt(pair, 0)
	if err != nil {
		return nil, err
	}
	reader.leafID, reader.leafData = pair, data
	return data, nil
}

func (reader *Reader) checkPadding(data []byte) error {
	length, err := paddedContentLength(data)
	if err != nil {
		return err
	}
	if int64(length) != reader.size%int64(reader.capability.BlockSize) {
		return ErrSizeMismatch
	}
	reader.lastChecked = true
	return nil
}

func (reader *Reader) checkLast() error {
	if !reader.sized || reader.lastChecked {
		return nil
	}
	pair, err := reader.leafPair(reader.leaves - 1)
	if err != nil {
		return err
	}
	data, err := reader.leaf(pair)
	if err != nil {
		return err
	}
	return reader.checkPadding(data)
}

func (reader *Reader) decrypt(pair Pair, level Level) ([]byte, error) {
	if err := reader.ctx.Err(); err != nil {
		return nil, err
	}
	block, err, _ := GetBlock(reader.ctx, reader.source, pair.Reference, reader.ttl, reader.options)
	if err != nil {
		// A block fetch is not the end of the decoded content. Keep a source
		// EOF from making io.ReadAll or io.Copy accept a truncated file.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if len(block) != int(reader.capability.BlockSize) {
		return nil, ErrInvalidBlockSize
	}
	return DecryptNodeFromSource(block, pair.Key, level)
}

func (reader *Reader) leafPair(index int64) (Pair, error) {
	if index < 0 || index >= reader.leaves {
		return Pair{}, io.EOF
	}
	pair := reader.capability.Root
	base := int64(0)
	for level := reader.capability.Level; level > 0; level-- {
		pairs, err := reader.internal(pair, level, level == reader.capability.Level)
		if err != nil {
			return Pair{}, err
		}
		span := subtreeLeafCapacity(reader.capability.BlockSize, level-1)
		if reader.sized {
			count := min(reader.leaves-base, subtreeLeafCapacity(reader.capability.BlockSize, level))
			want := (count-1)/span + 1
			if int64(len(pairs)) != want {
				return Pair{}, ErrSizeMismatch
			}
		}
		child := index / span
		if child >= int64(len(pairs)) {
			return Pair{}, ErrInvalidNode
		}
		pair = pairs[child]
		base += child * span
		index %= span
	}
	return pair, nil
}
