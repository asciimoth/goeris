package fs

import (
	"crypto/subtle"
	"io"
	stdfs "io/fs"
	"math"
	"sync"

	"github.com/asciimoth/eris-tools/eris"
	"golang.org/x/crypto/blake2b"
)

type nodeID struct {
	pair  eris.Pair
	level eris.Level
}

type treeSummary struct {
	leaves int64
	last   eris.Pair
}

// Reader decodes one ERIS byte sequence and supports random access. It fetches
// internal nodes when it opens and fetches leaf blocks only when they are read.
type Reader struct {
	mu         sync.Mutex
	source     BlockSource
	capability eris.Capability
	size       int64
	position   int64
	leaves     int64
	maxSize    int64
	cache      map[nodeID][]eris.Pair
	summaries  map[nodeID]treeSummary
	closed     bool
}

// NewReader opens an ERIS byte sequence from an abstract block source.
func NewReader(capability eris.Capability, source BlockSource) (*Reader, error) {
	return newReader(capability, source, -1)
}

func newReader(capability eris.Capability, source BlockSource, maxSize int64) (*Reader, error) {
	if source == nil || capability.Validate() != nil {
		return nil, eris.ErrInvalidCapability
	}
	reader := &Reader{
		source: source, capability: capability, maxSize: maxSize,
		cache: make(map[nodeID][]eris.Pair), summaries: make(map[nodeID]treeSummary),
	}
	leaves, last, err := reader.inspect(capability.Root, capability.Level, true)
	if err != nil {
		return nil, err
	}
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
		return nil, ErrInvalidIndex
	}
	reader.leaves = leaves
	reader.size = (leaves-1)*blockSize + int64(lastLength)
	if maxSize >= 0 && reader.size > maxSize {
		return nil, ErrIndexTooLarge
	}
	return reader, nil
}

// Size returns the decoded content size.
func (reader *Reader) Size() int64 {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.size
}

// Read implements io.Reader.
func (reader *Reader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, ErrClosed
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
		return 0, ErrClosed
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
		node, err := reader.decrypt(pair, 0)
		if err != nil {
			return written, err
		}
		amount := min(requested-written, len(node)-within)
		copy(buffer[written:written+amount], node[within:within+amount])
		written += amount
		offset += int64(amount)
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
		return 0, ErrClosed
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
			return 0, eris.Pair{}, ErrIndexTooLarge
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
	pairs, err := parsePairs(node)
	if err != nil {
		return nil, err
	}
	reader.cache[id] = pairs
	return pairs, nil
}

func (reader *Reader) decrypt(pair eris.Pair, level eris.Level) ([]byte, error) {
	block, err := reader.source.GetBlock(pair.Reference)
	if err != nil {
		return nil, err
	}
	if len(block) != int(reader.capability.BlockSize) {
		return nil, eris.ErrInvalidBlockSize
	}
	return eris.DecryptNode(block, pair, level)
}

func (reader *Reader) leafPair(index int64) (eris.Pair, error) {
	if index < 0 || index >= reader.leaves {
		return eris.Pair{}, io.EOF
	}
	pair := reader.capability.Root
	for level := reader.capability.Level; level > 0; level-- {
		pairs, err := reader.internal(pair, level, level == reader.capability.Level)
		if err != nil {
			return eris.Pair{}, err
		}
		span := subtreeCapacity(reader.capability.BlockSize, level-1)
		child := index / span
		if child >= int64(len(pairs)) {
			return eris.Pair{}, eris.ErrInvalidNode
		}
		pair = pairs[child]
		index %= span
	}
	return pair, nil
}

func parsePairs(node []byte) ([]eris.Pair, error) {
	if len(node)%eris.PairSize != 0 {
		return nil, eris.ErrInvalidNode
	}
	pairs := make([]eris.Pair, 0, len(node)/eris.PairSize)
	ended := false
	for offset := 0; offset < len(node); offset += eris.PairSize {
		slot := node[offset : offset+eris.PairSize]
		zero := true
		for _, value := range slot {
			zero = zero && value == 0
		}
		if zero {
			ended = true
			continue
		}
		if ended {
			return nil, eris.ErrInvalidNode
		}
		var pair eris.Pair
		copy(pair.Reference[:], slot[:32])
		copy(pair.Key[:], slot[32:])
		pairs = append(pairs, pair)
	}
	if len(pairs) == 0 {
		return nil, eris.ErrInvalidNode
	}
	return pairs, nil
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
