package flow_test

import (
	"context"
	"math"
	"runtime"
	"strconv"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// extremes are the integers a caller can pass that a size, a count or a bound is
// most likely to mishandle: both ends of the range, the values next to them,
// zero, minus one and a figure that is large but not at the end.
var extremes = []int{
	math.MaxInt, math.MaxInt - 1, math.MaxInt/2 + 1, math.MaxInt / 2, 1 << 40, 1 << 31, 1 << 16, 1 << 10,
	2, 1, 0, -1, -2, math.MinInt + 1, math.MinInt,
}

// allocatedBy reports how many bytes f allocated, which is how a test tells that a
// call sized something by a number it was given.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// noPanic runs f and fails the test when it panics: a number a caller passes is
// refused or handled, and never takes the process down.
func noPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

func TestMapReduceTakesAChunkSizeOfAnySizeAndNeverSizesAnythingByIt(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	sum := func(_ context.Context, chunk []int) (int, error) {
		total := 0
		for _, n := range chunk {
			total += n
		}
		return total, nil
	}
	for _, size := range extremes {
		t.Run(sizeName(size), func(t *testing.T) {
			noPanic(t, "MapReduce", func() {
				var results []int
				var run flow.MapReduceResult
				var err error
				bytes := allocatedBy(func() {
					results, run, err = flow.MapReduce(context.Background(), items, sum, flow.MapReduceOptions{ChunkSize: size})
				})
				if size < 0 {
					wantKind(t, err, schemaflux.ErrConfiguration)
					if results != nil || run.Chunks != 0 {
						t.Errorf("results = %v, chunks = %d beside an error", results, run.Chunks)
					}
					return
				}
				if err != nil {
					t.Fatalf("MapReduce = %v", err)
				}
				wantChunks := (len(items) + max(1, min(size, len(items))) - 1) / max(1, min(size, len(items)))
				if size == 0 {
					wantChunks = 1 // the default of 25 holds all five items
				}
				if run.Chunks != wantChunks || len(results) != wantChunks || !run.Complete() {
					t.Errorf("chunks = %d, results = %v, want %d chunks", run.Chunks, results, wantChunks)
				}
				total := 0
				for _, n := range results {
					total += n
				}
				if total != 15 {
					t.Errorf("the chunks add up to %d, want 15: an item was lost or repeated", total)
				}
				if bytes > 1<<20 {
					t.Errorf("a chunk size of %d made the call allocate %d bytes", size, bytes)
				}
			})
		})
	}
}

func TestMapReduceTakesAConcurrencyOfAnySizeAndStartsNoMoreWorkersThanThereAreChunks(t *testing.T) {
	items := []int{1, 2, 3}
	count := func(_ context.Context, chunk []int) (int, error) { return len(chunk), nil }
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "MapReduce", func() {
				var err error
				var results []int
				bytes := allocatedBy(func() {
					results, _, err = flow.MapReduce(context.Background(), items, count,
						flow.MapReduceOptions{ChunkSize: 1, Concurrency: n})
				})
				if n < 0 {
					wantKind(t, err, schemaflux.ErrConfiguration)
					return
				}
				if err != nil || len(results) != 3 {
					t.Fatalf("MapReduce = %v, %v", results, err)
				}
				if bytes > 1<<20 {
					t.Errorf("a Concurrency of %d made the call allocate %d bytes", n, bytes)
				}
			})
		})
	}
}

func TestMapReduceOfNoItemsIgnoresTheNumbersItIsGiven(t *testing.T) {
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "MapReduce", func() {
				results, run, err := flow.MapReduce(context.Background(), []int(nil),
					func(context.Context, []int) (int, error) { return 0, nil },
					flow.MapReduceOptions{ChunkSize: n, Concurrency: n})
				if n < 0 {
					wantKind(t, err, schemaflux.ErrConfiguration)
					return
				}
				if err != nil || len(results) != 0 || run.Chunks != 0 {
					t.Errorf("MapReduce of nothing = %v, %+v, %v", results, run, err)
				}
			})
		})
	}
}

