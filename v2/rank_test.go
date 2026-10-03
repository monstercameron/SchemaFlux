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

// rankScored scores the middle item best, then the first, then the last.
const rankScored = `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":2,"score":0.1}]}`

var rankItems = []string{"x", "y", "z"}

func rankedValues(res schemaflux.RankResult[string]) []string {
	var out []string
	for _, item := range res.Items {
		out = append(out, item.Item)
	}
	return out
}

func TestRankReturnsTheItemsBestFirstInTheOrderOfTheirScores(t *testing.T) {
	t.Parallel()
	model := answeringWith(rankScored)
	res, err := schemaflux.Rank(operationClient(model), rankItems, "postgres pooling")
	if err != nil {
		t.Fatal(err)
	}
	want := []schemaflux.RankedItem[string]{
		{Item: "y", Index: 1, Rank: 1, Score: 0.9},
		{Item: "x", Index: 0, Rank: 2, Score: 0.5},
		{Item: "z", Index: 2, Rank: 3, Score: 0.1},
	}
	if !reflect.DeepEqual(res.Items, want) || res.Query != "postgres pooling" || res.TotalItems != 3 || res.ReturnedItems != 3 {
		t.Errorf("got %+v", res)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, `Query: "postgres pooling"`)
	wantText(t, req, `[2] "z"`)
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestRankOrdersByScoreWhateverOrderTheModelListedThem(t *testing.T) {
	t.Parallel()
	res, err := schemaflux.Rank(operationClient(answeringWith(`{"rankings":[{"index":2,"score":0.1},{"index":0,"score":0.5},{"index":1,"score":0.9}]}`)), rankItems, "q")
	if err != nil || !reflect.DeepEqual(rankedValues(res), []string{"y", "x", "z"}) {
		t.Errorf("got %v, %v", rankedValues(res), err)
	}
}

func TestRankReturnsTheCallersOwnValuesWhenTwoItemsAreEqual(t *testing.T) {
	t.Parallel()
	items := []chooseLaptop{{"a", 1}, {"a", 1}}
	res, err := schemaflux.Rank(operationClient(answeringWith(`{"rankings":[{"index":0,"score":0.2},{"index":1,"score":0.8}]}`)), items, "q")
	if err != nil || len(res.Items) != 2 || res.Items[0].Index != 1 || res.Items[0].Item != items[1] {
		t.Errorf("got %+v, %v", res, err)
	}
}

func TestRankOfNoItemsIsAnEmptyResultAndSendsNothing(t *testing.T) {
	t.Parallel()
	for name, items := range map[string][]string{"nil": nil, "an empty list": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(rankScored)
			res, err := schemaflux.Rank(operationClient(model), items, "q")
			if err != nil || res.Items != nil || res.Query != "q" || res.TotalItems != 0 || res.ReturnedItems != 0 || model.Calls() != 0 {
				t.Errorf("got %+v, %v after %d requests", res, err, model.Calls())
			}
		})
	}
}

func TestRankRefusesABlankQueryAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]string{"empty": "", "spaces": "   ", "a newline": "\n"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(rankScored)
			res, err := schemaflux.Rank(operationClient(model), rankItems, query)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "rank")
			if e.Message != "query is blank" || res.Items != nil || res.Query != "" || model.Calls() != 0 {
				t.Errorf("message %q, result %+v, %d requests", e.Message, res, model.Calls())
			}
		})
	}
}

