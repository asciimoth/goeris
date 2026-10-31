package fs

import (
	"errors"
	"io"

	"github.com/asciimoth/eris-tools/eris"
)

// EncodeContent reads content, encodes it with ERIS, and writes each encrypted
// block to sink. It keeps only one content block and a bounded set of pending
// tree pairs per level. It does not access host I/O.
func EncodeContent(content io.Reader, secret eris.Secret, blockSize eris.BlockSize, sink eris.BlockSink) (eris.Capability, error) {
	if !blockSize.Valid() {
		return eris.Capability{}, eris.ErrInvalidBlockSize
	}
	if content == nil || sink == nil {
		return eris.Capability{}, ErrInvalidEntry
	}
	encoder := treeEncoder{secret: secret, blockSize: blockSize, sink: sink}
	for {
		node := make([]byte, int(blockSize))
		n, err := io.ReadFull(content, node)
		switch {
		case err == nil:
			pair, putErr := putNode(node, secret, 0, sink)
			if putErr != nil {
				return eris.Capability{}, putErr
			}
			if putErr = encoder.add(0, pair); putErr != nil {
				return eris.Capability{}, putErr
			}
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			node[n] = 0x80
			pair, putErr := putNode(node, secret, 0, sink)
			if putErr != nil {
				return eris.Capability{}, putErr
			}
			if putErr = encoder.add(0, pair); putErr != nil {
				return eris.Capability{}, putErr
			}
			return encoder.finish()
		default:
			return eris.Capability{}, err
		}
	}
}

// EncodeIndex encodes a canonical ERIS-FS index and writes its ERIS blocks.
func EncodeIndex(index Index, secret eris.Secret, sink eris.BlockSink) (eris.Capability, error) {
	data, err := index.MarshalBinary()
	if err != nil {
		return eris.Capability{}, err
	}
	return EncodeContent(newSliceReader(data), secret, eris.ChooseBlockSize(len(data)), sink)
}

// DecodeIndex reads and parses an ERIS-FS index from source.
func DecodeIndex(capability eris.Capability, source eris.BlockSource) (Index, error) {
	return DecodeIndexWithLimit(capability, source, DefaultMaxIndexSize)
}

// DecodeIndexWithLimit reads and parses an index whose decoded representation
// is at most limit bytes. A non-positive limit is invalid.
func DecodeIndexWithLimit(capability eris.Capability, source eris.BlockSource, limit int64) (Index, error) {
	if limit <= 0 {
		return Index{}, ErrInvalidLimit
	}
	reader, err := newReader(capability, source, limit)
	if err != nil {
		return Index{}, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return Index{}, err
	}
	return ParseIndex(data)
}

func putNode(node []byte, secret eris.Secret, level eris.Level, sink eris.BlockSink) (eris.Pair, error) {
	block, pair, err := eris.EncryptNode(node, secret, level)
	if err != nil {
		return eris.Pair{}, err
	}
	if err = sink.PutBlock(pair.Reference, block); err != nil {
		return eris.Pair{}, err
	}
	return pair, nil
}

type treeEncoder struct {
	secret    eris.Secret
	blockSize eris.BlockSize
	sink      eris.BlockSink
	levels    [][]eris.Pair
}

func (encoder *treeEncoder) add(level int, pair eris.Pair) error {
	if level > int(^eris.Level(0)) {
		return ErrInvalidEntry
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

func (encoder *treeEncoder) parent(level int) (eris.Pair, error) {
	if level >= int(^eris.Level(0)) {
		return eris.Pair{}, ErrInvalidEntry
	}
	node := make([]byte, int(encoder.blockSize))
	for i, pair := range encoder.levels[level] {
		offset := i * eris.PairSize
		copy(node[offset:offset+32], pair.Reference[:])
		copy(node[offset+32:offset+eris.PairSize], pair.Key[:])
	}
	encoder.levels[level] = encoder.levels[level][:0]
	return putNode(node, encoder.secret, eris.Level(level+1), encoder.sink)
}

func (encoder *treeEncoder) finish() (eris.Capability, error) {
	for {
		count, rootLevel, root := encoder.pending()
		if count == 1 {
			return eris.Capability{BlockSize: encoder.blockSize, Level: eris.Level(rootLevel), Root: root}, nil
		}
		level := 0
		for len(encoder.levels[level]) == 0 {
			level++
		}
		parent, err := encoder.parent(level)
		if err != nil {
			return eris.Capability{}, err
		}
		if err = encoder.add(level+1, parent); err != nil {
			return eris.Capability{}, err
		}
	}
}

func (encoder *treeEncoder) pending() (int, int, eris.Pair) {
	count := 0
	rootLevel := 0
	var root eris.Pair
	for level, pairs := range encoder.levels {
		count += len(pairs)
		if len(pairs) != 0 {
			rootLevel = level
			root = pairs[0]
		}
	}
	return count, rootLevel, root
}

type sliceReader struct {
	data []byte
	off  int
}

func newSliceReader(data []byte) *sliceReader { return &sliceReader{data: data} }

func (reader *sliceReader) Read(buffer []byte) (int, error) {
	if reader.off == len(reader.data) {
		return 0, io.EOF
	}
	n := copy(buffer, reader.data[reader.off:])
	reader.off += n
	return n, nil
}
