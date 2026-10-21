package schemaflux_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

const dedupeNoDuplicates = `{"duplicates":[]}`

// dedupeUnwritable is an item that cannot be written as JSON, and whose error
// says what it holds.
type dedupeUnwritable struct{ secret string }

func (u dedupeUnwritable) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot write " + u.secret)
}

// dedupeLead is an item with a field JSON leaves out.
type dedupeLead struct {
	Name   string `json:"name"`
	source int
}

func dedupeEmpty() schemaflux.DeduplicateResult[string] {
	return schemaflux.DeduplicateResult[string]{}
}

func TestDeduplicateGroupsTheItemsTheModelMatchesAtOrAboveTheThreshold(t *testing.T) {
	t.Parallel()
	items := []string{"alpha", "beta", "gamma", "delta"}
	cases := []struct {
		name      string
		threshold float64
		answer    string
		unique    []string
		groups    [][]string
		indices   []int
		groupIdx  [][]int
		removed   int
	}{
		{"no duplicates", 0.8, dedupeNoDuplicates, items, [][]string{}, []int{0, 1, 2, 3}, nil, 0},
		{"one duplicate", 0.9, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`,
			[]string{"alpha", "gamma", "delta"}, [][]string{{"alpha", "beta"}}, []int{0, 2, 3}, [][]int{{0, 1}}, 1},
		{"a similarity on the threshold is a duplicate", 0.95, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`,
			[]string{"alpha", "gamma", "delta"}, [][]string{{"alpha", "beta"}}, []int{0, 2, 3}, [][]int{{0, 1}}, 1},
		{"a similarity under the threshold is not", 0.96, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95}]}`,
			items, [][]string{}, []int{0, 1, 2, 3}, nil, 0},
		{"a threshold of zero keeps whatever was reported", 0, `{"duplicates":[{"index":3,"duplicate_of":2,"similarity":0.5}]}`,
			[]string{"alpha", "beta", "gamma"}, [][]string{{"gamma", "delta"}}, []int{0, 1, 2}, [][]int{{2, 3}}, 1},
		{"a threshold of one needs a similarity of one", 1, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":1},{"index":3,"duplicate_of":2,"similarity":0.99}]}`,
			[]string{"alpha", "gamma", "delta"}, [][]string{{"alpha", "beta"}}, []int{0, 2, 3}, [][]int{{0, 1}}, 1},
		{"matches are followed into one group", 0.7, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95},{"index":2,"duplicate_of":1,"similarity":0.8}]}`,
			[]string{"alpha", "delta"}, [][]string{{"alpha", "beta", "gamma"}}, []int{0, 3}, [][]int{{0, 1, 2}}, 2},
		{"a chain is cut where a link is under the threshold", 0.85, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.95},{"index":2,"duplicate_of":1,"similarity":0.8}]}`,
			[]string{"alpha", "gamma", "delta"}, [][]string{{"alpha", "beta"}}, []int{0, 2, 3}, [][]int{{0, 1}}, 1},
		{"two groups", 0.8, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":3,"duplicate_of":2,"similarity":0.85}]}`,
			[]string{"alpha", "gamma"}, [][]string{{"alpha", "beta"}, {"gamma", "delta"}}, []int{0, 2}, [][]int{{0, 1}, {2, 3}}, 2},
		{"every item the same as the first", 0.8, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":2,"duplicate_of":0,"similarity":0.9},{"index":3,"duplicate_of":0,"similarity":0.9}]}`,
			[]string{"alpha"}, [][]string{{"alpha", "beta", "gamma", "delta"}}, []int{0}, [][]int{{0, 1, 2, 3}}, 3},
		{"an item the same as one and the same entry twice", 0.8, `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":1,"duplicate_of":0,"similarity":0.9}]}`,
			[]string{"alpha", "gamma", "delta"}, [][]string{{"alpha", "beta"}}, []int{0, 2, 3}, [][]int{{0, 1}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(tc.answer)
			res, err := schemaflux.Deduplicate(operationClient(model), items, tc.threshold)
			if err != nil {
				t.Fatal(err)
			}
			want := schemaflux.DeduplicateResult[string]{Unique: tc.unique, Duplicates: tc.groups, TotalRemoved: tc.removed, UniqueIndices: tc.indices, DuplicateIndices: tc.groupIdx}
			if !reflect.DeepEqual(res, want) {
				t.Errorf("got %+v, want %+v", res, want)
			}
			if model.Calls() != 1 {
				t.Errorf("%d requests were sent, want one for the whole list", model.Calls())
			}
		})
	}
}

