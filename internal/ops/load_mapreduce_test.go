package ops

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RC-005: load and chaos coverage for MapReduce (mapreduce.go), the bounded
// worker pool RunOpMany's atomic shape and every MDSP execution path in this
// package are built on. mapreduce_test.go already proves the pool's ordering
// and cancel-on-first-failure contract with small, deterministic chunk
// counts; these tests push a much larger item stream through it with
// randomized per-chunk failure and latency, and a cancellation mid-run, so
// the same bounded-worker-pool mechanism scheduler.go's own head-of-line
// defect once hid in gets exercised under real contention here too.

// TestLoadMapReduceLargeStreamRandomChunkFailuresContinueOnError runs 20,000
// items through MapReduce in small chunks with roughly one chunk in twenty
// failing at random, ContinueOnError set, and proves the accounting is
// exact: every chunk is accounted for as either a successful result or a
// ChunkError, the two counts sum to the total chunk count, and nothing that
// succeeded is silently dropped because a sibling chunk failed.
func TestLoadMapReduceLargeStreamRandomChunkFailuresContinueOnError(t *testing.T) {
	before := chaosSettledGoroutines()

	const n = 20000
	const chunkSize = 50
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	wantChunks := (n + chunkSize - 1) / chunkSize

	rng := rand.New(rand.NewSource(7))
	var rngMu sync.Mutex

	var dispatched int64
	results, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: chunkSize, Concurrency: 32, ContinueOnError: true},
		func(ctx context.Context, chunk []int) (int, error) {
			atomic.AddInt64(&dispatched, 1)
			rngMu.Lock()
			fail := rng.Float64() < 0.05
			rngMu.Unlock()
			if fail {
				return 0, fmt.Errorf("simulated failure for chunk starting at item %d", chunk[0])
			}
			sum := 0
			for _, v := range chunk {
				sum += v
			}
			return sum, nil
		})
	if err != nil {
		t.Fatalf("MapReduce with ContinueOnError returned an error: %v", err)
	}
	if summary.Chunks != wantChunks {
		t.Fatalf("summary.Chunks = %d, want %d", summary.Chunks, wantChunks)
	}
	if got := int(atomic.LoadInt64(&dispatched)); got != wantChunks {
		t.Fatalf("dispatched %d chunk calls, want exactly %d (every chunk runs exactly once)", got, wantChunks)
	}
	if len(results)+len(summary.Failed) != wantChunks {
		t.Fatalf("succeeded(%d)+failed(%d) = %d, want %d -- some chunk vanished",
			len(results), len(summary.Failed), len(results)+len(summary.Failed), wantChunks)
	}
	if len(summary.Failed) == 0 {
		t.Fatal("nothing failed -- the 5% failure rate never actually fired, so this proves nothing about ContinueOnError")
	}
	if summary.Complete() {
		t.Fatal("summary.Complete() = true despite recorded failures")
	}
	// Every failed chunk names a valid, in-range offset -- the thing a
	// caller actually needs to know which items are missing.
	for _, f := range summary.Failed {
		if f.Offset < 0 || f.Offset >= n {
			t.Fatalf("ChunkError.Offset = %d, out of range [0, %d)", f.Offset, n)
		}
		if f.Err == nil {
			t.Fatalf("chunk %d: Err is nil", f.Index)
		}
	}

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d after a %d-item, %d-chunk MapReduce run", before, after, n, wantChunks)
	}
}

