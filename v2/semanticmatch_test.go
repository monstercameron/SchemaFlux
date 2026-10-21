package schemaflux_test

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// matchTwoPairs pairs the first source with the second target and the second
// source with the third, and leaves the first target unmatched.
const matchTwoPairs = `{"matches":[` +
	`{"source_index":0,"target_index":1,"score":0.9,"explanation":"same role","is_partial":false},` +
	`{"source_index":1,"target_index":2,"score":0.6,"explanation":"near","is_partial":false}],` +
	`"unmatched_sources":[],"unmatched_targets":[0]}`

var (
	matchSources = []string{"r1", "r2"}
	matchTargets = []string{"j1", "j2", "j3"}
)

func matchOf(t *testing.T, model schemaflux.Provider, opts ...schemaflux.SemanticMatchOptions) (schemaflux.SemanticMatchResult[string, string], error) {
	t.Helper()
	return schemaflux.SemanticMatch(operationClient(model), matchSources, matchTargets, opts...)
}

func TestSemanticMatchReturnsThePairsWithTheCallersOwnValuesAndPositions(t *testing.T) {
	t.Parallel()
	model := answeringWith(matchTwoPairs)
	res, err := matchOf(t, model, schemaflux.SemanticMatchOptions{MaxMatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []schemaflux.MatchPair[string, string]{
		{Source: "r1", SourceIndex: 0, Target: "j2", TargetIndex: 1, Score: 0.9, Explanation: "same role"},
		{Source: "r2", SourceIndex: 1, Target: "j3", TargetIndex: 2, Score: 0.6, Explanation: "near"},
	}
	if !reflect.DeepEqual(res.Matches, want) || res.TotalMatches != 2 || math.Abs(res.AverageScore-0.75) > 1e-9 {
		t.Errorf("got %+v", res)
	}
	if len(res.UnmatchedSources) != 0 || !reflect.DeepEqual(res.UnmatchedTargets, []int{0}) {
		t.Errorf("unmatched sources %v targets %v", res.UnmatchedSources, res.UnmatchedTargets)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, `[S0] "r1"`)
	wantText(t, req, `[T2] "j3"`)
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestSemanticMatchOfOneSourceIsTheOldMatchOne(t *testing.T) {
	t.Parallel()
	body := `{"matches":[{"source_index":0,"target_index":2,"score":0.8,"explanation":"x","is_partial":false}],"unmatched_sources":[],"unmatched_targets":[0,1]}`
	res, err := schemaflux.SemanticMatch(operationClient(answeringWith(body)), []string{"only"}, matchTargets)
	if err != nil || len(res.Matches) != 1 || res.Matches[0].Target != "j3" || !reflect.DeepEqual(res.UnmatchedTargets, []int{0, 1}) {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestSemanticMatchOfAnEmptyListLeavesEveryItemUnmatchedAndSendsNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		sources   []string
		targets   []string
		unmatched [2][]int
	}{
		{"no sources", nil, matchTargets, [2][]int{nil, {0, 1, 2}}},
		{"no targets", matchSources, nil, [2][]int{{0, 1}, nil}},
		{"neither", []string{}, []string{}, [2][]int{nil, nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(matchTwoPairs)
			res, err := schemaflux.SemanticMatch(operationClient(model), tc.sources, tc.targets)
			if err != nil || res.Matches != nil || res.TotalMatches != 0 || res.AverageScore != 0 || model.Calls() != 0 {
				t.Fatalf("got %+v, %v after %d requests", res, err, model.Calls())
			}
			if !reflect.DeepEqual(res.UnmatchedSources, tc.unmatched[0]) || !reflect.DeepEqual(res.UnmatchedTargets, tc.unmatched[1]) {
				t.Errorf("unmatched sources %v targets %v", res.UnmatchedSources, res.UnmatchedTargets)
			}
		})
	}
}

func TestSemanticMatchWithNoOptionsAsksForTheBestFitAtHalfAndAllowsPartialPairs(t *testing.T) {
	t.Parallel()
	model := answeringWith(matchTwoPairs)
	if _, err := matchOf(t, model); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Find the single best match for each source item.")
	wantText(t, req, "Maximum 1 matches per source item.")
	wantText(t, req, "Minimum similarity threshold: 0.50")
	wantText(t, req, "Allow partial matches (flag them in output).")
	wantText(t, req, "Include brief explanations for why items matched.")
	wantNoText(t, req, "Match on these fields")
	wantNoText(t, req, "Matching criteria")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestSemanticMatchEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.SemanticMatchOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Criteria", schemaflux.SemanticMatchOptions{Criteria: "same seniority"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Matching criteria: "same seniority"`)
		}},
		{"Strategy", schemaflux.SemanticMatchOptions{Strategy: "all-matches"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Find all matches above threshold for each source item.")
		}},
		{"Strategy one-to-many", schemaflux.SemanticMatchOptions{Strategy: "one-to-many"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Each source can match multiple targets.")
		}},
		{"Threshold", schemaflux.SemanticMatchOptions{Threshold: 0.55}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum similarity threshold: 0.55")
		}},
		{"Threshold negative means zero", schemaflux.SemanticMatchOptions{Threshold: -1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum similarity threshold: 0.00")
		}},
		{"MaxMatches", schemaflux.SemanticMatchOptions{MaxMatches: 3}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Maximum 3 matches per source item.")
		}},
		{"MaxMatches negative means no limit", schemaflux.SemanticMatchOptions{MaxMatches: -1}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Maximum")
		}},
		{"Fields", schemaflux.SemanticMatchOptions{Fields: []string{"name", "sku"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Match on these fields: ["name","sku"]`)
		}},
		{"FieldWeights", schemaflux.SemanticMatchOptions{Fields: []string{"sku"}, FieldWeights: map[string]float64{"sku": 2}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Field weights: "sku"=2.0x`)
		}},
		{"OmitExplanations", schemaflux.SemanticMatchOptions{OmitExplanations: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Include brief explanations")
		}},
		{"NoPartial", schemaflux.SemanticMatchOptions{NoPartial: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Allow partial matches")
		}},
		{"Bidirectional", schemaflux.SemanticMatchOptions{Bidirectional: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "A match must hold in both directions")
		}},
		{"Steering", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Steering: "prefer recent"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "prefer recent")
		}},
		{"Model", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(matchTwoPairs)
			if _, err := matchOf(t, model, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

// matchPairs writes the answer to matchSources and matchTargets for the pairs
// given as {source, target, score}, with the unmatched lists worked out.
func matchPairs(partial bool, pairs ...[3]float64) string {
	var entries []string
	sources, targets := map[int]bool{}, map[int]bool{}
	for _, p := range pairs {
		entries = append(entries, fmt.Sprintf(`{"source_index":%d,"target_index":%d,"score":%v,"explanation":"e","is_partial":%v}`, int(p[0]), int(p[1]), p[2], partial))
		sources[int(p[0])], targets[int(p[1])] = true, true
	}
	list := func(total int, used map[int]bool) string {
		var out []string
		for i := 0; i < total; i++ {
			if !used[i] {
				out = append(out, fmt.Sprint(i))
			}
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	return `{"matches":[` + strings.Join(entries, ",") + `],"unmatched_sources":` + list(2, sources) + `,"unmatched_targets":` + list(3, targets) + `}`
}

func TestSemanticMatchEnforcesWhatItsOptionsSayItChecks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		opts    schemaflux.SemanticMatchOptions
		refused bool
	}{
		{"a pair above the default threshold", matchPairs(false, [3]float64{0, 0, 0.5}), schemaflux.SemanticMatchOptions{}, false},
		{"a pair below the default threshold", matchPairs(false, [3]float64{0, 0, 0.49}), schemaflux.SemanticMatchOptions{}, true},
		{"a pair below a higher threshold", matchPairs(false, [3]float64{0, 0, 0.7}), schemaflux.SemanticMatchOptions{Threshold: 0.8}, true},
		{"a pair at a threshold", matchPairs(false, [3]float64{0, 0, 0.8}), schemaflux.SemanticMatchOptions{Threshold: 0.8}, false},
		{"a low pair with a negative threshold", matchPairs(false, [3]float64{0, 0, 0.01}), schemaflux.SemanticMatchOptions{Threshold: -1}, false},
		{"a partial pair by default", matchPairs(true, [3]float64{0, 0, 0.9}), schemaflux.SemanticMatchOptions{}, false},
		{"a partial pair with NoPartial", matchPairs(true, [3]float64{0, 0, 0.9}), schemaflux.SemanticMatchOptions{NoPartial: true}, true},
		{"two pairs for a source by default", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{0, 1, 0.8}), schemaflux.SemanticMatchOptions{}, true},
		{"two pairs for a source with MaxMatches 2", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{0, 1, 0.8}), schemaflux.SemanticMatchOptions{MaxMatches: 2}, false},
		{"three pairs for a source with MaxMatches 2", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{0, 1, 0.8}, [3]float64{0, 2, 0.7}), schemaflux.SemanticMatchOptions{MaxMatches: 2}, true},
		{"three pairs for a source with no limit", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{0, 1, 0.8}, [3]float64{0, 2, 0.7}), schemaflux.SemanticMatchOptions{MaxMatches: -1}, false},
		{"a target used twice by default", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{1, 0, 0.8}), schemaflux.SemanticMatchOptions{}, false},
		{"a target used twice one-to-one", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{1, 0, 0.8}), schemaflux.SemanticMatchOptions{Strategy: "one-to-one"}, true},
		{"a pair beaten for its target", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{1, 0, 0.8}), schemaflux.SemanticMatchOptions{Bidirectional: true}, true},
		{"pairs that beat nobody", matchPairs(false, [3]float64{0, 0, 0.9}, [3]float64{1, 1, 0.8}), schemaflux.SemanticMatchOptions{Bidirectional: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := matchOf(t, answeringWith(tc.body), tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "semanticmatch")
			if res.Matches != nil || res.UnmatchedSources != nil || res.TotalMatches != 0 {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestSemanticMatchReturnsThePartialFlagAndFieldScoresTheModelGave(t *testing.T) {
	t.Parallel()
	body := `{"matches":[{"source_index":0,"target_index":0,"score":0.7,"explanation":"e","is_partial":true,"field_scores":{"name":0.9}}],"unmatched_sources":[1],"unmatched_targets":[1,2]}`
	res, err := matchOf(t, answeringWith(body))
	if err != nil || len(res.Matches) != 1 || !res.Matches[0].IsPartial || res.Matches[0].FieldScores["name"] != 0.9 {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestSemanticMatchRefusesAnAnswerThatIsNotARelationBetweenTheTwoLists(t *testing.T) {
	t.Parallel()
	pair := func(s, tg int, score string) string {
		return fmt.Sprintf(`{"source_index":%d,"target_index":%d,"score":%s,"explanation":"e","is_partial":false}`, s, tg, score)
	}
	cases := map[string]string{
		"a source that does not exist":       `{"matches":[` + pair(5, 0, "0.9") + `],"unmatched_sources":[0,1],"unmatched_targets":[1,2]}`,
		"a target that does not exist":       `{"matches":[` + pair(0, 7, "0.9") + `],"unmatched_sources":[1],"unmatched_targets":[0,1,2]}`,
		"a negative index":                   `{"matches":[` + pair(-1, 0, "0.9") + `],"unmatched_sources":[0,1],"unmatched_targets":[1,2]}`,
		"a score above one":                  `{"matches":[` + pair(0, 0, "1.5") + `],"unmatched_sources":[1],"unmatched_targets":[1,2]}`,
		"a negative score":                   `{"matches":[` + pair(0, 0, "-0.5") + `],"unmatched_sources":[1],"unmatched_targets":[1,2]}`,
		"a pair twice":                       `{"matches":[` + pair(0, 0, "0.9") + `,` + pair(0, 0, "0.9") + `],"unmatched_sources":[1],"unmatched_targets":[1,2]}`,
		"a matched source listed unmatched":  `{"matches":[` + pair(0, 0, "0.9") + `],"unmatched_sources":[0,1],"unmatched_targets":[1,2]}`,
		"an item neither matched nor listed": `{"matches":[` + pair(0, 0, "0.9") + `],"unmatched_sources":[],"unmatched_targets":[1,2]}`,
		"an unmatched item listed twice":     `{"matches":[` + pair(0, 0, "0.9") + `],"unmatched_sources":[1,1],"unmatched_targets":[1,2]}`,
		"no matches member":                  `{"unmatched_sources":[0,1],"unmatched_targets":[0,1,2]}`,
		"text":                               `r1 goes with j1.`,
		"a bare list":                        `[]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := matchOf(t, answeringWith(body))
			wantError(t, err, schemaflux.ErrInvalidAnswer, "semanticmatch")
			if res.Matches != nil || res.UnmatchedSources != nil || res.UnmatchedTargets != nil {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestSemanticMatchRefusesABadOptionBeforeAnythingIsSentEvenForAnEmptyList(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.SemanticMatchOptions{
		"a strategy that is not one":    {Strategy: "closest"},
		"a threshold above one":         {Threshold: 1.5},
		"a threshold that is no number": {Threshold: math.NaN()},
		"a threshold of infinity":       {Threshold: math.Inf(1)},
		"a negative output ceiling":     {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":        {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":        {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, empty %v", name, empty), func(t *testing.T) {
				t.Parallel()
				model := answeringWith(matchTwoPairs)
				sources := matchSources
				if empty {
					sources = nil
				}
				_, err := schemaflux.SemanticMatch(operationClient(model), sources, matchTargets, opts)
				wantError(t, err, schemaflux.ErrConfiguration, "semanticmatch")
				if model.Calls() != 0 {
					t.Errorf("%d requests were sent", model.Calls())
				}
			})
		}
	}
}

func TestSemanticMatchNamesTheFieldOfABadOptionAndNotItsValue(t *testing.T) {
	t.Parallel()
	_, err := matchOf(t, answeringWith(matchTwoPairs), schemaflux.SemanticMatchOptions{Strategy: "CANARY-strategy"})
	e := wantError(t, err, schemaflux.ErrConfiguration, "semanticmatch")
	if want := "Strategy is not one of best-fit, all-matches, one-to-one and one-to-many"; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	_, err = matchOf(t, answeringWith(matchTwoPairs), schemaflux.SemanticMatchOptions{Threshold: 2})
	if e := wantError(t, err, schemaflux.ErrConfiguration, "semanticmatch"); e.Message != "Threshold must be a number up to 1" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestSemanticMatchRefusesAnItemJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(matchTwoPairs)
	_, err := schemaflux.SemanticMatch(operationClient(model), []any{"fine", make(chan int)}, matchTargets)
	wantError(t, err, schemaflux.ErrInvalidRequest, "semanticmatch")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSemanticMatchWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.SemanticMatch(nilContext, matchSources, matchTargets)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "semanticmatch"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestSemanticMatchMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(matchTwoPairs)
		_, err := matchOf(t, model,
			schemaflux.SemanticMatchOptions{Threshold: 0.4, Criteria: "first", MaxMatches: 2},
			schemaflux.SemanticMatchOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.SemanticMatchOptions{Threshold: 0.6})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Minimum similarity threshold: 0.60")
		wantText(t, req, `Matching criteria: "first"`)
		wantText(t, req, "Maximum 2 matches")
		wantText(t, req, "later")
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(matchTwoPairs)
		_, err := matchOf(t, model, schemaflux.SemanticMatchOptions{OmitExplanations: true}, schemaflux.SemanticMatchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Include brief explanations")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := matchOf(t, answeringWith(matchTwoPairs), schemaflux.SemanticMatchOptions{}, schemaflux.SemanticMatchOptions{Strategy: "closest"})
		wantError(t, err, schemaflux.ErrConfiguration, "semanticmatch")
	})
}

func TestSemanticMatchReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			res, err := matchOf(t, &model)
			wantError(t, err, tc.err.Kind, "semanticmatch")
			if res.Matches != nil || res.UnmatchedTargets != nil {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestSemanticMatchResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	res := schemaflux.SemanticMatchResult[string, string]{
		Matches:          []schemaflux.MatchPair[string, string]{{Source: "a", SourceIndex: 0, Target: "b", TargetIndex: 1, Score: 0.5, Explanation: "e", FieldScores: map[string]float64{"f": 1}, IsPartial: true}},
		UnmatchedSources: []int{1}, UnmatchedTargets: []int{0}, TotalMatches: 1, AverageScore: 0.5,
	}
	got, err := collectionJSON(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"matches":[{"source":"a","source_index":0,"target":"b","target_index":1,"score":0.5,"explanation":"e","field_scores":{"f":1},"is_partial":true}],"unmatched_sources":[1],"unmatched_targets":[0],"total_matches":1,"average_score":0.5}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
