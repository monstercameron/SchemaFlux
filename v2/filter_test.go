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

const filterKeepsFirstAndThird = `{"ids":["i-000001","i-000003"]}`

var filterItems = []string{"a", "b", "c", "d"}

func TestFilterReturnsTheItemsTheModelKeptInTheirOriginalOrder(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"ids":["i-000003","i-000001"]}`)
	got, err := schemaflux.Filter(operationClient(model), filterItems, "short names")
	if err != nil || !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("got %v, %v, want [a c] in the order of the input", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "Filter criteria: short names")
	wantText(t, req, `{"id":"i-000004","item":"d"}`)
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestFilterReturnsTheCallersOwnValuesWhenTwoItemsAreEqual(t *testing.T) {
	t.Parallel()
	items := []chooseLaptop{{"a", 1}, {"a", 1}, {"b", 2}}
	got, err := schemaflux.Filter(operationClient(answeringWith(`{"ids":["i-000002"]}`)), items, "the second")
	if err != nil || len(got) != 1 || got[0] != items[1] {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestFilterAcceptsAnAnswerThatKeepsNothingOrEverything(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		body string
		want []string
	}{
		"nothing":    {`{"ids":[]}`, []string{}},
		"everything": {`{"ids":["i-000001","i-000002","i-000003","i-000004"]}`, filterItems},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Filter(operationClient(answeringWith(tc.body)), filterItems, "x")
			if err != nil || len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
				t.Errorf("got %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestFilterOfNoItemsIsTheInputSliceAndSendsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{"nil": nil, "an empty list": {}}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(filterKeepsFirstAndThird)
			got, err := schemaflux.Filter(operationClient(model), items, "x")
			if err != nil || len(got) != 0 || (got == nil) != (items == nil) || model.Calls() != 0 {
				t.Errorf("got %#v, %v after %d requests", got, err, model.Calls())
			}
		})
	}
}

func TestFilterRefusesBlankCriteriaAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, criteria := range map[string]string{"empty": "", "spaces": "   ", "a newline": "\n\t"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(filterKeepsFirstAndThird)
			got, err := schemaflux.Filter(operationClient(model), filterItems, criteria)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "filter")
			if e.Message != "criteria is blank" || got != nil || model.Calls() != 0 {
				t.Errorf("message %q, result %v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestFilterWithNoOptionsKeepsTheMatchesAtSevenTenthsConfidence(t *testing.T) {
	t.Parallel()
	model := answeringWith(filterKeepsFirstAndThird)
	if _, err := schemaflux.Filter(operationClient(model), filterItems, "x"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Keep items that match the criteria")
	wantText(t, req, "Minimum confidence: 0.70")
	wantText(t, req, "- Return the ids of the items that match the criteria and discard the rest")
	wantNoText(t, req, "Include reasons")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestFilterEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.FilterOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Exclude", schemaflux.FilterOptions{Exclude: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Remove items that match the criteria")
			wantText(t, req, "- Discard the ids of the items that match the criteria and return the ids of the rest")
			wantNoText(t, req, "Keep items that match")
		}},
		{"MinConfidence", schemaflux.FilterOptions{MinConfidence: 0.9}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum confidence: 0.90")
		}},
		{"MinConfidence negative leaves the instruction out", schemaflux.FilterOptions{MinConfidence: -1}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Minimum confidence")
		}},
		{"MinConfidence of one", schemaflux.FilterOptions{MinConfidence: 1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Minimum confidence: 1.00")
		}},
		{"IncludeReasons", schemaflux.FilterOptions{IncludeReasons: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Include reasons for each decision")
		}},
		{"Steering", schemaflux.FilterOptions{Options: schemaflux.Options{Steering: "be generous"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be generous")
		}},
		{"Model", schemaflux.FilterOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.FilterOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.FilterOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.FilterOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.FilterOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.FilterOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(filterKeepsFirstAndThird)
			if _, err := schemaflux.Filter(operationClient(model), filterItems, "x", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestFilterRefusesAnAnswerThatIsNotASubsetOfTheItems(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"an id that was not offered": `{"ids":["i-000001","i-000009"]}`,
		"an id twice":                `{"ids":["i-000001","i-000001"]}`,
		"the item itself":            `{"ids":["a"]}`,
		"a null list":                `{"ids":null}`,
		"no list":                    `{}`,
		"a bare list":                `["i-000001"]`,
		"text":                       `Keep the first.`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Filter(operationClient(answeringWith(body)), filterItems, "x")
			wantError(t, err, schemaflux.ErrInvalidAnswer, "filter")
			if got != nil {
				t.Errorf("a refused answer left %v beside the error", got)
			}
		})
	}
}

func TestFilterRefusesABadOptionBeforeAnythingIsSentEvenForNoItems(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.FilterOptions{
		"a confidence above one":         {MinConfidence: 1.01},
		"a confidence that is no number": {MinConfidence: math.NaN()},
		"a confidence of infinity":       {MinConfidence: math.Inf(1)},
		"a negative infinity":            {MinConfidence: math.Inf(-1)},
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		for _, items := range [][]string{filterItems, nil} {
			t.Run(fmt.Sprintf("%s, %d items", name, len(items)), func(t *testing.T) {
				t.Parallel()
				model := answeringWith(filterKeepsFirstAndThird)
				_, err := schemaflux.Filter(operationClient(model), items, "x", opts)
				wantError(t, err, schemaflux.ErrConfiguration, "filter")
				if model.Calls() != 0 {
					t.Errorf("%d requests were sent", model.Calls())
				}
			})
		}
	}
}

func TestFilterNamesTheFieldOfABadConfidence(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Filter(operationClient(answeringWith(filterKeepsFirstAndThird)), filterItems, "x", schemaflux.FilterOptions{MinConfidence: 2})
	e := wantError(t, err, schemaflux.ErrConfiguration, "filter")
	if e.Message != "MinConfidence must be a number up to 1" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestFilterRefusesAnItemJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(filterKeepsFirstAndThird)
	_, err := schemaflux.Filter(operationClient(model), []any{"fine", make(chan int)}, "x")
	wantError(t, err, schemaflux.ErrInvalidRequest, "filter")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestFilterWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Filter(nilContext, filterItems, "x")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "filter"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestFilterMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later confidence wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(filterKeepsFirstAndThird)
		_, err := schemaflux.Filter(operationClient(model), filterItems, "x",
			schemaflux.FilterOptions{MinConfidence: 0.6, Options: schemaflux.Options{Model: "first"}},
			schemaflux.FilterOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.FilterOptions{MinConfidence: 0.8})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Minimum confidence: 0.80")
		wantText(t, req, "later")
		if req.Model != "first" {
			t.Errorf("model = %q", req.Model)
		}
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(filterKeepsFirstAndThird)
		_, err := schemaflux.Filter(operationClient(model), filterItems, "x", schemaflux.FilterOptions{Exclude: true}, schemaflux.FilterOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Remove items that match the criteria")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Filter(operationClient(answeringWith(filterKeepsFirstAndThird)), filterItems, "x",
			schemaflux.FilterOptions{}, schemaflux.FilterOptions{MinConfidence: 5})
		wantError(t, err, schemaflux.ErrConfiguration, "filter")
	})
}

func TestFilterSplitsALongListIntoSeveralCallsAndKeepsEachChunkToItsOwnIds(t *testing.T) {
	t.Parallel()
	items := make([]int, 1500)
	for i := range items {
		items[i] = i
	}
	model := answeringWith(`{"ids":["i-000001"]}`)
	got, err := schemaflux.Filter(operationClient(model), items, "the first of each chunk")
	if err != nil {
		t.Fatal(err)
	}
	if model.Calls() < 2 || len(got) != model.Calls() || got[0] != 0 {
		t.Fatalf("%d requests and %d items kept: %v", model.Calls(), len(got), got[:min(len(got), 5)])
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("the items are not in their original order: %v", got)
		}
	}
}

func TestFilterReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			got, err := schemaflux.Filter(operationClient(&model), filterItems, "x")
			wantError(t, err, tc.err.Kind, "filter")
			if got != nil {
				t.Errorf("a failed call left %v beside the error", got)
			}
		})
	}
}

func TestFilterReadsAnyKindOfItem(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"ids":["i-000002"]}`)
	got, err := schemaflux.Filter(operationClient(model), []map[string]string{{"k": "vv"}, {"k": "ww"}}, "x")
	if err != nil || len(got) != 1 || got[0]["k"] != "ww" {
		t.Fatalf("got %v, %v", got, err)
	}
	if !strings.Contains(model.Last().User, `"k":"vv"`) {
		t.Errorf("the user message does not carry the items: %s", excerpt(model.Last().User))
	}
}
