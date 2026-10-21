package schemaflux_test

import (
	"context"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

const chooseSecondAnswer = `{"id":"i-000002"}`

var chooseOptions = []string{"alpha", "beta", "gamma"}

// chooseLaptop is an option of the caller's own type, compared by value.
type chooseLaptop struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

func TestChooseReturnsTheOptionTheModelNamedByItsId(t *testing.T) {
	t.Parallel()
	model := answeringWith(chooseSecondAnswer)
	best, err := schemaflux.Choose(operationClient(model), chooseOptions)
	if err != nil || best != "beta" {
		t.Fatalf("got %q, %v, want beta", best, err)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	wantText(t, req, `{"id":"i-000001","item":"alpha"}`)
	wantText(t, req, `{"id":"i-000003","item":"gamma"}`)
	if req.Schema != nil {
		t.Errorf("a schema was declared: %v", req.Schema)
	}
}

func TestChooseReturnsTheCallersOwnValueEvenWhenTwoOptionsAreEqual(t *testing.T) {
	t.Parallel()
	options := []chooseLaptop{{"a", 900}, {"b", 1200}, {"b", 1200}}
	best, err := schemaflux.Choose(operationClient(answeringWith(`{"id":"i-000003"}`)), options)
	if err != nil || best != options[2] {
		t.Errorf("got %+v, %v", best, err)
	}
}

func TestChooseOfOneOptionReturnsItWithoutACall(t *testing.T) {
	t.Parallel()
	model := answeringWith(chooseSecondAnswer)
	best, err := schemaflux.Choose(operationClient(model), []string{"only"})
	if err != nil || best != "only" || model.Calls() != 0 {
		t.Errorf("got %q, %v after %d requests", best, err, model.Calls())
	}
}

func TestChooseRefusesNoOptionsAsAnInvalidRequestBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	for name, options := range map[string][]string{"nil": nil, "an empty list": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(chooseSecondAnswer)
			best, err := schemaflux.Choose(operationClient(model), options)
			e := wantError(t, err, schemaflux.ErrInvalidRequest, "choose")
			if e.Message != "options is empty" || best != "" || model.Calls() != 0 {
				t.Errorf("message %q, value %q, %d requests", e.Message, best, model.Calls())
			}
		})
	}
}

func TestChooseWithNoOptionsValueAsksForTheBestAtTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(chooseSecondAnswer)
	if _, err := schemaflux.Choose(operationClient(model), chooseOptions); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	wantText(t, req, "Provide reasoning for your choice")
	wantText(t, req, "Use scoring strategy")
	wantNoText(t, req, "Selection criteria")
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" || req.WebSearch {
		t.Errorf("temperature %v ceiling %d model %q web %v", req.Temperature, req.MaxTokens, req.Model, req.WebSearch)
	}
}

func TestChooseEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ChooseOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Criteria", schemaflux.ChooseOptions{Criteria: []string{"under $1500", "lightest"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Selection criteria: under $1500, lightest")
		}},
		{"Strategy", schemaflux.ChooseOptions{Strategy: "tournament"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use tournament strategy")
			wantNoText(t, req, "Use scoring strategy")
		}},
		{"OmitReasoning", schemaflux.ChooseOptions{OmitReasoning: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Provide reasoning for your choice")
		}},
		{"Steering", schemaflux.ChooseOptions{Options: schemaflux.Options{Steering: "prefer the cheaper"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "prefer the cheaper")
		}},
		{"Model", schemaflux.ChooseOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ChooseOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ChooseOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ChooseOptions{Options: schemaflux.Options{MaxOutputTokens: 55}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 55 {
				t.Errorf("ceiling = %d", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ChooseOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("no web search")
			}
		}},
		{"Effort", schemaflux.ChooseOptions{Options: schemaflux.Options{Effort: schemaflux.EffortHigh}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "high" {
				t.Errorf("effort = %q", req.Effort)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(chooseSecondAnswer)
			if _, err := schemaflux.Choose(operationClient(model), chooseOptions, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestChooseRefusesAnAnswerThatIsNotTheIdOfAnOption(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"an id that was not offered": `{"id":"i-000009"}`,
		"the option itself":          `{"id":"beta"}`,
		"no id":                      `{}`,
		"an empty id":                `{"id":""}`,
		"text":                       `The best is beta.`,
		"a list":                     `["i-000002"]`,
		"a number":                   `{"id":2}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			best, err := schemaflux.Choose(operationClient(answeringWith(body)), chooseOptions)
			wantError(t, err, schemaflux.ErrInvalidAnswer, "choose")
			if best != "" {
				t.Errorf("a refused answer left %q beside the error", best)
			}
		})
	}
}

func TestChooseRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ChooseOptions{
		"a strategy that is not one":     {Strategy: "random"},
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 9}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: 9}},
		"a bad setting beside good ones": {Criteria: []string{"cheap"}, Strategy: "tournament", Options: schemaflux.Options{MaxOutputTokens: -5}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(chooseSecondAnswer)
			_, err := schemaflux.Choose(operationClient(model), chooseOptions, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "choose")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
}

func TestChooseNamesTheFieldOfABadStrategyAndNotItsValue(t *testing.T) {
	t.Parallel()
	_, err := schemaflux.Choose(operationClient(answeringWith(chooseSecondAnswer)), chooseOptions, schemaflux.ChooseOptions{Strategy: "CANARY-strategy"})
	e := wantError(t, err, schemaflux.ErrConfiguration, "choose")
	if want := "Strategy is not one of sequential, tournament and scoring"; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	for _, text := range chainText(err) {
		if strings.Contains(text, "CANARY") {
			t.Errorf("the value reached %q", text)
		}
	}
}

func TestChooseRefusesAnOptionJSONCannotWriteAsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	model := answeringWith(chooseSecondAnswer)
	_, err := schemaflux.Choose(operationClient(model), []any{"fine", make(chan int)})
	wantError(t, err, schemaflux.ErrInvalidRequest, "choose")
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestChooseWithANilContextIsAnInvalidRequest(t *testing.T) {
	t.Parallel()
	var nilContext context.Context
	_, err := schemaflux.Choose(nilContext, chooseOptions)
	if e := wantError(t, err, schemaflux.ErrInvalidRequest, "choose"); e.Message != "the context is nil" {
		t.Errorf("message = %q", e.Message)
	}
}

func TestChooseMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later strategy wins and a later zero leaves the earlier", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(chooseSecondAnswer)
		_, err := schemaflux.Choose(operationClient(model), chooseOptions,
			schemaflux.ChooseOptions{Strategy: "sequential", Criteria: []string{"first"}},
			schemaflux.ChooseOptions{Options: schemaflux.Options{Steering: "later"}},
			schemaflux.ChooseOptions{Strategy: "tournament"})
		if err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Use tournament strategy")
		wantText(t, req, "Selection criteria: first")
		wantText(t, req, "later")
	})
	t.Run("a bool turned on stays on", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(chooseSecondAnswer)
		_, err := schemaflux.Choose(operationClient(model), chooseOptions, schemaflux.ChooseOptions{OmitReasoning: true}, schemaflux.ChooseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Provide reasoning for your choice")
	})
	t.Run("a refused setting in a later value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Choose(operationClient(answeringWith(chooseSecondAnswer)), chooseOptions,
			schemaflux.ChooseOptions{}, schemaflux.ChooseOptions{Strategy: "random"})
		wantError(t, err, schemaflux.ErrConfiguration, "choose")
	})
}

func TestChooseReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
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
			best, err := schemaflux.Choose(operationClient(&model), chooseOptions)
			wantError(t, err, tc.err.Kind, "choose")
			if best != "" {
				t.Errorf("a failed call left %q beside the error", best)
			}
		})
	}
}

func TestChooseReadsAnyKindOfOption(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		options any
		seen    string
	}{
		"a struct": {[]chooseLaptop{{"slim", 999}, {"heavy", 1}}, `"name":"slim"`},
		"a map":    {[]map[string]string{{"k": "vv"}, {"k": "ww"}}, `"k":"vv"`},
		"numbers":  {[]int{42, 43}, `"item":42`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"id":"i-000001"}`)
			var err error
			switch options := tc.options.(type) {
			case []chooseLaptop:
				_, err = schemaflux.Choose(operationClient(model), options)
			case []map[string]string:
				_, err = schemaflux.Choose(operationClient(model), options)
			case []int:
				_, err = schemaflux.Choose(operationClient(model), options)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, excerpt(model.Last().User))
			}
		})
	}
}