func TestDeduplicateFindsItemsWrittenTheSameWithoutAskingAndSendsOnlyTheDistinctOnes(t *testing.T) {
	t.Parallel()
	t.Run("all the same: no request", func(t *testing.T) {
		t.Parallel()
		var model schemafluxtest.Provider
		res, err := schemaflux.Deduplicate(operationClient(&model), []string{"same", "same", "same"}, 0.8)
		want := schemaflux.DeduplicateResult[string]{Unique: []string{"same"}, Duplicates: [][]string{{"same", "same", "same"}}, TotalRemoved: 2, UniqueIndices: []int{0}, DuplicateIndices: [][]int{{0, 1, 2}}}
		if err != nil || model.Calls() != 0 || !reflect.DeepEqual(res, want) {
			t.Fatalf("got %+v, %v after %d requests, want %+v", res, err, model.Calls(), want)
		}
	})
	t.Run("a repeat among distinct items: the repeat is not sent", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(dedupeNoDuplicates)
		res, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b", "a", "c"}, 0.8)
		if err != nil {
			t.Fatal(err)
		}
		if got := model.Last().User; got != "Items:\n0: \"a\"\n1: \"b\"\n2: \"c\"" {
			t.Errorf("the model was shown %q, want three numbered distinct items", got)
		}
		if !reflect.DeepEqual(res.Unique, []string{"a", "b", "c"}) || !reflect.DeepEqual(res.DuplicateIndices, [][]int{{0, 2}}) || res.TotalRemoved != 1 {
			t.Errorf("got %+v", res)
		}
	})
	t.Run("the model's numbers are those of the distinct items, and are mapped back to the list", func(t *testing.T) {
		t.Parallel()
		// Shown: 0 "a", 1 "b", 2 "c". The model says 2 is a copy of 1: the items at 3 and 1.
		model := answeringWith(`{"duplicates":[{"index":2,"duplicate_of":1,"similarity":0.9}]}`)
		res, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b", "a", "c"}, 0.8)
		if err != nil {
			t.Fatal(err)
		}
		want := schemaflux.DeduplicateResult[string]{
			Unique: []string{"a", "b"}, Duplicates: [][]string{{"a", "a"}, {"b", "c"}}, TotalRemoved: 2,
			UniqueIndices: []int{0, 1}, DuplicateIndices: [][]int{{0, 2}, {1, 3}},
		}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("got %+v, want %+v", res, want)
		}
	})
	t.Run("two distinct items are enough to ask", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(dedupeNoDuplicates)
		if _, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "a", "b"}, 0.8); err != nil || model.Calls() != 1 {
			t.Fatalf("%v after %d requests", err, model.Calls())
		}
	})
	t.Run("one distinct item is not enough to ask", func(t *testing.T) {
		t.Parallel()
		var model schemafluxtest.Provider
		if _, err := schemaflux.Deduplicate(operationClient(&model), []string{"a", "a"}, 0.8); err != nil || model.Calls() != 0 {
			t.Fatalf("%v after %d requests", err, model.Calls())
		}
	})
}

