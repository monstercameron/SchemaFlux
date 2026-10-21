package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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

		twoInFlight = make(chan struct{})
		overlapped  sync.Once
	)

	swapLLMCaller(t, func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		mu.Lock()
		calls++
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		if peak >= 2 {
			overlapped.Do(func() { close(twoInFlight) })
		}
		mu.Unlock()

		// A call is held until two are in flight together, so the overlap the
		// assertions below look for is arranged and not left to how the
		// scheduler happens to run a short sleep. The wait is bounded so that a
		// pool that never runs two calls at once fails the peak check instead of
		// hanging.
		select {
		case <-twoInFlight:
		case <-time.After(5 * time.Second):
		}

		mu.Lock()
		inFlight--
		mu.Unlock()

		return `{"rank_score": 0.5}`, nil
	})

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

	swapLLMCaller(t, func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(system, "rank_score") {
			scoringCalls++
			return `{"rank_score": 0.5}`, nil
		}
		wholeListAttempted = true
		return `{"ids": []}`, nil
	})

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

	swapLLMCaller(t, func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		if strings.Contains(system, "rank_score") {
			scoringAttempted = true
			return `{"rank_score": 0.5}`, nil
		}
		return `{"ids":["i-000002","i-000003","i-000001"]}`, nil
	})

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
		swapLLMCaller(t, func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
			if strings.Contains(system, "rank_score") {
				return `{"rank_score": 0.5}`, nil
			}
			// A duplicated id: not a permutation, so the whole-list answer is
			// rejected and Sort must fall back to scoring.
			return `{"ids":["i-000001","i-000001","i-000003"]}`, nil
		})

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

// rankByItem installs a model that scores each item from a table, so the order a
// scoring sort returns can be asserted and not only its length. The item is the
// last line of the scoring request.
func rankByItem(t *testing.T, scores map[string]string) {
	t.Helper()
	swapLLMCaller(t, func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		line := user[strings.LastIndex(user, "\n")+1:]
		item := strings.Trim(line, `"`)
		body, ok := scores[item]
		if !ok {
			return "", fmt.Errorf("no score scripted for %q", item)
		}
		return body, nil
	})
}

func TestSortByScoringReturnsTheItemsInScoreOrder(t *testing.T) {
	rankByItem(t, map[string]string{
		"low":      `{"rank_score":0.1}`,
		"high":     `{"rank_score":0.9}`,
		"middle":   `{"rank_score":0.5}`,
		"tie-b":    `{"rank_score":0.5}`,
		"top":      `{"rank_score":1}`,
		"bottom":   `{"rank_score":0}`,
		"almost-b": `{"rank_score":0.9}`,
	})
	items := []string{"low", "high", "middle", "tie-b", "top", "bottom", "almost-b"}
	// Highest first, and equal scores keep the order the caller gave.
	want := []string{"top", "high", "almost-b", "middle", "tie-b", "low", "bottom"}

	opts := NewSortOptions().WithCriteria("any")
	got, err := sortByScoringFallback(context.Background(), items, opts, opts.toOpOptions())
	if err != nil {
		t.Fatalf("sortByScoringFallback: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestSortAboveTheThresholdReturnsTheItemsInScoreOrder(t *testing.T) {
	count := sortScoringThreshold + 10
	scores := make(map[string]string, count)
	items := make([]string, count)
	// Item k scores k/count, so the sorted order is the input reversed.
	for k := range items {
		items[k] = fmt.Sprintf("item-%03d", k)
		scores[items[k]] = fmt.Sprintf(`{"rank_score":%v}`, float64(k)/float64(count))
	}
	rankByItem(t, scores)

	got, err := Sort(items, NewSortOptions().WithCriteria("any"))
	if err != nil {
		t.Fatalf("Sort: %v", err)
	}
	for k, item := range got {
		if want := items[count-1-k]; item != want {
			t.Fatalf("position %d = %s, want %s", k, item, want)
		}
	}
}

func TestSortByScoringRefusesAScoreThatIsNotOnTheScale(t *testing.T) {
	for _, body := range []string{
		`{"rank_score":null}`,
		`{"rank_score":12}`,
		`{"rank_score":-3}`,
		`{"rank_score":1.0000001}`,
		`{"rank_score":-0.0000001}`,
		`{"rank_score":"0.5"}`,
		`{"rank_score":[0.5]}`,
		`{}`,
		`0.5`,
		`not json`,
	} {
		rankByItem(t, map[string]string{"a": `{"rank_score":0.4}`, "b": body, "c": `{"rank_score":0.6}`})
		opts := NewSortOptions().WithCriteria("any")
		got, err := sortByScoringFallback(context.Background(), []string{"a", "b", "c"}, opts, opts.toOpOptions())
		if err == nil {
			t.Errorf("%s: the sort went ahead and put the items in the order %v", body, got)
			continue
		}
		if got != nil {
			t.Errorf("%s: a result came back beside the error: %v", body, got)
		}
		if strings.Contains(err.Error(), "12") && body == `{"rank_score":12}` {
			t.Errorf("%s: the error repeats the model's number: %v", body, err)
		}
	}
}

func TestSortReportsWhyTheFallbackFailed(t *testing.T) {
	// The whole-list answer is refused, and then every scoring call is cut off
	// by the caller's cancellation. The caller has to be able to see that.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	swapLLMCaller(t, func(_ context.Context, system, _ string, _ types.OpOptions) (string, error) {
		if strings.Contains(system, "rank_score") {
			cancel()
			return "", context.Canceled
		}
		return `{"ids":["i-000001","i-000001","i-000003"]}`, nil
	})

	result, err := SortResult([]string{"a", "b", "c"}, NewSortOptions().WithCriteria("any").WithContext(ctx))
	if err == nil {
		t.Fatalf("Sort succeeded: %v", result.Value)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cancellation that ended the sort is not reachable from the error: %v", err)
	}
	if result.Meta.Strategy != "scoring-fallback" {
		t.Errorf("Strategy = %q, want scoring-fallback: scoring calls were made", result.Meta.Strategy)
	}
}

