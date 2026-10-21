package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// There was no chunk → per-chunk operation → merge primitive, so a collection
// larger than one context window had no supported handling at all.

func TestChunk(t *testing.T) {
	cases := []struct {
		name  string
		items []int
		size  int
		want  [][]int
	}{
		{"exact_multiple", []int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		{"remainder", []int{1, 2, 3, 4, 5}, 2, [][]int{{1, 2}, {3, 4}, {5}}},
		{"size_larger_than_input", []int{1, 2}, 10, [][]int{{1, 2}}},
		{"size_one", []int{1, 2, 3}, 1, [][]int{{1}, {2}, {3}}},
		{"empty", nil, 3, nil},
		{"zero_size_is_one_chunk", []int{1, 2, 3}, 0, [][]int{{1, 2, 3}}},
		{"negative_size_is_one_chunk", []int{1, 2, 3}, -5, [][]int{{1, 2, 3}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Chunk(tc.items, tc.size)
			if len(got) != len(tc.want) {
				t.Fatalf("Chunk = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if len(got[i]) != len(tc.want[i]) {
					t.Fatalf("Chunk = %v, want %v", got, tc.want)
				}
				for j := range tc.want[i] {
					if got[i][j] != tc.want[i][j] {
						t.Fatalf("Chunk = %v, want %v", got, tc.want)
					}
				}
			}
		})
	}
}

// Every item reaches the operation exactly once, whatever the chunk size.
func TestMapReduceCoversEveryItemExactlyOnce(t *testing.T) {
	for _, size := range []int{1, 3, 7, 25, 100} {
		t.Run(fmt.Sprintf("chunk_%d", size), func(t *testing.T) {
			items := make([]int, 50)
			for i := range items {
				items[i] = i
			}

			var mu = make(chan struct{}, 1)
			mu <- struct{}{}
			seen := map[int]int{}

			_, summary, err := MapReduce(context.Background(), items,
				MapReduceOptions{ChunkSize: size},
				func(_ context.Context, chunk []int) (int, error) {
					<-mu
					for _, item := range chunk {
						seen[item]++
					}
					mu <- struct{}{}
					return len(chunk), nil
				})
			if err != nil {
				t.Fatalf("MapReduce: %v", err)
			}

			if len(seen) != len(items) {
				t.Fatalf("saw %d distinct items, want %d", len(seen), len(items))
			}
			for item, count := range seen {
				if count != 1 {
					t.Errorf("item %d was seen %d times", item, count)
				}
			}

			wantChunks := (len(items) + size - 1) / size
			if summary.Chunks != wantChunks {
				t.Errorf("Chunks = %d, want %d", summary.Chunks, wantChunks)
			}
		})
	}
}

// Results come back in input order however the chunks finish, because a caller
// merging a sort or a filter needs the input's order to mean something.
func TestMapReduceReturnsResultsInInputOrder(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}

	results, _, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 2, Concurrency: 4},
		func(_ context.Context, chunk []int) (int, error) {
			// Later chunks finish first, so order cannot come from timing.
			time.Sleep(time.Duration(8-chunk[0]) * time.Millisecond)
			return chunk[0], nil
		})
	if err != nil {
		t.Fatalf("MapReduce: %v", err)
	}

	want := []int{0, 2, 4, 6}
	if len(results) != len(want) {
		t.Fatalf("results = %v, want %v", results, want)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("results = %v, want %v", results, want)
		}
	}
}

