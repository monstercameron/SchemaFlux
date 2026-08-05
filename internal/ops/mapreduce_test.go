package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
// limit rather than the machine.
func TestMapReduceBoundsConcurrency(t *testing.T) {
	items := make([]int, 40)

	var inFlight, peak int32

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
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return 0, nil
		})
	if err != nil {
		t.Fatalf("MapReduce: %v", err)
	}

	if got := atomic.LoadInt32(&peak); got > 3 {
		t.Errorf("peak concurrency was %d, want at most 3", got)
	}
}

// A cancelled caller context stops the run.
func TestMapReduceHonoursCancellation(t *testing.T) {
	items := make([]int, 100)

	ctx, cancel := context.WithCancel(context.Background())
	var started int32

	go func() {
		for atomic.LoadInt32(&started) < 2 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	_, _, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: 1},
		func(ctx context.Context, chunk []int) (int, error) {
			atomic.AddInt32(&started, 1)
			time.Sleep(2 * time.Millisecond)
			return 0, ctx.Err()
		})

	if err == nil {
		t.Fatal("a cancelled run must report an error")
	}
	if got := atomic.LoadInt32(&started); got > 20 {
		t.Errorf("%d chunks ran after cancellation", got)
	}
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
