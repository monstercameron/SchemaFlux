package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// This file covers the root package's retry/control-flow helpers: When,
// RetryAfterFrom, MapReduceFlat, Chunk, Guard, DecomposeToSlice, and
// NormalizeBatch.

// --- When --------------------------------------------------------------

var errBillingDown = errors.New("billing service unreachable")

// When must build a Case whose condition matches by error TYPE (ops.caseMatches
// compares reflect.TypeOf(cond) == reflect.TypeOf(input)), so the wrapper has
// to hand the condition through to ops.When unchanged rather than wrapping or
// stringifying it.
func TestWhenBuildsAnErrorTypeCase(t *testing.T) {
	ran := false
	matched, err := schemaflux.Match(context.Background(), errBillingDown,
		schemaflux.When(errBillingDown, func() { ran = true }),
		schemaflux.Otherwise(func() { t.Error("the default case must not run") }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !ran {
		t.Errorf("matched=%v ran=%v, want the error-typed case to run", matched, ran)
	}
}

// A When whose condition type does not match the input falls through to the
// default, proving the condition was not silently accepted as "always match".
func TestWhenDoesNotMatchADifferentType(t *testing.T) {
	defaulted := false
	matched, err := schemaflux.Match(context.Background(), "a string input",
		schemaflux.When(errBillingDown, func() { t.Error("the error case must not run for a string input") }),
		schemaflux.Otherwise(func() { defaulted = true }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !defaulted {
		t.Errorf("matched=%v defaulted=%v, want the default case to run", matched, defaulted)
	}
}

// --- RetryAfterFrom ------------------------------------------------------

func TestRetryAfterFromRecoversTheWaitThroughWrapping(t *testing.T) {
	base := &llm.RateLimitError{
		APIError:   &llm.APIError{Provider: "openai", StatusCode: 429},
		RetryAfter: 17 * time.Second,
	}
	wrapped := errors.Join(errors.New("request failed"), base)

	wait, ok := schemaflux.RetryAfterFrom(wrapped)
	if !ok {
		t.Fatal("RetryAfterFrom did not recognise a wrapped RateLimitError")
	}
	if wait != 17*time.Second {
		t.Errorf("wait = %s, want 17s", wait)
	}
}

// A plain error is not a rate limit -- RetryAfterFrom must not invent a wait.
func TestRetryAfterFromReportsFalseForAnOrdinaryError(t *testing.T) {
	_, ok := schemaflux.RetryAfterFrom(errors.New("some other failure"))
	if ok {
		t.Error("RetryAfterFrom claimed a wait for a non-rate-limit error")
	}
}

// --- Chunk -----------------------------------------------------------------

func TestChunkSplitsIntoRunsOfAtMostSize(t *testing.T) {
	got := schemaflux.Chunk([]int{1, 2, 3, 4, 5, 6, 7}, 3)
	want := [][]int{{1, 2, 3}, {4, 5, 6}, {7}}
	if len(got) != len(want) {
		t.Fatalf("got %d chunks, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("chunk %d = %v, want %v", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("chunk %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestChunkOfEmptySliceIsEmpty(t *testing.T) {
	got := schemaflux.Chunk([]int{}, 5)
	if len(got) != 0 {
		t.Errorf("got %v, want no chunks for an empty input", got)
	}
}

// --- MapReduceFlat -----------------------------------------------------------

// MapReduceFlat must reach ops.MapReduceFlat with the caller's ChunkSize and
// operation closure, concatenating each chunk's slice in input order.
func TestMapReduceFlatConcatenatesChunksInOrder(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	var chunksSeen [][]int

	out, summary, err := schemaflux.MapReduceFlat(context.Background(), items,
		schemaflux.MapReduceOptions{ChunkSize: 2, Concurrency: 1},
		func(_ context.Context, chunk []int) ([]int, error) {
			chunksSeen = append(chunksSeen, append([]int(nil), chunk...))
			doubled := make([]int, len(chunk))
			for i, v := range chunk {
				doubled[i] = v * 2
			}
			return doubled, nil
		})
	if err != nil {
		t.Fatalf("MapReduceFlat: %v", err)
	}
	want := []int{2, 4, 6, 8, 10}
	if len(out) != len(want) {
		t.Fatalf("out = %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("out[%d] = %d, want %d", i, out[i], want[i])
		}
	}
	if len(chunksSeen) != 3 {
		t.Fatalf("chunk sizes forwarded incorrectly: saw %d chunks, want 3 (2,2,1)", len(chunksSeen))
	}
	if summary.Chunks != 3 {
		t.Errorf("summary.Chunks = %d, want 3", summary.Chunks)
	}
}

// A chunk failure is reported rather than silently dropped from the flattened
// result, since MapReduceFlat has no way to know the caller's default value.
func TestMapReduceFlatChunkFailureIsReported(t *testing.T) {
	items := []int{1, 2, 3, 4}
	failing := errors.New("chunk exploded")

	_, _, err := schemaflux.MapReduceFlat(context.Background(), items,
		schemaflux.MapReduceOptions{ChunkSize: 2, Concurrency: 1},
		func(_ context.Context, chunk []int) ([]int, error) {
			if chunk[0] == 3 {
				return nil, failing
			}
			return chunk, nil
		})
	if err == nil {
		t.Fatal("expected the chunk failure to surface")
	}
}

// --- Guard -----------------------------------------------------------------

// Guard is a pure Go check with no provider call -- it evaluates the caller's
// predicates in order and reports every one that failed.
func TestGuardEvaluatesAllChecksAndReportsFailures(t *testing.T) {
	type state struct{ Balance int }

	result := schemaflux.Guard(context.Background(), state{Balance: -5},
		func(s state) (bool, string) { return s.Balance >= 0, "balance must not be negative" },
		func(s state) (bool, string) { return s.Balance < 1000, "balance under the cap" },
	)
	if result.CanProceed {
		t.Error("CanProceed = true, want false since one check failed")
	}
	if len(result.FailedChecks) != 1 || result.FailedChecks[0] != "balance must not be negative" {
		t.Errorf("FailedChecks = %v, want exactly the one failing message", result.FailedChecks)
	}
}

func TestGuardAllChecksPassingProceeds(t *testing.T) {
	type state struct{ Balance int }

	result := schemaflux.Guard(context.Background(), state{Balance: 5},
		func(s state) (bool, string) { return s.Balance >= 0, "" },
	)
	if !result.CanProceed {
		t.Errorf("CanProceed = false, want true: %v", result.FailedChecks)
	}
	if len(result.FailedChecks) != 0 {
		t.Errorf("FailedChecks = %v, want none", result.FailedChecks)
	}
}

// --- DecomposeToSlice --------------------------------------------------------

func TestDecomposeToSliceReturnsScriptedParts(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"parts":["design the schema","write the migration"],"root_parts":["design the schema","write the migration"]}`, nil)

	parts, err := schemaflux.DecomposeToSlice[string, string]("ship the feature", schemaflux.NewDecomposeOptions())
	if err != nil {
		t.Fatalf("DecomposeToSlice: %v", err)
	}
	if len(parts) != 2 || parts[0] != "design the schema" || parts[1] != "write the migration" {
		t.Errorf("parts = %v, want the two scripted parts in order", parts)
	}
}

func TestDecomposeToSliceProviderFailureIsAnError(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("provider unavailable"))

	_, err := schemaflux.DecomposeToSlice[string, string]("ship the feature", schemaflux.NewDecomposeOptions())
	if err == nil {
		t.Fatal("expected an error when the provider is unreachable")
	}
}

// --- NormalizeBatch ----------------------------------------------------------

func TestNormalizeBatchNormalizesEveryItemInOrder(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"normalized":"NORMALIZED","changes":[]}`, nil)

	results, err := schemaflux.NormalizeBatch([]string{"a", "b", "c"}, schemaflux.NewNormalizeOptions())
	if err != nil {
		t.Fatalf("NormalizeBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3 (one per item)", len(results))
	}
	for i, r := range results {
		if r.Normalized != "NORMALIZED" {
			t.Errorf("result[%d].Normalized = %q, want the scripted value", i, r.Normalized)
		}
	}
}

func TestNormalizeBatchEmptyInputMakesNoProviderCall(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "", errors.New("must not be called"))

	results, err := schemaflux.NormalizeBatch([]string{}, schemaflux.NewNormalizeOptions())
	if err != nil {
		t.Fatalf("NormalizeBatch: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for an empty input", len(results))
	}
}