// A failing chunk fails the call by default: a partial result presented as a
// whole one is the failure this library exists to remove.
func TestMapReduceStopsOnTheFirstFailure(t *testing.T) {
	// Numbered, so the failing chunk is chosen by identity rather than by
	// whichever goroutine happened to run second.
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}
	var started int32

	_, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 4, Concurrency: 1},
		func(_ context.Context, chunk []int) (int, error) {
			atomic.AddInt32(&started, 1)
			if chunk[0]/4 == 1 {
				return 0, errors.New("chunk two failed")
			}
			return 0, nil
		})

	if err == nil {
		t.Fatal("a failing chunk must fail the call")
	}

	var chunkErr ChunkError
	if !errors.As(err, &chunkErr) {
		t.Fatalf("the error should identify the chunk, got %T: %v", err, err)
	}
	if chunkErr.Index != 1 {
		t.Errorf("Index = %d, want 1", chunkErr.Index)
	}
	if chunkErr.Offset != 4 {
		t.Errorf("Offset = %d, want 4 -- the caller needs to know which items are missing", chunkErr.Offset)
	}
	if summary.Complete() {
		t.Error("the summary reports a complete run after a failure")
	}

	// Some chunks are cancelled rather than all ten being run.
	if got := atomic.LoadInt32(&started); got == 10 {
		t.Error("every chunk ran; the failure should have cancelled the rest")
	}
}

// A failing chunk cancels the others, and a chunk that stops because of that
// returns the context's error. The one reported is the chunk that failed on its
// own, which is the cause, not the first one to return.
func TestMapReduceReportsTheFailureThatCausedTheCancellationNotTheCancellation(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := MapReduce(context.Background(), []int{0, 1}, MapReduceOptions{ChunkSize: 1, Concurrency: 2},
		func(ctx context.Context, chunk []int) (int, error) {
			if chunk[0] == 0 {
				<-ctx.Done()
				return 0, ctx.Err()
			}
			return 0, boom
		})
	var failed ChunkError
	if !errors.As(err, &failed) || failed.Index != 1 || !errors.Is(err, boom) {
		t.Errorf("err = %v; want the failure of chunk 1, not the cancellation of chunk 0", err)
	}
}

// ContinueOnError keeps going and says exactly what is missing.
func TestMapReduceContinueOnErrorReportsFailures(t *testing.T) {
	// Numbered so a chunk identifies itself by its contents. Keying failure on
	// invocation order would be keying it on goroutine scheduling.
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}

	results, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 5, ContinueOnError: true, Concurrency: 1},
		func(_ context.Context, chunk []int) (string, error) {
			chunkIndex := chunk[0] / 5
			if chunkIndex%2 == 1 {
				return "", fmt.Errorf("chunk %d failed", chunkIndex)
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("ContinueOnError must not return an error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("results = %v, want the two chunks that succeeded", results)
	}
	if summary.Complete() {
		t.Error("Complete() must be false when chunks failed")
	}
	if len(summary.Failed) != 2 {
		t.Fatalf("Failed = %+v, want two entries", summary.Failed)
	}
	if summary.Failed[0].Index != 1 || summary.Failed[1].Index != 3 {
		t.Errorf("Failed indices = %d, %d, want 1 and 3", summary.Failed[0].Index, summary.Failed[1].Index)
	}
}

// Concurrency is bounded, because the limit is usually the provider's rate
// limit rather than the machine. The bound is a ceiling and not a serial run:
// three chunks have to be in flight at once for any of them to finish, so a
// run that serialised them would hang here and fail on the deadline.
func TestMapReduceBoundsConcurrencyAndUsesIt(t *testing.T) {
	items := make([]int, 40)

	var inFlight, peak int32
	arrived := make(chan struct{}, 3)
	release := make(chan struct{})
	var once sync.Once

	_, _, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 1, Concurrency: 3},
		func(_ context.Context, chunk []int) (int, error) {
			current := atomic.AddInt32(&inFlight, 1)
			for {
				observed := atomic.LoadInt32(&peak)
				if current <= observed || atomic.CompareAndSwapInt32(&peak, observed, current) {
					break
				}
			}
			// The first three chunks wait for each other: none leaves until
			// all three are inside.
			select {
			case arrived <- struct{}{}:
				if len(arrived) == 3 {
					once.Do(func() { close(release) })
				}
				select {
				case <-release:
				case <-time.After(5 * time.Second):
					return 0, errors.New("the chunks did not overlap")
				}
			default:
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return 0, nil
		})
	if err != nil {
		t.Fatalf("MapReduce: %v", err)
	}

	if got := atomic.LoadInt32(&peak); got != 3 {
		t.Errorf("peak concurrency was %d, want exactly 3", got)
	}
}

