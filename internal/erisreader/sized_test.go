package erisreader

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"testing"

	"github.com/asciimoth/goeris"
)

func TestSizedReaderBoundaries(t *testing.T) {
	for _, blockSize := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		for _, size := range []int{0, 1, int(blockSize) - 1, int(blockSize), int(blockSize) + 1, 16*int(blockSize) - 1, 16 * int(blockSize), 16*int(blockSize) + 1} {
			t.Run(fmt.Sprintf("%d/%d", blockSize, size), func(t *testing.T) {
				content := make([]byte, size)
				for i := range content {
					content[i] = byte(i * 31)
				}
				cap, blocks, err := eris.Encode(content, eris.Secret{}, blockSize)
				if err != nil {
					t.Fatal(err)
				}
				source := &failAfterSource{BlockSource: blocks}
				reader, err := NewReaderWithSize(cap, source, int64(size))
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
	cap, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		t.Fatal(err)
	}
	source := &failAfterSource{BlockSource: blocks}
	reader, err := NewReaderWithSize(cap, source, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
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
	blocks := make(eris.BlockSet)
	cap := eris.Capability{BlockSize: eris.BlockSize1KiB}
	for _, test := range []struct {
		cap    eris.Capability
		source eris.BlockSource
		size   int64
		want   error
	}{
		{eris.Capability{}, blocks, 0, eris.ErrInvalidCapability}, {cap, nil, 0, eris.ErrInvalidCapability},
		{cap, blocks, -1, ErrInvalidSize}, {cap, blocks, math.MaxInt64, ErrSizeMismatch},
	} {
		if _, err := NewReaderWithSize(test.cap, test.source, test.size); !errors.Is(err, test.want) {
			t.Fatalf("constructor: %v", err)
		}
	}
	for _, actual := range []int{0, 2, 1023, 1024, 1025, 16384, 17000} {
		content := bytes.Repeat([]byte{'x'}, actual)
		cap, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
		if err != nil {
			t.Fatal(err)
		}
		for _, declared := range []int64{0, 1, 1023, 1024, 1025, 16000, 17001, 32768} {
			if declared == int64(actual) {
				continue
			}
			reader, err := NewReaderWithSize(cap, blocks, declared)
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
	pair, err := putNode(raw, eris.Secret{}, 0, blocks)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReaderWithSize(eris.Capability{BlockSize: eris.BlockSize1KiB, Root: pair}, blocks, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, eris.ErrInvalidPadding) {
		t.Fatal(err)
	}
	// Empty-file reads still check the sole padding block.
	empty, emptyBlocks, _ := eris.Encode(nil, eris.Secret{}, eris.BlockSize1KiB)
	reader, _ = NewReaderWithSize(empty, make(eris.BlockSet), 0)
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
	reader, _ = NewReaderWithSize(empty, emptyBlocks, 0)
	if n, err := reader.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("empty: %d, %v", n, err)
	}
	// End reads of block-aligned files must fetch and validate the padding leaf.
	cap, blocks, _ = eris.Encode(make([]byte, 1024), eris.Secret{}, eris.BlockSize1KiB)
	source := &failAfterSource{BlockSource: blocks, failAt: 3, err: io.ErrUnexpectedEOF}
	reader, _ = NewReaderWithSize(cap, source, 1024)
	if n, err := reader.Read(make([]byte, 1024)); n != 1024 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("padding fetch: %d, %v", n, err)
	}
	// A missing root must fail when checking EOF as well as on a normal read.
	reader, _ = NewReaderWithSize(cap, make(eris.BlockSet), 1024)
	if _, err := reader.ReadAt(make([]byte, 1), 1024); !errors.Is(err, eris.ErrMissingBlock) {
		t.Fatal(err)
	}
}

func TestSizedReaderRejectsMalformedVisitedTree(t *testing.T) {
	blocks := make(eris.BlockSet)
	leaf := make([]byte, 1024)
	leaf[0] = 0x80
	pair, err := putNode(leaf, eris.Secret{}, 0, blocks)
	if err != nil {
		t.Fatal(err)
	}
	internal := make([]byte, 1024)
	copy(internal, pair.Reference[:])
	copy(internal[32:], pair.Key[:])
	// Only one child where the advertised size needs two.
	root, err := putNode(internal, eris.Secret{}, 1, blocks)
	if err != nil {
		t.Fatal(err)
	}
	cap := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1, Root: root}
	reader, _ := NewReaderWithSize(cap, blocks, 1024)
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatal(err)
	}
	// Reject a forged internal root key on the lazy path.
	key := eris.Key{1}
	// Use the same helper as the core malformed-root tests: ChaCha is symmetric.
	encrypted, err := eris.DecryptNodeFromSource(internal, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := blocks.PutBlock(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	cap.Root = eris.Pair{Reference: ref, Key: key}
	reader, _ = NewReaderWithSize(cap, blocks, 1024)
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, eris.ErrInvalidRootKey) {
		t.Fatal(err)
	}
}

func TestReaderSizeOverflow(t *testing.T) {
	// A small DAG can represent a tree larger than int64 without storing its
	// expanded content. Both byte-size and leaf-count overflow must be rejected.
	for _, test := range []struct {
		level eris.Level
		want  error
	}{{14, ErrInvalidTree}, {16, eris.ErrInvalidNode}} {
		blocks := make(eris.BlockSet)
		leaf := make([]byte, 1024)
		leaf[0] = 0x80
		pair, err := putNode(leaf, eris.Secret{}, 0, blocks)
		if err != nil {
			t.Fatal(err)
		}
		for level := eris.Level(1); level <= test.level; level++ {
			node := make([]byte, 1024)
			for i := range 16 {
				copy(node[i*64:], pair.Reference[:])
				copy(node[i*64+32:], pair.Key[:])
			}
			pair, err = putNode(node, eris.Secret{}, level, blocks)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := NewReader(eris.Capability{BlockSize: eris.BlockSize1KiB, Level: test.level, Root: pair}, blocks); !errors.Is(err, test.want) {
			t.Fatalf("level %d: %v", test.level, err)
		}
	}
}

func BenchmarkReaderOpenAndRead(b *testing.B) {
	content := make([]byte, 2<<20)
	for i := 0; i < len(content); i += 1024 {
		binary.LittleEndian.PutUint64(content[i:], uint64(i))
	}
	cap, blocks, err := eris.Encode(content, eris.Secret{}, eris.BlockSize1KiB)
	if err != nil {
		b.Fatal(err)
	}
	for _, sized := range []bool{false, true} {
		name := "v1-size-scan"
		if sized {
			name = "v2-known-size"
		}
		b.Run(name, func(b *testing.B) {
			source := &failAfterSource{BlockSource: blocks}
			b.ReportAllocs()
			for b.Loop() {
				var reader *Reader
				var err error
				if sized {
					reader, err = NewReaderWithSize(cap, source, int64(len(content)))
				} else {
					reader, err = NewReader(cap, source)
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
