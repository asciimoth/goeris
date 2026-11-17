package eris

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/goeris/internal/batch"
)

// combinedProbe returns a nonzero result even on failure. Tests can therefore
// detect both an incorrect method call and a leaked result from a failed call.
type combinedProbe struct {
	call  func(string, context.Context, Reference, []byte, []Reference) error
	block []byte
	ref   Reference
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (probe *combinedProbe) getBlock(ctx context.Context, ref Reference, ttl TTL, options BlockOptions) ([]byte, error, BlockOptions) {
	return probe.block, probe.call("GetBlock", ctx, ref, nil, nil), BlockOptions{}
}

func (probe *combinedProbe) putBlock(ctx context.Context, block []byte, ttl TTL, options BlockOptions, expected ...Reference) (Reference, error) {
	return probe.ref, probe.call("PutBlock", ctx, Reference{}, block, expected)
}

func (probe *combinedProbe) updateBlockOptions(context.Context, Reference, TTL, BlockOptions) (bool, error) {
	return false, nil
}

type combinedOperation struct {
	name  string
	write bool
	call  func(*CombinedBlockStore, context.Context, []byte, Reference, []Reference) (any, error)
}

var combinedOperations = []combinedOperation{
	{"GetBlock", false, func(store *CombinedBlockStore, ctx context.Context, _ []byte, ref Reference, _ []Reference) (any, error) {
		blockResult, blockErr, _ := GetBlock(ctx, store, ref, 0, BlockOptions{})
		return blockResult, blockErr
	}},
	{"PutBlock", true, func(store *CombinedBlockStore, ctx context.Context, block []byte, _ Reference, expected []Reference) (any, error) {
		return PutBlock(ctx, store, block, 0, BlockOptions{}, expected...)
	}},
}

func (operation combinedOperation) result(block []byte, ref Reference, err error) any {
	if err != nil {
		block, ref = nil, Reference{}
	}
	if operation.write {
		return ref
	}
	return block
}

func TestCombinedBlockStoreZeroValue(t *testing.T) {
	t.Parallel()
	var store CombinedBlockStore
	store.OnError = func(int, error) { t.Fatal("unexpected callback") }
	for _, operation := range combinedOperations {
		got, err := operation.call(&store, context.Background(), nil, Reference{}, nil)
		if err != ErrNoBlockStorage || !reflect.DeepEqual(got, operation.result(nil, Reference{}, err)) {
			t.Errorf("%s = (%v, %v), want zero result and ErrNoBlockStorage", operation.name, got, err)
		}
	}
}

func TestNewCombinedBlockStoreRetainsMethods(t *testing.T) {
	t.Parallel()
	block := bytes.Repeat([]byte{13}, int(BlockSize1KiB))
	ref, err := ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range combinedOperations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("first store unavailable")
			var events []string
			first := &combinedProbe{call: func(method string, _ context.Context, _ Reference, _ []byte, _ []Reference) error {
				events = append(events, "first "+method)
				return failure
			}}
			last := &combinedProbe{call: func(method string, _ context.Context, _ Reference, _ []byte, _ []Reference) error {
				events = append(events, "last "+method)
				return nil
			}}
			entries := []any{nil, first, struct{}{}, last, nil}
			store := NewCombinedBlockStore(entries...)
			if len(events) != 0 {
				t.Fatal("constructor called storage methods")
			}
			store.OnError = func(index int, err error) {
				if index != 1 || err != failure {
					t.Fatalf("callback = (%d, %v), want (1, %v)", index, err, failure)
				}
				events = append(events, "error")
			}

			// The constructor must retain the original method receivers and order,
			// independently of later changes to the caller's input slice.
			slices.Reverse(entries)
			clear(entries)
			entries[0] = &combinedProbe{call: func(string, context.Context, Reference, []byte, []Reference) error {
				t.Fatal("called a store added after construction")
				return nil
			}}
			// Changes to the original store remain visible through its bound methods.
			last.block, last.ref = block, ref
			for range 2 {
				got, err := operation.call(store, context.Background(), block, ref, []Reference{ref})
				if err != nil || !reflect.DeepEqual(got, operation.result(block, ref, nil)) {
					t.Fatalf("result = (%v, %v), want data from the original store", got, err)
				}
			}
			wantEvents := []string{
				"first " + operation.name, "error", "last " + operation.name,
				"first " + operation.name, "error", "last " + operation.name,
			}
			if !slices.Equal(events, wantEvents) {
				t.Fatalf("events = %v, want %v", events, wantEvents)
			}
		})
	}
}

