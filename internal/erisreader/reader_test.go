package erisreader

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestReaderHelpers(t *testing.T) {
	if _, err := paddedLength([]byte{1}); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("bad padded byte error = %v", err)
	}
	if _, err := paddedLength(nil); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("empty padded data error = %v", err)
	}
	if subtreeCapacity(eris.BlockSize32KiB, 100) != math.MaxInt64 {
		t.Fatal("capacity did not saturate")
	}
}

func TestReaderRandomAccess(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(int(eris.BlockSize1KiB)*40 + 123)
	capability, blocks, err := eris.Encode(content, eris.Secret{8}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if reader.Size() != int64(len(content)) {
		t.Fatalf("Size() = %d", reader.Size())
	}
	if reader.IsClosed() {
		t.Fatal("new reader is closed")
	}
	buffer := make([]byte, 3000)
	n, err := reader.ReadAt(buffer, 900)
	if err != nil || !bytes.Equal(buffer[:n], content[900:3900]) {
		t.Fatalf("ReadAt = (%d, %v)", n, err)
	}
	n, err = reader.ReadAt(buffer, int64(len(content)-100))
	if n != 100 || !errors.Is(err, io.EOF) || !bytes.Equal(buffer[:n], content[len(content)-100:]) {
		t.Fatalf("end ReadAt = (%d, %v)", n, err)
	}
	if position, err := reader.Seek(-10, io.SeekEnd); err != nil || position != int64(len(content)-10) {
		t.Fatalf("Seek = (%d, %v)", position, err)
	}
	buffer = make([]byte, 20)
	n, err = reader.Read(buffer)
	if n != 10 || !errors.Is(err, io.EOF) || !bytes.Equal(buffer[:n], content[len(content)-10:]) {
		t.Fatalf("Read = (%d, %v)", n, err)
	}
	if _, err = reader.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek accepted")
	}
	if _, err = reader.Seek(0, 99); err == nil {
		t.Fatal("invalid seek origin accepted")
	}
	if position, seekErr := reader.Seek(0, io.SeekCurrent); seekErr != nil || position != int64(len(content)) {
		t.Fatalf("current Seek = (%d, %v)", position, seekErr)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if !reader.IsClosed() {
		t.Fatal("closed reader is open")
	}
	if _, err = reader.Read(buffer); !errors.Is(err, stdfs.ErrClosed) {
		t.Fatalf("closed Read error = %v", err)
	}
	if _, err = reader.ReadAt(buffer, 0); !errors.Is(err, stdfs.ErrClosed) {
		t.Fatalf("closed ReadAt error = %v", err)
	}
	if _, err = reader.Seek(0, io.SeekStart); !errors.Is(err, stdfs.ErrClosed) {
		t.Fatalf("closed Seek error = %v", err)
	}
}

func TestScannedReaderCacheBound(t *testing.T) {
	// Distinct leaves produce more than 64 distinct internal nodes.
	content := make([]byte, 1200*1024+17)
	for i := 0; i+8 <= len(content); i += 1024 {
		binary.LittleEndian.PutUint64(content[i:], uint64(i))
	}
	capability, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(capability, blocks)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if reader.CacheSize() > 64 {
		t.Fatalf("cache after scan has %d nodes", reader.CacheSize())
	}
	if len(reader.summaries) != 0 {
		t.Fatal("reader retained scan summaries")
	}
	var buffer [8]byte
	for i := 1199; i >= 0; i -= 16 {
		offset := i * 1024
		if _, err := reader.ReadAt(buffer[:], int64(offset)); err != nil || !bytes.Equal(buffer[:], content[offset:offset+8]) {
			t.Fatalf("read at %d: %v", offset, err)
		}
		if reader.CacheSize() > 64 {
			t.Fatalf("cache after read has %d nodes", reader.CacheSize())
		}
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read after cache eviction: %v", err)
	}
}

func TestReaderLimits(t *testing.T) {
	for _, size := range []int{0, 1, 1023, 1024, 17000} {
		capability, blocks, err := eris.Encode(make([]byte, size), eris.Secret{}, eris.BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int64{-1, 0, int64(size) - 1, int64(size), int64(size) + 1} {
			reader, err := NewReaderWithLimit(capability, blocks, limit)
			if limit >= 0 && limit < int64(size) {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("size %d, limit %d: %v", size, limit, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("size %d, limit %d: %v", size, limit, err)
			}
			if reader.Size() != int64(size) {
				t.Fatal("wrong size")
			}
			_ = reader.Close()
		}
		if _, err := NewReader(capability, nil); !errors.Is(err, eris.ErrInvalidCapability) {
			t.Fatalf("nil source: %v", err)
		}
	}
	if _, err := NewReader(eris.Capability{}, make(eris.BlockSet)); !errors.Is(err, eris.ErrInvalidCapability) {
		t.Fatalf("invalid capability: %v", err)
	}
}

func TestReaderRejectsBadBlocks(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(3000)
	capability, blocks, _ := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	missing := cloneBlockSet(blocks)
	delete(missing, capability.Root.Reference)
	if _, err := NewReader(capability, missing); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing root error = %v", err)
	}
	wrongSize := cloneBlockSet(blocks)
	wrongSize[capability.Root.Reference] = []byte{1}
	if _, err := NewReader(capability, wrongSize); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("short root error = %v", err)
	}
	if _, err := NewReader(capability, malformedSource(make([]byte, eris.BlockSize32KiB))); !errors.Is(err, eris.ErrInvalidBlockSize) {
		t.Fatalf("wrong size from source error = %v", err)
	}
	corrupt := cloneBlockSet(blocks)
	corrupt[capability.Root.Reference][0] ^= 1
	if _, err := NewReader(capability, corrupt); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("corrupt root error = %v", err)
	}
	leafCap, leafBlocks, _ := eris.Encode([]byte("x"), eris.Secret{}, eris.BlockSize1KiB)
	leafBlocks[leafCap.Root.Reference] = make([]byte, eris.BlockSize1KiB)
	if _, err := NewReader(leafCap, leafBlocks); !errors.Is(err, eris.ErrInvalidBlock) {
		t.Fatalf("corrupt leaf error = %v", err)
	}
	reader, _ := NewReader(capability, blocks)
	if _, err := reader.ReadAt(nil, -1); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("negative ReadAt error = %v", err)
	}
	if n, err := reader.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt = (%d, %v)", n, err)
	}
	if n, err := reader.ReadAt(make([]byte, 1), int64(len(content))); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("end ReadAt = (%d, %v)", n, err)
	}
	root, err := eris.DecryptNode(blocks[capability.Root.Reference], capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := eris.ParseInternalNode(root)
	if err != nil {
		t.Fatal(err)
	}
	firstPair := pairs[0]
	delete(blocks, firstPair.Reference)
	if _, err := reader.ReadAt(make([]byte, 1), 0); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing leaf read error = %v", err)
	}
}