// A cancelled caller context stops the run, and it is cancelled from inside the
// operation so that nothing depends on timing: the chunk that cancels returns
// at once, and no chunk after it is started.
func TestMapReduceHonoursCancellation(t *testing.T) {
	items := make([]int, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started int32

	_, _, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: 1},
		func(ctx context.Context, chunk []int) (int, error) {
			if atomic.AddInt32(&started, 1) == 2 {
				cancel()
			}
			return 0, ctx.Err()
		})

	if err == nil {
		t.Fatal("a cancelled run must report an error")
	}
	if got := atomic.LoadInt32(&started); got != 2 {
		t.Errorf("%d chunks started, want exactly the two that ran before the cancel took effect", got)
	}
}

// With ContinueOnError a cancelled caller context used to come back as a nil
// error beside a result missing the chunks that never ran.
func TestMapReduceWithContinueOnErrorStillReportsACancelledContext(t *testing.T) {
	items := make([]int, 50)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started int32
	results, summary, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: 1, ContinueOnError: true},
		func(ctx context.Context, chunk []int) (int, error) {
			if atomic.AddInt32(&started, 1) == 3 {
				cancel()
			}
			return 1, nil
		})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's cancellation", err)
	}
	if results != nil {
		t.Errorf("results = %v, want none beside the error", results)
	}
	if summary.Complete() || summary.Chunks != 50 {
		t.Errorf("summary = %+v, want the chunks that never ran reported as failed", summary)
	}
}

func TestMapReduceWithContinueOnErrorAndNoCancellationReturnsNoError(t *testing.T) {
	_, summary, err := MapReduce(context.Background(), make([]int, 6),
		MapReduceOptions{ChunkSize: 2, ContinueOnError: true},
		func(_ context.Context, chunk []int) (int, error) { return len(chunk), nil })
	if err != nil || !summary.Complete() {
		t.Errorf("err = %v, summary = %+v", err, summary)
	}
}

// With ContinueOnError the result is the successful chunks only, so result k is
// not chunk k; the positions are rebuilt from Failed.
func TestMapReduceCompactsResultsAndFailedGivesThePositions(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}
	results, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 2, Concurrency: 1, ContinueOnError: true},
		func(_ context.Context, chunk []int) (int, error) {
			if chunk[0] == 2 {
				return 0, errors.New("the second chunk failed")
			}
			return chunk[0], nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0] != 0 || results[1] != 4 || results[2] != 6 {
		t.Fatalf("results = %v, want the successful chunks in chunk order", results)
	}
	if len(summary.Failed) != 1 || summary.Failed[0].Index != 1 || summary.Failed[0].Offset != 2 {
		t.Fatalf("Failed = %+v, want chunk 1 at offset 2", summary.Failed)
	}
}

func TestMapReduceTreatsANilContextAsTheBackgroundContext(t *testing.T) {
	//lint:ignore SA1012 a nil context is the case under test
	results, _, err := MapReduce(nil, []int{1, 2, 3}, MapReduceOptions{ChunkSize: 1},
		func(ctx context.Context, chunk []int) (int, error) {
			if ctx == nil {
				return 0, errors.New("the operation was handed a nil context")
			}
			return chunk[0], nil
		})
	if err != nil || len(results) != 3 {
		t.Errorf("results = %v, err = %v", results, err)
	}
}