func TestCombinedBlockStoreFallback(t *testing.T) {
	t.Parallel()
	block := bytes.Repeat([]byte{7}, int(BlockSize1KiB))
	ref, err := ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	failures := []error{errors.New("first failure"), errors.New("second failure"), errors.New("last failure")}
	for _, operation := range combinedOperations {
		for _, test := range []struct {
			name    string
			count   int
			success int // -1 means all attempts fail.
		}{
			{"empty", 0, -1},
			{"single success", 1, 0},
			{"single failure", 1, -1},
			{"first success", 3, 0},
			{"middle success", 3, 1},
			{"last success", 3, 2},
			{"all fail", 3, -1},
		} {
			for _, callback := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/callback=%t", operation.name, test.name, callback), func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
					defer cancel()
					var events []string
					var entries []any
					for i := range test.count {
						resultBlock, resultRef := block, ref
						if i != test.success {
							resultBlock, resultRef = []byte("failed result"), Reference{0xff}
						}
						entries = append(entries, &combinedProbe{
							block: resultBlock,
							ref:   resultRef,
							call: func(method string, gotCtx context.Context, gotRef Reference, gotBlock []byte, expected []Reference) error {
								events = append(events, fmt.Sprintf("call %d", i))
								if method != operation.name {
									t.Fatalf("called %s, want %s", method, operation.name)
								}
								if gotCtx != ctx {
									t.Fatal("context was changed or supplied to the wrong method")
								}
								if operation.write {
									if !bytes.Equal(gotBlock, block) || !slices.Equal(expected, []Reference{ref}) {
										t.Fatal("write arguments changed")
									}
								} else if gotRef != ref {
									t.Fatal("read reference changed")
								}
								if i == test.success {
									return nil
								}
								return failures[i]
							},
						})
					}
					store := NewCombinedBlockStore(entries...)
					if callback {
						store.OnError = func(index int, err error) {
							events = append(events, fmt.Sprintf("error %d", index))
							if err != failures[index] {
								t.Fatalf("callback error = %v, want original error %v", err, failures[index])
							}
						}
					}
					attempts := test.count
					var wantErr error
					switch {
					case test.count == 0:
						wantErr = ErrNoBlockStorage
					case test.success >= 0:
						attempts = test.success + 1
					default:
						wantErr = failures[test.count-1]
					}
					var wantEvents []string
					for i := range attempts {
						if i > 0 && callback {
							wantEvents = append(wantEvents, fmt.Sprintf("error %d", i-1))
						}
						wantEvents = append(wantEvents, fmt.Sprintf("call %d", i))
					}
					got, err := operation.call(store, ctx, block, ref, []Reference{ref})
					if err != wantErr || !reflect.DeepEqual(got, operation.result(block, ref, wantErr)) {
						t.Fatalf("result = (%v, %v), want (%v, %v)", got, err, operation.result(block, ref, wantErr), wantErr)
					}
					if !slices.Equal(events, wantEvents) {
						t.Fatalf("events = %v, want %v", events, wantEvents)
					}
				})
			}
		}
	}
}

