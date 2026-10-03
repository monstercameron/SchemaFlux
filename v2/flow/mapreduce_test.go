package flow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

func numbers(n int) []int {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	return items
}

// sumChunk is an operation that returns the sum of its chunk.
func sumChunk(_ context.Context, chunk []int) (int, error) {
	total := 0
	for _, n := range chunk {
		total += n
	}
	return total, nil
}

func TestMapReduceSplitsTheItemsIntoChunksAndReturnsTheResultsInInputOrder(t *testing.T) {
	cases := []struct {
		name    string
		items   int
		options flow.MapReduceOptions
		sizes   []int
	}{
		{"the default chunk is 25", 60, flow.MapReduceOptions{}, []int{25, 25, 10}},
		{"an exact multiple of the chunk size", 50, flow.MapReduceOptions{}, []int{25, 25}},
		{"fewer items than a chunk", 7, flow.MapReduceOptions{}, []int{7}},
		{"a chunk of one", 5, flow.MapReduceOptions{ChunkSize: 1}, []int{1, 1, 1, 1, 1}},
		{"a chunk of three with a remainder", 8, flow.MapReduceOptions{ChunkSize: 3}, []int{3, 3, 2}},
		{"a chunk larger than the input", 4, flow.MapReduceOptions{ChunkSize: 100}, []int{4}},
		{"a single item", 1, flow.MapReduceOptions{}, []int{1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, run, err := flow.MapReduce(context.Background(), numbers(tc.items), func(_ context.Context, chunk []int) (int, error) {
				return len(chunk), nil
			}, tc.options)
			if err != nil || !reflect.DeepEqual(got, tc.sizes) {
				t.Fatalf("MapReduce = %v, %v; want chunks of %v", got, err, tc.sizes)
			}
			if run.Chunks != len(tc.sizes) || !run.Complete() || run.Failed != nil {
				t.Errorf("run = %+v", run)
			}
		})
	}
}

func TestMapReduceGivesEachChunkItsItemsInOrderWhateverOrderTheChunksFinishIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got, _, err := flow.MapReduce(context.Background(), numbers(10), func(ctx context.Context, chunk []int) (int, error) {
			// Earlier chunks take longer, so they finish last.
			return wait(time.Duration(10-chunk[0])*time.Millisecond, chunk[0]*100+len(chunk))(ctx)
		}, flow.MapReduceOptions{ChunkSize: 2, Concurrency: 5})
		want := []int{2, 202, 402, 602, 802}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("MapReduce = %v, %v; want %v in the order of the input", got, err, want)
		}
	})
}

func TestMapReduceStartsTheChunksInOrderAndBoundsHowManyRunAtOnce(t *testing.T) {
	cases := []struct {
		name        string
		chunks      int
		concurrency int
		peak        int32
	}{
		{"the default is four", 10, 0, 4},
		{"one at a time", 5, 1, 1},
		{"two at a time", 6, 2, 2},
		{"more than there are chunks", 3, 9, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var running, peak atomic.Int32
				var mu sync.Mutex
				var started []int
				gate := make(chan struct{})
				var wg sync.WaitGroup
				wg.Go(func() {
					_, _, err := flow.MapReduce(context.Background(), numbers(tc.chunks), func(_ context.Context, chunk []int) (int, error) {
						mu.Lock()
						started = append(started, chunk[0])
						mu.Unlock()
						now := running.Add(1)
						for {
							old := peak.Load()
							if now <= old || peak.CompareAndSwap(old, now) {
								break
							}
						}
						<-gate
						running.Add(-1)
						return 0, nil
					}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: tc.concurrency})
					if err != nil {
						t.Error(err)
					}
				})
				for i := 0; i < tc.chunks; i++ {
					synctest.Wait()
					if running.Load() > tc.peak {
						t.Fatalf("%d chunks running at once, want at most %d", running.Load(), tc.peak)
					}
					gate <- struct{}{}
				}
				wg.Wait()
				if peak.Load() != tc.peak {
					t.Errorf("peak = %d, want %d", peak.Load(), tc.peak)
				}
				if tc.concurrency == 1 && !reflect.DeepEqual(started, numbers(tc.chunks)) {
					t.Errorf("chunks started in the order %v; with Concurrency 1 the run is sequential", started)
				}
			})
		})
	}
}