func TestDeduplicateReturnsTheCallersOwnValuesAndNewSlices(t *testing.T) {
	t.Parallel()
	t.Run("an item JSON leaves a field out of is returned as the caller wrote it", func(t *testing.T) {
		t.Parallel()
		items := []dedupeLead{{"ann", 1}, {"ann", 2}, {"bob", 3}}
		res, err := schemaflux.Deduplicate(operationClient(answeringWith(dedupeNoDuplicates)), items, 0.8)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Unique) != 2 || res.Unique[0].source != 1 || res.Unique[1].source != 3 {
			t.Errorf("unique = %+v: the first of a group is kept, with all of its fields", res.Unique)
		}
		if len(res.Duplicates) != 1 || res.Duplicates[0][1].source != 2 {
			t.Errorf("duplicates = %+v", res.Duplicates)
		}
	})
	t.Run("pointers are returned as they are", func(t *testing.T) {
		t.Parallel()
		a, b := &dedupeLead{Name: "ann"}, &dedupeLead{Name: "bob"}
		res, err := schemaflux.Deduplicate(operationClient(answeringWith(dedupeNoDuplicates)), []*dedupeLead{a, b}, 0.8)
		if err != nil || len(res.Unique) != 2 || res.Unique[0] != a || res.Unique[1] != b {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("changing a result never changes the list", func(t *testing.T) {
		t.Parallel()
		for _, items := range [][]string{{"x"}, {"x", "y"}, {"x", "x"}, {"x", "y", "z"}} {
			input := append([]string(nil), items...)
			res, err := schemaflux.Deduplicate(operationClient(answeringWith(dedupeNoDuplicates)), input, 0.8)
			if err != nil {
				t.Fatal(err)
			}
			for i := range res.Unique {
				res.Unique[i] = "changed"
			}
			for _, group := range res.Duplicates {
				for i := range group {
					group[i] = "changed"
				}
			}
			if !reflect.DeepEqual(input, items) {
				t.Errorf("the list %q became %q", items, input)
			}
		}
	})
}

func TestDeduplicateOfNoItemsOrOneAsksNothingAndKeepsEverything(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		items []string
		want  schemaflux.DeduplicateResult[string]
	}{
		{"a nil list", nil, schemaflux.DeduplicateResult[string]{Unique: []string{}, Duplicates: [][]string{}, UniqueIndices: []int{}}},
		{"an empty list", []string{}, schemaflux.DeduplicateResult[string]{Unique: []string{}, Duplicates: [][]string{}, UniqueIndices: []int{}}},
		{"one item", []string{"only"}, schemaflux.DeduplicateResult[string]{Unique: []string{"only"}, Duplicates: [][]string{}, UniqueIndices: []int{0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			var recorder schemaflux.Recorder
			res, err := schemaflux.Deduplicate(operationClient(&model, recordingTo(&recorder)), tc.items, 0.8)
			if err != nil || model.Calls() != 0 || !reflect.DeepEqual(res, tc.want) {
				t.Fatalf("got %+v, %v after %d requests, want %+v", res, err, model.Calls(), tc.want)
			}
			if r := onlyRecord(t, &recorder); r.Op != "deduplicate" || r.Requests != 0 || r.Err != nil {
				t.Errorf("record = %q requests %d err %v", r.Op, r.Requests, r.Err)
			}
		})
	}
}

func TestDeduplicateWithNoOptionsAsksAtTheSmartTierForADeclaredSchemaWithNoReasoning(t *testing.T) {
	t.Parallel()
	model := answeringWith(dedupeNoDuplicates)
	if _, err := schemaflux.Deduplicate(operationClient(model), []string{"alpha", "beta"}, 0.8); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
	if req.Schema == nil || req.SchemaName != "duplicates" {
		t.Errorf("schema declared %v under the name %q", req.Schema != nil, req.SchemaName)
	}
	if req.Effort != "none" || !req.EffortPreferred {
		t.Errorf("effort %q preferred %v: with nothing numeric in the items it prefers no reasoning", req.Effort, req.EffortPreferred)
	}
}

func TestDeduplicateDoesNotTellTheModelTheThreshold(t *testing.T) {
	t.Parallel()
	asked := func(threshold float64) string {
		model := answeringWith(dedupeNoDuplicates)
		if _, err := schemaflux.Deduplicate(operationClient(model), []string{"alpha", "beta"}, threshold); err != nil {
			t.Fatal(err)
		}
		return requestText(model.Last())
	}
	if asked(0.1) != asked(0.99) || asked(0) != asked(1) {
		t.Error("the threshold changed what the model is asked: it is applied in Go, after the answer")
	}
}

func TestDeduplicatePrefersNoReasoningOnlyWhenNothingTheCallerSuppliedMentionsANumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		items      []string
		opts       schemaflux.Options
		wantEffort schemaflux.Effort
	}{
		{"nothing numeric", []string{"alpha", "beta"}, schemaflux.Options{}, schemaflux.EffortNone},
		{"a number in an item", []string{"alpha", "12 dozen"}, schemaflux.Options{}, ""},
		{"a number in the steering", []string{"alpha", "beta"}, schemaflux.Options{Steering: "same order size"}, schemaflux.EffortNone},
		{"a digit in the steering", []string{"alpha", "beta"}, schemaflux.Options{Steering: "within 5 units"}, ""},
		{"the caller's own effort replaces the preference", []string{"alpha", "beta"}, schemaflux.Options{Effort: schemaflux.EffortHigh}, schemaflux.EffortHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(dedupeNoDuplicates)
			if _, err := schemaflux.Deduplicate(operationClient(model), tc.items, 0.8, tc.opts); err != nil {
				t.Fatal(err)
			}
			req := model.Last()
			if req.Effort != tc.wantEffort {
				t.Errorf("effort = %q, want %q", req.Effort, tc.wantEffort)
			}
			if wantPreferred := tc.wantEffort == schemaflux.EffortNone; req.EffortPreferred != wantPreferred {
				t.Errorf("effort preferred = %v, want %v", req.EffortPreferred, wantPreferred)
			}
		})
	}
}

func TestDeduplicateEachCommonSettingChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Model", schemaflux.Options{Model: "pinned-dedup"}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-dedup" {
				t.Errorf("model %q", req.Model)
			}
		}},
		{"TierFast", schemaflux.Options{Tier: schemaflux.TierFast}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 2000 {
				t.Errorf("max tokens %d", req.MaxTokens)
			}
		}},
		{"Effort", schemaflux.Options{Effort: schemaflux.EffortLow}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort %q preferred %v", req.Effort, req.EffortPreferred)
			}
		}},
		{"ModeStrict", schemaflux.Options{Mode: schemaflux.ModeStrict}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature %v", req.Temperature)
			}
		}},
		{"Steering", schemaflux.Options{Steering: "prefer earlier records"}, func(t *testing.T, req schemaflux.Request) { wantText(t, req, "prefer earlier records") }},
		{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 61}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 61 {
				t.Errorf("max tokens %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.Options{WebSearch: true}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("web search was not enabled")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(dedupeNoDuplicates)
			if _, err := schemaflux.Deduplicate(operationClient(model), []string{"alpha", "beta"}, 0.8, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestDeduplicateMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  []schemaflux.Options
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"no value", nil, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "test-model" || req.MaxTokens != 4000 {
				t.Errorf("model %q ceiling %d", req.Model, req.MaxTokens)
			}
		}},
		{"one value is used as it is", []schemaflux.Options{{Model: "one", Steering: "keep the first"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "keep the first")
			if req.Model != "one" {
				t.Errorf("model %q", req.Model)
			}
		}},
		{"each value adds its settings", []schemaflux.Options{{Steering: "keep the first"}, {Model: "chosen-model"}, {MaxOutputTokens: 70, WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "keep the first")
			if req.Model != "chosen-model" || req.MaxTokens != 70 || !req.WebSearch {
				t.Errorf("model %q ceiling %d web %v", req.Model, req.MaxTokens, req.WebSearch)
			}
		}},
		{"the later value wins", []schemaflux.Options{{Model: "early", MaxOutputTokens: 10}, {Model: "late", MaxOutputTokens: 20}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "late" || req.MaxTokens != 20 {
				t.Errorf("model %q ceiling %d", req.Model, req.MaxTokens)
			}
		}},
		{"a later zero value does not undo an earlier one", []schemaflux.Options{{Model: "early", WebSearch: true, Steering: "keep the first"}, {}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "keep the first")
			if req.Model != "early" || !req.WebSearch {
				t.Errorf("model %q web %v", req.Model, req.WebSearch)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(dedupeNoDuplicates)
			if _, err := schemaflux.Deduplicate(operationClient(model), []string{"alpha", "beta"}, 0.8, tc.opts...); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestDeduplicateAcceptsTheEndsOfTheThresholdRange(t *testing.T) {
	t.Parallel()
	for _, threshold := range []float64{0, 1, math.SmallestNonzeroFloat64, math.Nextafter(1, 0)} {
		model := answeringWith(dedupeNoDuplicates)
		if _, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b"}, threshold); err != nil || model.Calls() != 1 {
			t.Errorf("threshold %v: %v after %d requests", threshold, err, model.Calls())
		}
	}
}

func TestDeduplicateRefusesAThresholdOutsideZeroToOneAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]float64{
		"negative":                    -0.1,
		"the smallest negative":       -math.SmallestNonzeroFloat64,
		"above one":                   1.1,
		"just above one":              math.Nextafter(1, 2),
		"not a number":                math.NaN(),
		"infinite":                    math.Inf(1),
		"negative infinite":           math.Inf(-1),
		"the largest float":           math.MaxFloat64,
		"the most negative float":     -math.MaxFloat64,
		"a percentage written as one": 85,
	}
	for name, threshold := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := answeringWith(dedupeNoDuplicates)
			res, err := schemaflux.Deduplicate(operationClient(model, recordingTo(&recorder)), []string{"a", "b"}, threshold)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
			if !strings.Contains(e.Error(), "threshold") {
				t.Errorf("the error does not name the argument: %v", e)
			}
			if model.Calls() != 0 || !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
			}
			if r := onlyRecord(t, &recorder); r.Op != "deduplicate" || r.Requests != 0 || r.Err != err {
				t.Errorf("record = %q requests %d err %v", r.Op, r.Requests, r.Err)
			}
		})
	}
}

func TestDeduplicateRefusesACommonSettingNoCallCanUseBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []schemaflux.Options
	}{
		{"a negative output ceiling", []schemaflux.Options{{MaxOutputTokens: -1}}},
		{"the lowest output ceiling", []schemaflux.Options{{MaxOutputTokens: math.MinInt}}},
		{"a tier that does not exist", []schemaflux.Options{{Tier: 9}}},
		{"a mode that does not exist", []schemaflux.Options{{Mode: 9}}},
		{"a bad ceiling in the later of two values", []schemaflux.Options{{Model: "good"}, {MaxOutputTokens: -5}}},
		{"a bad tier in the later of two values", []schemaflux.Options{{Steering: "good"}, {Tier: 9}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(dedupeNoDuplicates)
			res, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b"}, 0.8, tc.opts...)
			wantError(t, err, schemaflux.ErrConfiguration, "deduplicate")
			if model.Calls() != 0 || !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
			}
		})
	}
}

func TestDeduplicateReportsTheThresholdBeforeASettingWhenBothAreBad(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Deduplicate(operationClient(answeringWith(dedupeNoDuplicates)), []string{"a", "b"}, 5, schemaflux.Options{MaxOutputTokens: -1})
	wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
}