func TestCombinedBlockStoreCapabilities(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		for _, test := range []struct {
			name    string
			wrap    func(*combinedProbe) any
			methods []string
		}{
			{"source", func(p *combinedProbe) any { return struct{ BlockSource }{p} }, []string{"GetBlock"}},
			{"sink", func(p *combinedProbe) any { return struct{ BlockSink }{p} }, []string{"PutBlock"}},
			{"store", func(p *combinedProbe) any { return struct{ BlockStore }{p} }, []string{"GetBlock", "PutBlock"}},
			{"separate interfaces", func(p *combinedProbe) any {
				return separateBlockStore{p, p}
			}, []string{"GetBlock", "PutBlock"}},
			{"all", func(p *combinedProbe) any { return p }, []string{"GetBlock", "PutBlock"}},
		} {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				failure := errors.New("storage failed")
				var events []string
				entry := test.wrap(&combinedProbe{call: func(method string, _ context.Context, _ Reference, _ []byte, _ []Reference) error {
					events = append(events, method)
					return failure
				}})
				// Unsupported entries before, between, and after attempts must not
				// change callback indexes or replace the last storage error.
				store := NewCombinedBlockStore(nil, entry, struct{}{}, entry, nil)
				store.OnError = func(index int, err error) {
					if index != 1 || err != failure {
						t.Fatalf("callback = (%d, %v), want (1, %v)", index, err, failure)
					}
					events = append(events, "error")
				}
				wantErr := ErrNoBlockStorage
				var wantEvents []string
				calledMethod := operation.name
				if slices.Contains(test.methods, calledMethod) {
					wantErr = failure
					wantEvents = []string{calledMethod, "error", calledMethod}
				}
				got, err := operation.call(store, context.Background(), nil, Reference{}, nil)
				if err != wantErr || !reflect.DeepEqual(got, operation.result(nil, Reference{}, wantErr)) {
					t.Fatalf("result = (%v, %v), want zero result and %v", got, err, wantErr)
				}
				if !slices.Equal(events, wantEvents) {
					t.Fatalf("events = %v, want %v", events, wantEvents)
				}
			})
		}
	}
}

func TestCombinedBlockStoreWriteReferences(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		if !operation.write {
			continue
		}
		for _, expected := range [][]Reference{nil, {}, {{1}}, {{1}, {2}}} {
			t.Run(fmt.Sprintf("%s/%v", operation.name, expected), func(t *testing.T) {
				t.Parallel()
				block := bytes.Repeat([]byte{9}, int(BlockSize1KiB))
				failure := errors.New("write failed")
				calls := 0
				probe := &combinedProbe{ref: Reference{3}, call: func(_ string, _ context.Context, _ Reference, gotBlock []byte, gotExpected []Reference) error {
					calls++
					if !bytes.Equal(gotBlock, block) || !slices.Equal(gotExpected, expected) {
						t.Fatal("write arguments changed")
					}
					return failure
				}}
				store := NewCombinedBlockStore(probe, probe)
				got, err := operation.call(store, context.Background(), block, Reference{}, expected)
				wantErr, wantCalls := failure, 2
				if len(expected) > 1 {
					wantErr, wantCalls = ErrInvalidBlock, 0
				}
				if err != wantErr || got != (Reference{}) || calls != wantCalls {
					t.Fatalf("result = (%v, %v), calls = %d", got, err, calls)
				}
			})
		}
	}
}

func TestCombinedBlockStoreMixedCapabilities(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		operation combinedOperation
		indexes   []int
	}{
		{combinedOperations[0], []int{0, 1, 5, 6}},
		{combinedOperations[1], []int{3, 4, 5, 6}},
	} {
		t.Run(test.operation.name, func(t *testing.T) {
			t.Parallel()
			failure := errors.New("store unavailable")
			block, ref := []byte{7}, Reference{9}
			var calls, reports []int
			probe := func(index int) *combinedProbe {
				return &combinedProbe{block: block, ref: ref, call: func(method string, _ context.Context, _ Reference, _ []byte, _ []Reference) error {
					calls = append(calls, index)
					wantMethod := test.operation.name
					if method != wantMethod {
						t.Fatalf("called %s, want %s", method, wantMethod)
					}
					if index == 6 {
						return nil
					}
					return failure
				}}
			}
			store := NewCombinedBlockStore(
				struct{ BlockSource }{probe(0)},
				struct{ BlockSource }{probe(1)},
				nil,
				struct{ BlockSink }{probe(3)},
				struct{ BlockSink }{probe(4)},
				struct{ BlockStore }{probe(5)},
				probe(6),
				probe(7),
			)
			store.OnError = func(index int, err error) {
				reports = append(reports, index)
				if err != failure {
					t.Fatalf("callback error = %v, want %v", err, failure)
				}
			}
			got, err := test.operation.call(store, context.Background(), block, ref, nil)
			if err != nil || !reflect.DeepEqual(got, test.operation.result(block, ref, nil)) {
				t.Fatalf("result = (%v, %v), want successful store result", got, err)
			}
			if !slices.Equal(calls, test.indexes) || !slices.Equal(reports, test.indexes[:len(test.indexes)-1]) {
				t.Fatalf("calls = %v, reports = %v, want calls %v and reports for each failed call", calls, reports, test.indexes)
			}
		})
	}
}

