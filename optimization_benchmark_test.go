package eris

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

// benchmarkSink isolates encoding from storage I/O and verification.
type benchmarkSink struct{ BlockSet }

func (benchmarkSink) PutBlock(_ context.Context, requests []PutBlockRequest) ([]Reference, error) {
	refs := make([]Reference, len(requests))
	for i := range requests {
		refs[i] = *requests[i].Expected
	}
	return refs, nil
}

func BenchmarkEncodeStreamBuffers(b *testing.B) {
	store := NewBlockCapabilityStore(benchmarkSink{})
	for _, tc := range []struct {
		size      int
		blockSize BlockSize
	}{
		{100, BlockSize1KiB},
		{2 << 20, BlockSize1KiB},
		{32 << 20, BlockSize32KiB},
	} {
		b.Run(fmt.Sprintf("%d/%d", tc.size, tc.blockSize), func(b *testing.B) {
			content := make([]byte, tc.size)
			var input bytes.Reader
			ctx := b.Context()
			b.ReportAllocs()
			b.SetBytes(int64(tc.size))
			for b.Loop() {
				input.Reset(content)
				if _, err := store.EncodeContent(ctx, &input, Secret{1}, tc.blockSize, BlockOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCapabilityCodec(b *testing.B) {
	c := Capability{BlockSize: BlockSize32KiB, Level: 2, Root: Pair{Reference: Reference{1}, Key: Key{2}}}
	urn := c.String()
	ref := c.Root.Reference.String()
	b.Run("ParseURN", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ParseURN(urn); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("MarshalCBOR", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := c.MarshalCBOR(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = c.String()
		}
	})
	b.Run("ParseReference", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := ParseReference(ref); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkLargeInternalNode(b *testing.B) {
	for _, count := range []int{2, 512} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			node := make([]byte, BlockSize32KiB)
			for i := range count {
				node[i*PairSize] = 1
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ParseInternalNode(node); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type batchBenchmarkStore struct {
	BlockSet
	results []Block
}

func (s batchBenchmarkStore) GetBlock(_ context.Context, requests []BlockRequest) ([]Block, error) {
	return s.results[:len(requests)], nil
}

func BenchmarkCombinedReadSuccess(b *testing.B) {
	requests := make([]BlockRequest, 64)
	data := make([]byte, BlockSize1KiB)
	ref, _ := ReferenceOf(data)
	results := make([]Block, len(requests))
	for i := range requests {
		requests[i].Reference = ref
		results[i].Data = data
	}
	store := NewCombinedBlockStore(batchBenchmarkStore{results: results})
	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := store.GetBlock(ctx, requests); err != nil {
			b.Fatal(err)
		}
	}
}