func TestRankWithNoOptionsKeepsEveryItemAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(rankScored)
	if _, err := schemaflux.Rank(operationClient(model), rankItems, "q"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Minimum score threshold: 0.00")
	wantNoText(t, req, "Ranking factors")
	wantNoText(t, req, "Boost:")
	wantNoText(t, req, "Penalize:")
	wantNoText(t, req, "brief explanation")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestRankEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.RankOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"MinScore", schemaflux.RankOptions{MinScore: 0.25}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum score threshold: 0.25")
		}},
		{"Factors", schemaflux.RankOptions{Factors: []string{"price", "speed"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Ranking factors to consider: "price", "speed"`)
		}},
		{"FactorWeights", schemaflux.RankOptions{Factors: []string{"price", "speed"}, FactorWeights: map[string]float64{"price": 0.4, "speed": 0.6}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Factor weights: "price"=0.40, "speed"=0.60`)
		}},
		{"BoostFields", schemaflux.RankOptions{BoostFields: map[string]float64{"recent": 1.5}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Boost: "recent"(+1.5x)`)
		}},
		{"PenalizeFields", schemaflux.RankOptions{PenalizeFields: map[string]float64{"stale": 2}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Penalize: "stale"(-2.0x)`)
		}},
		{"Steering", schemaflux.RankOptions{Options: schemaflux.Options{Steering: "newer is better"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "newer is better")
		}},
		{"Model", schemaflux.RankOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.RankOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.RankOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.RankOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.RankOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.RankOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(rankScored)
			if _, err := schemaflux.Rank(operationClient(model), rankItems, "q", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestRankAppliesTopKAndMinScoreToTheOrderedResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.RankOptions
		want []string
	}{
		{"neither", schemaflux.RankOptions{}, []string{"y", "x", "z"}},
		{"TopK of two", schemaflux.RankOptions{TopK: 2}, []string{"y", "x"}},
		{"TopK of one", schemaflux.RankOptions{TopK: 1}, []string{"y"}},
		{"TopK above the count", schemaflux.RankOptions{TopK: 9}, []string{"y", "x", "z"}},
		{"MinScore drops the low", schemaflux.RankOptions{MinScore: 0.3}, []string{"y", "x"}},
		{"MinScore of exactly a score keeps it", schemaflux.RankOptions{MinScore: 0.5}, []string{"y", "x"}},
		{"MinScore above all", schemaflux.RankOptions{MinScore: 0.95}, nil},
		{"both", schemaflux.RankOptions{TopK: 1, MinScore: 0.3}, []string{"y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Rank(operationClient(answeringWith(rankScored)), rankItems, "q", tc.opts)
			if err != nil || !reflect.DeepEqual(rankedValues(res), tc.want) || res.ReturnedItems != len(tc.want) || res.TotalItems != 3 {
				t.Errorf("got %v (returned %d of %d), %v", rankedValues(res), res.ReturnedItems, res.TotalItems, err)
			}
			for i, item := range res.Items {
				if item.Rank != i+1 {
					t.Errorf("item %d has rank %d", i, item.Rank)
				}
			}
		})
	}
}

func TestRankReturnsTheExplanationAndFactorScoresTheModelGave(t *testing.T) {
	t.Parallel()
	body := `{"rankings":[{"index":0,"score":0.5,"explanation":"fits","factor_scores":{"price":0.4}},{"index":1,"score":0.9,"explanation":"best","factor_scores":{"price":0.9}},{"index":2,"score":0.1,"explanation":"poor"}]}`
	res, err := schemaflux.Rank(operationClient(answeringWith(body)), rankItems, "q", schemaflux.RankOptions{Factors: []string{"price"}, IncludeExplanation: true})
	if err != nil {
		t.Fatal(err)
	}
	if first := res.Items[0]; first.Explanation != "best" || first.FactorScores["price"] != 0.9 {
		t.Errorf("first = %+v", first)
	}
	if last := res.Items[2]; last.Explanation != "poor" || last.FactorScores != nil {
		t.Errorf("last = %+v", last)
	}
}

func TestRankIncludeExplanationAsksForOneAndRefusesAnAnswerWithout(t *testing.T) {
	t.Parallel()
	model := answeringWith(rankScored)
	_, err := schemaflux.Rank(operationClient(model), rankItems, "q", schemaflux.RankOptions{IncludeExplanation: true})
	wantError(t, err, schemaflux.ErrInvalidAnswer, "rank")
	wantText(t, model.Last(), "Include a brief explanation for each item's ranking.")
}