func TestCombinedBlockStoreCanceledContext(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		for _, expired := range []bool{false, true} {
			for _, empty := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/expired=%t/empty=%t", operation.name, expired, empty), func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					wantErr := context.Canceled
					if expired {
						ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
						defer cancel()
						wantErr = context.DeadlineExceeded
					}
					var entries []any
					if !empty {
						entries = []any{&combinedProbe{call: func(string, context.Context, Reference, []byte, []Reference) error {
							t.Fatal("storage called with canceled context")
							return nil
						}}}
					}
					store := NewCombinedBlockStore(entries...)
					store.OnError = func(int, error) { t.Fatal("unexpected callback") }
					got, err := operation.call(store, ctx, nil, Reference{}, nil)
					if err != wantErr || !reflect.DeepEqual(got, operation.result(nil, Reference{}, wantErr)) {
						t.Fatalf("result = (%v, %v), want zero result and %v", got, err, wantErr)
					}
				})
			}
		}
	}
}

func TestCombinedBlockStoreCancellationBetweenAttempts(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		for _, cancelInCallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/callback=%t", operation.name, cancelInCallback), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failure := errors.New("attempt failed")
				calls, reports := 0, 0
				probe := &combinedProbe{call: func(_ string, gotCtx context.Context, _ Reference, _ []byte, _ []Reference) error {
					calls++
					if gotCtx != ctx {
						t.Fatal("context changed")
					}
					if !cancelInCallback {
						cancel()
					}
					return failure
				}}
				store := NewCombinedBlockStore(probe, nil, probe)
				store.OnError = func(index int, err error) {
					reports++
					if index != 0 || err != failure {
						t.Fatalf("callback = (%d, %v)", index, err)
					}
					if cancelInCallback {
						cancel()
					}
				}
				got, err := operation.call(store, ctx, nil, Reference{}, nil)
				if err != context.Canceled || !reflect.DeepEqual(got, operation.result(nil, Reference{}, err)) || calls != 1 || reports != 1 {
					t.Fatalf("result = (%v, %v), calls = %d, reports = %d", got, err, calls, reports)
				}
			})
		}
	}
}

func TestCombinedBlockStoreCancellationAtCompletion(t *testing.T) {
	t.Parallel()
	failure := errors.New("final store failure")
	for _, operation := range combinedOperations {
		for _, test := range []struct {
			name string
			err  error
		}{
			{"success", nil},
			{"storage error", failure},
			{"context error", context.Canceled},
		} {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				block, ref := []byte{7}, Reference{9}
				calls := 0
				probe := &combinedProbe{block: block, ref: ref, call: func(string, context.Context, Reference, []byte, []Reference) error {
					calls++
					cancel()
					return test.err
				}}
				entries := []any{probe, nil, struct{}{}}
				if test.err == nil {
					// Success must be returned even if another store is available.
					entries = append(entries, probe)
				}
				store := NewCombinedBlockStore(entries...)
				store.OnError = func(int, error) { t.Fatal("unexpected callback") }
				got, err := operation.call(store, ctx, block, ref, nil)
				if err != test.err || !reflect.DeepEqual(got, operation.result(block, ref, test.err)) || calls != 1 {
					t.Fatalf("result = (%v, %v), calls = %d, want original result and one call", got, err, calls)
				}
			})
		}
	}
}

