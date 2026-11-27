package eris

import (
	"context"
	"errors"
)

// combinedBatchMethod retains a child's methods without calling them during
// construction. Read its limit at each call, since stores can change limits.
type combinedBatchMethod[Request, Result any] struct {
	index int
	limit func() int
	call  func(context.Context, []Request) ([]Result, error)
}

type combinedBatchOperation[Request, Result any] struct {
	methods []combinedBatchMethod[Request, Result]
	onError func(int, error)
	retry   func(Result) bool // Missing reads and unchanged options need fallback.
	read    bool
}

// run keeps successful results and retries only unresolved requests. Consecutive
// missing reads or unchanged updates form a fallback batch. After a stopping
// child error, retry just the failed request before resuming at the first child.
// Thus a successful write is never replayed, and later writes are not attempted
// if all stores fail for an earlier write.
func (op combinedBatchOperation[Request, Result]) run(ctx context.Context, requests []Request) ([]Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(op.methods) == 0 {
		return nil, ErrNoBlockStorage
	}
	method := op.methods[0]
	next := op
	next.methods = op.methods[1:]
	var results []Result
	var firstMissing error
	finish := func(err error) ([]Result, error) {
		if firstMissing == nil {
			return results, err
		}
		if err == nil {
			return results, firstMissing
		}
		return results, errors.Join(firstMissing, err)
	}
	// A full read result with missing slots is complete, despite its error.
	appendResults := func(got []Result, err error, count int) bool {
		results = append(results, got...)
		if op.read && len(got) == count && errors.Is(err, ErrMissingBlock) &&
			!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if firstMissing == nil {
				firstMissing = err
			}
			return true
		}
		return err == nil
	}
	for len(requests) > 0 {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		count := len(requests)
		if limit := method.limit(); limit < 0 {
			count = 1
		} else if limit > 0 {
			count = min(count, limit)
		}
		chunk := requests[:count]
		got, err := method.call(ctx, chunk)
		if !op.valid(got, err, count) {
			got, err = nil, ErrInvalidBatchResult
		}
		if err == nil && (op.read || op.retry == nil) {
			// A validated read or write success has no fallback slots.
			results = append(results, got...)
			requests = requests[count:]
			continue
		}
		missing := false
		for i := 0; i < len(got); {
			if op.retry == nil || !op.retry(got[i]) {
				results = append(results, got[i])
				i++
				continue
			}
			end := i + 1
			for end < len(got) && op.retry(got[end]) {
				end++
			}
			// Errors on updates describe the next, unprocessed request; false
			// results in the completed prefix are not errors.
			var cause error
			if op.read {
				missing = true
				cause = err
				if len(got) < count {
					cause = combinedMissingError(err)
				}
			}
			if len(next.methods) == 0 {
				appendResults(got[i:end], cause, end-i)
			} else {
				if cause != nil && op.onError != nil {
					op.onError(method.index, cause)
				}
				recovered, fallbackErr := next.run(ctx, chunk[i:end])
				if !appendResults(recovered, fallbackErr, end-i) {
					return finish(fallbackErr)
				}
			}
			i = end
		}
		processed := len(got)
		// A child can report an error after completing every request. There
		// is no failed request to retry. Only a read's missing-slot error is
		// resolved by filling those slots from later sources.
		if processed == count && err != nil && (!missing ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return finish(err)
		}
		if processed < count {
			if len(next.methods) == 0 {
				return finish(err)
			}
			if op.onError != nil {
				op.onError(method.index, err)
			}
			recovered, fallbackErr := next.run(ctx, chunk[processed:processed+1])
			if !appendResults(recovered, fallbackErr, 1) {
				return finish(fallbackErr)
			}
			processed++
		}
		requests = requests[processed:]
	}
	return finish(nil)
}

// valid checks result counts before they are used as request indexes. Sources
// must also supply a missing-block error for zero slots in a completed prefix.
func (op combinedBatchOperation[Request, Result]) valid(got []Result, err error, count int) bool {
	if len(got) > count || err == nil && len(got) != count {
		return false
	}
	if !op.read {
		return true
	}
	for _, result := range got {
		if op.retry(result) {
			return errors.Is(err, ErrMissingBlock)
		}
	}
	return true
}

// A read can join its first missing error with a later stopping error. Keep the
// missing component for its zero slots so a recovered stopping error does not
// leak into the final missing-block error. Preserve ordinary wrapped errors.
func combinedMissingError(err error) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if errors.Is(cause, ErrMissingBlock) {
				return cause
			}
		}
	}
	return err
}
