package schemaflux_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const sortThirdFirstSecond = `{"ids":["i-000003","i-000001","i-000002"]}`

var sortItems = []string{"low", "medium", "critical"}

func TestSortReturnsTheItemsInTheOrderTheModelGave(t *testing.T) {
	t.Parallel()
	model := answeringWith(sortThirdFirstSecond)
	got, err := schemaflux.Sort(operationClient(model), sortItems, "most urgent first")
	if err != nil || !reflect.DeepEqual(got, []string{"critical", "low", "medium"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, "Sort criteria: most urgent first")
	wantText(t, req, `{"id":"i-000003","item":"critical"}`)
	wantText(t, req, `exactly 3 ids`)
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestSortReturnsTheCallersOwnValuesWhenTwoItemsAreEqual(t *testing.T) {
	t.Parallel()
	items := []chooseLaptop{{"a", 1}, {"a", 1}, {"b", 2}}
	got, err := schemaflux.Sort(operationClient(answeringWith(`{"ids":["i-000003","i-000002","i-000001"]}`)), items, "x")
	if err != nil || !reflect.DeepEqual(got, []chooseLaptop{items[2], items[1], items[0]}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestSortOfFewerThanTwoItemsIsTheInputAndSendsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{"nil": nil, "an empty list": {}, "one item": {"only"}}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(sortThirdFirstSecond)
			got, err := schemaflux.Sort(operationClient(model), items, "x")
			if err != nil || !reflect.DeepEqual(got, items) || model.Calls() != 0 {
				t.Errorf("got %#v, %v after %d requests", got, err, model.Calls())
			}
		})
	}
}

func TestSortRefusesBlankCriteriaAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, criteria := range map[string]string{"empty": "", "spaces": "  ", "a tab": "\t"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(sortThirdFirstSecond)
			got, err := schemaflux.Sort(operationClient(model), sortItems, criteria)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "sort")
			if e.Message != "criteria is blank" || got != nil || model.Calls() != 0 {
				t.Errorf("message %q, result %v, %d requests", e.Message, got, model.Calls())
			}
		})
	}
}

func TestSortWithNoOptionsAscendsAndKeepsEqualItemsInOrder(t *testing.T) {
	t.Parallel()
	model := answeringWith(sortThirdFirstSecond)
	if _, err := schemaflux.Sort(operationClient(model), sortItems, "x"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Direction: ascending")
	wantText(t, req, "Maintain relative order of equal elements")
	wantNoText(t, req, "Secondary criteria")
	wantNoText(t, req, "Comparison logic")
	wantNoText(t, req, "Include sort scores")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v ceiling %d model %q", req.Temperature, req.MaxTokens, req.Model)
	}
}

func TestSortEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.SortOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Direction", schemaflux.SortOptions{Direction: "descending"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Direction: descending")
		}},
		{"SecondaryCriteria", schemaflux.SortOptions{SecondaryCriteria: []string{"by name", "by age"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Secondary criteria: by name, by age")
		}},
		{"ComparisonLogic", schemaflux.SortOptions{ComparisonLogic: "numeric, not alphabetic"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Comparison logic: numeric, not alphabetic")
		}},
		{"NoStableOrder", schemaflux.SortOptions{NoStableOrder: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Maintain relative order of equal elements")
		}},
		{"IncludeScores", schemaflux.SortOptions{IncludeScores: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Include sort scores")
		}},
		{"Steering", schemaflux.SortOptions{Options: schemaflux.Options{Steering: "newest matters"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "newest matters")
		}},
		{"Model", schemaflux.SortOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.SortOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.SortOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.SortOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.SortOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.SortOptions{Options: schemaflux.Options{Effort: schemaflux.EffortMedium}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "medium" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(sortThirdFirstSecond)
			if _, err := schemaflux.Sort(operationClient(model), sortItems, "x", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestSortRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.SortOptions{
		"a direction that is not one": {Direction: "sideways"},
		"a negative output ceiling":   {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":      {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":      {Options: schemaflux.Options{Mode: 9}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(sortThirdFirstSecond)
			_, err := schemaflux.Sort(operationClient(model), sortItems, "x", opts)
			wantError(t, err, schemaflux.ErrConfiguration, "sort")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestSortNamesTheFieldOfABadDirectionAndNotItsValue(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Sort(operationClient(answeringWith(sortThirdFirstSecond)), sortItems, "x", schemaflux.SortOptions{Direction: "CANARY-direction"})
	e := wantError(t, err, schemaflux.ErrConfiguration, "sort")
	if want := "Direction is not one of ascending and descending"; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	for _, text := range chainText(err) {
		if strings.Contains(text, "CANARY") {
			t.Errorf("the value reached %q", text)
		}
	}
}

func TestSortScoresEachItemWhenTheWholeListAnswerIsNotAPermutation(t *testing.T) {
	t.Parallel()
	for name, refused := range map[string]string{
		"an id left out":        `{"ids":["i-000001","i-000002"]}`,
		"an id twice":           `{"ids":["i-000001","i-000001","i-000002"]}`,
		"an id not offered":     `{"ids":["i-000001","i-000002","i-000009"]}`,
		"a null list":           `{"ids":null}`,
		"text":                  `critical first`,
		"no list":               `{}`,
		"more ids than offered": `{"ids":["i-000001","i-000002","i-000003","i-000003"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			model := answeringWith(refused, `{"rank_score":0.5}`)
			got, err := schemaflux.Sort(operationClient(model, recordingTo(&recorder)), sortItems, "x")
			if err != nil || !reflect.DeepEqual(got, sortItems) {
				t.Fatalf("got %v, %v, want the items in their own order, scored equal", got, err)
			}
			if model.Calls() != 4 {
				t.Errorf("%d requests were sent, want the refused answer and three scores", model.Calls())
			}
			if r := onlyRecord(t, &recorder); r.Strategy != "scoring-fallback" {
				t.Errorf("strategy = %q", r.Strategy)
			}
		})
	}
}

func TestSortRefusesWhenTheScoringFallbackFailsToo(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"ids":["i-000001"]}`, `not a score`)
	got, err := schemaflux.Sort(operationClient(model), sortItems, "x")
	wantError(t, err, schemaflux.ErrInvalidAnswer, "sort")
	if got != nil {
		t.Errorf("a refused answer left %v beside the error", got)
	}
}

func TestSortOfALongListScoresEachItemAndOrdersInGo(t *testing.T) {
	t.Parallel()
	items := make([]int, 60)
	for i := range items {
		items[i] = i
	}
	var recorder schemaflux.Recorder
	model := answeringWith(`{"rank_score":0.5}`)
	got, err := schemaflux.Sort(operationClient(model, recordingTo(&recorder)), items, "x", schemaflux.SortOptions{Direction: "descending", ComparisonLogic: "by weight"})
	if err != nil || !reflect.DeepEqual(got, items) {
		t.Fatalf("got %v, %v", got, err)
	}
	if model.Calls() != 60 {
		t.Errorf("%d requests were sent, want one for each item", model.Calls())
	}
	wantText(t, model.Last(), "Direction: descending")
	wantText(t, model.Last(), "Comparison logic: by weight")
	if r := onlyRecord(t, &recorder); r.Strategy != "scoring" || r.Requests != 60 {
		t.Errorf("strategy %q requests %d", r.Strategy, r.Requests)
	}
}

func TestSortRefusesAnItemJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(sortThirdFirstSecond)
	_, err := schemaflux.Sort(operationClient(model), []any{"fine", make(chan int)}, "x")
	wantError(t, err, schemaflux.ErrInvalidRequest, "sort")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSortWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Sort(nilContext, sortItems, "x")
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "sort"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestSortMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later direction wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(sortThirdFirstSecond)
		_, err := schemaflux.Sort(operationClient(model), sortItems, "x",
			schemaflux.SortOptions{Direction: "ascending", SecondaryCriteria: []string{"first"}},
			schemaflux.SortOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.SortOptions{Direction: "descending"})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Direction: descending")
		wantText(t, req, "Secondary criteria: first")
		wantText(t, req, "later")
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(sortThirdFirstSecond)
		_, err := schemaflux.Sort(operationClient(model), sortItems, "x", schemaflux.SortOptions{NoStableOrder: true}, schemaflux.SortOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Maintain relative order")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Sort(operationClient(answeringWith(sortThirdFirstSecond)), sortItems, "x",
			schemaflux.SortOptions{}, schemaflux.SortOptions{Direction: "sideways"})
		wantError(t, err, schemaflux.ErrConfiguration, "sort")
	})
}

func TestSortReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			got, err := schemaflux.Sort(operationClient(&model), sortItems, "x")
			wantError(t, err, tc.err.Kind, "sort")
			if got != nil {
				t.Errorf("a failed call left %v beside the error", got)
			}
		})
	}
}

func TestSortRecordsWhichPathRan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		items []string
		body  string
		want  string
	}{
		{"fewer than two items", []string{"only"}, sortThirdFirstSecond, "trivial"},
		{"a whole-list answer", sortItems, sortThirdFirstSecond, "whole-list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var recorder schemaflux.Recorder
			if _, err := schemaflux.Sort(operationClient(answeringWith(tc.body), recordingTo(&recorder)), tc.items, "x"); err != nil {
				t.Fatal(err)
			}
			if r := onlyRecord(t, &recorder); r.Op != "sort" || r.Strategy != tc.want {
				t.Errorf("op %q strategy %q, want %q", r.Op, r.Strategy, tc.want)
			}
		})
	}
}