// A panic in the caller's operation fails its chunk. It does not stop the
// process, and nothing the panic carried is in the error.
func TestMapReduceRecoversAPanickingOperation(t *testing.T) {
	secret := "ssn 123-45-6789"
	results, summary, err := MapReduce(context.Background(), []int{0, 1, 2, 3},
		MapReduceOptions{ChunkSize: 1, Concurrency: 1, ContinueOnError: true},
		func(_ context.Context, chunk []int) (int, error) {
			if chunk[0] == 1 {
				panic(secret)
			}
			return chunk[0], nil
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 3 {
		t.Errorf("results = %v, want the three chunks that did not panic", results)
	}
	if len(summary.Failed) != 1 || summary.Failed[0].Index != 1 {
		t.Fatalf("Failed = %+v", summary.Failed)
	}
	if strings.Contains(summary.Failed[0].Error(), "123-45") {
		t.Errorf("the panic value is in the error: %v", summary.Failed[0])
	}

	// Without ContinueOnError the panic is the run's failure.
	_, _, err = MapReduce(context.Background(), []int{0, 1},
		MapReduceOptions{ChunkSize: 1, Concurrency: 1},
		func(_ context.Context, chunk []int) (int, error) { panic(secret) })
	if err == nil || strings.Contains(err.Error(), "123-45") {
		t.Errorf("err = %v", err)
	}
}

func TestMapReduceRefusesANilOperationAsAConfigurationError(t *testing.T) {
	_, _, err := MapReduce[int, int](context.Background(), []int{1}, MapReduceOptions{}, nil)
	wantKind(t, err, types.KindInvalidRequest)
}

// The degenerate inputs.
func TestMapReduceDegenerateInputs(t *testing.T) {
	t.Run("no_items", func(t *testing.T) {
		results, summary, err := MapReduce(context.Background(), []int(nil),
			MapReduceOptions{}, func(context.Context, []int) (int, error) {
				t.Error("the operation must not run for an empty input")
				return 0, nil
			})
		if err != nil || len(results) != 0 || summary.Chunks != 0 {
			t.Errorf("results=%v summary=%+v err=%v", results, summary, err)
		}
	})

	t.Run("no_operation", func(t *testing.T) {
		if _, _, err := MapReduce[int, int](context.Background(), []int{1}, MapReduceOptions{}, nil); err == nil {
			t.Error("a nil operation must be reported")
		}
	})

	t.Run("defaults_are_applied", func(t *testing.T) {
		items := make([]int, DefaultChunkSize*2)
		_, summary, err := MapReduce(context.Background(), items, MapReduceOptions{},
			func(context.Context, []int) (int, error) { return 0, nil })
		if err != nil {
			t.Fatalf("MapReduce: %v", err)
		}
		if summary.Chunks != 2 {
			t.Errorf("Chunks = %d, want 2 at the default chunk size", summary.Chunks)
		}
	})
}

// MapReduceFlat concatenates in input order, which is the common case.
func TestMapReduceFlat(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6}

	flattened, summary, err := MapReduceFlat(context.Background(), items,
		MapReduceOptions{ChunkSize: 2},
		func(_ context.Context, chunk []int) ([]int, error) {
			return chunk[:1], nil // keep the first of each chunk
		})
	if err != nil {
		t.Fatalf("MapReduceFlat: %v", err)
	}

	want := []int{1, 3, 5}
	if len(flattened) != len(want) {
		t.Fatalf("flattened = %v, want %v", flattened, want)
	}
	for i := range want {
		if flattened[i] != want[i] {
			t.Fatalf("flattened = %v, want %v", flattened, want)
		}
	}
	if !summary.Complete() {
		t.Error("a clean run should report complete")
	}
}

// ChunkError names the items a caller is missing.
func TestChunkErrorMessage(t *testing.T) {
	err := ChunkError{Index: 3, Offset: 75, Err: errors.New("provider unavailable")}

	message := err.Error()
	for _, want := range []string{"chunk 3", "75", "provider unavailable"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q: %s", want, message)
		}
	}
	if !errors.Is(err, err.Err) {
		t.Error("ChunkError does not unwrap to its cause")
	}
}