func TestVoteRefusesANumberOfSamplesNobodyMeansAndSizesNothingByIt(t *testing.T) {
	step := returns("a")
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "Vote", func() {
				var err error
				var record flow.VoteRecord
				var got string
				bytes := allocatedBy(func() {
					got, record, err = flow.Vote(context.Background(), n, step, flow.ExactAgreement[string](1))
				})
				if n < 1 || n > 64 {
					e := wantKind(t, err, schemaflux.ErrInvalidRequest)
					if e.Op != "flow.vote" {
						t.Errorf("Op = %q", e.Op)
					}
					if got != "" || record != (flow.VoteRecord{}) {
						t.Errorf("Vote = %q, %+v beside an error", got, record)
					}
					if bytes > 1<<16 {
						t.Errorf("refusing %d samples allocated %d bytes", n, bytes)
					}
					return
				}
				if err != nil || got != "a" || record.Requested != n || record.Succeeded != n {
					t.Errorf("Vote = %q, %+v, %v", got, record, err)
				}
			})
		})
	}
}

func TestVoteNamesTheMaximumNumberOfSamplesWithoutTheValueGiven(t *testing.T) {
	_, _, err := flow.Vote(context.Background(), 1<<40, returns("a"), flow.ExactAgreement[string](1))
	e := wantKind(t, err, schemaflux.ErrInvalidRequest)
	if e.Error() == "" || strings.Contains(e.Error(), "1099511627776") {
		t.Errorf("the error repeats the number given: %v", e)
	}
	for _, want := range []string{"64"} {
		if !strings.Contains(e.Error(), want) {
			t.Errorf("the error does not state the maximum %s: %v", want, e)
		}
	}
}

func TestVoteTakesAConcurrencyOfAnySize(t *testing.T) {
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "Vote", func() {
				var err error
				var got string
				bytes := allocatedBy(func() {
					got, _, err = flow.Vote(context.Background(), 3, returns("a"), flow.ExactAgreement[string](1), flow.VoteOptions{Concurrency: n})
				})
				if n < 0 {
					wantKind(t, err, schemaflux.ErrConfiguration)
					return
				}
				if err != nil || got != "a" {
					t.Fatalf("Vote = %q, %v", got, err)
				}
				if bytes > 1<<20 {
					t.Errorf("a Concurrency of %d made the call allocate %d bytes", n, bytes)
				}
			})
		})
	}
}

func TestUntilRefusesANumberOfAttemptsNobodyMeansAndRunsNothing(t *testing.T) {
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "Until", func() {
				var runs int
				step := func(context.Context) (int, error) { runs++; return 1, nil }
				got, made, err := flow.Until(context.Background(), step, func(int) bool { return true }, n)
				if n < 1 || n > 1000 {
					wantKind(t, err, schemaflux.ErrInvalidRequest)
					if got != 0 || made != 0 || runs != 0 {
						t.Errorf("Until = %d, %d after %d runs beside an error", got, made, runs)
					}
					return
				}
				if err != nil || got != 1 || made != 1 {
					t.Errorf("Until = %d, %d, %v", got, made, err)
				}
			})
		})
	}
}

func TestUntilWithTheMostAttemptsItAllowsStopsWhenTheyRunOut(t *testing.T) {
	var runs int
	step := func(context.Context) (int, error) { runs++; return 0, nil }
	_, made, err := flow.Until(context.Background(), step, func(int) bool { return false }, 1000)
	wantKind(t, err, schemaflux.ErrInvalidAnswer)
	if made != 1000 || runs != 1000 {
		t.Errorf("made %d attempts and ran %d times, want 1000", made, runs)
	}
}

func TestExactAgreementTakesAMinimumOfAnySize(t *testing.T) {
	samples := []string{"a", "a", "b"}
	for _, n := range extremes {
		t.Run(sizeName(n), func(t *testing.T) {
			noPanic(t, "ExactAgreement", func() {
				outcome, err := flow.ExactAgreement[string](n)(context.Background(), samples)
				if n < 1 {
					wantKind(t, err, schemaflux.ErrInvalidRequest)
					return
				}
				if err != nil {
					t.Fatalf("ExactAgreement(%d) = %v", n, err)
				}
				if outcome.Abstain != (n > 2) {
					t.Errorf("Abstain = %v with 2 of 3 samples agreeing and a minimum of %d", outcome.Abstain, n)
				}
			})
		})
	}
}

func sizeName(n int) string {
	switch n {
	case math.MaxInt:
		return "MaxInt"
	case math.MinInt:
		return "MinInt"
	}
	return "n=" + strconv.Itoa(n)
}
