package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Direct unit tests for OP-101 (stable invocation-local ids) and OP-106
// (scoring promoted to the primary strategy above sortScoringThreshold, with
// bounded concurrency and strategy reporting). collection_identity_test.go
// and the root integration/property tests exercise the same protocol through
// Choose/Filter/Sort; these exercise the helpers and the strategy selection
// directly.

// itemID is one-based and zero-padded to six digits, so the tenth item does
// not collide with the first on string-prefix comparisons and the format is
// stable regardless of how many items there are.
func TestItemIDFormatsOneBasedSixDigits(t *testing.T) {
	cases := []struct {
		position int
		want     string
	}{
		{0, "i-000001"},
		{1, "i-000002"},
		{8, "i-000009"},
		{9, "i-000010"},
		{99, "i-000100"},
		{999999, "i-1000000"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := itemID(tc.position); got != tc.want {
				t.Errorf("itemID(%d) = %q, want %q", tc.position, got, tc.want)
			}
		})
	}
}

// itemIDs assigns one id per item, in order, and idPositions inverts it.
func TestItemIDsAndIDPositionsRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 5, 12} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			ids := itemIDs(n)
			if len(ids) != n {
				t.Fatalf("itemIDs(%d) returned %d ids", n, len(ids))
			}
			positions := idPositions(ids)
			for i, id := range ids {
				if positions[id] != i {
					t.Errorf("idPositions[%q] = %d, want %d", id, positions[id], i)
				}
			}
		})
	}
}

// tagItems pairs each item with its assigned id, in input order -- what the
// model is shown never includes the item's fields in the answer channel.
func TestTagItemsPairsIDAndItem(t *testing.T) {
	items := catalogue()
	tagged := tagItems(items)

	if len(tagged) != len(items) {
		t.Fatalf("tagItems returned %d entries for %d items", len(tagged), len(items))
	}
	for i, entry := range tagged {
		if entry.ID != itemID(i) {
			t.Errorf("tagged[%d].ID = %q, want %q", i, entry.ID, itemID(i))
		}
		if entry.Item != items[i] {
			t.Errorf("tagged[%d].Item = %+v, want %+v", i, entry.Item, items[i])
		}
	}
}

// resolveSubsetByIDs is Filter's coverage check: a subset of assigned ids,
// reconstructed in the caller's own input order regardless of the order the
// ids were answered in.
func TestResolveSubsetByIDs(t *testing.T) {
	items := catalogue() // p1, p2, p3

	cases := []struct {
		name    string
		ids     []string
		wantErr bool
		want    []product
	}{
		{"empty_answer_is_the_empty_subset", []string{}, false, []product{}},
		{"full_answer_in_order", itemIDs(3), false, items},
		{"out_of_order_answer_reconstructs_input_order",
			[]string{itemID(2), itemID(0)}, false, []product{items[0], items[2]}},
		{"single_id", []string{itemID(1)}, false, []product{items[1]}},
		{"unassigned_id_is_refused", []string{"i-000009"}, true, nil},
		{"duplicated_id_is_refused", []string{itemID(0), itemID(0)}, true, nil},
		{"domain_value_standing_in_for_an_id_is_refused", []string{"p1"}, true, nil},
		{"more_ids_than_items_is_refused",
			[]string{itemID(0), itemID(1), itemID(2), itemID(0)}, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSubsetByIDs(items, tc.ids)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveSubsetByIDs(%v) = %+v, want an error", tc.ids, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSubsetByIDs(%v): %v", tc.ids, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d items, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// resolvePermutationByIDs is Sort's coverage check: every assigned id present
// exactly once, reconstructed in the order the ids were answered in -- that
// order is the sorted order.
func TestResolvePermutationByIDs(t *testing.T) {
	items := catalogue() // p1, p2, p3

	cases := []struct {
		name    string
		ids     []string
		wantErr bool
		want    []product
	}{
		{"identity_order", itemIDs(3), false, items},
		{"reversed", []string{itemID(2), itemID(1), itemID(0)},
			false, []product{items[2], items[1], items[0]}},
		{"missing_an_id_is_refused", []string{itemID(0), itemID(1)}, true, nil},
		{"duplicated_id_is_refused",
			[]string{itemID(0), itemID(0), itemID(2)}, true, nil},
		{"unassigned_id_is_refused",
			[]string{itemID(0), itemID(1), "i-000009"}, true, nil},
		{"too_many_ids_is_refused",
			[]string{itemID(0), itemID(1), itemID(2), itemID(0)}, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePermutationByIDs(items, tc.ids)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolvePermutationByIDs(%v) = %+v, want an error", tc.ids, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePermutationByIDs(%v): %v", tc.ids, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d items, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// OP-106: sortByScoringFallback runs one call per item, and no more than
// DefaultConcurrency at a time -- the bounded worker pool MapReduce already
// implements, reused rather than a second copy of it (CF-009).
func TestSortByScoringFallbackBoundedConcurrency(t *testing.T) {
	items := make([]string, 20)
	for i := range items {
		items[i] = fmt.Sprintf("item-%02d", i)
	}

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		calls    int
	)

	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		mu.Lock()
		calls++
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()

		// Long enough that concurrent calls actually overlap in the goroutine
		// scheduler, short enough not to make the test slow.
		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()

		return `{"rank_score": 0.5}`, nil
	})
	defer func() { customLLMCaller = previous }()

	opts := NewSortOptions().WithCriteria("any")
	sorted, err := sortByScoringFallback(context.Background(), items, opts, opts.toOpOptions())
	if err != nil {
		t.Fatalf("sortByScoringFallback: %v", err)
	}
	if len(sorted) != len(items) {
		t.Fatalf("sorted %d items, want %d", len(sorted), len(items))
	}
	if calls != len(items) {
		t.Errorf("made %d calls, want exactly one per item (%d)", calls, len(items))
	}
	if peak == 0 {
		t.Fatal("no call was ever recorded as in flight")
	}
	if peak > DefaultConcurrency {
		t.Errorf("peak concurrent calls = %d, want at most DefaultConcurrency (%d)", peak, DefaultConcurrency)
	}
	if peak < 2 {
		t.Errorf("peak concurrent calls = %d; calls never overlapped, so concurrency was not exercised", peak)
	}
}

// OP-106: above sortScoringThreshold, Sort goes straight to scoring rather
// than attempting -- and paying for -- a whole-list answer first.
func TestSortPromotesScoringAboveThreshold(t *testing.T) {
	items := make([]string, sortScoringThreshold+1)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}

	// sortByScoringFallback calls this from up to DefaultConcurrency worker
	// goroutines at once (that concurrency is the other half of what this
	// test is checking), so the counters need a lock: an unsynchronized
	// increment here is exactly the kind of race that only shows up once in
	// a while, which is worse than one that always fails.
	var mu sync.Mutex
	var wholeListAttempted bool
	var scoringCalls int

	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(system, "rank_score") {
			scoringCalls++
			return `{"rank_score": 0.5}`, nil
		}
		wholeListAttempted = true
		return `{"ids": []}`, nil
	})
	defer func() { customLLMCaller = previous }()

	sorted, err := Sort(items, NewSortOptions().WithCriteria("any"))
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	if wholeListAttempted {
		t.Error("a batch above the threshold attempted the whole-list answer instead of going straight to scoring")
	}
	if scoringCalls != len(items) {
		t.Errorf("made %d scoring calls, want one per item (%d)", scoringCalls, len(items))
	}
	if len(sorted) != len(items) {
		t.Errorf("sorted %d items, want %d", len(sorted), len(items))
	}
}

