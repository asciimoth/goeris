package eris

import (
	"bytes"
	"context"
	"io"

	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/internal/streamio"
)

// EncodeContent reads content, encodes it with ERIS, and writes each encrypted
// block to sink. It keeps at most 64 encrypted blocks (2 MiB) and a bounded
// set of pending tree pairs per level. Batches respect the sink limit.
// The result matches Encode for the same bytes, secret, and block size.
// Each write uses NoTTL and passes ctx unchanged. Cancellation
// is checked between input reads and block writes; it cannot stop a blocked
// input Read. Each block write receives a copy of options, with Root always
// set to true for the capability's root block.
//
// Encoding stops at the first io.EOF, including EOF returned with data. Other
// read errors are preserved. After 100 consecutive empty reads it returns
// io.ErrNoProgress. A nil reader or sink returns ErrInvalidInput. A failed call
// can leave blocks in the sink and returns no usable capability.
func EncodeContent(ctx context.Context, content io.Reader, secret Secret, blockSize BlockSize, sink BlockSink, options BlockOptions) (Capability, error) {
	if !blockSize.Valid() {
		return Capability{}, ErrInvalidBlockSize
	}
	if content == nil || sink == nil {
		return Capability{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	encoder := treeEncoder{ctx: ctx, secret: secret, blockSize: blockSize, sink: sink, options: options, batchSize: batch.Limit(sink.BatchSize())}
	ended := false
	for {
		if err := ctx.Err(); err != nil {
			return Capability{}, err
		}
		node := make([]byte, int(blockSize))
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
		}
		encoder.ended = ended && n < len(node)
		writeOptions := options
		if encoder.ended && len(encoder.levels) == 0 {
			writeOptions.Root = true
		}
		pair, err := encoder.putNode(node, 0, writeOptions)
		if err != nil {
			return Capability{}, err
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

// EncodeContentRecommended reads content and writes its ERIS blocks with the
// recommended block size. It buffers at most 16 KiB to select 1 KiB blocks for
// shorter content or 32 KiB blocks otherwise. The input is read once and need
// not support seeking. The result matches EncodeRecommended for the same bytes
// and secret. Writes and errors follow EncodeContent.
func EncodeContentRecommended(ctx context.Context, content io.Reader, secret Secret, sink BlockSink, options BlockOptions) (Capability, error) {
	if content == nil || sink == nil {
		return Capability{}, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Capability{}, err
	}
	prefix := make([]byte, 1<<14)
	n, err := streamio.ReadFull(ctx, content, prefix)
	var input io.Reader = bytes.NewReader(prefix[:n])
	switch err {
	case nil:
		input = io.MultiReader(input, content)
	case io.EOF:
	default:
		return Capability{}, err
	}
	return EncodeContent(ctx, input, secret, ChooseBlockSize(n), sink, options)
}

// putNode computes references locally so parent nodes can be built before the
// pending batch is written. No capability is returned until flush succeeds.
func (encoder *treeEncoder) putNode(node []byte, level Level, options BlockOptions) (Pair, error) {
	if err := encoder.ctx.Err(); err != nil {
		return Pair{}, err
	}
	block, key, err := EncryptNodeForStorage(node, encoder.secret, level)
	if err != nil {
		return Pair{}, err
	}
	// Encryption has already checked the block length.
	ref, _ := ReferenceOf(block)
	encoder.writes = append(encoder.writes, PutBlockRequest{Data: block, TTL: NoTTL, Options: options, Expected: &ref})
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
	refs, err := encoder.sink.PutBlock(encoder.ctx, encoder.writes)
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
	clear(encoder.writes)
	encoder.writes = encoder.writes[:0]
	return nil
}

type treeEncoder struct {
	ctx       context.Context
	secret    Secret
	blockSize BlockSize
	sink      BlockSink
	options   BlockOptions
	levels    [][]Pair
	ended     bool
	batchSize int
	writes    []PutBlockRequest
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
	node := make([]byte, int(encoder.blockSize))
	for i, pair := range encoder.levels[level] {
		offset := i * PairSize
		copy(node[offset:offset+32], pair.Reference[:])
		copy(node[offset+32:offset+PairSize], pair.Key[:])
	}
	options := encoder.options
	if encoder.ended {
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
