package eris

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"math"
	"testing"

	"github.com/asciimoth/goeris/internal/batch"
)

func TestReaderHelpers(t *testing.T) {
	if _, err := paddedContentLength([]byte{1}); !errors.Is(err, ErrInvalidPadding) {
		t.Fatalf("bad padded byte error = %v", err)
	}
	if _, err := paddedContentLength(nil); !errors.Is(err, ErrInvalidPadding) {
		t.Fatalf("empty padded data error = %v", err)
	}
	if subtreeLeafCapacity(BlockSize32KiB, 100) != math.MaxInt64 {
		t.Fatal("capacity did not saturate")
	}
}

func TestReaderRandomAccess(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(int(BlockSize1KiB)*40 + 123)
	store := newTestStore()
	capability, err := store.Encode(t.Context(), content, Secret{8}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.NewReader(context.Background(), capability, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader := opened.(*Reader)
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
	store := newTestStore()
	capability, err := store.Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.NewReader(context.Background(), capability, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader := opened.(*Reader)
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
		store := newTestStore()
		capability, err := store.Encode(t.Context(), make([]byte, size), Secret{}, BlockSize1KiB, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int64{-1, 0, int64(size) - 1, int64(size), int64(size) + 1} {
			reader, err := store.NewReaderWithLimit(context.Background(), capability, limit, NoTTL, BlockOptions{})
			if limit >= 0 && limit < int64(size) {
				if !errors.Is(err, ErrDecodedTooLarge) {
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
		if _, err := NewBlockCapabilityStore(nil).NewReader(context.Background(), capability, BlockOptions{}); !errors.Is(err, ErrInvalidCapability) {
			t.Fatalf("nil source: %v", err)
		}
	}
	if _, err := newTestStore().NewReader(context.Background(), Capability{}, BlockOptions{}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("invalid capability: %v", err)
	}
}

func TestReaderRejectsBadBlocks(t *testing.T) {
	t.Parallel()
	content := deterministicBytes(3000)
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	store := NewBlockCapabilityStore(blocks)
	capability, _ := store.Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	missing := cloneBlockSet(blocks)
	delete(missing.Blocks, capability.Root.Reference)
	if _, err := NewBlockCapabilityStore(missing).NewReader(context.Background(), capability, BlockOptions{}); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing root error = %v", err)
	}
	wrongSize := cloneBlockSet(blocks)
	wrongSize.Blocks[capability.Root.Reference] = Block{Data: []byte{1}}
	if _, err := NewBlockCapabilityStore(wrongSize).NewReader(context.Background(), capability, BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("short root error = %v", err)
	}
	if _, err := NewBlockCapabilityStore(malformedSource(make([]byte, BlockSize32KiB))).NewReader(context.Background(), capability, BlockOptions{}); !errors.Is(err, ErrInvalidBlockSize) {
		t.Fatalf("wrong size from source error = %v", err)
	}
	corrupt := cloneBlockSet(blocks)
	corrupt.Blocks[capability.Root.Reference].Data[0] ^= 1
	if _, err := NewBlockCapabilityStore(corrupt).NewReader(context.Background(), capability, BlockOptions{}); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("corrupt root error = %v", err)
	}
	leafBlocks := BlockSet{Blocks: make(map[Reference]Block)}
	leafCap, _ := NewBlockCapabilityStore(leafBlocks).Encode(t.Context(), []byte("x"), Secret{}, BlockSize1KiB, BlockOptions{})
	leafBlocks.Blocks[leafCap.Root.Reference] = Block{Data: make([]byte, BlockSize1KiB)}
	if _, err := NewBlockCapabilityStore(leafBlocks).NewReader(context.Background(), leafCap, BlockOptions{}); !errors.Is(err, ErrInvalidBlock) {
		t.Fatalf("corrupt leaf error = %v", err)
	}
	reader, _ := store.NewReader(context.Background(), capability, BlockOptions{})
	if _, err := reader.ReadAt(nil, -1); !errors.Is(err, stdfs.ErrInvalid) {
		t.Fatalf("negative ReadAt error = %v", err)
	}
	if n, err := reader.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt = (%d, %v)", n, err)
	}
	if n, err := reader.ReadAt(make([]byte, 1), int64(len(content))); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("end ReadAt = (%d, %v)", n, err)
	}
	root, err := DecryptNode(blocks.Blocks[capability.Root.Reference].Data, capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := ParseInternalNode(root)
	if err != nil {
		t.Fatal(err)
	}
	firstPair := pairs[0]
	delete(blocks.Blocks, firstPair.Reference)
	if _, err := reader.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing leaf read error = %v", err)
	}
}

func TestReaderRejectsMalformedTreesAndPadding(t *testing.T) {
	t.Parallel()
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	store := NewBlockCapabilityStore(blocks)
	badLeaf := make([]byte, BlockSize1KiB)
	badLeafBlock, badLeafPair, _ := EncryptNode(badLeaf, Secret{}, 0)
	blocks.Blocks[badLeafPair.Reference] = Block{Data: badLeafBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Root: badLeafPair}, BlockOptions{}); !errors.Is(err, ErrInvalidPadding) {
		t.Fatalf("bad padding error = %v", err)
	}

	validLeaf := make([]byte, BlockSize1KiB)
	validLeaf[0] = 0x80
	leafBlock, leafPair, _ := EncryptNode(validLeaf, Secret{}, 0)
	blocks.Blocks[leafPair.Reference] = Block{Data: leafBlock}
	oneChildNode := makeNode(leafPair)
	oneBlock, onePair, _ := EncryptNode(oneChildNode, Secret{}, 1)
	blocks.Blocks[onePair.Reference] = Block{Data: oneBlock}
	oneCap := Capability{BlockSize: BlockSize1KiB, Level: 1, Root: onePair}
	if _, err := store.NewReader(context.Background(), oneCap, BlockOptions{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("one-child root error = %v", err)
	}
	levelTwoNode := makeNode(onePair, onePair)
	levelTwoBlock, levelTwoPair, _ := EncryptNode(levelTwoNode, Secret{}, 2)
	blocks.Blocks[levelTwoPair.Reference] = Block{Data: levelTwoBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: 2, Root: levelTwoPair}, BlockOptions{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("short non-final subtree error = %v", err)
	}
	fullChildren := make([]Pair, 16)
	for i := range fullChildren {
		fullChildren[i] = leafPair
	}
	fullNode := makeNode(fullChildren...)
	fullBlock, fullPair, _ := EncryptNode(fullNode, Secret{}, 1)
	blocks.Blocks[fullPair.Reference] = Block{Data: fullBlock}
	repeatedNode := makeNode(fullPair, fullPair)
	repeatedBlock, repeatedPair, _ := EncryptNode(repeatedNode, Secret{}, 2)
	blocks.Blocks[repeatedPair.Reference] = Block{Data: repeatedBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: 2, Root: repeatedPair}, BlockOptions{}); err != nil {
		t.Fatalf("repeated valid subtree error = %v", err)
	}
	missingInternal := Pair{Reference: Reference{9}, Key: Key{8}}
	missingNode := makeNode(missingInternal, onePair)
	missingBlock, missingPair, _ := EncryptNode(missingNode, Secret{}, 2)
	blocks.Blocks[missingPair.Reference] = Block{Data: missingBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: 2, Root: missingPair}, BlockOptions{}); !errors.Is(err, ErrMissingBlock) {
		t.Fatalf("missing internal error = %v", err)
	}

	emptyNode := make([]byte, BlockSize1KiB)
	emptyBlock, emptyPair, _ := EncryptNode(emptyNode, Secret{}, 1)
	blocks.Blocks[emptyPair.Reference] = Block{Data: emptyBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: 1, Root: emptyPair}, BlockOptions{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("empty root error = %v", err)
	}

	twoChildren := makeNode(leafPair, leafPair)
	rootBlock, rootPair, _ := EncryptNode(twoChildren, Secret{}, 1)
	blocks.Blocks[rootPair.Reference] = Block{Data: rootBlock}
	badKeyCap := Capability{BlockSize: BlockSize1KiB, Level: 1, Root: rootPair}
	badKeyCap.Root.Key[0] ^= 1
	if _, err := store.NewReader(context.Background(), badKeyCap, BlockOptions{}); !errors.Is(err, ErrInvalidRootKey) {
		t.Fatalf("bad root key error = %v", err)
	}

	brokenSlots := makeNode(leafPair)
	copy(brokenSlots[2*PairSize:], brokenSlots[:PairSize])
	brokenBlock, brokenPair, _ := EncryptNode(brokenSlots, Secret{}, 1)
	blocks.Blocks[brokenPair.Reference] = Block{Data: brokenBlock}
	if _, err := store.NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: 1, Root: brokenPair}, BlockOptions{}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("non-contiguous pairs error = %v", err)
	}

	if _, err := ParseInternalNode([]byte{1}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("short pairs error = %v", err)
	}
}

