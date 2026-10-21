package ops

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every failure of MapReduce's own has a kind; a chunk's operation that panics
// is an invariant violation marked as a panic, and carries nothing of the
// panic; and an error a chunk's operation returns is reachable as the very
// value it returned.
func TestMapReduceFailuresHaveKinds(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	items := []int{1, 2, 3, 4}
	ok := func(_ context.Context, chunk []int) (int, error) { return len(chunk), nil }
	cases := []struct {
		name     string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
		narrow   types.Narrowing
	}{
		{"no operation", func() error {
			_, _, err := MapReduce[int, int](context.Background(), items, MapReduceOptions{}, nil)
			return err
		},
			"mapreduce: invalid request: no operation given", types.KindInvalidRequest, types.ErrInvalidRequest, types.NarrowNone},
		{"an operation that panics", func() error {
			_, _, err := MapReduce(context.Background(), items, MapReduceOptions{ChunkSize: 4},
				func(context.Context, []int) (int, error) { panic("secret") })
			return err
		}, "chunk 0 (items 0 onward): mapreduce: invariant violation: the operation panicked on chunk 0",
			types.KindInvariantViolation, types.ErrInvariantViolation, types.NarrowPanic},
		{"an operation that panics, with the run going on", func() error {
			_, summary, err := MapReduce(context.Background(), items, MapReduceOptions{ChunkSize: 2, ContinueOnError: true},
				func(_ context.Context, chunk []int) (int, error) {
					if chunk[0] == 1 {
						panic("secret")
					}
					return 1, nil
				})
			if err != nil || len(summary.Failed) != 1 {
				t.Fatalf("err = %v, failed = %v", err, summary.Failed)
			}
			return summary.Failed[0].Err
		}, "mapreduce: invariant violation: the operation panicked on chunk 0",
			types.KindInvariantViolation, types.ErrInvariantViolation, types.NarrowPanic},
		{"an operation that ends its goroutine", func() error {
			_, summary, err := MapReduce(context.Background(), items, MapReduceOptions{ChunkSize: 4, ContinueOnError: true},
				func(context.Context, []int) (int, error) { runtime.Goexit(); return 0, nil })
			if err != nil || len(summary.Failed) != 1 {
				t.Fatalf("err = %v, failed = %v", err, summary.Failed)
			}
			return summary.Failed[0].Err
		}, "mapreduce: invariant violation: a step panicked or ended its goroutine without returning",
			types.KindInvariantViolation, types.ErrInvariantViolation, types.NarrowPanic},
		{"a cancelled context, with the run going on", func() error {
			_, _, err := MapReduce(cancelled, items, MapReduceOptions{ChunkSize: 2, ContinueOnError: true}, ok)
			return err
		}, "context canceled", types.KindCanceled, types.ErrCanceled, types.NarrowNone},
		{"a cancelled context, with the run stopping at the first failure", func() error {
			_, _, err := MapReduce(cancelled, items, MapReduceOptions{ChunkSize: 2}, ok)
			return err
		}, "", types.KindCanceled, types.ErrCanceled, types.NarrowNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if tc.text != "" && err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("the panic's value is in the error: %v", err)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if got := types.NarrowingOf(err); got != tc.narrow {
				t.Errorf("narrowing = %v, want %v", got, tc.narrow)
			}
		})
	}

	failures := []error{
		errors.New("the caller's own failure"),
		&types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"},
		&types.OperationError{Kind: types.KindRateLimited, Op: "theirs", Message: "slow down"},
	}
	for _, failure := range failures {
		fail := func(context.Context, []int) (int, error) { return 0, failure }
		t.Run("a chunk's own error stops the run as the very value it was: "+failure.Error(), func(t *testing.T) {
			_, _, err := MapReduce(context.Background(), items, MapReduceOptions{ChunkSize: 4}, fail)
			var chunk ChunkError
			if !errors.As(err, &chunk) || chunk.Err != failure || !errors.Is(err, failure) {
				t.Errorf("err = %#v, want a ChunkError holding the very value the operation returned", err)
			}
			if types.KindOf(err) != types.KindOf(failure) {
				t.Errorf("kind = %v, want the operation's own, %v", types.KindOf(err), types.KindOf(failure))
			}
		})
		t.Run("a chunk's own error is in the summary as the very value it was: "+failure.Error(), func(t *testing.T) {
			_, summary, err := MapReduce(context.Background(), items, MapReduceOptions{ChunkSize: 4, ContinueOnError: true}, fail)
			if err != nil || len(summary.Failed) != 1 || summary.Failed[0].Err != failure {
				t.Errorf("err = %v, failed = %#v", err, summary.Failed)
			}
		})
		t.Run("MapReduceFlat returns the same: "+failure.Error(), func(t *testing.T) {
			_, _, err := MapReduceFlat(context.Background(), items, MapReduceOptions{ChunkSize: 4},
				func(context.Context, []int) ([]int, error) { return nil, failure })
			if !errors.Is(err, failure) {
				t.Errorf("err = %v", err)
			}
		})
	}
}
