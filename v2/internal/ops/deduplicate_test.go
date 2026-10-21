package ops

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type dedupCall struct {
	system, user string
	opts         types.OpOptions
	calls        int
}

// dedupWith runs Deduplicate against a scripted answer and returns what the
// provider was sent.
func dedupWith(t *testing.T, items []string, threshold float64, response string, opts ...types.OpOptions) (DeduplicateResult[string], error, *dedupCall) {
	t.Helper()
	sent := &dedupCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent.system, sent.user, sent.opts = system, user, opt
		sent.calls++
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Deduplicate(items, threshold, opts...)
	return result, err, sent
}

var dedupFive = []string{"alpha", "beta", "alpha!", "gamma", "Alpha"}

// LX-021. Deduplicate failed open: an answer it could not parse returned every
// item as unique with a nil error, so a caller could not tell "no duplicates"
// from "the model said nothing usable". Each of these used to succeed.
func TestDeduplicateFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     types.ErrorKind
	}{
		{"prose", `These all look different to me.`, types.KindMalformedOutput},
		{"an empty object", `{}`, types.KindSchemaViolation},
		{"another shape", `{"groups":[[0,2],[1],[3],[4]]}`, types.KindSchemaViolation},
		{"duplicates is null", `{"duplicates":null}`, types.KindSchemaViolation},
		{"an entry with no index", `{"duplicates":[{"duplicate_of":0,"similarity":0.9}]}`, types.KindSchemaViolation},
		{"an entry with no duplicate_of", `{"duplicates":[{"index":2,"similarity":0.9}]}`, types.KindSchemaViolation},
		{"an entry with no similarity", `{"duplicates":[{"index":2,"duplicate_of":0}]}`, types.KindSchemaViolation},
		{"an index past the end", `{"duplicates":[{"index":5,"duplicate_of":0,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"a negative index", `{"duplicates":[{"index":-1,"duplicate_of":0,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"duplicate_of past the end", `{"duplicates":[{"index":2,"duplicate_of":9,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"an item that duplicates itself", `{"duplicates":[{"index":2,"duplicate_of":2,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"an item that duplicates a later one", `{"duplicates":[{"index":0,"duplicate_of":2,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"an item listed twice with two answers", `{"duplicates":[{"index":4,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":2,"similarity":0.9}]}`, types.KindInvariantViolation},
		{"a similarity above the scale", `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":1.4}]}`, types.KindInvariantViolation},
		{"a negative similarity", `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":-0.2}]}`, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := dedupWith(t, dedupFive, 0.8, tc.response)
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if got := types.KindOf(err); got != tc.want {
				t.Errorf("kind = %v, want %v (%v)", got, tc.want, err)
			}
			if len(result.Unique)+len(result.Duplicates)+len(result.UniqueIndices)+len(result.DuplicateIndices)+result.TotalRemoved != 0 {
				t.Errorf("a failed call returned a usable result: %+v", result)
			}
			for _, item := range dedupFive {
				if strings.Contains(err.Error(), item) {
					t.Errorf("the error repeats an item: %v", err)
				}
			}
		})
	}

	// A provider failure is the same: nothing back.
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", ErrNoProvider
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Deduplicate(dedupFive, 0.8)
	if !errors.Is(err, ErrNoProvider) || len(result.Unique) != 0 {
		t.Fatalf("a provider failure returned err=%v and Unique=%v", err, result.Unique)
	}
}

// The result says which input items were kept and which collapsed into them,
// by index, in the order of the input. It used to carry values only, in the
// order the model listed them, so a caller could not map it back.
func TestDeduplicateBuildsGroupsFromTheLinks(t *testing.T) {
	cases := []struct {
		name      string
		items     []string
		threshold float64
		response  string
		unique    []int
		groups    [][]int
	}{
		{"no duplicates", dedupFive, 0.8, `{"duplicates":[]}`, []int{0, 1, 2, 3, 4}, nil},
		{"one pair", dedupFive, 0.8, `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9}]}`, []int{0, 1, 3, 4}, [][]int{{0, 2}}},
		{"a group of three", dedupFive, 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":0,"similarity":0.95}]}`,
			[]int{0, 1, 3}, [][]int{{0, 2, 4}}},
		{"a chain is one group", dedupFive, 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":2,"similarity":0.95}]}`,
			[]int{0, 1, 3}, [][]int{{0, 2, 4}}},
		{"two groups", dedupFive, 0.8,
			`{"duplicates":[{"index":3,"duplicate_of":1,"similarity":0.9},{"index":2,"duplicate_of":0,"similarity":0.9}]}`,
			[]int{0, 1, 4}, [][]int{{0, 2}, {1, 3}}},
		{"entries out of order", dedupFive, 0.8,
			`{"duplicates":[{"index":4,"duplicate_of":2,"similarity":0.9},{"index":2,"duplicate_of":0,"similarity":0.9}]}`,
			[]int{0, 1, 3}, [][]int{{0, 2, 4}}},
		{"the same entry twice", dedupFive, 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":2,"duplicate_of":0,"similarity":0.9}]}`,
			[]int{0, 1, 3, 4}, [][]int{{0, 2}}},
		{"everything duplicates the first", dedupFive, 0.5,
			`{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.6},{"index":2,"duplicate_of":0,"similarity":0.6},{"index":3,"duplicate_of":0,"similarity":0.6},{"index":4,"duplicate_of":0,"similarity":0.6}]}`,
			[]int{0}, [][]int{{0, 1, 2, 3, 4}}},
		{"two items", []string{"a", "A"}, 0.8, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":1}]}`, []int{0}, [][]int{{0, 1}}},
		{"a position the model also sends is ignored", dedupFive, 0.8,
			`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9,"reason":"same word"}],"note":"one pair"}`,
			[]int{0, 1, 3, 4}, [][]int{{0, 2}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := dedupWith(t, tc.items, tc.threshold, tc.response)
			if err != nil {
				t.Fatalf("Deduplicate: %v", err)
			}
			if !reflect.DeepEqual(result.UniqueIndices, tc.unique) {
				t.Errorf("UniqueIndices = %v, want %v", result.UniqueIndices, tc.unique)
			}
			if len(result.DuplicateIndices) != len(tc.groups) || (len(tc.groups) > 0 && !reflect.DeepEqual(result.DuplicateIndices, tc.groups)) {
				t.Errorf("DuplicateIndices = %v, want %v", result.DuplicateIndices, tc.groups)
			}

			// The values are the caller's own, at those indices.
			if len(result.Unique) != len(tc.unique) {
				t.Fatalf("Unique = %v, want %d items", result.Unique, len(tc.unique))
			}
			for k, index := range tc.unique {
				if result.Unique[k] != tc.items[index] {
					t.Errorf("Unique[%d] = %q, want item %d", k, result.Unique[k], index)
				}
			}
			removed := 0
			for g, group := range tc.groups {
				removed += len(group) - 1
				for k, index := range group {
					if result.Duplicates[g][k] != tc.items[index] {
						t.Errorf("Duplicates[%d][%d] = %q, want item %d", g, k, result.Duplicates[g][k], index)
					}
				}
			}
			if result.TotalRemoved != removed || len(result.Unique)+result.TotalRemoved != len(tc.items) {
				t.Errorf("TotalRemoved = %d with %d unique of %d items", result.TotalRemoved, len(result.Unique), len(tc.items))
			}

			// Kept items and groups together account for every input once.
			groups := append([][]int(nil), result.DuplicateIndices...)
			inGroup := map[int]bool{}
			for _, group := range groups {
				for _, index := range group {
					inGroup[index] = true
				}
			}
			for _, index := range result.UniqueIndices {
				if !inGroup[index] {
					groups = append(groups, []int{index})
				}
			}
			if err := CoversExactlyOnce(len(tc.items), groups); err != nil {
				t.Errorf("the result is not a partition of the input: %v", err)
			}
		})
	}
}

// The threshold was written into the prompt and checked nowhere. The model now
// reports how similar each pair is and Go applies the threshold, so the same
// answer gives different groups at different thresholds, and the request does
// not change with it.
func TestDeduplicateAppliesTheThresholdInGo(t *testing.T) {
	const response = `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":4,"duplicate_of":0,"similarity":0.7},{"index":3,"duplicate_of":1,"similarity":0.5}]}`
	cases := []struct {
		threshold float64
		unique    []int
	}{
		{0.95, []int{0, 1, 2, 3, 4}},
		{0.9, []int{0, 1, 3, 4}},
		{0.8, []int{0, 1, 3, 4}},
		{0.7, []int{0, 1, 3}},
		{0.6, []int{0, 1, 3}},
		{0.5, []int{0, 1}},
		{0, []int{0, 1}},
		{1, []int{0, 1, 2, 3, 4}},
	}
	var systems, users []string
	for _, tc := range cases {
		result, err, sent := dedupWith(t, dedupFive, tc.threshold, response)
		if err != nil {
			t.Fatalf("threshold %v: %v", tc.threshold, err)
		}
		if !reflect.DeepEqual(result.UniqueIndices, tc.unique) {
			t.Errorf("threshold %v: UniqueIndices = %v, want %v", tc.threshold, result.UniqueIndices, tc.unique)
		}
		systems, users = append(systems, sent.system), append(users, sent.user)
		if everything := strings.ToLower(sent.system + sent.user + sent.opts.Steering); strings.Contains(everything, "threshold") {
			t.Errorf("threshold %v: the request mentions the threshold", tc.threshold)
		}
	}
	for i := range systems {
		if systems[i] != systems[0] || users[i] != users[0] {
			t.Fatal("the request differs between thresholds")
		}
	}

	// A link below the threshold breaks a chain where it is weak, and nowhere
	// else: 4 stays with 2, and 2 is no longer with 0.
	chain := `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.6},{"index":4,"duplicate_of":2,"similarity":0.95}]}`
	result, err, _ := dedupWith(t, dedupFive, 0.8, chain)
	if err != nil {
		t.Fatalf("Deduplicate: %v", err)
	}
	if want := [][]int{{2, 4}}; !reflect.DeepEqual(result.DuplicateIndices, want) {
		t.Errorf("DuplicateIndices = %v, want %v", result.DuplicateIndices, want)
	}
}

func TestDeduplicateRefusesAThresholdOffTheScale(t *testing.T) {
	for _, threshold := range []float64{-0.1, 1.01, 2, 85, math.NaN(), math.Inf(1), math.Inf(-1)} {
		result, err, sent := dedupWith(t, dedupFive, threshold, `{"duplicates":[]}`)
		if err == nil {
			t.Errorf("threshold %v was accepted: %+v", threshold, result)
		}
		if sent.calls != 0 {
			t.Errorf("threshold %v: the provider was called", threshold)
		}
	}
	for _, threshold := range []float64{0, 0.5, 0.85, 1} {
		if _, err, _ := dedupWith(t, dedupFive, threshold, `{"duplicates":[]}`); err != nil {
			t.Errorf("threshold %v was refused: %v", threshold, err)
		}
	}
}

// Nothing to compare means nothing to send.
func TestDeduplicateWithNothingToCompareMakesNoCall(t *testing.T) {
	for _, items := range [][]string{nil, {}, {"only"}} {
		result, err, sent := dedupWith(t, items, 0.8, `not json`)
		if err != nil {
			t.Fatalf("%d items: %v", len(items), err)
		}
		if sent.calls != 0 {
			t.Errorf("%d items: the provider was called", len(items))
		}
		if len(result.Unique) != len(items) || len(result.UniqueIndices) != len(items) || result.TotalRemoved != 0 {
			t.Errorf("%d items: result = %+v", len(items), result)
		}
	}
}

// The request declares its shape, numbers the items, and keeps the caller's
// data out of the system prompt.
func TestDeduplicateRequest(t *testing.T) {
	items := []string{"The app crashed.", "The app crashed during checkout.", "Invoices are monthly."}
	_, err, sent := dedupWith(t, items, 0.8, `{"duplicates":[]}`, types.OpOptions{Steering: "Two items are duplicates only if they state the same facts"})
	if err != nil {
		t.Fatalf("Deduplicate: %v", err)
	}
	if problem := llm.StrictSchemaProblem(sent.opts.JSONSchema); problem != "" {
		t.Fatalf("the schema breaks a strict-mode rule, or is missing: %s", problem)
	}
	if sent.opts.SchemaName == "" {
		t.Error("the schema has no name")
	}
	for i, item := range items {
		if strings.Contains(sent.system, item) {
			t.Errorf("item %d is in the system prompt", i)
		}
	}
	for _, line := range []string{`0: "The app crashed."`, `1: "The app crashed during checkout."`, `2: "Invoices are monthly."`} {
		if !strings.Contains(sent.user, line) {
			t.Errorf("the user prompt has no line %q:\n%s", line, sent.user)
		}
	}
	if sent.opts.Steering != "Two items are duplicates only if they state the same facts" {
		t.Errorf("the caller's steering was changed: %q", sent.opts.Steering)
	}
	if sent.system != deduplicateSystemPrompt {
		t.Error("the system prompt is not the constant, so it does not cache across calls")
	}
}

// An item that cannot be rendered is refused before anything is sent.
func TestDeduplicateRefusesAnItemItCannotRender(t *testing.T) {
	called := false
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		called = true
		return `{"duplicates":[]}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Deduplicate([]any{"fine", make(chan int)}, 0.8)
	if err == nil || called || len(result.Unique) != 0 {
		t.Fatalf("err = %v, provider called = %v, Unique = %v", err, called, result.Unique)
	}
}
