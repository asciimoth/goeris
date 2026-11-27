package eris_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/prefetchtest"
)

type ignorePrefetch struct{ eris.BlockSource }

func (ignorePrefetch) PrefetchBlock(context.Context, []eris.BlockRequest) {}

func TestPrefetchDelayedReader(t *testing.T) {
	data := make([]byte, 40*1024+17)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		store := prefetchtest.New(blocks, time.Microsecond, 3)
		var source eris.BlockSource = store
		if !enabled {
			source = ignorePrefetch{store}
		}
		reader, err := eris.NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), cap, int64(len(data)), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Small reads leave time between hints and the next demand block.
		var got bytes.Buffer
		if _, err = io.CopyBuffer(&got, struct{ io.Reader }{reader}, make([]byte, 600)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), data) {
			t.Fatal("content changed")
		}
		stats := store.Snapshot()
		if enabled && stats.Cold != 0 || !enabled && stats.Cold == 0 {
			t.Fatalf("enabled %v: cold reads %d", enabled, stats.Cold)
		}
		_ = reader.Close()
	}
}

func TestPrefetchDelayedTraversal(t *testing.T) {
	data := make([]byte, 18000)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	store := prefetchtest.New(blocks, time.Hour, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := eris.NewBlockCapabilityStore(store).Prefetch(ctx, cap, 5, eris.BlockOptions{HighPriority: true}); err != nil {
		t.Fatal(err)
	}
	stats := store.Snapshot()
	if stats.Cold != 0 || len(stats.Reads) >= len(blocks.Blocks) {
		t.Fatal("leaves read or unhinted index")
	}
	for ref := range blocks.Blocks {
		if _, err, _ := eris.GetBlock(ctx, store, ref, 5, eris.BlockOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if store.Snapshot().Cold != 0 {
		t.Fatal("whole-content prefetch left cold blocks")
	}
}

func FuzzPrefetchReaderOperations(f *testing.F) {
	f.Add([]byte("content"), []byte{0, 1, 5, 3, 2}, uint8(1))
	f.Add(make([]byte, 70000), []byte{0, 0, 0, 0, 15, 5, 1}, uint8(3))
	f.Fuzz(func(t *testing.T, data, operations []byte, limit uint8) {
		if len(data) > 1<<18 || len(operations) > 256 {
			t.Skip()
		}
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		store := prefetchtest.New(blocks, 0, int(limit%65))
		reader, err := eris.NewBlockCapabilityStore(store).NewReaderWithSize(context.Background(), cap, int64(len(data)), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		reference := bytes.NewReader(data)
		for _, op := range operations {
			if op&1 != 0 {
				offset := int64(int(op) * 1024 % (len(data) + 1))
				if _, err := reader.Seek(offset, io.SeekStart); err != nil {
					t.Fatal(err)
				}
				_, _ = reference.Seek(offset, io.SeekStart)
			}
			got, want := make([]byte, 1+int(op)*37), make([]byte, 1+int(op)*37)
			n, err := reader.Read(got)
			m, wantErr := reference.Read(want)
			// Reader can return EOF with the last partial read; bytes.Reader returns
			// it on the next call. Both must return exactly the same bytes.
			if n != m || !bytes.Equal(got[:n], want[:m]) || err != nil && err != io.EOF || wantErr == io.EOF && err != io.EOF {
				t.Fatalf("read %d,%v want %d,%v", n, err, m, wantErr)
			}
			if op%11 == 0 {
				if err := reader.(*eris.Reader).Prefetch(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		}
		if store.Snapshot().Cold != 0 {
			t.Fatal("a demand path was read without a hint")
		}
		_ = reader.Close()
	})
}

func TestPrefetchConcurrentReader(t *testing.T) {
	data := bytes.Repeat([]byte{42}, 20000)
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	store := prefetchtest.New(blocks, 0, 3)
	reader, err := eris.NewBlockCapabilityStore(store).NewReaderWithSize(context.Background(), cap, int64(len(data)), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 10 {
				if err := reader.(*eris.Reader).Prefetch(context.Background()); err != nil {
					t.Error(err)
					return
				}
				buf := make([]byte, 1000)
				if _, err := reader.ReadAt(buf, 1500); err != nil || !bytes.Equal(buf, data[1500:2500]) {
					t.Error("concurrent read", err)
					return
				}
			}
		})
	}
	wg.Wait()
	_ = reader.Close()
}

func TestPrefetchRepeatedSubtrees(t *testing.T) {
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), make([]byte, 1<<20), eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	store := prefetchtest.New(blocks, 0, 3)
	if err := eris.NewBlockCapabilityStore(store).Prefetch(context.Background(), cap, eris.NoTTL, eris.BlockOptions{}); err != nil {
		t.Fatal(err)
	}
	seen := map[eris.Reference]bool{}
	for _, r := range store.Snapshot().Reads {
		if seen[r.Reference] {
			t.Fatal("repeated internal node read")
		}
		seen[r.Reference] = true
	}
}

func TestReaderHintsFutureLeavesWithoutReadingThem(t *testing.T) {
	data := make([]byte, 100*1024)
	for i := range data {
		data[i] = byte(i / 1024)
	}
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, _ := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	store := prefetchtest.New(blocks, 0, 2)
	reader, err := eris.NewBlockCapabilityStore(store).NewReaderWithSize(context.Background(), cap, int64(len(data)), eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	stats := store.Snapshot()
	// A level-two tree needs exactly two indexes and the requested leaf.
	if len(stats.Reads) != 3 {
		t.Fatalf("read speculative blocks: %d reads", len(stats.Reads))
	}
	hinted := map[eris.Reference]bool{}
	for _, r := range stats.Hints {
		hinted[r.Reference] = true
	}
	read := map[eris.Reference]bool{}
	for _, r := range stats.Reads {
		read[r.Reference] = true
	}
	for leaf := 1; leaf <= 4; leaf++ {
		// Nonfinal leaves encrypt exactly one full, unpadded content block.
		_, pair, err := eris.EncryptNode(data[leaf*1024:(leaf+1)*1024], eris.Secret{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !hinted[pair.Reference] || read[pair.Reference] {
			t.Fatalf("leaf %d was not only hinted", leaf)
		}
	}
	// A jump removes speculation: only the new path and target can be hinted.
	before := len(stats.Hints)
	if _, err := reader.ReadAt(make([]byte, 1), 80*1024); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Snapshot().Hints) - before; got != 2 {
		t.Fatalf("random jump hinted %d blocks, want index and leaf", got)
	}
	_ = reader.Close()
}

func TestReaderPrefetchPaddingAndCacheEviction(t *testing.T) {
	// Unique leaves force more internal nodes than the reader can cache. The
	// exact content length also requires a separate padding leaf at EOF.
	data := make([]byte, 80*16*1024)
	for i := 0; i < len(data)/1024; i++ {
		data[i*1024] = byte(i)
		data[i*1024+1] = byte(i >> 8)
	}
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	cap, err := eris.NewBlockCapabilityStore(blocks).Encode(t.Context(), data, eris.Secret{}, eris.BlockSize1KiB, eris.BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 3, 64} {
		store := prefetchtest.New(blocks, 0, limit)
		reader, err := eris.NewBlockCapabilityStore(store).NewReaderWithSize(context.Background(), cap, int64(len(data)), eris.BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Start at the last content byte, without sequential read-ahead. The
		// padding leaf still needs its own demand hint.
		if _, err := reader.ReadAt(make([]byte, 1), int64(len(data)-1)); err != nil {
			t.Fatal(err)
		}
		if store.Snapshot().Cold != 0 {
			t.Fatal("padding was read without a hint")
		}
		var output bytes.Buffer
		if _, err := io.CopyBuffer(&output, struct{ io.Reader }{reader}, make([]byte, 400)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), data) {
			t.Fatal("content changed across cache eviction")
		}
		if store.Snapshot().Cold != 0 {
			t.Fatal("cache eviction lost a demand hint")
		}
		_ = reader.Close()
	}
}