func TestMapReduceStopsAtTheFirstFailureAndReturnsTheOperationsErrorAsItWasGiven(t *testing.T) {
	var calls atomic.Int32
	got, run, err := flow.MapReduce(context.Background(), numbers(6), func(_ context.Context, chunk []int) (int, error) {
		calls.Add(1)
		if chunk[0] == 2 {
			return 0, errStep
		}
		return chunk[0], nil
	}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 1})
	if err != errStep || got != nil {
		t.Errorf("MapReduce = %v, %v; want no results and the operation's error as it was given", got, err)
	}
	if calls.Load() != 3 {
		t.Errorf("%d chunks ran; none after the failing one starts", calls.Load())
	}
	if run.Chunks != 6 || run.Complete() || len(run.Failed) != 4 {
		t.Fatalf("run = %+v", run)
	}
	if run.Failed[0].Index != 2 || run.Failed[0].Err != errStep {
		t.Errorf("Failed[0] = %+v, want chunk 2 with the operation's error as it was given", run.Failed[0])
	}
	for _, failed := range run.Failed[1:] {
		wantKind(t, failed.Err, schemaflux.ErrCanceled)
	}
}

func TestMapReduceReturnsTheFailureThatCameFirstInTimeAndNotTheLowestChunk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, run, err := flow.MapReduce(context.Background(), numbers(3), func(ctx context.Context, chunk []int) (int, error) {
			if chunk[0] == 1 {
				return 0, errStep
			}
			<-ctx.Done()
			return 0, ctx.Err()
		}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 3})
		if err != errStep {
			t.Errorf("err = %v; chunks 0 and 2 failed because chunk 1 did, and their cancellation is not what is returned", err)
		}
		if len(run.Failed) != 3 || run.Failed[0].Err != context.Canceled || run.Failed[1].Err != errStep {
			t.Errorf("run.Failed = %+v", run.Failed)
		}
	})
}

func TestMapReduceWithContinueOnErrorKeepsTheChunksThatSucceededAndListsTheRest(t *testing.T) {
	failure := fmt.Errorf("chunk failed: %w", errStep)
	got, run, err := flow.MapReduce(context.Background(), numbers(10), func(_ context.Context, chunk []int) (int, error) {
		if chunk[0] == 4 {
			return 0, failure
		}
		return chunk[0], nil
	}, flow.MapReduceOptions{ChunkSize: 2, Concurrency: 1, ContinueOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{0, 2, 6, 8}) {
		t.Errorf("results = %v; only the chunks that succeeded, in chunk order, so result k is not chunk k", got)
	}
	if run.Chunks != 5 || run.Complete() || len(run.Failed) != 1 {
		t.Fatalf("run = %+v", run)
	}
	failed := run.Failed[0]
	if failed.Index != 2 || failed.Offset != 4 || failed.Err != failure {
		t.Errorf("Failed[0] = %+v; want chunk 2, offset 4 and the operation's error as it was given", failed)
	}
	if !errors.Is(failed, errStep) || !strings.Contains(failed.Error(), "chunk 2 (items 4 onward)") {
		t.Errorf("the ChunkError does not unwrap or name the chunk: %v", failed)
	}
}

func TestMapReduceWithContinueOnErrorReportsEveryFailedChunkInOrder(t *testing.T) {
	_, run, err := flow.MapReduce(context.Background(), numbers(8), func(context.Context, []int) (int, error) {
		return 0, errStep
	}, flow.MapReduceOptions{ChunkSize: 2, ContinueOnError: true})
	if err != nil {
		t.Fatalf("err = %v; with ContinueOnError the failures are in the result", err)
	}
	if len(run.Failed) != 4 {
		t.Fatalf("run = %+v", run)
	}
	for i, failed := range run.Failed {
		if failed.Index != i || failed.Offset != i*2 || failed.Err != errStep {
			t.Errorf("Failed[%d] = %+v", i, failed)
		}
	}
}

func TestMapReduceTurnsAPanicIntoAFailedChunkThatHoldsNothingOfIt(t *testing.T) {
	const canary = "panic-payload-4471"
	t.Run("stopping at it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			leaks := noLeak(t)
			got, run, err := flow.MapReduce(context.Background(), numbers(4), func(ctx context.Context, chunk []int) (int, error) {
				if chunk[0] == 1 {
					panic(canary)
				}
				<-ctx.Done()
				return 0, ctx.Err()
			}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 4})
			e := wantKind(t, err, schemaflux.ErrInternal)
			if got != nil || run.Complete() {
				t.Errorf("got = %v, run = %+v", got, run)
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
				t.Errorf("the panic's value is in the error: %v", err)
			}
			leaks()
		})
	})
	t.Run("continuing past it", func(t *testing.T) {
		got, run, err := flow.MapReduce(context.Background(), numbers(4), func(_ context.Context, chunk []int) (int, error) {
			if chunk[0] == 1 {
				panic(canary)
			}
			return chunk[0], nil
		}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 1, ContinueOnError: true})
		if err != nil || !reflect.DeepEqual(got, []int{0, 2, 3}) || len(run.Failed) != 1 {
			t.Fatalf("MapReduce = %v, %+v, %v", got, run, err)
		}
		var e *schemaflux.Error
		if !errors.As(run.Failed[0].Err, &e) || e.Kind != schemaflux.ErrInternal {
			t.Errorf("Failed[0].Err = %v; an error the library makes, held in a result, is an *Error", run.Failed[0].Err)
		}
		if strings.Contains(run.Failed[0].Error(), canary) {
			t.Errorf("the panic's value is in %v", run.Failed[0])
		}
	})
}

