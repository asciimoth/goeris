package batch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestRun(t *testing.T) {
	failure := errors.New("failure")
	for _, tc := range []struct {
		name                      string
		size, limit, fail, cancel int
		want                      int
		err                       error
	}{
		{"empty canceled", 0, 1, -1, 0, 0, nil},
		{"too large", 3, 2, -1, -1, 0, ErrTooLarge},
		{"negative limit", 2, -1, -1, -1, 0, ErrTooLarge},
		{"negative single", 1, -1, -1, -1, 1, nil},
		{"unlimited", 4, 0, -1, -1, 4, nil},
		{"exact limit", 4, 4, -1, -1, 4, nil},
		{"first failure", 4, 0, 0, -1, 0, failure},
		{"partial failure", 4, 0, 2, -1, 2, failure},
		{"canceled", 4, 0, -1, 0, 0, context.Canceled},
		{"cancel between", 4, 0, -1, 2, 2, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel == 0 {
				cancel()
			}
			requests := make([]int, tc.size)
			for i := range requests {
				requests[i] = i
			}
			calls := 0
			got, err := Run(ctx, requests, tc.limit, func(v int) (int, error) {
				calls++
				if v == tc.fail {
					return 999, failure
				}
				if calls == tc.cancel {
					cancel()
				}
				return v * 2, nil
			})
			if !errors.Is(err, tc.err) || len(got) != tc.want {
				t.Fatalf("results %v, error %v", got, err)
			}
			for i, v := range got {
				if v != i*2 {
					t.Fatal("order changed")
				}
			}
			wantCalls := tc.want
			if tc.err == failure {
				wantCalls++
			}
			if calls != wantCalls {
				t.Fatalf("calls %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestLimit(t *testing.T) {
	for _, tc := range [][2]int{{-1, 1}, {0, 64}, {1, 1}, {63, 63}, {64, 64}, {65, 64}} {
		if got := Limit(tc[0]); got != tc[1] {
			t.Fatalf("Limit(%d) = %d", tc[0], got)
		}
	}
}

func FuzzRun(f *testing.F) {
	f.Add([]byte{1, 2, 3}, uint8(2), uint8(1), true)
	f.Add([]byte{}, uint8(0), uint8(0), false)
	f.Fuzz(func(t *testing.T, data []byte, limit, stop uint8, canceled bool) {
		if len(data) > 256 {
			data = data[:256]
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failure := errors.New("stop")
		calls := 0
		got, err := Run(ctx, data, int(limit), func(v byte) (byte, error) {
			i := calls
			calls++
			if i == int(stop) {
				if canceled {
					cancel()
				} else {
					return 0, failure
				}
			}
			return v, nil
		})
		want := len(data)
		var wantErr error
		if limit > 0 && len(data) > int(limit) {
			want = 0
			wantErr = ErrTooLarge
		} else if int(stop) < len(data) {
			if canceled {
				if int(stop)+1 < len(data) {
					want = int(stop) + 1
					wantErr = context.Canceled
				}
			} else {
				want = int(stop)
				wantErr = failure
			}
		}
		if !errors.Is(err, wantErr) || len(got) != want || !reflect.DeepEqual(append([]byte{}, got...), append([]byte{}, data[:want]...)) {
			t.Fatalf("results %v, err %v; want prefix %d, %v", got, err, want, wantErr)
		}
	})
}

func TestRead(t *testing.T) {
	missing := errors.New("missing")
	wrapped := fmt.Errorf("storage: %w", missing)
	failure := errors.New("read failed")
	for _, tc := range []struct {
		name       string
		errs       []error
		limit      int
		cancelAt   int // Zero cancels before the first read; positive values count calls.
		want       []int
		wantCalls  int
		wantErrors []error
		exact      error
	}{
		{name: "empty canceled", cancelAt: 0},
		{name: "oversized", errs: []error{nil, nil}, limit: 1, cancelAt: -1, wantErrors: []error{ErrTooLarge}, exact: ErrTooLarge},
		{name: "all present", errs: []error{nil, nil, nil}, cancelAt: -1, want: []int{1, 2, 3}, wantCalls: 3},
		{name: "missing first", errs: []error{missing, nil, nil}, cancelAt: -1, want: []int{0, 2, 3}, wantCalls: 3, wantErrors: []error{missing}, exact: missing},
		{name: "missing middle", errs: []error{nil, wrapped, nil}, cancelAt: -1, want: []int{1, 0, 3}, wantCalls: 3, wantErrors: []error{missing}, exact: wrapped},
		{name: "missing last", errs: []error{nil, nil, missing}, cancelAt: -1, want: []int{1, 2, 0}, wantCalls: 3, wantErrors: []error{missing}, exact: missing},
		{name: "all missing", errs: []error{wrapped, missing, missing}, cancelAt: -1, want: []int{0, 0, 0}, wantCalls: 3, wantErrors: []error{missing}, exact: wrapped},
		{name: "multiple gaps", errs: []error{missing, nil, wrapped, nil}, cancelAt: -1, want: []int{0, 2, 0, 4}, wantCalls: 4, wantErrors: []error{missing}, exact: missing},
		{name: "fatal first", errs: []error{failure, missing, nil}, cancelAt: -1, wantCalls: 1, wantErrors: []error{failure}, exact: failure},
		{name: "fatal after missing", errs: []error{wrapped, nil, failure, nil}, cancelAt: -1, want: []int{0, 2}, wantCalls: 3, wantErrors: []error{missing, failure}},
		{name: "already canceled", errs: []error{nil, missing}, cancelAt: 0, wantErrors: []error{context.Canceled}, exact: context.Canceled},
		{name: "cancel after missing", errs: []error{wrapped, nil}, cancelAt: 1, want: []int{0}, wantCalls: 1, wantErrors: []error{missing, context.Canceled}},
		{name: "cancel after success", errs: []error{nil, missing}, cancelAt: 1, want: []int{1}, wantCalls: 1, wantErrors: []error{context.Canceled}, exact: context.Canceled},
		{name: "joined cancellation", errs: []error{errors.Join(missing, context.Canceled), nil}, cancelAt: -1, wantCalls: 1, wantErrors: []error{missing, context.Canceled}},
		{name: "joined deadline", errs: []error{errors.Join(missing, context.DeadlineExceeded), nil}, cancelAt: -1, wantCalls: 1, wantErrors: []error{missing, context.DeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelAt == 0 {
				cancel()
			}
			calls := 0
			got, err := Read(ctx, tc.errs, tc.limit, missing, func(failure error) (int, error) {
				calls++
				if calls == tc.cancelAt {
					cancel()
				}
				return calls, failure // Nonzero failed results must not escape.
			})
			if !slices.Equal(got, tc.want) || calls != tc.wantCalls {
				t.Fatalf("results %v, calls %d; want %v, %d", got, calls, tc.want, tc.wantCalls)
			}
			if len(tc.wantErrors) == 0 && err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantErrors {
				if !errors.Is(err, want) {
					t.Fatalf("error %v does not match %v", err, want)
				}
			}
			if tc.exact != nil && err != tc.exact {
				t.Fatalf("error %v was not preserved", err)
			}
		})
	}
}

func FuzzRead(f *testing.F) {
	f.Add([]byte{1, 0, 2, 0}, uint8(0))
	f.Add([]byte{1, 0, 3, 0}, uint8(4))
	f.Add([]byte{5, 0}, uint8(0))
	f.Fuzz(func(t *testing.T, actions []byte, limit uint8) {
		if len(actions) > 128 {
			actions = actions[:128]
		}
		missing := errors.New("missing")
		failure := errors.New("failed")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		got, err := Read(ctx, actions, int(limit), missing, func(action byte) (int, error) {
			calls++
			switch action % 6 {
			case 1:
				return 999, missing
			case 2:
				return 999, fmt.Errorf("wrapped: %w", missing)
			case 3:
				return 999, failure
			case 4:
				cancel()
			case 5:
				cancel()
				return 999, missing
			}
			return calls, nil
		})
		if limit > 0 && len(actions) > int(limit) {
			if len(got) != 0 || calls != 0 || err != ErrTooLarge {
				t.Fatal(got, calls, err)
			}
			return
		}
		var want []int
		wantCalls := 0
		missingSeen, failed, canceled, stopCanceled := false, false, false, false
		for i, action := range actions {
			if canceled {
				stopCanceled = true
				break
			}
			wantCalls++
			kind := action % 6
			if kind == 3 {
				failed = true
				break
			}
			value := i + 1
			if kind == 1 || kind == 2 || kind == 5 {
				missingSeen = true
				value = 0
			}
			want = append(want, value)
			canceled = kind == 4 || kind == 5
		}
		if !slices.Equal(got, want) || calls != wantCalls {
			t.Fatal(got, want, calls, wantCalls)
		}
		if errors.Is(err, missing) != missingSeen || errors.Is(err, failure) != failed || errors.Is(err, context.Canceled) != stopCanceled {
			t.Fatal(err, missingSeen, failed, stopCanceled)
		}
		if !missingSeen && !failed && !stopCanceled && err != nil {
			t.Fatal(err)
		}
	})
}