// Concurrency: 1 is documented as making the run sequential, and a caller asks
// for it because their provider is rate-limited. The semaphore version was
// serialized but not ordered -- it dispatched in whatever order the scheduler
// woke ten goroutines, so chunk 7 could run before chunk 0 -- and the ordering
// is the part that matters to the caller who asked.
func TestMapReduceSequentialRunsChunksInOrder(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}

	var mu sync.Mutex
	var seen []int

	_, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 4, Concurrency: 1},
		func(_ context.Context, chunk []int) (int, error) {
			mu.Lock()
			seen = append(seen, chunk[0])
			mu.Unlock()
			return chunk[0], nil
		})
	if err != nil {
		t.Fatalf("MapReduce: %v", err)
	}
	if !summary.Complete() {
		t.Fatalf("summary reports failures: %+v", summary.Failed)
	}

	for i, first := range seen {
		if want := i * 4; first != want {
			t.Fatalf("chunk %d started with item %d, want %d -- dispatch order is not the input order: %v",
				i, first, want, seen)
		}
	}
}

// The cancellation this test guards is only meaningful if the failing chunk is
// reached before the rest. Under the old dispatch, a failure in the last
// scheduled chunk cancelled nothing and every chunk was paid for.
func TestMapReduceSequentialFailureStopsTheRest(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}

	for _, failAt := range []int{0, 1, 5} {
		t.Run(fmt.Sprintf("chunk_%d_fails", failAt), func(t *testing.T) {
			var started int32

			_, summary, err := MapReduce(context.Background(), items,
				MapReduceOptions{ChunkSize: 4, Concurrency: 1},
				func(_ context.Context, chunk []int) (int, error) {
					atomic.AddInt32(&started, 1)
					if chunk[0]/4 == failAt {
						return 0, errors.New("this chunk failed")
					}
					return 0, nil
				})

			if err == nil {
				t.Fatal("a failing chunk must fail the call")
			}
			if summary.Complete() {
				t.Error("the summary reports a complete run after a failure")
			}

			// Sequential dispatch means the run stops at the failing chunk:
			// every chunk up to and including it ran, and nothing after it did.
			if got, want := int(atomic.LoadInt32(&started)), failAt+1; got != want {
				t.Errorf("%d chunks ran, want %d -- the failure did not stop the run", got, want)
			}
		})
	}
}

// A chunk the run never reached is reported as cancelled, not as a successful
// empty result. Reporting it as success is the failure this library exists to
// remove, one layer down.
func TestMapReduceUnreachedChunksAreNotReportedAsSuccess(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}

	values, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: 4, Concurrency: 1, ContinueOnError: true},
		func(ctx context.Context, chunk []int) (int, error) {
			if chunk[0] == 0 {
				return 0, errors.New("first chunk failed")
			}
			return chunk[0], nil
		})
	if err != nil {
		t.Fatalf("ContinueOnError should not fail the call: %v", err)
	}
	if summary.Complete() {
		t.Fatal("a failed chunk must be reported")
	}
	if len(summary.Failed) != 1 {
		t.Fatalf("Failed = %+v, want exactly the one chunk that failed", summary.Failed)
	}
	// ContinueOnError runs everything, so the other nine results are real.
	if len(values) != 9 {
		t.Errorf("got %d results, want 9", len(values))
	}
}