func TestDeduplicateRefusesAnAnswerThatIsNotWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"text":                          "item 1 is a copy of item 0",
		"no answer at all":              "",
		"null":                          "null",
		"an empty object":               `{}`,
		"a null list":                   `{"duplicates":null}`,
		"a list that is text":           `{"duplicates":"none"}`,
		"an index outside the list":     `{"duplicates":[{"index":8,"duplicate_of":0,"similarity":0.9}]}`,
		"a negative index":              `{"duplicates":[{"index":-1,"duplicate_of":0,"similarity":0.9}]}`,
		"a match outside the list":      `{"duplicates":[{"index":1,"duplicate_of":9,"similarity":0.9}]}`,
		"a negative match":              `{"duplicates":[{"index":1,"duplicate_of":-1,"similarity":0.9}]}`,
		"an item matched with itself":   `{"duplicates":[{"index":1,"duplicate_of":1,"similarity":0.9}]}`,
		"an item matched with a later":  `{"duplicates":[{"index":0,"duplicate_of":1,"similarity":0.9}]}`,
		"a similarity above one":        `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":1.1}]}`,
		"a similarity below zero":       `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":-0.1}]}`,
		"an item reported twice":        `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":1,"duplicate_of":0,"similarity":0.8}]}`,
		"an item matched with two":      `{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9},{"index":2,"duplicate_of":1,"similarity":0.9}]}`,
		"an entry with no index":        `{"duplicates":[{"duplicate_of":0,"similarity":0.9}]}`,
		"an entry with no match":        `{"duplicates":[{"index":1,"similarity":0.9}]}`,
		"an entry with no similarity":   `{"duplicates":[{"index":1,"duplicate_of":0}]}`,
		"an entry with a null match":    `{"duplicates":[{"index":1,"duplicate_of":null,"similarity":0.9}]}`,
		"an entry that is a number":     `{"duplicates":[1]}`,
		"an index that is text":         `{"duplicates":[{"index":"1","duplicate_of":0,"similarity":0.9}]}`,
		"a good entry and a bad one":    `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":2,"duplicate_of":7,"similarity":0.9}]}`,
		"the old shape with groups":     `{"groups":[[0,1]]}`,
		"a bare list of entries":        `[{"index":1,"duplicate_of":0,"similarity":0.9}]`,
		"an index as large as the list": `{"duplicates":[{"index":3,"duplicate_of":0,"similarity":0.9}]}`,
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(answer)
			res, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b", "c"}, 0.5)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "deduplicate")
			if model.Calls() == 0 {
				t.Error("nothing was sent")
			}
			if !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestDeduplicateIndexesTheDistinctItemsNotTheList(t *testing.T) {
	t.Parallel()
	// Four items, two distinct: the model was shown two, and an index of 2 is outside what it saw.
	model := answeringWith(`{"duplicates":[{"index":2,"duplicate_of":0,"similarity":0.9}]}`)
	_, err := schemaflux.Deduplicate(operationClient(model), []string{"a", "b", "a", "b"}, 0.5)
	wantError(t, err, schemaflux.ErrInvalidAnswer, "deduplicate")
}

func TestDeduplicateReturnsTheErrorOfAProviderThatFailsAndNothingElse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  *schemaflux.Error
	}{
		{"unavailable", &schemaflux.Error{Kind: schemaflux.ErrUnavailable}},
		{"rate limited", &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429}},
		{"unauthorized", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401}},
		{"timed out", &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
		{"refused", &schemaflux.Error{Kind: schemaflux.ErrRefused}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(tc.err)
			res, err := schemaflux.Deduplicate(operationClient(&model), []string{"a", "b"}, 0.8)
			wantError(t, err, tc.err.Kind, "deduplicate")
			if !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestDeduplicateRefusesAnItemThatCannotBeWrittenAsJSONBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	model := answeringWith(dedupeNoDuplicates)
	res, err := schemaflux.Deduplicate(operationClient(model), []any{"fine", make(chan int)}, 0.8)
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
	if model.Calls() != 0 || !reflect.DeepEqual(res, schemaflux.DeduplicateResult[any]{}) {
		t.Errorf("%d requests were sent, result %+v, error %v", model.Calls(), res, e)
	}
}

func TestDeduplicateWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var none context.Context
	res, err := schemaflux.Deduplicate(none, []string{"a", "b"}, 0.8)
	e := wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
	if !strings.Contains(e.Error(), "context") || !reflect.DeepEqual(res, dedupeEmpty()) {
		t.Errorf("got %+v and %v", res, e)
	}
	_, err = schemaflux.Deduplicate(none, []string{"a", "b"}, 7)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate"); !strings.Contains(e.Error(), "context") {
		t.Errorf("a nil context and a bad threshold: %v, want the context named first", e)
	}
}

