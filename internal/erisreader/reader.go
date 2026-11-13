// Package erisreader provides a shared random-access reader for ERIS trees.
package erisreader

import (
	"crypto/subtle"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"sync"

	"github.com/asciimoth/goeris"
	"golang.org/x/crypto/blake2b"
)

var (
	ErrInvalidSize  = errors.New("eris reader: invalid decoded size")
	ErrInvalidTree  = errors.New("eris reader: invalid tree")
	ErrSizeMismatch = errors.New("eris reader: decoded size mismatch")
	ErrTooLarge     = errors.New("eris reader: decoded content is too large")
)

type nodeID struct {
	pair  eris.Pair
	level eris.Level
}

type treeSummary struct {
	leaves int64
	last   eris.Pair
}

// Reader decodes one ERIS byte sequence and supports random access. Operations
// on one reader are safe for concurrent use. At most 64 internal nodes and the
// most recent leaf are cached. Use NewReaderWithSize to avoid scanning the tree
// on open.
type Reader struct {
	mu          sync.Mutex
	source      eris.BlockSource
	ttl         eris.TTL
	capability  eris.Capability
	size        int64
	position    int64
	leaves      int64
	maxSize     int64
	cache       map[nodeID][]eris.Pair
	summaries   map[nodeID]treeSummary
	closed      bool
	sized       bool
	lastChecked bool
	leafID      eris.Pair
	leafData    []byte
}

// NewReader opens an ERIS byte sequence from an abstract block source. It
// scans internal nodes and the final leaf to obtain the decoded length and
// check the tree shape. The scan keeps one temporary summary per distinct
// internal node. Other leaf blocks are fetched when read. Use
// NewReaderWithSize when a v2 index already supplies the decoded length.
func NewReader(capability eris.Capability, source eris.BlockSource) (*Reader, error) {
	return NewReaderWithLimit(capability, source, -1, eris.NoTTL)
}

// NewReaderWithSize opens content with a known decoded size, without block I/O.
// It checks tree shape on each visited path and padding when the end is read.
// Unvisited blocks are not checked. Opening and Stat do not validate content.
// This is the reader used for uncompressed v2 files. Its cache holds at most
// 64 internal nodes and one leaf, independent of file size.
func NewReaderWithSize(capability eris.Capability, source eris.BlockSource, size int64) (*Reader, error) {
	if source == nil || capability.Validate() != nil {
		return nil, eris.ErrInvalidCapability
	}
	if size < 0 {
		return nil, ErrInvalidSize
	}
	leaves := size/int64(capability.BlockSize) + 1
	level := eris.Level(0)
	for subtreeCapacity(capability.BlockSize, level) < leaves {
		level++
	}
	if level != capability.Level {
		return nil, ErrSizeMismatch
	}
	return &Reader{
		source: source, capability: capability, size: size, leaves: leaves,
		maxSize: -1, sized: true, cache: make(map[nodeID][]eris.Pair),
	}, nil
}