// An operation that ends its goroutine without returning (runtime.Goexit, as
// t.FailNow does) is an error for its chunk, not a worker that is gone for good:
// with one worker and ContinueOnError the chunks after it still run.
func TestMapReduceReportsAChunkThatEndsItsGoroutine(t *testing.T) {
	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("continue, %d workers", workers), func(t *testing.T) {
			items := make([]int, 10)
			for i := range items {
				items[i] = i
			}
			results, summary, err := MapReduce(context.Background(), items,
				MapReduceOptions{ChunkSize: 1, Concurrency: workers, ContinueOnError: true},
				func(_ context.Context, chunk []int) (int, error) {
					if chunk[0] == 3 {
						runtime.Goexit()
					}
					return chunk[0] * 10, nil
				})
			if err != nil {
				t.Fatalf("MapReduce: %v", err)
			}
			if len(summary.Failed) != 1 || summary.Failed[0].Index != 3 || types.KindOf(summary.Failed[0].Err) != types.KindInvariantViolation {
				t.Fatalf("failed = %+v, want chunk 3 as an invariant violation", summary.Failed)
			}
			if len(results) != 9 {
				t.Fatalf("%d results, want the 9 chunks that returned", len(results))
			}
		})
	}
	t.Run("stop on the first failure", func(t *testing.T) {
		items := make([]int, 10)
		_, summary, err := MapReduce(context.Background(), items,
			MapReduceOptions{ChunkSize: 1, Concurrency: 1},
			func(_ context.Context, chunk []int) (int, error) {
				runtime.Goexit()
				return 0, nil
			})
		if err == nil || types.KindOf(err) != types.KindInvariantViolation {
			t.Fatalf("error = %v, want an invariant violation", err)
		}
		if len(summary.Failed) != 10 {
			t.Errorf("%d chunks failed, want the one that ended and the nine that never ran", len(summary.Failed))
		}
	})
}

func TestMapReduceLeavesNoGoroutineBehindOver200Runs(t *testing.T) {
	before := chaosSettledGoroutines()
	items := make([]int, 12)
	for run := 0; run < 200; run++ {
		continueOnError := run%2 == 0
		_, _, _ = MapReduce(context.Background(), items,
			MapReduceOptions{ChunkSize: 2, Concurrency: 3, ContinueOnError: continueOnError},
			func(_ context.Context, chunk []int) (int, error) {
				switch run % 4 {
				case 1:
					panic("boom")
				case 2:
					runtime.Goexit()
				case 3:
					return 0, errors.New("failed")
				}
				return 1, nil
			})
	}
	if after := chaosSettledGoroutines(); after > before {
		t.Errorf("%d goroutines before 200 runs, %d after", before, after)
	}
}

// Two chunks fail with errors of their own, the second only after the first has
// cancelled the run. The error returned is the failure that stopped the run, the
// first in time, whichever of the two chunks has the lower index, and not the
// cancellation it caused in others.
func TestMapReduceReportsTheFailureThatStoppedTheRunNotTheLowestChunk(t *testing.T) {
	early := errors.New("failed first")
	late := errors.New("failed second")
	for _, earlyIndex := range []int{0, 1} {
		for name, run := range map[string]func(operation func(context.Context, []int) ([]int, error)) error{
			"MapReduce": func(operation func(context.Context, []int) ([]int, error)) error {
				_, _, err := MapReduce(context.Background(), []int{0, 1, 2, 3, 4, 5}, MapReduceOptions{ChunkSize: 1, Concurrency: 2}, operation)
				return err
			},
			"MapReduceFlat": func(operation func(context.Context, []int) ([]int, error)) error {
				_, _, err := MapReduceFlat(context.Background(), []int{0, 1, 2, 3, 4, 5}, MapReduceOptions{ChunkSize: 1, Concurrency: 2}, operation)
				return err
			},
		} {
			t.Run(fmt.Sprintf("%s, chunk %d fails first", name, earlyIndex), func(t *testing.T) {
				lateIndex := 1 - earlyIndex
				lateArrived := make(chan struct{})
				err := run(func(ctx context.Context, chunk []int) ([]int, error) {
					switch chunk[0] {
					case lateIndex:
						close(lateArrived)
						<-ctx.Done()
						return nil, late
					case earlyIndex:
						<-lateArrived
						return nil, early
					}
					return chunk, nil
				})
				var failed ChunkError
				if !errors.As(err, &failed) {
					t.Fatalf("err = %v, want a ChunkError", err)
				}
				if failed.Index != earlyIndex || !errors.Is(err, early) || errors.Is(err, late) {
					t.Errorf("err = %v; want chunk %d, the failure that stopped the run", err, earlyIndex)
				}
			})
		}
	}
}