func TestReaderRejectsMalformedTreesAndPadding(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	badLeaf := make([]byte, eris.BlockSize1KiB)
	badLeafBlock, badLeafPair, _ := eris.EncryptNode(badLeaf, eris.Secret{}, 0)
	blocks[badLeafPair.Reference] = badLeafBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Root: badLeafPair}, blocks); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatalf("bad padding error = %v", err)
	}

	validLeaf := make([]byte, eris.BlockSize1KiB)
	validLeaf[0] = 0x80
	leafBlock, leafPair, _ := eris.EncryptNode(validLeaf, eris.Secret{}, 0)
	blocks[leafPair.Reference] = leafBlock
	oneChildNode := makeNode(leafPair)
	oneBlock, onePair, _ := eris.EncryptNode(oneChildNode, eris.Secret{}, 1)
	blocks[onePair.Reference] = oneBlock
	oneCap := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: onePair}
	if _, err := NewReader(oneCap, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("one-child root error = %v", err)
	}
	levelTwoNode := makeNode(onePair, onePair)
	levelTwoBlock, levelTwoPair, _ := eris.EncryptNode(levelTwoNode, eris.Secret{}, 2)
	blocks[levelTwoPair.Reference] = levelTwoBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: levelTwoPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("short non-final subtree error = %v", err)
	}
	fullChildren := make([]eris.Pair, 16)
	for i := range fullChildren {
		fullChildren[i] = leafPair
	}
	fullNode := makeNode(fullChildren...)
	fullBlock, fullPair, _ := eris.EncryptNode(fullNode, eris.Secret{}, 1)
	blocks[fullPair.Reference] = fullBlock
	repeatedNode := makeNode(fullPair, fullPair)
	repeatedBlock, repeatedPair, _ := eris.EncryptNode(repeatedNode, eris.Secret{}, 2)
	blocks[repeatedPair.Reference] = repeatedBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: repeatedPair}, blocks); err != nil {
		t.Fatalf("repeated valid subtree error = %v", err)
	}
	missingInternal := eris.Pair{Reference: eris.Reference{9}, Key: eris.Key{8}}
	missingNode := makeNode(missingInternal, onePair)
	missingBlock, missingPair, _ := eris.EncryptNode(missingNode, eris.Secret{}, 2)
	blocks[missingPair.Reference] = missingBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 2, Root: missingPair}, blocks); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatalf("missing internal error = %v", err)
	}

	emptyNode := make([]byte, eris.BlockSize1KiB)
	emptyBlock, emptyPair, _ := eris.EncryptNode(emptyNode, eris.Secret{}, 1)
	blocks[emptyPair.Reference] = emptyBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: emptyPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("empty root error = %v", err)
	}

	twoChildren := makeNode(leafPair, leafPair)
	rootBlock, rootPair, _ := eris.EncryptNode(twoChildren, eris.Secret{}, 1)
	blocks[rootPair.Reference] = rootBlock
	badKeyCap := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: rootPair}
	badKeyCap.Root.Key[0] ^= 1
	if _, err := NewReader(badKeyCap, blocks); !errors.Is(err, eris.ErrInvalidRootKey) {
		t.Fatalf("bad root key error = %v", err)
	}

	brokenSlots := makeNode(leafPair)
	copy(brokenSlots[2*eris.PairSize:], brokenSlots[:eris.PairSize])
	brokenBlock, brokenPair, _ := eris.EncryptNode(brokenSlots, eris.Secret{}, 1)
	blocks[brokenPair.Reference] = brokenBlock
	if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: brokenPair}, blocks); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("non-contiguous pairs error = %v", err)
	}

	if _, err := eris.ParseInternalNode([]byte{1}); !errors.Is(err, eris.ErrInvalidNode) {
		t.Fatalf("short pairs error = %v", err)
	}
}

