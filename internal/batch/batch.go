// Package batch supplies ordered, non-atomic batch processing for block stores.
package batch

import (
	"context"
	"errors"
)

// ErrTooLarge is shared by every block storage implementation.
var ErrTooLarge = errors.New("eris: batch size exceeds storage limit")

// Run rejects oversized batches before processing. It stops on the first error
// and returns only the completed prefix. Empty batches succeed, even if ctx is
// canceled. A negative limit is treated as one, the safe default.
func Run[Request, Result any](ctx context.Context, requests []Request, limit int, call func(Request) (Result, error)) ([]Result, error) {
	if limit < 0 {
		limit = 1
	}
	if limit > 0 && len(requests) > limit {
		return nil, ErrTooLarge
	}
	if len(requests) == 0 {
		return nil, nil
	}
	var results []Result
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		result, err := call(request)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// Read follows Run's limit and cancellation rules, but continues after errors
// that match missing. It inserts a zero result for each missing item so result
// indexes still match request indexes. Other errors stop processing before the
// failed item. The first missing error is preserved, joined with the stopping
// error if there is one. Cancellation errors always stop processing, even when
// an error also matches missing. The missing argument must be non-nil.
func Read[Request, Result any](ctx context.Context, requests []Request, limit int, missing error, call func(Request) (Result, error)) ([]Result, error) {
	var firstMissing error
	results, err := Run(ctx, requests, limit, func(request Request) (Result, error) {
		result, err := call(request)
		if errors.Is(err, missing) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if firstMissing == nil {
				firstMissing = err
			}
			var zero Result
			return zero, nil
		}
		return result, err
	})
	if firstMissing == nil {
		return results, err
	}
	if err == nil {
		return results, firstMissing
	}
	return results, errors.Join(firstMissing, err)
}

// Limit caps automatic read and write batches at 64 blocks (at most 2 MiB of
// encrypted block data), even when the store accepts an unlimited batch.
func Limit(reported int) int {
	if reported < 0 {
		return 1
	}
	if reported == 0 || reported > 64 {
		return 64
	}
	return reported
}