func TestDeduplicateAddsOneRecordForEachCallWithTheOperationsName(t *testing.T) {
	t.Parallel()
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(dedupeNoDuplicates)
		if _, err := schemaflux.Deduplicate(operationClient(model, recordingTo(&recorder)), []string{"a", "b"}, 0.8); err != nil {
			t.Fatal(err)
		}
		r := onlyRecord(t, &recorder)
		if r.Op != "deduplicate" || r.Err != nil || r.Requests != 1 || r.Provider != "test" {
			t.Errorf("record = %q err %v requests %d provider %q", r.Op, r.Err, r.Requests, r.Provider)
		}
		if r.Requested != schemaflux.ContractSchemaAndInvariantChecked || r.Delivered != schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("levels = %v and %v, want schema and invariants checked", r.Requested, r.Delivered)
		}
	})
	t.Run("a call that fails on a link", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		answer := `{"duplicates":[{"index":8,"duplicate_of":0,"similarity":0.9}]}`
		_, err := schemaflux.Deduplicate(operationClient(answeringWith(answer), recordingTo(&recorder)), []string{"a", "b"}, 0.8)
		wantError(t, err, schemaflux.ErrInvalidAnswer, "deduplicate")
		r := onlyRecord(t, &recorder)
		if r.Op != "deduplicate" || r.Err != err || r.Requests < 1 || r.Delivered >= schemaflux.ContractSchemaAndInvariantChecked {
			t.Errorf("record = %q err %v requests %d delivered %v", r.Op, r.Err, r.Requests, r.Delivered)
		}
	})
	t.Run("a call the provider fails", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
		_, err := schemaflux.Deduplicate(operationClient(&model, recordingTo(&recorder)), []string{"a", "b"}, 0.8)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrUnavailable, "deduplicate")
		if r.Op != "deduplicate" || r.Err != err {
			t.Errorf("record = %q err %v", r.Op, r.Err)
		}
	})
	t.Run("a call refused before anything is sent: an item that cannot be written", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(dedupeNoDuplicates)
		_, err := schemaflux.Deduplicate(operationClient(model, recordingTo(&recorder)), []any{"a", make(chan int)}, 0.8)
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrInvalidRequest, "deduplicate")
		if r.Op != "deduplicate" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a call refused before anything is sent: a bad setting", func(t *testing.T) {
		t.Parallel()
		var recorder schemaflux.Recorder
		model := answeringWith(dedupeNoDuplicates)
		_, err := schemaflux.Deduplicate(operationClient(model, recordingTo(&recorder)), []string{"a", "b"}, 0.8, schemaflux.Options{MaxOutputTokens: -1})
		r := onlyRecord(t, &recorder)
		wantError(t, r.Err, schemaflux.ErrConfiguration, "deduplicate")
		if r.Op != "deduplicate" || r.Err != err || r.Requests != 0 || model.Calls() != 0 {
			t.Errorf("record = %q err %v requests %d", r.Op, r.Err, r.Requests)
		}
	})
	t.Run("a recorder on the context and one on the client each get the Record", func(t *testing.T) {
		t.Parallel()
		var onClient, onContext schemaflux.Recorder
		ctx := schemaflux.WithRecorder(operationClient(answeringWith(dedupeNoDuplicates), recordingTo(&onClient)), &onContext)
		if _, err := schemaflux.Deduplicate(ctx, []string{"a", "b"}, 0.8); err != nil {
			t.Fatal(err)
		}
		if onlyRecord(t, &onClient).Op != "deduplicate" || onlyRecord(t, &onContext).Op != "deduplicate" {
			t.Error("a recorder was not given the Record")
		}
	})
}

func TestDeduplicateUnderAFloorIsHeldToTheLevelItEstablishes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		items   []string
		refused bool
	}{
		{"a floor of schema constrained is met", schemaflux.ContractSchemaConstrained, []string{"a", "b"}, false},
		{"a floor of schema and invariants is met", schemaflux.ContractSchemaAndInvariantChecked, []string{"a", "b"}, false},
		{"a floor of schema and invariants is met by a list that asks nothing", schemaflux.ContractSchemaAndInvariantChecked, []string{"a", "a"}, false},
		{"a floor of evidence is not", schemaflux.ContractEvidenceChecked, []string{"a", "b"}, true},
		{"a floor of full governance is not", schemaflux.ContractFullyGoverned, []string{"a", "b"}, true},
		{"a floor above the level refuses a list that would ask nothing", schemaflux.ContractEvidenceChecked, []string{"a"}, true},
	}
	for _, tc := range cases {
		for _, withRecorder := range []bool{false, true} {
			name := tc.name
			if withRecorder {
				name += ", with a recorder"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var recorder schemaflux.Recorder
				model := namedModel(dedupeNoDuplicates)
				settings := []func(*schemaflux.Client){underFloor(tc.floor)}
				if withRecorder {
					settings = append(settings, recordingTo(&recorder))
				}
				res, err := schemaflux.Deduplicate(operationClient(model, settings...), tc.items, 0.8)
				if tc.refused {
					wantError(t, err, schemaflux.ErrPolicy, "deduplicate")
					if model.Calls() != 0 || !reflect.DeepEqual(res, dedupeEmpty()) {
						t.Errorf("%d requests were sent, result %+v", model.Calls(), res)
					}
				} else if err != nil || len(res.Unique) == 0 {
					t.Fatalf("got %+v, %v", res, err)
				}
				if withRecorder {
					r := onlyRecord(t, &recorder)
					if r.Op != "deduplicate" || (r.Err != nil) != tc.refused || r.Requested != schemaflux.ContractSchemaAndInvariantChecked {
						t.Errorf("record = %q err %v requested %v", r.Op, r.Err, r.Requested)
					}
					if tc.refused && r.Requests != 0 {
						t.Errorf("a refused call recorded %d requests", r.Requests)
					}
				}
			})
		}
	}
}