// A run whose caller gave up before anything started reports the cancellation,
// and still reports the chunks that never ran as failed.
func TestMapReduceOnAContextAlreadyCancelledStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int32
	results, summary, err := MapReduce(ctx, make([]int, 6), MapReduceOptions{ChunkSize: 2},
		func(context.Context, []int) (int, error) {
			ran.Add(1)
			return 0, nil
		})
	if ran.Load() != 0 {
		t.Errorf("%d chunks ran on a cancelled context", ran.Load())
	}
	if !errors.Is(err, context.Canceled) || results != nil {
		t.Errorf("results = %v, err = %v; want none and the cancellation", results, err)
	}
	if len(summary.Failed) != 3 || summary.Chunks != 3 {
		t.Errorf("summary = %+v, want the three chunks that never ran as failed", summary)
	}
}

func TestChunkOfAnySizeIsOneChunkWhenTheSizeIsAtLeastTheLengthAndNeverOverflows(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	sizes := []int{5, 6, 1 << 20, 1 << 40, math.MaxInt/2 + 1, math.MaxInt - 1, math.MaxInt, 0, -1, math.MinInt}
	for _, size := range sizes {
		chunks := Chunk(items, size)
		if len(chunks) != 1 || len(chunks[0]) != 5 {
			t.Errorf("Chunk(5 items, %d) = %v, want one chunk of all five", size, chunks)
		}
	}
	for size, want := range map[int]int{1: 5, 2: 3, 3: 2, 4: 2} {
		chunks := Chunk(items, size)
		if len(chunks) != want {
			t.Errorf("Chunk(5 items, %d) made %d chunks, want %d", size, len(chunks), want)
		}
		total := 0
		for _, chunk := range chunks {
			total += len(chunk)
		}
		if total != 5 {
			t.Errorf("Chunk(5 items, %d) holds %d items, want 5", size, total)
		}
	}
	if got := Chunk([]int(nil), math.MaxInt); got != nil {
		t.Errorf("Chunk of nothing = %v", got)
	}
}

func TestChunkOfAListOfZeroSizeItemsLongerThanHalfTheRangeDoesNotWrapAnOffset(t *testing.T) {
	// A slice of zero-size elements can be as long as the range of int: no memory
	// is held for it, and a sum of a start and a size that passes the end of the
	// range would wrap round to a negative offset.
	items := make([]struct{}, math.MaxInt-3)
	chunks := Chunk(items, math.MaxInt/2+2)
	if len(chunks) != 2 || len(chunks[0])+len(chunks[1]) != len(items) {
		t.Fatalf("chunks = %d, lengths do not add up to the list", len(chunks))
	}
}

func TestMapReduceWithAChunkSizeOfAnyMagnitudeRunsEveryItemOnce(t *testing.T) {
	for _, size := range []int{math.MaxInt, math.MaxInt - 1, 1 << 40, 7, 3, 1} {
		results, summary, err := MapReduce(context.Background(), []int{1, 2, 3, 4, 5, 6, 7}, MapReduceOptions{ChunkSize: size},
			func(_ context.Context, chunk []int) (int, error) { return len(chunk), nil })
		if err != nil || len(summary.Failed) != 0 {
			t.Fatalf("size %d: %v, %+v", size, err, summary)
		}
		total := 0
		for _, n := range results {
			total += n
		}
		if total != 7 {
			t.Errorf("size %d: the chunks held %d items, want 7", size, total)
		}
	}
}
