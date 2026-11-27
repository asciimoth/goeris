package eris

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"
)

func TestSizedReaderBoundaries(t *testing.T) {
	for _, blockSize := range []BlockSize{BlockSize1KiB, BlockSize32KiB} {
		for _, size := range []int{0, 1, int(blockSize) - 1, int(blockSize), int(blockSize) + 1, 16*int(blockSize) - 1, 16 * int(blockSize), 16*int(blockSize) + 1} {
			t.Run(fmt.Sprintf("%d/%d", blockSize, size), func(t *testing.T) {
				content := make([]byte, size)
				for i := range content {
					content[i] = byte(i * 31)
				}
				blocks := BlockSet{Blocks: make(map[Reference]Block)}
				cap, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, blockSize, BlockOptions{})
				if err != nil {
					t.Fatal(err)
				}
				source := &failAfterSource{BlockSource: blocks}
				reader, err := NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), cap, int64(size), BlockOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = reader.Close() }()
				if source.calls != 0 || reader.Size() != int64(size) {
					t.Fatal("open read blocks or returned wrong size")
				}
				offsets := []int{size, max(0, size-5), size / 2, 0}
				for _, offset := range offsets {
					for _, length := range []int{0, 1, 17, int(blockSize) + 1} {
						buffer := make([]byte, length)
						n, err := reader.ReadAt(buffer, int64(offset))
						want := min(length, size-offset)
						if n != want || !bytes.Equal(buffer[:n], content[offset:offset+n]) {
							t.Fatalf("offset %d length %d: %d bytes", offset, length, n)
						}
						if n < length && !errors.Is(err, io.EOF) || n == length && err != nil {
							t.Fatalf("read error: %v", err)
						}
					}
				}
				if _, err := reader.Seek(0, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(reader)
				if err != nil || !bytes.Equal(got, content) {
					t.Fatalf("sequential: %v", err)
				}
			})
		}
	}
}

func TestSizedReaderRandomAccessCostAndCacheBound(t *testing.T) {
	// More than 64 distinct internal nodes, to exercise eviction. Every leaf
	// differs so de-duplication cannot hide unnecessary block reads.
	content := make([]byte, 1200*1024+17)
	for i := 0; i+8 <= len(content); i += 1024 {
		binary.LittleEndian.PutUint64(content[i:], uint64(i))
	}
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := &failAfterSource{BlockSource: blocks}
	opened, err := NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), cap, int64(len(content)), BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader := opened.(*Reader)
	defer func() { _ = reader.Close() }()
	var p [8]byte
	if _, err := reader.ReadAt(p[:], 512*1024); err != nil {
		t.Fatal(err)
	}
	if source.calls != int(cap.Level)+1 {
		t.Fatalf("one read fetched %d blocks at level %d", source.calls, cap.Level)
	}
	if _, err := reader.ReadAt(p[:], 512*1024); err != nil {
		t.Fatal(err)
	}
	if source.calls != int(cap.Level)+1 {
		t.Fatal("repeat read missed cache")
	}
	for i := 0; i < 1200; i += 16 {
		if _, err := reader.ReadAt(p[:], int64(i*1024)); err != nil || !bytes.Equal(p[:], content[i*1024:i*1024+8]) {
			t.Fatalf("read %d: %v", i, err)
		}
		if reader.CacheSize() > 64 {
			t.Fatalf("cache grew to %d", reader.CacheSize())
		}
	}
	// Concurrent positional reads must not change the sequential position.
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			var p [8]byte
			for i := range 10 {
				offset := (worker*10 + i) * 1024
				if _, err := reader.ReadAt(p[:], int64(offset)); err != nil || !bytes.Equal(p[:], content[offset:offset+8]) {
					t.Errorf("concurrent read: %v", err)
				}
			}
		})
	}
	wg.Wait()
	if position, err := reader.Seek(0, io.SeekCurrent); err != nil || position != 0 {
		t.Fatalf("position %d: %v", position, err)
	}
}