func TestMapReduceTurnsAChunkThatEndsItsGoroutineIntoAnInternalError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := noLeak(t)
		_, _, err := flow.MapReduce(context.Background(), numbers(3), func(ctx context.Context, chunk []int) (int, error) {
			if chunk[0] == 0 {
				return goexit[int](ctx)
			}
			<-ctx.Done()
			return 0, ctx.Err()
		}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 3})
		wantKind(t, err, schemaflux.ErrInternal)
		leaks()
	})
}

func TestMapReduceOfNoItemsIsNoResultsNoChunksAndNoError(t *testing.T) {
	var calls atomic.Int32
	for _, items := range [][]int{nil, {}} {
		got, run, err := flow.MapReduce(context.Background(), items, func(context.Context, []int) (int, error) {
			calls.Add(1)
			return 0, nil
		})
		if err != nil || len(got) != 0 || run.Chunks != 0 || !run.Complete() || calls.Load() != 0 {
			t.Errorf("MapReduce(%#v) = %v, %+v, %v after %d calls", items, got, run, err, calls.Load())
		}
	}
}

func TestMapReduceRefusesAnArgumentItCannotUseAndRunsNothing(t *testing.T) {
	var calls atomic.Int32
	operation := func(context.Context, []int) (int, error) {
		calls.Add(1)
		return 0, nil
	}
	cases := []struct {
		name      string
		operation func(context.Context, []int) (int, error)
		options   []flow.MapReduceOptions
		kind      schemaflux.Kind
	}{
		{"no operation", nil, nil, schemaflux.ErrInvalidRequest},
		{"a negative chunk size", operation, []flow.MapReduceOptions{{ChunkSize: -1}}, schemaflux.ErrConfiguration},
		{"a negative concurrency", operation, []flow.MapReduceOptions{{Concurrency: -4}}, schemaflux.ErrConfiguration},
		{"a negative chunk size in a later options value", operation, []flow.MapReduceOptions{{ChunkSize: 2}, {ChunkSize: -2}}, schemaflux.ErrConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, run, err := flow.MapReduce(context.Background(), numbers(5), tc.operation, tc.options...)
			e := wantKind(t, err, tc.kind)
			if e.Op != "flow.mapreduce" || got != nil || run.Chunks != 0 || calls.Load() != 0 {
				t.Errorf("Op = %q, got = %v, run = %+v, calls = %d", e.Op, got, run, calls.Load())
			}
		})
	}
}

func TestMapReduceRefusesANilContextAndAContextThatIsOverAndRunsNothing(t *testing.T) {
	var calls atomic.Int32
	operation := func(context.Context, []int) (int, error) {
		calls.Add(1)
		return 0, nil
	}
	var nilContext context.Context
	_, _, err := flow.MapReduce(nilContext, numbers(3), operation)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		for _, items := range [][]int{numbers(3), nil} {
			got, _, err := flow.MapReduce(done.ctx, items, operation)
			wantKind(t, err, done.kind)
			if !errors.Is(err, done.is) || got != nil {
				t.Errorf("%s: err = %v, got = %v", done.name, err, got)
			}
		}
	}
	if calls.Load() != 0 {
		t.Errorf("%d chunks ran", calls.Load())
	}
}

func TestMapReduceReturnsTheContextsErrorAndNoPartialResultWhenTheCallerCancelsDuringAContinuingRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type outcome struct {
			got []int
			run flow.MapReduceResult
			err error
		}
		result := make(chan outcome)
		go func() {
			got, run, err := flow.MapReduce(ctx, numbers(6), func(ctx context.Context, chunk []int) (int, error) {
				if chunk[0] == 0 {
					return 0, nil
				}
				<-ctx.Done()
				return 0, ctx.Err()
			}, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 2, ContinueOnError: true})
			result <- outcome{got, run, err}
		}()
		synctest.Wait()
		cancel()
		r := <-result
		wantKind(t, r.err, schemaflux.ErrCanceled)
		if !errors.Is(r.err, context.Canceled) || r.got != nil || r.run.Complete() {
			t.Errorf("err = %v, got = %v, run = %+v; a run that was stopped did not finish", r.err, r.got, r.run)
		}
	})
}