// Below the threshold, Sort still prefers the single whole-list call.
func TestSortPrefersWholeListBelowThreshold(t *testing.T) {
	items := []string{"low", "critical", "medium"}

	var scoringAttempted bool

	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		if strings.Contains(system, "rank_score") {
			scoringAttempted = true
			return `{"rank_score": 0.5}`, nil
		}
		return `{"ids":["i-000002","i-000003","i-000001"]}`, nil
	})
	defer func() { customLLMCaller = previous }()

	sorted, err := Sort(items, NewSortOptions().WithCriteria("any"))
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	if scoringAttempted {
		t.Error("a batch below the threshold reached the scoring path when the whole-list answer was valid")
	}
	if len(sorted) != 3 || sorted[0] != "critical" || sorted[1] != "medium" || sorted[2] != "low" {
		t.Errorf("sorted = %v", sorted)
	}
}

// SortResult reports which strategy actually ran (OP-106), for every path:
// trivial, whole-list, scoring, and the scoring fallback reached when a
// whole-list answer is rejected.
func TestSortResultReportsStrategy(t *testing.T) {
	t.Run("trivial", func(t *testing.T) {
		result, err := SortResult([]string{"only"}, NewSortOptions().WithCriteria("any"))
		if err != nil {
			t.Fatalf("SortResult: %v", err)
		}
		if result.Meta.Strategy != "trivial" {
			t.Errorf("Strategy = %q, want %q", result.Meta.Strategy, "trivial")
		}
	})

	t.Run("whole_list", func(t *testing.T) {
		restore := stubLLM(`{"ids":["i-000002","i-000001","i-000003"]}`)
		defer restore()

		result, err := SortResult([]string{"a", "b", "c"}, NewSortOptions().WithCriteria("any"))
		if err != nil {
			t.Fatalf("SortResult: %v", err)
		}
		if result.Meta.Strategy != "whole-list" {
			t.Errorf("Strategy = %q, want %q", result.Meta.Strategy, "whole-list")
		}
		if len(result.Value) != 3 {
			t.Errorf("Value = %v, want 3 items", result.Value)
		}
	})

	t.Run("scoring_above_threshold", func(t *testing.T) {
		items := make([]string, sortScoringThreshold+5)
		for i := range items {
			items[i] = fmt.Sprintf("item-%03d", i)
		}
		restore := stubLLM(`{"rank_score": 0.5}`)
		defer restore()

		result, err := SortResult(items, NewSortOptions().WithCriteria("any"))
		if err != nil {
			t.Fatalf("SortResult: %v", err)
		}
		if result.Meta.Strategy != "scoring" {
			t.Errorf("Strategy = %q, want %q", result.Meta.Strategy, "scoring")
		}
	})

	t.Run("scoring_fallback_when_whole_list_is_rejected", func(t *testing.T) {
		previous := customLLMCaller
		setLLMCaller(func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
			if strings.Contains(system, "rank_score") {
				return `{"rank_score": 0.5}`, nil
			}
			// A duplicated id: not a permutation, so the whole-list answer is
			// rejected and Sort must fall back to scoring.
			return `{"ids":["i-000001","i-000001","i-000003"]}`, nil
		})
		defer func() { customLLMCaller = previous }()

		result, err := SortResult([]string{"a", "b", "c"}, NewSortOptions().WithCriteria("any"))
		if err != nil {
			t.Fatalf("SortResult: %v", err)
		}
		if result.Meta.Strategy != "scoring-fallback" {
			t.Errorf("Strategy = %q, want %q", result.Meta.Strategy, "scoring-fallback")
		}
		if len(result.Value) != 3 {
			t.Errorf("Value = %v, want 3 items", result.Value)
		}
	})
}
