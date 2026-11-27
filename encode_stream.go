package eris

import (
	"bytes"
	"context"
	"io"
	"slices"
	"sync"

	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/internal/streamio"
)

var recommendedPrefixPool = sync.Pool{
	New: func() any { return new([recommendedBlockSizeThreshold]byte) },
}

// encodeContent reads content, encodes it with ERIS, and writes each encrypted
// block to sink. It keeps at most 64 encrypted blocks (2 MiB) and a bounded
// set of pending tree pairs per level. Batches respect the sink limit.
// Each write uses NoTTL and passes ctx unchanged. Cancellation
// is checked between input reads and block writes; it cannot stop a blocked
// input Read. Each block write receives a copy of options, with Root always
// set on the root block unless PreserveRoot is enabled.
//
// Encoding stops at the first io.EOF, including EOF returned with data. Other
// read errors are preserved. After 100 consecutive empty reads it returns
// io.ErrNoProgress. A size other than BlockSize1KiB or BlockSize32KiB returns
// ErrInvalidBlockSize. A nil reader returns ErrInvalidInput. A failed
// call can leave blocks in the sink and returns no usable capability.
func encodeContent(ctx context.Context, content io.Reader, secret Secret, sink BlockSink, options EncodeOptions) (Capability, error) {
	blockSize := options.BlockSize
	if !blockSize.Valid() {
		return Capability{}, ErrInvalidBlockSize
	}
	if content == nil {
		return Capability{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	encoder := treeEncoder{
		ctx: ctx, leafHash: newLeafHasher(secret), blockSize: blockSize, sink: sink,
		options: options.BlockOptions, preserveRoot: options.PreserveRoot,
		batchSize: batch.Limit(sink.BatchSize()),
	}
	// Pending writes have their own buffers.
	node := make([]byte, int(blockSize))
	ended := false
	for {
		if err := ctx.Err(); err != nil {
			return Capability{}, err
		}
		n := 0
		if !ended {
			var err error
			n, err = streamio.ReadFull(ctx, content, node)
			if err != nil && err != io.EOF {
				return Capability{}, err
			}
			ended = err == io.EOF
		}
		if n < len(node) {
			node[n] = 0x80
			clear(node[n+1:])
		}
		encoder.ended = ended && n < len(node)
		writeOptions := encoder.options
		singleLeaf := encoder.ended && len(encoder.levels) == 0
		if !encoder.preserveRoot && singleLeaf {
			writeOptions.Root = true
		}
		pair, err := encoder.putNode(node, 0, writeOptions)
		if err != nil {
			return Capability{}, err
		}
		if singleLeaf {
			if err := encoder.flush(); err != nil {
				return Capability{}, err
			}
			return Capability{BlockSize: blockSize, Root: pair}, nil
		}
		if err = encoder.add(0, pair); err != nil {
			return Capability{}, err
		}
		if ended && n < len(node) {
			return encoder.finish()
		}
		// EOF with a full block needs one more block for padding, but must
		// not cause another read from the source.
	}
}

// encodeContentRecommended reads content and writes its ERIS blocks with the
// recommended block size. It buffers at most 16 KiB to select 1 KiB blocks for
// shorter content or 32 KiB blocks otherwise. The input is read once and need
// not support seeking. Writes and errors follow encodeContent.
func encodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, sink BlockSink, options EncodeOptions) (Capability, error) {
	if content == nil {
		return Capability{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	prefix := recommendedPrefixPool.Get().(*[recommendedBlockSizeThreshold]byte)
	defer func() {
		// The next stream's reader can inspect the entire buffer.
		clear(prefix[:])
		recommendedPrefixPool.Put(prefix)
	}()
	n, err := streamio.ReadFull(ctx, content, prefix[:])
	var input io.Reader = bytes.NewReader(prefix[:n])
	switch err {
	case nil:
		input = io.MultiReader(input, content)
	case io.EOF:
	default:
		return Capability{}, err
	}
	options.BlockSize = ChooseBlockSize(n)
	return encodeContent(ctx, input, secret, sink, options)
}

// putNode computes references locally so parent nodes can be built before the
// pending batch is written. No capability is returned until flush succeeds.
func (encoder *treeEncoder) putNode(node []byte, level Level, options BlockOptions) (Pair, error) {
	if err := encoder.ctx.Err(); err != nil {
		return Pair{}, err
	}
	if !ValidBlockLength(len(node)) {
		return Pair{}, ErrInvalidBlockSize
	}
	// Grow only as needed. After a flush, reuse the buffers in these slots.
	encoder.writes = slices.Grow(encoder.writes, 1)[:len(encoder.writes)+1]
	request := &encoder.writes[len(encoder.writes)-1]
	if request.Expected == nil {
		request.Expected = new(Reference)
	}
	if len(request.Data) != len(node) {
		request.Data = make([]byte, len(node))
	}
	key := encryptNodeInto(request.Data, node, encoder.leafHash, level)
	ref, _ := ReferenceOf(request.Data)
	*request.Expected = ref
	request.TTL = NoTTL
	request.Options = options
	if len(encoder.writes) >= encoder.batchSize {
		if err := encoder.flush(); err != nil {
			return Pair{}, err
		}
	}
	return Pair{Reference: ref, Key: key}, nil
}

func (encoder *treeEncoder) flush() error {
	if len(encoder.writes) == 0 {
		return nil
	}
	if err := encoder.ctx.Err(); err != nil {
		return err
	}
	writes := slices.Clip(encoder.writes)
	refs, err := encoder.sink.PutBlock(encoder.ctx, writes)
	if err != nil {
		return err
	}
	if len(refs) != len(encoder.writes) {
		return ErrInvalidBatchResult
	}
	for i, ref := range refs {
		if ref != *encoder.writes[i].Expected {
			return ErrInvalidBlock
		}
	}
	encoder.writes = encoder.writes[:0]
	return nil
}

type treeEncoder struct {
	ctx          context.Context
	leafHash     *leafHasher
	blockSize    BlockSize
	sink         BlockSink
	options      BlockOptions
	preserveRoot bool
	levels       [][]Pair
	ended        bool
	batchSize    int
	writes       []PutBlockRequest
	parentNode   []byte
}

func (encoder *treeEncoder) add(level int, pair Pair) error {
	if level > int(^Level(0)) {
		return ErrInvalidTree
	}
	for len(encoder.levels) <= level {
		encoder.levels = append(encoder.levels, nil)
	}
	encoder.levels[level] = append(encoder.levels[level], pair)
	arity, _ := encoder.blockSize.Arity()
	if len(encoder.levels[level]) < arity {
		return nil
	}
	parent, err := encoder.parent(level)
	if err != nil {
		return err
	}
	return encoder.add(level+1, parent)
}

func (encoder *treeEncoder) parent(level int) (Pair, error) {
	if level >= int(^Level(0)) {
		return Pair{}, ErrInvalidTree
	}
	if encoder.parentNode == nil {
		encoder.parentNode = make([]byte, int(encoder.blockSize))
	}
	node := encoder.parentNode
	pairs := encoder.levels[level]
	for i, pair := range pairs {
		offset := i * PairSize
		copy(node[offset:offset+ReferenceSize], pair.Reference[:])
		copy(node[offset+ReferenceSize:offset+PairSize], pair.Key[:])
	}
	clear(node[len(pairs)*PairSize:])
	options := encoder.options
	if !encoder.preserveRoot && encoder.ended {
		if count, _, _ := encoder.pending(); count == len(encoder.levels[level]) {
			// This parent contains all remaining pairs, so it is the root.
			options.Root = true
		}
	}
	encoder.levels[level] = encoder.levels[level][:0]
	return encoder.putNode(node, Level(level+1), options)
}

func (encoder *treeEncoder) finish() (Capability, error) {
	for {
		count, rootLevel, root := encoder.pending()
		if count == 1 {
			if err := encoder.flush(); err != nil {
				return Capability{}, err
			}
			return Capability{BlockSize: encoder.blockSize, Level: Level(rootLevel), Root: root}, nil
		}
		level := 0
		for len(encoder.levels[level]) == 0 {
			level++
		}
		parent, err := encoder.parent(level)
		if err != nil {
			return Capability{}, err
		}
		if err = encoder.add(level+1, parent); err != nil {
			return Capability{}, err
		}
	}
}

func (encoder *treeEncoder) pending() (int, int, Pair) {
	count := 0
	rootLevel := 0
	var root Pair
	for level, pairs := range encoder.levels {
		count += len(pairs)
		if len(pairs) != 0 {
			rootLevel = level
			root = pairs[0]
		}
	}
	return count, rootLevel, root
}