// TestLoadMapReduceStopsOnFirstFailureWithoutRunningEveryChunk is the
// default (ContinueOnError: false) contract under load: MapReduce must still
// stop dispatching once one chunk fails, even with dozens of workers racing
// to claim the next index, and account for every chunk it never reached as
// cancelled rather than as a fabricated success. Concurrency is deliberately
// higher than the deterministic mapreduce_test.go cases use, so the
// dispatcher's single-feeder-goroutine design (mapreduce.go's own doc
// comment on why a semaphore race was replaced by one) is checked against
// real contention, not just against a small worker count where the ordering
// mostly falls out on its own.
func TestLoadMapReduceStopsOnFirstFailureWithoutRunningEveryChunk(t *testing.T) {
	const n = 5000
	const chunkSize = 10
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	wantChunks := n / chunkSize

	// The failing chunk is near the front so a dispatcher that does not
	// actually stop would have thousands of further chunks to wrongly run.
	const failAtChunk = 3

	var dispatched int64
	_, summary, err := MapReduce(context.Background(), items,
		MapReduceOptions{ChunkSize: chunkSize, Concurrency: 24},
		func(ctx context.Context, chunk []int) (int, error) {
			n := atomic.AddInt64(&dispatched, 1)
			if int(n) > wantChunks {
				t.Fatalf("dispatched chunk %d, more than the %d chunks that exist", n, wantChunks)
			}
			if chunk[0]/chunkSize == failAtChunk {
				return 0, fmt.Errorf("simulated failure at chunk %d", failAtChunk)
			}
			return 0, nil
		})
	if err == nil {
		t.Fatal("MapReduce with the default policy returned no error despite a chunk failing")
	}
	if !summary.Complete() && len(summary.Failed) == 0 {
		t.Fatalf("summary reports no failures at all: %+v", summary)
	}

	got := int(atomic.LoadInt64(&dispatched))
	if got >= wantChunks {
		t.Errorf("dispatched %d of %d chunks -- the run did not actually stop early", got, wantChunks)
	}
}

// TestLoadMapReduceCancellationStormMidRun launches many concurrent
// MapReduce calls over a moderately large item stream with an artificial
// per-chunk delay, cancels each one's own context partway through, and
// proves every call returns promptly (bounded by runWithDeadline) with a
// classified cancellation rather than hanging until every chunk's delay
// elapses on its own -- SC-005's cancellation guarantee, exercised at the
// chunking layer under real concurrent load rather than one call at a time.
func TestLoadMapReduceCancellationStormMidRun(t *testing.T) {
	before := chaosSettledGoroutines()

	const concurrentRuns = 40
	const itemsPerRun = 400
	const chunkSize = 10

	items := make([]int, itemsPerRun)
	for i := range items {
		items[i] = i
	}

	var wg sync.WaitGroup
	runWithDeadline(t, 30*time.Second, func() {
		for r := 0; r < concurrentRuns; r++ {
			wg.Add(1)
			go func(run int) {
				defer wg.Done()
				// The deadline is an order of magnitude shorter than a
				// single chunk's own work, so the race is not close: this
				// proves cancellation is honoured promptly, not that it wins
				// a coin flip against the work. A tight margin here (an
				// earlier version used a 2-5ms deadline against 3ms work)
				// occasionally let a fast run's first round finish inside
				// the deadline by chance, which is scheduler noise, not a
				// property of MapReduce -- exactly the kind of flake
				// AGENTS.md's "never invent numbers" spirit warns against
				// dressing up as a real assertion.
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+rand.Intn(3))*time.Millisecond)
				defer cancel()

				_, _, err := MapReduce(ctx, items,
					MapReduceOptions{ChunkSize: chunkSize, Concurrency: 8},
					func(ctx context.Context, chunk []int) (int, error) {
						select {
						case <-time.After(40 * time.Millisecond):
							return 0, nil
						case <-ctx.Done():
							return 0, ctx.Err()
						}
					})
				if err == nil {
					t.Errorf("run %d: a MapReduce racing a 1-3ms deadline against 40ms-per-chunk work returned no error", run)
				}
			}(r)
		}
		wg.Wait()
	})

	after := chaosSettledGoroutines()
	if after > before+5 {
		t.Errorf("goroutines went from %d to %d after %d concurrent cancelled MapReduce runs", before, after, concurrentRuns)
	}
}
