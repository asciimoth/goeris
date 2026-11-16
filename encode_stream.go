package eris

import (
	"bytes"
	"io"

	"github.com/asciimoth/goeris/internal/streamio"
)

// EncodeContent reads content, encodes it with ERIS, and writes each encrypted
// block to sink. It keeps only one content block and a bounded set of pending
// tree pairs per level. The result matches Encode for the same bytes, secret,
// and block size. Each write uses NoTTL.
//
// Encoding stops at the first io.EOF, including EOF returned with data. Other
// read errors are preserved. After 100 consecutive empty reads it returns
// io.ErrNoProgress. A nil reader or sink returns ErrInvalidInput. A failed call
// can leave blocks in the sink and returns no usable capability.
func EncodeContent(content io.Reader, secret Secret, blockSize BlockSize, sink BlockSink) (Capability, error) {
	if !blockSize.Valid() {
		return Capability{}, ErrInvalidBlockSize
	}
	if content == nil || sink == nil {
		return Capability{}, ErrInvalidInput
	}
	encoder := treeEncoder{secret: secret, blockSize: blockSize, sink: sink}
	ended := false
	for {
		node := make([]byte, int(blockSize))
		n := 0
		if !ended {
			var err error
			n, err = streamio.ReadFull(content, node)
			if err != nil && err != io.EOF {
				return Capability{}, err
			}
			ended = err == io.EOF
		}
		if n < len(node) {
			node[n] = 0x80
		}
		pair, err := putNode(node, secret, 0, sink)
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
func EncodeContentRecommended(content io.Reader, secret Secret, sink BlockSink) (Capability, error) {
	if content == nil || sink == nil {
		return Capability{}, ErrInvalidInput
	}
	prefix := make([]byte, 1<<14)
	n, err := streamio.ReadFull(content, prefix)
	var input io.Reader = bytes.NewReader(prefix[:n])
	switch err {
	case nil:
		input = io.MultiReader(input, content)
	case io.EOF:
	default:
		return Capability{}, err
	}
	return EncodeContent(input, secret, ChooseBlockSize(n), sink)
}

func putNode(node []byte, secret Secret, level Level, sink BlockSink) (Pair, error) {
	block, key, err := EncryptNodeForStorage(node, secret, level)
	if err != nil {
		return Pair{}, err
	}
	ref, err := sink.PutBlock(block, NoTTL)
	if err != nil {
		return Pair{}, err
	}
	return Pair{Reference: ref, Key: key}, nil
}

type treeEncoder struct {
	secret    Secret
	blockSize BlockSize
	sink      BlockSink
	levels    [][]Pair
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
	encoder.levels[level] = encoder.levels[level][:0]
	return putNode(node, encoder.secret, Level(level+1), encoder.sink)
}

func (encoder *treeEncoder) finish() (Capability, error) {
	for {
		count, rootLevel, root := encoder.pending()
		if count == 1 {
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