func TestMapReduceMergesSeveralOptionsLeftToRightALaterNonZeroFieldWinning(t *testing.T) {
	cases := []struct {
		name    string
		options []flow.MapReduceOptions
		chunks  int
	}{
		{"none", nil, 4},
		{"a zero value", []flow.MapReduceOptions{{}}, 4},
		{"one value", []flow.MapReduceOptions{{ChunkSize: 10}}, 8},
		{"a later value wins", []flow.MapReduceOptions{{ChunkSize: 10}, {ChunkSize: 40}}, 2},
		{"a later zero value does not blank an earlier one", []flow.MapReduceOptions{{ChunkSize: 10}, {Concurrency: 2}}, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, run, err := flow.MapReduce(context.Background(), numbers(80), sumChunk, tc.options...)
			if err != nil || run.Chunks != tc.chunks {
				t.Errorf("chunks = %d, err = %v; want %d", run.Chunks, err, tc.chunks)
			}
		})
	}
	t.Run("ContinueOnError set by any value stays set", func(t *testing.T) {
		_, run, err := flow.MapReduce(context.Background(), numbers(4), func(context.Context, []int) (int, error) { return 0, errStep },
			flow.MapReduceOptions{ContinueOnError: true, ChunkSize: 1}, flow.MapReduceOptions{ChunkSize: 2})
		if err != nil || len(run.Failed) != 2 {
			t.Errorf("run = %+v, err = %v", run, err)
		}
	})
}

func TestMapReduceHandsTheOperationTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), contextKey{}, "mine")
	got, _, err := flow.MapReduce(ctx, numbers(3), func(ctx context.Context, _ []int) (string, error) {
		return fmt.Sprint(ctx.Value(contextKey{})), nil
	}, flow.MapReduceOptions{ChunkSize: 3})
	if err != nil || !reflect.DeepEqual(got, []string{"mine"}) {
		t.Errorf("MapReduce = %v, %v", got, err)
	}
}

func TestMapReduceReturnsTheErrorTheOperationGaveWhateverItsType(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "summarize"}
	for _, failure := range []error{errStep, fmt.Errorf("wrapped: %w", errStep), typed, context.DeadlineExceeded} {
		_, run, err := flow.MapReduce(context.Background(), numbers(3), func(context.Context, []int) (int, error) { return 0, failure }, flow.MapReduceOptions{ChunkSize: 1, Concurrency: 1})
		if err != failure || run.Failed[0].Err != failure {
			t.Errorf("err = %v, Failed[0].Err = %v; want %v itself both times", err, run.Failed[0].Err, failure)
		}
	}
}

func TestMapReduceLeavesNoGoroutineBehind(t *testing.T) {
	cases := []struct {
		name string
		op   func(context.Context, []int) (int, error)
		opts flow.MapReduceOptions
	}{
		{"every chunk succeeds", func(ctx context.Context, c []int) (int, error) { return wait(time.Second, c[0])(ctx) }, flow.MapReduceOptions{ChunkSize: 1}},
		{"one fails", func(ctx context.Context, c []int) (int, error) {
			if c[0] == 3 {
				return 0, errStep
			}
			return wait(time.Hour, c[0])(ctx)
		}, flow.MapReduceOptions{ChunkSize: 1}},
		{"one panics", func(ctx context.Context, c []int) (int, error) {
			if c[0] == 3 {
				panic("x")
			}
			return wait(time.Hour, c[0])(ctx)
		}, flow.MapReduceOptions{ChunkSize: 1}},
		{"every chunk fails and the run continues", func(context.Context, []int) (int, error) { return 0, errStep }, flow.MapReduceOptions{ChunkSize: 1, ContinueOnError: true}},
		{"a chunk ignores cancellation", func(_ context.Context, c []int) (int, error) {
			if c[0] == 0 {
				return 0, errStep
			}
			time.Sleep(time.Minute)
			return 0, nil
		}, flow.MapReduceOptions{ChunkSize: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				_, _, _ = flow.MapReduce(context.Background(), numbers(10), tc.op, tc.opts)
				leaks()
			})
		})
	}
}

func TestMapReduceResultIsCompleteOnlyWhenNoChunkFailed(t *testing.T) {
	cases := []struct {
		result flow.MapReduceResult
		want   bool
	}{
		{flow.MapReduceResult{}, true},
		{flow.MapReduceResult{Chunks: 3}, true},
		{flow.MapReduceResult{Chunks: 3, Failed: []flow.ChunkError{}}, true},
		{flow.MapReduceResult{Chunks: 3, Failed: []flow.ChunkError{{Index: 1}}}, false},
	}
	for _, tc := range cases {
		if got := tc.result.Complete(); got != tc.want {
			t.Errorf("%+v.Complete() = %v", tc.result, got)
		}
	}
}