func TestCombinedBlockStoreWrappedCancellation(t *testing.T) {
	t.Parallel()
	failure := errors.New("wrapped store failed")
	for _, operation := range combinedOperations {
		for _, test := range []struct {
			name       string
			canceled   bool
			remaining  bool
			attemptErr error
			wantErr    error
		}{
			{"before call", true, true, nil, context.Canceled},
			{"success", false, true, nil, nil},
			{"failure with fallback", false, true, failure, context.Canceled},
			{"final failure", false, false, failure, failure},
		} {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.canceled {
					cancel()
				}
				block, ref := []byte{7}, Reference{9}
				calls, reports := 0, 0
				probe := &combinedProbe{block: block, ref: ref, call: func(method string, gotCtx context.Context, gotRef Reference, gotBlock []byte, expected []Reference) error {
					calls++
					if gotCtx != ctx {
						t.Fatal("context changed")
					}
					if operation.write {
						if method != "PutBlock" || !bytes.Equal(gotBlock, block) || !slices.Equal(expected, []Reference{ref}) {
							t.Fatal("incorrect wrapped write call")
						}
					} else if method != "GetBlock" || gotRef != ref {
						t.Fatal("incorrect wrapped read call")
					}
					cancel()
					return test.attemptErr
				}}
				entries := []any{struct{ BlockStore }{probe}}
				if test.remaining {
					entries = append(entries, struct{ BlockStore }{probe})
				}
				store := NewCombinedBlockStore(entries...)
				store.OnError = func(index int, err error) {
					reports++
					if index != 0 || err != failure {
						t.Fatalf("callback = (%d, %v)", index, err)
					}
				}
				got, err := operation.call(store, ctx, block, ref, []Reference{ref})
				wantCalls, wantReports := 1, 0
				if test.canceled {
					wantCalls = 0
				} else if test.remaining && test.attemptErr != nil {
					wantReports = 1
				}
				if err != test.wantErr || !reflect.DeepEqual(got, operation.result(block, ref, test.wantErr)) || calls != wantCalls || reports != wantReports {
					t.Fatalf("result = (%v, %v), calls = %d, reports = %d; want error %v, %d calls and %d reports", got, err, calls, reports, test.wantErr, wantCalls, wantReports)
				}
			})
		}
	}
}

func TestCombinedBlockStoreBackendContextError(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			// An individual store can time out while the caller's context remains
			// active. That error must not prevent attempts on later stores.
			calls, reports := 0, 0
			probe := &combinedProbe{call: func(string, context.Context, Reference, []byte, []Reference) error {
				calls++
				if calls == 1 {
					return context.DeadlineExceeded
				}
				return nil
			}}
			store := NewCombinedBlockStore(probe, probe)
			store.OnError = func(index int, err error) {
				reports++
				if index != 0 || err != context.DeadlineExceeded {
					t.Fatalf("callback = (%d, %v)", index, err)
				}
			}
			_, err := operation.call(store, context.Background(), nil, Reference{}, nil)
			if err != nil || calls != 2 || reports != 1 {
				t.Fatalf("error = %v, calls = %d, reports = %d", err, calls, reports)
			}
		})
	}
}

func TestCombinedBlockStoreConcurrentCalls(t *testing.T) {
	t.Parallel()
	block := bytes.Repeat([]byte{11}, int(BlockSize1KiB))
	ref, err := ReferenceOf(block)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("first store unavailable")
	var failures, successes, reports atomic.Int64
	store := NewCombinedBlockStore(
		&combinedProbe{call: func(string, context.Context, Reference, []byte, []Reference) error {
			failures.Add(1)
			return failure
		}},
		&combinedProbe{block: block, ref: ref, call: func(string, context.Context, Reference, []byte, []Reference) error {
			successes.Add(1)
			return nil
		}},
	)
	store.OnError = func(index int, err error) {
		reports.Add(1)
		if index != 0 || err != failure {
			t.Errorf("callback = (%d, %v)", index, err)
		}
	}
	var group sync.WaitGroup
	for range 32 {
		for _, operation := range combinedOperations {
			group.Go(func() {
				got, err := operation.call(store, context.Background(), block, ref, []Reference{ref})
				if err != nil || !reflect.DeepEqual(got, operation.result(block, ref, nil)) {
					t.Errorf("%s returned an incorrect result: %v", operation.name, err)
				}
			})
		}
	}
	group.Wait()
	want := int64(32 * len(combinedOperations))
	if failures.Load() != want || successes.Load() != want || reports.Load() != want {
		t.Fatalf("failures = %d, successes = %d, reports = %d, want %d each", failures.Load(), successes.Load(), reports.Load(), want)
	}
}