func cloneBlockSet(blocks eris.BlockSet) eris.BlockSet {
	clone := make(eris.BlockSet, len(blocks))
	for ref, block := range blocks {
		clone[ref] = bytes.Clone(block)
	}
	return clone
}

func makeNode(pairs ...eris.Pair) []byte {
	node := make([]byte, eris.BlockSize1KiB)
	for i, pair := range pairs {
		offset := i * eris.PairSize
		copy(node[offset:offset+32], pair.Reference[:])
		copy(node[offset+32:offset+eris.PairSize], pair.Key[:])
	}
	return node
}

func deterministicBytes(length int) []byte {
	data := make([]byte, length)
	for i := range data {
		data[i] = byte(i*31 + i/7)
	}
	return data
}

type malformedSource []byte

func TestReaderRejectsSourceEOF(t *testing.T) {
	capability, blocks, err := eris.Encode([]byte("content"), eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	for _, sized := range []bool{false, true} {
		source := &failAfterSource{BlockSource: blocks, err: io.EOF}
		var reader *Reader
		if sized {
			reader, err = NewReaderWithSize(capability, source, 7)
		} else {
			reader, err = NewReader(capability, source)
		}
		if err != nil {
			t.Fatal(err)
		}
		source.failAt = source.calls + 1
		_, err = io.ReadAll(reader)
		_ = reader.Close()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("sized=%t: source EOF became a successful short read: %v", sized, err)
		}
	}
}

func (source malformedSource) GetBlock(eris.Reference) ([]byte, error) { return source, nil }

type failAfterSource struct {
	eris.BlockSource
	calls, failAt int
	err           error
}

func (source *failAfterSource) GetBlock(ref eris.Reference) ([]byte, error) {
	source.calls++
	if source.calls == source.failAt {
		return nil, source.err
	}
	return source.BlockSource.GetBlock(ref)
}
func putNode(node []byte, secret eris.Secret, level eris.Level, sink eris.BlockSink) (eris.Pair, error) {
	block, pair, err := eris.EncryptNode(node, secret, level)
	if err != nil {
		return eris.Pair{}, err
	}
	_, err = sink.PutBlock(block)
	return pair, err
}
