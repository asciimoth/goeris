package eris

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/asciimoth/goeris/internal/batch"
)

// Compare native batches with the established single-request helpers. The
// scripted stores have no mutable state, so only call grouping can differ.
func FuzzCombinedBatchFallback(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{1, 0, 2, 0, 1, 0, 0, 0, 1, 0, 0, 0})
	f.Add([]byte{1, 4, 0, 2, 3, 0, 2, 0, 2, 1, 4, 0})
	f.Add([]byte("000002000001")) // Missing results before a stopping error.
	f.Fuzz(func(t *testing.T, script []byte) {
		ctx := context.Background()
		blocks, puts := batchBlocks()
		refs, err := blocks.PutBlock(ctx, puts)
		if err != nil {
			t.Fatal(err)
		}
		requests := make([]BlockRequest, len(refs))
		positions := make(map[Reference]int)
		for i, ref := range refs {
			positions[ref] = i
			requests[i] = BlockRequest{Reference: ref, TTL: puts[i].TTL, Options: puts[i].Options}
		}
		causes := []error{ErrMissingBlock, context.Canceled}
		missingDetails := make(map[error]bool)
		var entries []any
		for storeIndex := range 3 {
			var outcomes [4]byte
			var failures [4]error
			for i := range outcomes {
				if offset := storeIndex*len(outcomes) + i; offset < len(script) {
					outcomes[i] = script[offset] % 5
				}
				failures[i] = fmt.Errorf("store %d request %d", storeIndex, i)
				causes = append(causes, failures[i])
				missingDetails[failures[i]] = outcomes[i] == 1 || outcomes[i] == 3
			}
			child := &combinedBatchProbe{}
			child.get = func(ctx context.Context, r []BlockRequest) ([]Block, error) {
				got, err := batch.Read(ctx, r, 0, ErrMissingBlock, func(request BlockRequest) (Block, error) {
					i := positions[request.Reference]
					switch outcomes[i] {
					case 1, 3:
						return Block{}, errors.Join(ErrMissingBlock, failures[i])
					case 2:
						return Block{}, failures[i]
					case 4:
						return Block{}, errors.Join(ErrMissingBlock, context.Canceled, failures[i])
					default:
						return blocks.Blocks[request.Reference], nil
					}
				})
				if err != nil && storeIndex%2 != 0 {
					err = fmt.Errorf("wrapped batch: %w", err)
				}
				return got, err
			}
			child.put = func(ctx context.Context, r []PutBlockRequest) ([]Reference, error) {
				return batch.Run(ctx, r, 0, func(request PutBlockRequest) (Reference, error) {
					i := positions[*request.Expected]
					if outcomes[i] >= 2 {
						return Reference{}, failures[i]
					}
					return *request.Expected, nil
				})
			}
			child.update = func(ctx context.Context, r []BlockRequest) ([]bool, error) {
				return batch.Run(ctx, r, 0, func(request BlockRequest) (bool, error) {
					i := positions[request.Reference]
					if outcomes[i] >= 2 {
						return false, failures[i]
					}
					return outcomes[i] == 0, nil
				})
			}
			entries = append(entries, child)
		}
		store := NewCombinedBlockStore(entries...)
		check := func(operation string, got, want any, gotErr, wantErr error) {
			t.Helper()
			if !reflect.DeepEqual(got, want) || (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("%s: results differ; got error %v, want %v", operation, gotErr, wantErr)
			}
			for _, cause := range causes {
				// A child batch can join a later missing error with its stopping
				// error. Keeping that error can retain extra missing details;
				// the first missing cause and all stopping causes must still match.
				if operation == "read" && missingDetails[cause] && errors.Is(gotErr, cause) && !errors.Is(wantErr, cause) {
					continue
				}
				if errors.Is(gotErr, cause) != errors.Is(wantErr, cause) {
					t.Fatalf("%s: error cause %v differs: got %v; want %v", operation, cause, gotErr, wantErr)
				}
			}
		}
		gotReads, gotErr := store.GetBlock(ctx, requests)
		wantReads, wantErr := batch.Read(ctx, requests, 0, ErrMissingBlock, func(r BlockRequest) (Block, error) {
			data, err, options := GetBlock(ctx, store, r.Reference, r.TTL, r.Options)
			return Block{Data: data, Options: options}, err
		})
		check("read", gotReads, wantReads, gotErr, wantErr)
		gotWrites, gotErr := store.PutBlock(ctx, puts)
		wantWrites, wantErr := batch.Run(ctx, puts, 0, func(r PutBlockRequest) (Reference, error) {
			return PutBlock(ctx, store, r.Data, r.TTL, r.Options, *r.Expected)
		})
		check("write", gotWrites, wantWrites, gotErr, wantErr)
		gotUpdates, gotErr := store.UpdateBlockOptions(ctx, requests)
		wantUpdates, wantErr := batch.Run(ctx, requests, 0, func(r BlockRequest) (bool, error) {
			return UpdateBlockOptions(ctx, store, r.Reference, r.TTL, r.Options)
		})
		check("update", gotUpdates, wantUpdates, gotErr, wantErr)
	})
}