func TestCombinedBlockStoreBlockSets(t *testing.T) {
	t.Parallel()
	block, pair, err := EncryptNode(make([]byte, BlockSize1KiB), Secret{}, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("read past corrupt and missing blocks", func(t *testing.T) {
		corrupt := append([]byte(nil), block...)
		corrupt[0] ^= 1
		var indexes []int
		store := NewCombinedBlockStore(
			BlockSet{Blocks: map[Reference]Block{pair.Reference: {Data: corrupt}}},
			BlockSet{Blocks: make(map[Reference]Block)},
			BlockSet{Blocks: map[Reference]Block{pair.Reference: {Data: block}}},
		)
		store.OnError = func(index int, err error) {
			indexes = append(indexes, index)
			want := []error{ErrInvalidBlock, ErrMissingBlock}
			if index >= len(want) || !errors.Is(err, want[index]) {
				t.Fatalf("unexpected callback: (%d, %v)", index, err)
			}
		}
		got, err, _ := GetBlock(context.Background(), store, pair.Reference, 0, BlockOptions{})
		if err != nil || !bytes.Equal(got, block) || !slices.Equal(indexes, []int{0, 1}) {
			t.Fatalf("read error = %v, callback indexes = %v", err, indexes)
		}
	})

	t.Run("write only to first successful sink", func(t *testing.T) {
		readOnly, writable, later := BlockSet{Blocks: make(map[Reference]Block)}, BlockSet{Blocks: make(map[Reference]Block)}, BlockSet{Blocks: make(map[Reference]Block)}
		var indexes []int
		store := NewCombinedBlockStore(struct{ BlockSource }{readOnly}, BlockSet{}, writable, later)
		store.OnError = func(index int, err error) {
			indexes = append(indexes, index)
			if err != ErrMissingBlock {
				t.Fatalf("callback error = %v", err)
			}
		}
		input := append([]byte(nil), block...)
		ref, err := PutBlock(context.Background(), store, input, 0, BlockOptions{}, pair.Reference)
		if err != nil || ref != pair.Reference || !slices.Equal(indexes, []int{1}) {
			t.Fatalf("write = (%v, %v), callback indexes = %v", ref, err, indexes)
		}
		input[0] ^= 1
		if len(readOnly.Blocks) != 0 || len(later.Blocks) != 0 || !bytes.Equal(writable.Blocks[ref].Data, block) {
			t.Fatal("write reached another store or retained the caller's buffer")
		}
	})

	t.Run("invalid writes fail in every sink", func(t *testing.T) {
		for _, test := range []struct {
			block    []byte
			expected []Reference
		}{
			{block, []Reference{{}}},
			{block, []Reference{pair.Reference, pair.Reference}},
			{[]byte{1}, nil},
		} {
			first, second := BlockSet{Blocks: make(map[Reference]Block)}, BlockSet{Blocks: make(map[Reference]Block)}
			store := NewCombinedBlockStore(first, second)
			ref, err := PutBlock(context.Background(), store, test.block, 0, BlockOptions{}, test.expected...)
			if !errors.Is(err, ErrInvalidBlock) || ref != (Reference{}) || len(first.Blocks) != 0 || len(second.Blocks) != 0 {
				t.Fatalf("invalid write = (%v, %v), store lengths = %d, %d", ref, err, len(first.Blocks), len(second.Blocks))
			}
		}
	})
}

func TestCombinedBlockStoreInFlightCancellation(t *testing.T) {
	t.Parallel()
	for _, operation := range combinedOperations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			done := make(chan error, 1)
			store := NewCombinedBlockStore(&combinedProbe{call: func(_ string, ctx context.Context, _ Reference, _ []byte, _ []Reference) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}})
			store.OnError = func(int, error) { t.Error("final error reported to callback") }
			go func() {
				_, err := operation.call(store, ctx, nil, Reference{}, nil)
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("storage call did not start")
			}
			cancel()
			select {
			case err := <-done:
				if err != context.Canceled {
					t.Fatalf("error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("storage call did not stop after cancellation")
			}
		})
	}
}

// BatchSize reports the maximum number of requests per call.
func (s *combinedProbe) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See BlockSource for error rules.
func (s *combinedProbe) GetBlock(ctx context.Context, requests []BlockRequest) ([]Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), ErrMissingBlock, func(r BlockRequest) (Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *combinedProbe) PutBlock(ctx context.Context, requests []PutBlockRequest) ([]Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r PutBlockRequest) (Reference, error) {
		var expected []Reference
		if r.Expected != nil {
			expected = []Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s *combinedProbe) UpdateBlockOptions(ctx context.Context, requests []BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// Two embedded interfaces have an ambiguous BatchSize method. Resolve it
// explicitly when read and write methods come from separate fields.
type separateBlockStore struct {
	BlockSource
	BlockSink
}

func (separateBlockStore) BatchSize() int { return 1 }