func TestDeduplicateUnderAnObserverSendsTheSameRequestAndRecordsOnce(t *testing.T) {
	// Not parallel: an observer is process state.
	var seen []string
	restore := telemetry.RegisterObserver(operationWatcher{finished: func(r telemetry.Result) {
		if r.Scope == telemetry.ScopeOperation {
			seen = append(seen, r.Operation)
		}
	}})
	defer restore()

	answer := `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9}]}`
	items := []string{"alpha", "beta", "gamma"}
	opts := schemaflux.Options{Steering: "prefer the first"}
	plain := answeringWith(answer)
	if _, err := schemaflux.Deduplicate(operationClient(plain), items, 0.8, opts); err != nil {
		t.Fatal(err)
	}
	var recorder schemaflux.Recorder
	watched := answeringWith(answer)
	res, err := schemaflux.Deduplicate(operationClient(watched, recordingTo(&recorder)), items, 0.8, opts)
	if err != nil || res.TotalRemoved != 1 {
		t.Fatalf("got %+v, %v", res, err)
	}
	if !reflect.DeepEqual(plain.Last(), watched.Last()) {
		t.Error("an observer changed what the model is asked")
	}
	if onlyRecord(t, &recorder).Op != "deduplicate" {
		t.Error("the Record under an observer has the wrong operation")
	}
	if len(seen) != 2 {
		t.Errorf("the observer saw %v, want one operation scope for each call", seen)
	}
}