func TestRankRefusesAnAnswerThatDoesNotRankEveryItemOnce(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"an item left out":            `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9}]}`,
		"an item twice":               `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":1,"score":0.2}]}`,
		"an index that is not one":    `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":3,"score":0.2}]}`,
		"a negative index":            `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":-1,"score":0.2}]}`,
		"a score above one":           `{"rankings":[{"index":0,"score":1.5},{"index":1,"score":0.9},{"index":2,"score":0.2}]}`,
		"a negative score":            `{"rankings":[{"index":0,"score":-0.1},{"index":1,"score":0.9},{"index":2,"score":0.2}]}`,
		"a null list":                 `{"rankings":null}`,
		"no list":                     `{}`,
		"a bare list":                 `[{"index":0,"score":0.5}]`,
		"text":                        `The second is best.`,
		"a factor that was not asked": `{"rankings":[{"index":0,"score":0.5,"factor_scores":{"other":1}},{"index":1,"score":0.9},{"index":2,"score":0.2}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, err := schemaflux.Rank(operationClient(answeringWith(body)), rankItems, "q", schemaflux.RankOptions{Factors: []string{"price"}})
			wantError(t, err, schemaflux.ErrInvalidAnswer, "rank")
			if res.Items != nil || res.Query != "" || res.TotalItems != 0 {
				t.Errorf("a refused answer left %+v beside the error", res)
			}
		})
	}
}

func TestRankRefusesABadOptionBeforeAnythingIsSentEvenForNoItems(t *testing.T) {
	t.Parallel()
	nan := math.NaN()
	cases := map[string]schemaflux.RankOptions{
		"a negative TopK":                  {TopK: -1},
		"a MinScore below zero":            {MinScore: -0.1},
		"a MinScore above one":             {MinScore: 1.1},
		"a MinScore that is no number":     {MinScore: nan},
		"a MinScore of infinity":           {MinScore: math.Inf(1)},
		"a weight for a factor not listed": {Factors: []string{"a"}, FactorWeights: map[string]float64{"b": 1}},
		"a weight with no factors":         {FactorWeights: map[string]float64{"a": 1}},
		"a weight that is no number":       {Factors: []string{"a"}, FactorWeights: map[string]float64{"a": nan}},
		"a boost that is no number":        {BoostFields: map[string]float64{"a": nan}},
		"a boost of infinity":              {BoostFields: map[string]float64{"a": math.Inf(1)}},
		"a penalty that is no number":      {PenalizeFields: map[string]float64{"a": nan}},
		"a negative output ceiling":        {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":           {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":           {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		for _, items := range [][]string{rankItems, nil} {
			t.Run(fmt.Sprintf("%s, %d items", name, len(items)), func(t *testing.T) {
				t.Parallel()
				model := answeringWith(rankScored)
				_, err := schemaflux.Rank(operationClient(model), items, "q", opts)
				wantError(t, err, schemaflux.ErrConfiguration, "rank")
				if model.Calls() != 0 {
					t.Errorf("%d requests were sent", model.Calls())
				}
			})
		}
	}
}

func TestRankNamesTheFieldOfABadOptionAndNeverAKeyOfAMap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.RankOptions
		want string
	}{
		{"TopK", schemaflux.RankOptions{TopK: -3}, "TopK must not be negative"},
		{"MinScore", schemaflux.RankOptions{MinScore: 2}, "MinScore must be a number from 0 to 1"},
		{"a weight key", schemaflux.RankOptions{Factors: []string{"a"}, FactorWeights: map[string]float64{"CANARY-key": 1}}, "FactorWeights has a key that is not in Factors"},
		{"a weight", schemaflux.RankOptions{Factors: []string{"CANARY-key"}, FactorWeights: map[string]float64{"CANARY-key": math.NaN()}}, "FactorWeights has a weight that is not a finite number"},
		{"a boost", schemaflux.RankOptions{BoostFields: map[string]float64{"CANARY-key": math.Inf(-1)}}, "BoostFields has a weight that is not a finite number"},
		{"a penalty", schemaflux.RankOptions{PenalizeFields: map[string]float64{"CANARY-key": math.NaN()}}, "PenalizeFields has a weight that is not a finite number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := schemaflux.Rank(operationClient(answeringWith(rankScored)), rankItems, "q", tc.opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "rank")
			if e.Message != tc.want {
				t.Errorf("message = %q, want %q", e.Message, tc.want)
			}
			for _, text := range chainText(err) {
				if strings.Contains(text, "CANARY") {
					t.Errorf("the key reached %q", text)
				}
			}
		})
	}
}

func TestRankRefusesAnItemJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(rankScored)
	_, err := schemaflux.Rank(operationClient(model), []any{"fine", make(chan int)}, "q")
	wantError(t, err, schemaflux.ErrInvalidRequest, "rank")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestRankWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Rank(nilContext, rankItems, "q")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "rank"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestRankMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(rankScored)
		res, err := schemaflux.Rank(operationClient(model), rankItems, "q",
			schemaflux.RankOptions{TopK: 2, MinScore: 0.2, Factors: []string{"first"}},
			schemaflux.RankOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.RankOptions{MinScore: 0.3})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Minimum score threshold: 0.30")
		wantText(t, req, `"first"`)
		wantText(t, req, "later")
		if !reflect.DeepEqual(rankedValues(res), []string{"y", "x"}) {
			t.Errorf("got %v", rankedValues(res))
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(rankScored)
		_, err := schemaflux.Rank(operationClient(model), rankItems, "q", schemaflux.RankOptions{IncludeExplanation: true}, schemaflux.RankOptions{})
		wantError(t, err, schemaflux.ErrInvalidAnswer, "rank")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Rank(operationClient(answeringWith(rankScored)), rankItems, "q", schemaflux.RankOptions{}, schemaflux.RankOptions{TopK: -1})
		wantError(t, err, schemaflux.ErrConfiguration, "rank")
	})
}

func TestRankReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			res, err := schemaflux.Rank(operationClient(&model), rankItems, "q")
			wantError(t, err, tc.err.Kind, "rank")
			if res.Items != nil || res.Query != "" {
				t.Errorf("a failed call left %+v beside the error", res)
			}
		})
	}
}

func TestRankResultMarshalsWithTheTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	res := schemaflux.RankResult[string]{
		Items: []schemaflux.RankedItem[string]{{Item: "y", Index: 1, Rank: 1, Score: 0.9, Explanation: "e", FactorScores: map[string]float64{"f": 0.5}}},
		Query: "q", TotalItems: 3, ReturnedItems: 1,
	}
	got, err := collectionJSON(res)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"items":[{"item":"y","index":1,"rank":1,"score":0.9,"explanation":"e","factor_scores":{"f":0.5}}],"query":"q","total_items":3,"returned_items":1}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
