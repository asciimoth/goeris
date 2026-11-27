package eris

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
)

// responseStore returns malformed result counts to exercise caller boundaries.
type responseStore struct {
	BlockSet
	count int
}

func (s responseStore) GetBlock(context.Context, []BlockRequest) ([]Block, error) {
	return make([]Block, s.count), nil
}
func (s responseStore) PutBlock(context.Context, []PutBlockRequest) ([]Reference, error) {
	return make([]Reference, s.count), nil
}
func (s responseStore) UpdateBlockOptions(context.Context, []BlockRequest) ([]bool, error) {
	return make([]bool, s.count), nil
}

func TestSingleBlockResultCounts(t *testing.T) {
	ctx := context.Background()
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			store := responseStore{count: count}
			data, err, options := GetBlock(ctx, store, Reference{}, NoTTL, BlockOptions{})
			if !errors.Is(err, ErrInvalidBatchResult) || data != nil || options != (BlockOptions{}) {
				t.Fatal("accepted malformed read results", data, err, options)
			}
			ref, err := PutBlock(ctx, store, make([]byte, BlockSize1KiB), NoTTL, BlockOptions{})
			if !errors.Is(err, ErrInvalidBatchResult) || ref != (Reference{}) {
				t.Fatal("accepted malformed write results", ref, err)
			}
			updated, err := UpdateBlockOptions(ctx, store, Reference{}, NoTTL, BlockOptions{})
			if !errors.Is(err, ErrInvalidBatchResult) || updated {
				t.Fatal("accepted malformed update results", updated, err)
			}
		})
	}
}

func TestReaderBatchResultCounts(t *testing.T) {
	content := make([]byte, 5*1024)
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	cap, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			source := &batchProbe{BlockSet: blocks}
			source.readHook = func(requests []BlockRequest) ([]Block, error) {
				if len(requests) == 1 {
					return blocks.GetBlock(context.Background(), requests)
				}
				got, err := blocks.GetBlock(context.Background(), requests)
				if err != nil {
					return nil, err
				}
				if count > len(got) {
					return append(got, got[0]), nil
				}
				return got[:count], nil
			}
			reader, err := NewBlockCapabilityStore(source).NewReaderWithSize(context.Background(), cap, int64(len(content)), BlockOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Calling the batch step directly detects zero progress without hanging a
			// whole Read call if the storage result count is not checked.
			buffer := bytes.Repeat([]byte{0xcc}, 2048)
			n, err := reader.(*Reader).readBatch(buffer, 0, 2)
			if n != 0 || !errors.Is(err, ErrInvalidBatchResult) || !bytes.Equal(buffer, bytes.Repeat([]byte{0xcc}, 2048)) {
				t.Fatal("accepted malformed read batch", n, err)
			}
			_ = reader.Close()
		})
	}
}

func TestBatchResultErrorsPreserved(t *testing.T) {
	failure := errors.New("storage failed")
	source := &batchProbe{readHook: func([]BlockRequest) ([]Block, error) { return nil, failure }}
	if _, err, _ := GetBlock(context.Background(), source, Reference{}, NoTTL, BlockOptions{}); err != failure {
		t.Fatal(err)
	}
}

func TestReaderCancellationBetweenCachedLeaves(t *testing.T) {
	content := make([]byte, 4*1024) // Identical leaves reuse the last-leaf cache.
	blocks := BlockSet{Blocks: make(map[Reference]Block)}
	capability, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &batchProbe{BlockSet: blocks}
	source.MaxBatchSize = 1
	reader, err := NewBlockCapabilityStore(source).NewReaderWithSize(ctx, capability, int64(len(content)), BlockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := reader.(*Reader).leafPair(0)
	if err != nil {
		t.Fatal(err)
	}
	source.readHook = func(requests []BlockRequest) ([]Block, error) {
		got, err := blocks.GetBlock(ctx, requests)
		if len(requests) == 1 && requests[0].Reference == pair.Reference {
			cancel()
		}
		return got, err
	}
	got := bytes.Repeat([]byte{0xcc}, 2*1024)
	n, err := reader.Read(got)
	if n != 1024 || !errors.Is(err, context.Canceled) {
		t.Fatal("read cached leaves after cancellation", n, err)
	}
	if !bytes.Equal(got[:n], content[:n]) || !bytes.Equal(got[n:], bytes.Repeat([]byte{0xcc}, len(got)-n)) {
		t.Fatal("incorrect read prefix")
	}
	_ = reader.Close()
}

// budgetInput fails if the encoder reads past the maximum pending batch before
// it calls storage. This also works for input that would otherwise never end.
type budgetInput struct{ remaining int }

func (input *budgetInput) Read(p []byte) (int, error) {
	if input.remaining == 0 {
		return 0, errors.New("encoder read beyond its pending batch")
	}
	n := min(len(p), input.remaining)
	clear(p[:n])
	input.remaining -= n
	return n, nil
}

func TestEncodeBatchBackpressure(t *testing.T) {
	failure := errors.New("storage rejected first batch")
	for _, limit := range []int{0, 1, 3, 65} {
		maximum := limit
		if maximum == 0 || maximum > 64 {
			maximum = 64
		}
		source := &budgetInput{remaining: maximum * int(BlockSize1KiB)}
		sink := &batchProbe{BlockSet: BlockSet{MaxBatchSize: limit}}
		sink.writeHook = func(requests []PutBlockRequest) ([]Reference, error) {
			if len(requests) != maximum {
				t.Fatal("unexpected batch length", len(requests), maximum)
			}
			return nil, failure
		}
		cap, err := NewBlockCapabilityStore(sink).EncodeContent(context.Background(), source, Secret{}, BlockSize1KiB, BlockOptions{})
		if err != failure || cap != (Capability{}) || len(sink.writes) != 1 {
			t.Fatal(cap, err, sink.writes)
		}
	}
}

func FuzzReaderBatchResultCounts(f *testing.F) {
	f.Add(uint8(0), false)
	f.Add(uint8(2), false)
	f.Add(uint8(3), true)
	f.Fuzz(func(t *testing.T, count uint8, failed bool) {
		ctx := context.Background()
		content := make([]byte, 3*1024)
		blocks := BlockSet{Blocks: make(map[Reference]Block)}
		cap, err := NewBlockCapabilityStore(blocks).Encode(t.Context(), content, Secret{}, BlockSize1KiB, BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("storage failed")
		source := &batchProbe{BlockSet: blocks}
		source.readHook = func(requests []BlockRequest) ([]Block, error) {
			if len(requests) == 1 {
				return blocks.GetBlock(ctx, requests)
			}
			got, err := blocks.GetBlock(ctx, requests)
			if err != nil {
				return nil, err
			}
			results := make([]Block, int(count))
			for i := range results {
				results[i] = got[i%len(got)]
			}
			if failed {
				return results, failure
			}
			return results, nil
		}
		reader, err := NewBlockCapabilityStore(source).NewReaderWithSize(ctx, cap, int64(len(content)), BlockOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 2048)
		n, err := reader.(*Reader).readBatch(got, 0, 2)
		switch {
		case count > 2 || !failed && count != 2:
			if n != 0 || !errors.Is(err, ErrInvalidBatchResult) {
				t.Fatal(n, err)
			}
		case failed:
			if n != int(count)*1024 || err != failure {
				t.Fatal(n, err)
			}
		default:
			if n != 2048 || err != nil {
				t.Fatal(n, err)
			}
		}
		_ = reader.Close()
	})
}