func TestDeduplicateResultHasNoTagsSoItMarshalsWithTheNamesOfItsFields(t *testing.T) {
	t.Parallel()
	res := schemaflux.DeduplicateResult[string]{Unique: []string{"a"}, Duplicates: [][]string{{"a", "b"}}, TotalRemoved: 1, UniqueIndices: []int{0}, DuplicateIndices: [][]int{{0, 1}}}
	got, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Unique":["a"],"Duplicates":[["a","b"]],"TotalRemoved":1,"UniqueIndices":[0],"DuplicateIndices":[[0,1]]}`
	if string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDeduplicateKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-77c0e2"
	opts := schemaflux.Options{Steering: "steer " + canary}
	items := []map[string]string{{canary + "-key": "text " + canary}, {"k": "other " + canary}, {"k": "third"}}
	run := func(ctx context.Context) error {
		_, err := schemaflux.Deduplicate(ctx, items, 0.8, opts)
		return err
	}
	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9}]}`)
		}, true, run)
	})
	t.Run("an answer that is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the duplicates are " + canary) }, true, run)
	})
	t.Run("an answer whose list is text", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"duplicates":"` + canary + `"}`) }, true, run)
	})
	t.Run("an answer with an entry off the list", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"duplicates":[{"index":9,"duplicate_of":0,"similarity":0.9,"note":"` + canary + `"}]}`)
		}, true, run)
	})
	t.Run("an answer with a similarity off the scale", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"duplicates":[{"index":1,"duplicate_of":0,"similarity":5,"note":"` + canary + `"}]}`)
		}, true, run)
	})
	t.Run("an answer with a member that is not a number", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"duplicates":[{"index":"` + canary + `","duplicate_of":0,"similarity":0.9}]}`)
		}, true, run)
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, run)
	})
	t.Run("a provider that fails with an error of its own", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(errors.New("vendor said " + canary)) }, false, run)
	})
	t.Run("a refused setting", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(dedupeNoDuplicates) }, true, func(ctx context.Context) error {
			bad := opts
			bad.MaxOutputTokens = -1
			_, err := schemaflux.Deduplicate(ctx, items, 0.8, bad)
			return err
		})
	})
	t.Run("a refused threshold", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(dedupeNoDuplicates) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Deduplicate(ctx, items, 8, opts)
			return err
		})
	})
	t.Run("an item that cannot be written as JSON", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(dedupeNoDuplicates) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Deduplicate(ctx, []dedupeUnwritable{{canary}, {"other"}}, 0.8, opts)
			return err
		})
	})
	t.Run("one item", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(dedupeNoDuplicates) }, true, func(ctx context.Context) error {
			_, err := schemaflux.Deduplicate(ctx, items[:1], 0.8, opts)
			return err
		})
	})
}

func TestDeduplicateGivesTheSameResultAndSendsTheSameRequestInEveryStateOfTheCall(t *testing.T) {
	// Not parallel: it compares what each state sent with what the plain call sent.
	items := []string{"alpha", "beta", "gamma"}
	const answer = `{"duplicates":[{"index":1,"duplicate_of":0,"similarity":0.9},{"index":2,"duplicate_of":1,"similarity":0.6}]}`
	cases := []struct {
		threshold float64
		want      schemaflux.DeduplicateResult[string]
	}{
		{0.95, schemaflux.DeduplicateResult[string]{Unique: items, Duplicates: [][]string{}, UniqueIndices: []int{0, 1, 2}}},
		{0.8, schemaflux.DeduplicateResult[string]{Unique: []string{"alpha", "gamma"}, Duplicates: [][]string{{"alpha", "beta"}}, TotalRemoved: 1, UniqueIndices: []int{0, 2}, DuplicateIndices: [][]int{{0, 1}}}},
		{0.5, schemaflux.DeduplicateResult[string]{Unique: []string{"alpha"}, Duplicates: [][]string{items}, TotalRemoved: 2, UniqueIndices: []int{0}, DuplicateIndices: [][]int{{0, 1, 2}}}},
	}
	opts := schemaflux.Options{Steering: "be strict"}
	for _, tc := range cases {
		var recorder schemaflux.Recorder
		states := []struct {
			name  string
			model *schemafluxtest.Provider
			set   []func(*schemaflux.Client)
		}{
			{"plain", answeringWith(answer), nil},
			{"a recorder", answeringWith(answer), []func(*schemaflux.Client){recordingTo(&recorder)}},
			{"a floor it meets", namedModel(answer), []func(*schemaflux.Client){underFloor(schemaflux.ContractSchemaAndInvariantChecked)}},
		}
		var plainRequest schemaflux.Request
		for i, state := range states {
			res, err := schemaflux.Deduplicate(operationClient(state.model, state.set...), items, tc.threshold, opts)
			if err != nil || !reflect.DeepEqual(res, tc.want) {
				t.Errorf("threshold %v, %s: got %+v, %v, want %+v", tc.threshold, state.name, res, err, tc.want)
			}
			if i == 0 {
				plainRequest = state.model.Last()
			} else if !reflect.DeepEqual(state.model.Last(), plainRequest) {
				t.Errorf("threshold %v, %s: the request differs from the plain call's", tc.threshold, state.name)
			}
		}
	}
}

func TestDeduplicateAnItemThatCannotBeWrittenLeavesNoCauseInTheChainInAnyState(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-chain-91ab"
	items := []dedupeUnwritable{{canary}, {"other"}}
	for name, settings := range map[string][]func(*schemaflux.Client){"plain": nil, "a recorder": {recordingTo(new(schemaflux.Recorder))}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaflux.Deduplicate(operationClient(answeringWith(dedupeNoDuplicates), settings...), items, 0.8)
			wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
			for _, text := range chainText(err) {
				if strings.Contains(text, canary) {
					t.Errorf("the chain says %q", text)
				}
			}
		})
	}
}

func TestDeduplicateKeepsTheErrorBeneathAProvidersFailure(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("the provider's own error")
	for name, settings := range map[string][]func(*schemaflux.Client){"plain": nil, "a recorder": {recordingTo(new(schemaflux.Recorder))}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: schemaflux.ErrInvalidRequest, Status: 400, Err: sentinel})
			_, err := schemaflux.Deduplicate(operationClient(&model, settings...), []string{"a", "b"}, 0.8)
			wantError(t, err, schemaflux.ErrInvalidRequest, "deduplicate")
			if !errors.Is(err, sentinel) {
				t.Errorf("the error of the provider is not beneath %v", err)
			}
		})
	}
}

func TestDeduplicateKeepsTheContextErrorBeneathACancelledOrExpiredCall(t *testing.T) {
	t.Parallel()
	for name, settings := range map[string][]func(*schemaflux.Client){"plain": nil, "a recorder": {recordingTo(new(schemaflux.Recorder))}} {
		t.Run(name+", cancelled", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(operationClient(answeringWith(dedupeNoDuplicates), settings...))
			cancel()
			res, err := schemaflux.Deduplicate(ctx, []string{"a", "b"}, 0.8)
			wantError(t, err, schemaflux.ErrCanceled, "deduplicate")
			if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("errors.Is(context.Canceled) = %v, result %+v", errors.Is(err, context.Canceled), res)
			}
		})
		t.Run(name+", expired", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithDeadline(operationClient(answeringWith(dedupeNoDuplicates), settings...), time.Now().Add(-time.Second))
			defer cancel()
			res, err := schemaflux.Deduplicate(ctx, []string{"a", "b"}, 0.8)
			wantError(t, err, schemaflux.ErrTimeout, "deduplicate")
			if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(res, dedupeEmpty()) {
				t.Errorf("errors.Is(context.DeadlineExceeded) = %v, result %+v", errors.Is(err, context.DeadlineExceeded), res)
			}
		})
	}
}