func cloneBlockSet(blocks BlockSet) BlockSet {
	clone := BlockSet{Blocks: make(map[Reference]Block, len(blocks.Blocks))}
	for ref, block := range blocks.Blocks {
		clone.Blocks[ref] = Block{Data: bytes.Clone(block.Data), Options: block.Options}
	}
	return clone
}

func makeNode(pairs ...Pair) []byte {
	node := make([]byte, BlockSize1KiB)
	for i, pair := range pairs {
		offset := i * PairSize
		copy(node[offset:offset+32], pair.Reference[:])
		copy(node[offset+32:offset+PairSize], pair.Key[:])
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
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), []byte("content"), Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sized := range []bool{false, true} {
		source := &failAfterSource{BlockSource: blocks, err: io.EOF}
		var reader CapabilityReader
		if sized {
			reader, err = NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), capability, 7, BlockOptions{})
		} else {
			reader, err = NewBlockCapabilityStore(source).NewReader(context.Background(), capability, BlockOptions{})
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

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (source malformedSource) getBlock(context.Context, Reference, TTL, BlockOptions) ([]byte, error, BlockOptions) {
	return source, nil, BlockOptions{}
}

type failAfterSource struct {
	BlockSource
	calls, failAt int
	err           error
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (source *failAfterSource) getBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	source.calls++
	if source.calls == source.failAt {
		return nil, source.err, BlockOptions{}
	}
	return GetBlock(ctx, source.BlockSource, ref, 0, BlockOptions{})
}

func storeTestNode(node []byte, secret Secret, level Level, sink BlockSink) (Pair, error) {
	block, pair, err := EncryptNode(node, secret, level)
	if err != nil {
		return Pair{}, err
	}
	_, err = PutBlock(context.Background(), sink, block, 0, BlockOptions{})
	return pair, err
}

// BatchSize reports the maximum number of requests per call.
func (s malformedSource) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See BlockSource for error rules.
func (s malformedSource) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

// BatchSize reports the maximum number of requests per call.
func (s *failAfterSource) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See BlockSource for error rules.
func (s *failAfterSource) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

func (malformedSource) PrefetchBlock(context.Context, []BlockRequest) {}