func TestSizedReaderRejectsWrongSizeAndCorruption(t *testing.T) {
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap := Capability{BlockSize: BlockSize1KiB}
	for _, test := range []struct {
		cap    Capability
		source BlockSource
		size   int64
		want   error
	}{
		{Capability{}, blocks, 0, ErrInvalidCapability}, {cap, nil, 0, ErrInvalidCapability},
		{cap, blocks, -1, ErrInvalidSize}, {cap, blocks, math.MaxInt64, ErrSizeMismatch},
	} {
		if _, err := NewBlockCapabilityStore(test.source).NewReaderWithSize(context.Background(), test.cap, test.size, BlockOptions{}); !errors.Is(err, test.want) {
			t.Fatalf("constructor: %v", err)
		}
	}
	for _, actual := range []int{0, 2, 1023, 1024, 1025, 16384, 17000} {
		content := bytes.Repeat([]byte{'x'}, actual)
		store := newTestStore()
		cap, err := store.Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, declared := range []int64{0, 1, 1023, 1024, 1025, 16000, 17001, 32768} {
			if declared == int64(actual) {
				continue
			}
			reader, err := store.NewReaderWithSize(context.Background(), cap, declared, BlockOptions{})
			if err == nil {
				_, err = io.ReadAll(reader)
				_ = reader.Close()
			}
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("actual %d declared %d: %v", actual, declared, err)
			}
		}
	}
	// A correctly encrypted but unpadded leaf must fail at the declared end.
	raw := bytes.Repeat([]byte{'x'}, 1024)
	pair, err := storeTestNode(raw, Secret{}, 0, blocks)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewBlockCapabilityStore(blocks).NewReaderWithSize(context.Background(), Capability{BlockSize: BlockSize1KiB, Root: pair}, 1, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrInvalidPadding) {
		t.Fatal(err)
	}
	// Empty-file reads still check the sole padding block.
	store := newTestStore()
	empty, _ := store.Encode(t.Context(), nil, Secret{}, BlockSize1KiB, BlockOptions{})
	reader, _ = newTestStore().NewReaderWithSize(context.Background(), empty, 0, BlockOptions{})
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrMissingBlock) {
		t.Fatal(err)
	}
	reader, _ = store.NewReaderWithSize(context.Background(), empty, 0, BlockOptions{})
	if n, err := reader.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("empty: %d, %v", n, err)
	}
	// End reads of block-aligned files must fetch and validate the padding leaf.
	blocks = BlockSet{Blocks: make(map[Reference]Block)}
	cap, _ = NewBlockCapabilityStore(blocks).Encode(t.Context(), make([]byte, 1024), Secret{}, BlockSize1KiB, BlockOptions{})
	source := &failAfterSource{BlockSource: blocks, failAt: 3, err: io.ErrUnexpectedEOF}
	reader, _ = NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), cap, 1024, BlockOptions{})
	if n, err := reader.Read(make([]byte, 1024)); n != 1024 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("padding fetch: %d, %v", n, err)
	}
	// A missing root must fail when checking EOF as well as on a normal read.
	reader, _ = newTestStore().NewReaderWithSize(context.Background(), cap, 1024, BlockOptions{})
	if _, err := reader.ReadAt(make([]byte, 1), 1024); !errors.Is(err, ErrMissingBlock) {
		t.Fatal(err)
	}
}

func TestSizedReaderRejectsMalformedVisitedTree(t *testing.T) {
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	store := NewBlockCapabilityStore(blocks)
	leaf := make([]byte, 1024)
	leaf[0] = 0x80
	pair, err := storeTestNode(leaf, Secret{}, 0, blocks)
	if err != nil {
		t.Fatal(err)
	}
	internal := make([]byte, 1024)
	copy(internal, pair.Reference[:])
	copy(internal[32:], pair.Key[:])
	// Only one child where the advertised size needs two.
	root, err := storeTestNode(internal, Secret{}, 1, blocks)
	if err != nil {
		t.Fatal(err)
	}
	cap := Capability{BlockSize: BlockSize1KiB, Level: 1, Root: root}
	reader, _ := store.NewReaderWithSize(context.Background(), cap, 1024, BlockOptions{})
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	// Reject a forged internal root key on the lazy path.
	key := Key{1}
	// Use the same helper as the core malformed-root tests: ChaCha is symmetric.
	encrypted, err := DecryptNodeFromSource(internal, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := PutBlock(context.Background(), blocks, encrypted, 0, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cap.Root = Pair{Reference: ref, Key: key}
	reader, _ = store.NewReaderWithSize(context.Background(), cap, 1024, BlockOptions{})
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrInvalidRootKey) {
		t.Fatal(err)
	}
}

func TestReaderSizeOverflow(t *testing.T) {
	// A small DAG can represent a tree larger than int64 without storing its
	// expanded content. Both byte-size and leaf-count overflow must be rejected.
	for _, test := range []struct {
		level Level
		want  error
	}{{14, ErrInvalidTree}, {16, ErrInvalidNode}} {
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		leaf := make([]byte, 1024)
		leaf[0] = 0x80
		pair, err := storeTestNode(leaf, Secret{}, 0, blocks)
		if err != nil {
			t.Fatal(err)
		}
		for level := Level(1); level <= test.level; level++ {
			node := make([]byte, 1024)
			for i := range 16 {
				copy(node[i*64:], pair.Reference[:])
				copy(node[i*64+32:], pair.Key[:])
			}
			pair, err = storeTestNode(node, Secret{}, level, blocks)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := NewBlockCapabilityStore(blocks).NewReader(context.Background(), Capability{BlockSize: BlockSize1KiB, Level: test.level, Root: pair}, BlockOptions{}); !errors.Is(err, test.want) {
			t.Fatalf("level %d: %v", test.level, err)
		}
	}
}

func BenchmarkReaderOpenAndRead(b *testing.B) {
	content := make([]byte, 2<<20)
	for i := 0; i < len(content); i += 1024 {
		binary.LittleEndian.PutUint64(content[i:], uint64(i))
	}
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap, err := NewBlockCapabilityStore(blocks).Encode(b.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		b.Fatal(err)
	}
	for _, sized := range []bool{false, true} {
		name := "size-scan"
		if sized {
			name = "known-size"
		}
		b.Run(name, func(b *testing.B) {
			source := &failAfterSource{BlockSource: blocks}
			store := NewBlockCapabilityStore(source)
			b.ReportAllocs()
			for b.Loop() {
				var reader CapabilityReader
				var err error
				if sized {
					reader, err = store.NewReaderWithSize(context.Background(), cap, int64(len(content)), BlockOptions{})
				} else {
					reader, err = store.NewReader(context.Background(), cap, BlockOptions{})
				}
				if err != nil {
					b.Fatal(err)
				}
				var buffer [32]byte
				if _, err = reader.ReadAt(buffer[:], 1<<20); err != nil {
					b.Fatal(err)
				}
				_ = reader.Close()
			}
			b.ReportMetric(float64(source.calls)/float64(b.N), "blocks/op")
		})
	}
}