// NewReaderWithLimit scans the tree and rejects decoded content above maxSize.
// A negative maxSize disables the size limit. Every block read uses ttl unchanged.
func NewReaderWithLimit(capability eris.Capability, source eris.BlockSource, maxSize int64, ttl eris.TTL) (*Reader, error) {
	if source == nil || capability.Validate() != nil {
		return nil, eris.ErrInvalidCapability
	}
	reader := &Reader{
		source: source, capability: capability, maxSize: maxSize, ttl: ttl,
		cache: make(map[nodeID][]eris.Pair), summaries: make(map[nodeID]treeSummary),
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
	lastLength, err := paddedLength(lastNode)
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
		return nil, ErrTooLarge
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

// Close releases references held by the reader. Close is idempotent.
func (reader *Reader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.closed = true
	reader.cache = nil
	reader.summaries = nil
	reader.source = nil
	reader.leafData = nil
	return nil
}

func (reader *Reader) inspect(pair eris.Pair, level eris.Level, root bool) (int64, eris.Pair, error) {
	if level == 0 {
		return 1, pair, nil
	}
	id := nodeID{pair: pair, level: level}
	if !root {
		if summary, ok := reader.summaries[id]; ok {
			return summary.leaves, summary.last, nil
		}
	}
	pairs, err := reader.internal(pair, level, root)
	if err != nil {
		return 0, eris.Pair{}, err
	}
	if root && len(pairs) < 2 {
		return 0, eris.Pair{}, eris.ErrInvalidNode
	}
	capacity := subtreeCapacity(reader.capability.BlockSize, level-1)
	var total int64
	var last eris.Pair
	for i, child := range pairs {
		count, candidate, inspectErr := reader.inspect(child, level-1, false)
		if inspectErr != nil {
			return 0, eris.Pair{}, inspectErr
		}
		if i < len(pairs)-1 && count != capacity {
			return 0, eris.Pair{}, eris.ErrInvalidNode
		}
		if count > math.MaxInt64-total {
			return 0, eris.Pair{}, eris.ErrInvalidNode
		}
		total += count
		if reader.maxSize >= 0 && total > reader.maxSize/int64(reader.capability.BlockSize)+1 {
			return 0, eris.Pair{}, ErrTooLarge
		}
		last = candidate
	}
	if !root {
		reader.summaries[id] = treeSummary{leaves: total, last: last}
	}
	return total, last, nil
}

func (reader *Reader) internal(pair eris.Pair, level eris.Level, root bool) ([]eris.Pair, error) {
	id := nodeID{pair: pair, level: level}
	if pairs, ok := reader.cache[id]; ok {
		return pairs, nil
	}
	node, err := reader.decrypt(pair, level)
	if err != nil {
		return nil, err
	}
	if root {
		digest := blake2b.Sum256(node)
		if subtle.ConstantTimeCompare(digest[:], pair.Key[:]) != 1 {
			return nil, eris.ErrInvalidRootKey
		}
	}
	pairs, err := eris.ParseInternalNode(node)
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

func (reader *Reader) leaf(pair eris.Pair) ([]byte, error) {
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
	length, err := paddedLength(data)
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

func (reader *Reader) decrypt(pair eris.Pair, level eris.Level) ([]byte, error) {
	block, err := reader.source.GetBlock(pair.Reference, reader.ttl)
	if err != nil {
		// A block fetch is not the end of the decoded content. Keep a source
		// EOF from making io.ReadAll or io.Copy accept a truncated file.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if len(block) != int(reader.capability.BlockSize) {
		return nil, eris.ErrInvalidBlockSize
	}
	return eris.DecryptNodeFromSource(block, pair.Key, level)
}

func (reader *Reader) leafPair(index int64) (eris.Pair, error) {
	if index < 0 || index >= reader.leaves {
		return eris.Pair{}, io.EOF
	}
	pair := reader.capability.Root
	base := int64(0)
	for level := reader.capability.Level; level > 0; level-- {
		pairs, err := reader.internal(pair, level, level == reader.capability.Level)
		if err != nil {
			return eris.Pair{}, err
		}
		span := subtreeCapacity(reader.capability.BlockSize, level-1)
		if reader.sized {
			count := min(reader.leaves-base, subtreeCapacity(reader.capability.BlockSize, level))
			want := (count-1)/span + 1
			if int64(len(pairs)) != want {
				return eris.Pair{}, ErrSizeMismatch
			}
		}
		child := index / span
		if child >= int64(len(pairs)) {
			return eris.Pair{}, eris.ErrInvalidNode
		}
		pair = pairs[child]
		base += child * span
		index %= span
	}
	return pair, nil
}

func paddedLength(node []byte) (int, error) {
	for i := len(node) - 1; i >= 0; i-- {
		switch node[i] {
		case 0:
			continue
		case 0x80:
			return i, nil
		default:
			return 0, eris.ErrInvalidPadding
		}
	}
	return 0, eris.ErrInvalidPadding
}

func subtreeCapacity(blockSize eris.BlockSize, level eris.Level) int64 {
	arity, _ := blockSize.Arity()
	capacity := int64(1)
	for range level {
		if capacity > math.MaxInt64/int64(arity) {
			return math.MaxInt64
		}
		capacity *= int64(arity)
	}
	return capacity
}