func TestChooseSendsTheSteeringOnceAndInTheOptions(t *testing.T) {
	var user string
	var steering string
	swapLLMCaller(t, func(_ context.Context, _, u string, opts types.OpOptions) (string, error) {
		user, steering = u, opts.Steering
		return `{"id":"i-000002"}`, nil
	})

	got, err := Choose([]string{"a", "b"}, NewChooseOptions().WithCriteria([]string{"cheapest-unique-criterion"}))
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if got != "b" {
		t.Errorf("chose %q", got)
	}
	if !strings.Contains(steering, "cheapest-unique-criterion") {
		t.Errorf("the criteria are not in the steering: %q", steering)
	}
	if strings.Contains(user, "cheapest-unique-criterion") {
		t.Errorf("the criteria are also written into the message, unmarked, beside the marked copy callLLM adds:\n%s", user)
	}
}

// The answers Choose, Filter and Sort read are the library's own: an id, a list
// of ids, a rank score. An answer that leaves out the member it was asked for, or
// says null for it, is refused and not read as an empty id, an empty list or a
// score of nothing. An empty list is an answer for Filter, and null is not.
func TestChooseRefusesAnAnswerThatHasNoUsableID(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"id":null}`, `{"id":4}`, `{"id":["i-000001"]}`, `{"other":"i-000001"}`,
		`null`, `[]`, `"i-000001"`, `not json`, ``,
	} {
		t.Run(body, func(t *testing.T) {
			defer stubLLM(body)()
			got, err := Choose([]string{"a", "b"}, NewChooseOptions().WithSteering("pick"))
			if err == nil {
				t.Fatalf("chose %q from an answer with no id", got)
			}
			if got != "" {
				t.Errorf("a result came back beside the error: %q", got)
			}
			var classified *types.OperationError
			if !errors.As(err, &classified) || classified.Op != "choose" {
				t.Errorf("the refusal is not classified under choose: %v", err)
			}
		})
	}
}

func TestFilterRefusesAnAnswerThatHasNoListOfIDs(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"ids":null}`, `{"ids":"i-000001"}`, `{"ids":[1]}`, `{"other":[]}`,
		`null`, `[]`, `["i-000001"]`, `not json`, ``,
	} {
		t.Run(body, func(t *testing.T) {
			defer stubLLM(body)()
			got, err := Filter([]string{"a", "b"}, NewFilterOptions().WithCriteria("any"))
			if err == nil {
				t.Fatalf("kept %v from an answer with no list of ids", got)
			}
			if got != nil {
				t.Errorf("a result came back beside the error: %v", got)
			}
			var classified *types.OperationError
			if !errors.As(err, &classified) || classified.Op != "filter" {
				t.Errorf("the refusal is not classified under filter: %v", err)
			}
		})
	}
}

func TestFilterTakesAnEmptyListOfIDsAsNothingKept(t *testing.T) {
	defer stubLLM(`{"ids":[]}`)()
	got, err := Filter([]string{"a", "b"}, NewFilterOptions().WithCriteria("any"))
	if err != nil || len(got) != 0 {
		t.Fatalf("an empty list: %v, %v", got, err)
	}
}

func TestSortRefusesAWholeListAnswerThatHasNoListOfIDs(t *testing.T) {
	// A refused whole-list answer sends the sort to scoring, and a scoring call
	// that is refused as well fails the sort, so the refusal is seen in the error.
	for _, body := range []string{`{}`, `{"ids":null}`, `{"other":[]}`, `null`, `[]`, `not json`} {
		t.Run(body, func(t *testing.T) {
			defer stubLLM(body)()
			got, err := Sort([]string{"a", "b", "c"}, NewSortOptions().WithCriteria("any"))
			if err == nil {
				t.Fatalf("sorted to %v from an answer with no list of ids", got)
			}
			if got != nil {
				t.Errorf("a result came back beside the error: %v", got)
			}
		})
	}
}

func TestSortByScoringNamesTheMemberAScoreLeavesOut(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{}`, "rank_score is missing"},
		{`{"other":1}`, "rank_score is missing"},
		{`{"rank_score":null}`, "rank_score is null"},
		{`{"rank_score":"0.5"}`, "rank_score is not a number"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			rankByItem(t, map[string]string{"a": `{"rank_score":0.4}`, "b": tc.body})
			opts := NewSortOptions().WithCriteria("any")
			_, err := sortByScoringFallback(context.Background(), []string{"a", "b"}, opts, opts.toOpOptions())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
			wantKind(t, err, types.KindSchemaViolation)
		})
	}
}
