package schemaflux_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// queryPlan is the value the Explain tests explain: three fields.
type queryPlan struct {
	Table string   `json:"table"`
	Index string   `json:"index"`
	Steps []string `json:"steps"`
}

const explainAnswer = `{"explanation":"The plan reads the orders table through its index.","summary":"An indexed read.","key_points":["uses an index","reads one table"]}`

func TestExplainReturnsTheExplanationAndWhatWasWorkedOutAboutTheValue(t *testing.T) {
	t.Parallel()
	model := answeringWith(explainAnswer)
	got, err := schemaflux.Explain(operationClient(model), queryPlan{Table: "orders", Index: "by_date", Steps: []string{"scan", "filter"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Explanation != "The plan reads the orders table through its index." || got.Summary != "An indexed read." ||
		len(got.KeyPoints) != 2 || got.KeyPoints[1] != "reads one table" {
		t.Errorf("got %+v", got)
	}
	if got.DataType != "schemaflux_test.queryPlan" || got.FieldCount != 3 || got.DataComplexity != "low" {
		t.Errorf("worked out here: type %q fields %d complexity %q", got.DataType, got.FieldCount, got.DataComplexity)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	wantText(t, model.Last(), `"table": "orders"`)
	wantText(t, model.Last(), "- Type: schemaflux_test.queryPlan")
}

func TestExplainWorksOutTheComplexityOfEachKindOfValue(t *testing.T) {
	t.Parallel()
	type six struct{ A, B, C, D, E, F int }
	type eleven struct{ A, B, C, D, E, F, G, H, I, J, K int }
	cases := []struct {
		name       string
		data       any
		dataType   string
		fieldCount int
		complexity string
	}{
		{"a struct of five fields", struct{ A, B, C, D, E int }{}, "struct { A int; B int; C int; D int; E int }", 5, "low"},
		{"a struct of six fields", six{}, "schemaflux_test.six", 6, "medium"},
		{"a struct of eleven fields", eleven{}, "schemaflux_test.eleven", 11, "high"},
		{"a pointer to a struct", &queryPlan{}, "schemaflux_test.queryPlan", 3, "low"},
		{"a slice", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, "[]int", 12, "medium"},
		{"a map", map[string]int{"a": 1, "b": 2}, "map[string]int", 2, "medium"},
		{"a string", "plain text", "string", 1, "low"},
		{"a number", 42, "int", 1, "low"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Explain(operationClient(answeringWith(explainAnswer)), tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if got.DataType != tc.dataType || got.FieldCount != tc.fieldCount || got.DataComplexity != tc.complexity {
				t.Errorf("type %q fields %d complexity %q, want %q %d %q", got.DataType, got.FieldCount, got.DataComplexity, tc.dataType, tc.fieldCount, tc.complexity)
			}
		})
	}
}

func TestExplainWithNoOptionsExplainsForANonTechnicalReaderAtDepthTwoInAParagraph(t *testing.T) {
	t.Parallel()
	model := answeringWith(explainAnswer)
	if _, err := schemaflux.Explain(operationClient(model), queryPlan{Table: "t"}); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	wantText(t, req, "Explain this for someone without technical background")
	wantText(t, req, "Provide a cohesive paragraph explanation")
	wantText(t, req, "Provide moderate detail with key concepts")
	wantText(t, req, "FOCUS AREA: overview")
	wantNoText(t, req, "ADDITIONAL CONTEXT")
	if req.Schema != nil {
		t.Error("Explain declares no schema")
	}
}

func TestExplainEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.ExplainOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Audience technical", schemaflux.ExplainOptions{Audience: "technical"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Provide a technical explanation")
		}},
		{"Audience children", schemaflux.ExplainOptions{Audience: "children"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "curious child")
		}},
		{"Audience executive", schemaflux.ExplainOptions{Audience: "executive"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "business executives")
		}},
		{"Audience beginner", schemaflux.ExplainOptions{Audience: "beginner"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "complete beginners")
		}},
		{"Audience expert", schemaflux.ExplainOptions{Audience: "expert"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "domain experts")
		}},
		{"Depth 1", schemaflux.ExplainOptions{Depth: 1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "very high-level and brief")
		}},
		{"Depth 3", schemaflux.ExplainOptions{Depth: 3}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "good detail and examples")
		}},
		{"Depth 4", schemaflux.ExplainOptions{Depth: 4}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "comprehensive with full technical depth")
		}},
		{"Format bullet-points", schemaflux.ExplainOptions{Format: "bullet-points"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "clear bullet points")
		}},
		{"Format step-by-step", schemaflux.ExplainOptions{Format: "step-by-step"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "numbered steps")
		}},
		{"Format qa", schemaflux.ExplainOptions{Format: "qa"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Q&A")
		}},
		{"Format structured", schemaflux.ExplainOptions{Format: "structured"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "structured sections")
		}},
		{"Focus", schemaflux.ExplainOptions{Focus: "limitations"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "FOCUS AREA: limitations")
		}},
		{"Background is quoted", schemaflux.ExplainOptions{Background: "a slow query\nreport"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `ADDITIONAL CONTEXT: "a slow query\nreport"`)
		}},
		{"Steering", schemaflux.ExplainOptions{Options: schemaflux.Options{Steering: "mention cost"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "mention cost")
		}},
		{"Model", schemaflux.ExplainOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.ExplainOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.ExplainOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.ExplainOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.ExplainOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.ExplainOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(explainAnswer)
			if _, err := schemaflux.Explain(operationClient(model), queryPlan{Table: "t"}, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestExplainRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.ExplainOptions{
		"an audience of no kind":         {Audience: "pirates"},
		"a capitalised audience":         {Audience: "Technical"},
		"a negative depth":               {Depth: -1},
		"a depth above four":             {Depth: 5},
		"a very large depth":             {Depth: math.MaxInt32},
		"a format of no kind":            {Format: "poem"},
		"a focus of no kind":             {Focus: "gossip"},
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 8}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: 8}},
		"a bad setting beside good ones": {Audience: "expert", Depth: 3, Focus: "limbo"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(explainAnswer)
			got, err := schemaflux.Explain(operationClient(model), queryPlan{}, opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "explain")
			if got.Explanation != "" || got.DataType != "" || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
			for _, repeated := range []string{"pirates", "Technical", "poem", "gossip", "limbo"} {
				if strings.Contains(e.Error(), repeated) {
					t.Errorf("the error repeats the caller's setting: %q", e.Error())
				}
			}
		})
	}
}

func TestExplainMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("zero values leave the defaults and do not blank a line of the prompt", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(explainAnswer)
		if _, err := schemaflux.Explain(operationClient(model), queryPlan{}, schemaflux.ExplainOptions{}); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "FOCUS AREA: overview")
		wantText(t, model.Last(), "Provide a cohesive paragraph")
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(explainAnswer)
		first := schemaflux.ExplainOptions{Audience: "executive", Depth: 4, Focus: "usage", Background: "kept", Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}}
		second := schemaflux.ExplainOptions{Depth: 1, Options: schemaflux.Options{Model: "second"}}
		if _, err := schemaflux.Explain(operationClient(model), queryPlan{}, first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 11 {
			t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
		}
		wantText(t, req, "very high-level and brief")
		wantText(t, req, "business executives")
		wantText(t, req, "FOCUS AREA: usage")
		wantText(t, req, `"kept"`)
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Explain(operationClient(answeringWith(explainAnswer)), queryPlan{},
			schemaflux.ExplainOptions{}, schemaflux.ExplainOptions{Depth: 9})
		wantError(t, err, schemaflux.ErrConfiguration, "explain")
	})
}

func TestExplainRefusesDataItCannotExplain(t *testing.T) {
	t.Parallel()
	var nilPlan *queryPlan
	cases := map[string]struct {
		data any
		kind schemaflux.Kind
	}{
		"nil":                         {nil, schemaflux.ErrConfiguration},
		"a nil pointer":               {nilPlan, schemaflux.ErrConfiguration},
		"a value JSON cannot write":   {make(chan int), schemaflux.ErrInvalidRequest},
		"a struct holding such value": {struct{ C chan int }{}, schemaflux.ErrInvalidRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(explainAnswer)
			got, err := schemaflux.Explain(operationClient(model), tc.data)
			wantError(t, err, tc.kind, "explain")
			if got.Explanation != "" || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestExplainRefusesAnAnswerThatDoesNotReportWhatWasAsked(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"it is not JSON":                "The plan reads a table.",
		"it is empty":                   "",
		"it leaves the summary out":     `{"explanation":"x","key_points":["a"]}`,
		"it leaves the key points out":  `{"explanation":"x","summary":"y"}`,
		"it leaves the explanation out": `{"summary":"y","key_points":["a"]}`,
		"its explanation is blank":      `{"explanation":"  ","summary":"y","key_points":["a"]}`,
		"it is a list":                  `["a","b"]`,
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Explain(operationClient(answeringWith(answer)), queryPlan{Table: "t"})
			wantError(t, err, schemaflux.ErrInvalidAnswer, "explain")
			if got.Explanation != "" || got.Summary != "" || got.KeyPoints != nil || got.DataType != "" || got.FieldCount != 0 || got.DataComplexity != "" {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestExplainReturnsAnErrorTheCallerCanClassifyWhenTheProviderFails(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
		"timed out":    schemaflux.ErrTimeout,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			_, err := schemaflux.Explain(operationClient(&model), queryPlan{Table: "t"})
			wantError(t, err, kind, "explain")
		})
	}
}

func TestExplainWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(explainAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Explain(ctx, queryPlan{Table: "t"})
	wantError(t, err, schemaflux.ErrCanceled, "explain")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestExplainKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-90ab4d"
	type canaryData struct {
		Text string `json:"text" desc:"CANARY-90ab4d-tag"`
	}
	opts := schemaflux.ExplainOptions{
		Options:    schemaflux.Options{Steering: "steer " + canary},
		Background: "background " + canary,
	}
	input := map[string]canaryData{canary + "-key": {Text: "text " + canary}}
	call := func(o schemaflux.ExplainOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Explain(ctx, input, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(explainAnswer) }, true, call(opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the plan is " + canary + " not json") }, true, call(opts))
	})
	t.Run("an answer of the wrong shape that quotes the data", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"explanation":["` + canary + `"],"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("a blank explanation", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"explanation":" ","summary":"` + canary + `","key_points":["` + canary + `"]}`)
		}, true, call(opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.Audience = canary
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
}
