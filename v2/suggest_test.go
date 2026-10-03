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

// suggestedFix is the type the Suggest tests ask for besides strings.
type suggestedFix struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

const suggestAnswer = `["Reply to the customer","Offer a refund","Escalate"]`

func TestSuggestReturnsTheListTheModelGave(t *testing.T) {
	t.Parallel()
	model := answeringWith(suggestAnswer)
	got, err := schemaflux.Suggest[string](operationClient(model), "My invoice is wrong")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "Reply to the customer" || got[2] != "Escalate" {
		t.Errorf("got %q", got)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	wantText(t, model.Last(), `"My invoice is wrong"`)
}

func TestSuggestReadsAListOfStructs(t *testing.T) {
	t.Parallel()
	model := answeringWith(`[{"name":"Re-send the invoice","priority":1},{"name":"Call","priority":2}]`)
	got, err := schemaflux.Suggest[suggestedFix](operationClient(model), map[string]any{"report": "late"})
	if err != nil || len(got) != 2 || got[0] != (suggestedFix{Name: "Re-send the invoice", Priority: 1}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSuggestReadsAnObjectThatHoldsTheList(t *testing.T) {
	t.Parallel()
	model := answeringWith(`{"suggestions":["a","b"]}`)
	got, err := schemaflux.Suggest[string](operationClient(model), "x")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSuggestWithNoOptionsAsksForFiveRankedSuggestionsContextuallyOnTheSmartTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(suggestAnswer)
	if _, err := schemaflux.Suggest[string](operationClient(model), "x"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 4000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	wantText(t, req, "Generate suggestions using contextual strategy")
	wantText(t, req, "Rank suggestions by relevance")
	wantText(t, req, "Return top 5 suggestions")
	for _, absent := range []string{"confidence scores", "Provide reasoning", "Domain context", "Constraints:", "Categories:"} {
		wantNoText(t, req, absent)
	}
	if req.Schema != nil {
		t.Error("Suggest declares no schema")
	}
}

func TestSuggestEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.SuggestOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Strategy pattern", schemaflux.SuggestOptions{Strategy: "pattern"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "using pattern strategy")
		}},
		{"Strategy goal", schemaflux.SuggestOptions{Strategy: "goal"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "using goal strategy")
		}},
		{"Strategy hybrid", schemaflux.SuggestOptions{Strategy: "hybrid"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "using hybrid strategy")
		}},
		{"Strategy contextual, named", schemaflux.SuggestOptions{Strategy: "contextual"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "using contextual strategy")
		}},
		{"TopN", schemaflux.SuggestOptions{TopN: 3}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Return top 3 suggestions")
		}},
		{"NoRanking leaves the order to the model", schemaflux.SuggestOptions{NoRanking: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Rank suggestions by relevance")
		}},
		{"IncludeScores", schemaflux.SuggestOptions{IncludeScores: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Include confidence scores (0-1)")
		}},
		{"IncludeReasons", schemaflux.SuggestOptions{IncludeReasons: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Provide reasoning for each suggestion")
		}},
		{"Domain", schemaflux.SuggestOptions{Domain: "billing"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Domain context: billing")
		}},
		{"Constraints", schemaflux.SuggestOptions{Constraints: []string{"under an hour", "no refunds"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Constraints: under an hour, no refunds")
		}},
		{"Categories", schemaflux.SuggestOptions{Categories: []string{"fix", "escalate"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Categories: fix, escalate")
		}},
		{"Steering comes first", schemaflux.SuggestOptions{Options: schemaflux.Options{Steering: "be brief"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "be brief. Generate suggestions")
		}},
		{"Model", schemaflux.SuggestOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.SuggestOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierQuick", schemaflux.SuggestOptions{Options: schemaflux.Options{Tier: schemaflux.TierQuick}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 1000 {
				t.Errorf("ceiling = %d, want the quick tier's 1000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.SuggestOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.SuggestOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.SuggestOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(suggestAnswer)
			if _, err := schemaflux.Suggest[string](operationClient(model), "x", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestSuggestRefusesAnAnswerWithMoreSuggestionsThanTopN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opts    schemaflux.SuggestOptions
		answer  string
		refused bool
	}{
		{"three where three are allowed", schemaflux.SuggestOptions{TopN: 3}, `["a","b","c"]`, false},
		{"four where three are allowed", schemaflux.SuggestOptions{TopN: 3}, `["a","b","c","d"]`, true},
		{"six where the default of five is allowed", schemaflux.SuggestOptions{}, `["a","b","c","d","e","f"]`, true},
		{"five where the default of five is allowed", schemaflux.SuggestOptions{}, `["a","b","c","d","e"]`, false},
		{"fewer than asked for", schemaflux.SuggestOptions{TopN: 4}, `["a"]`, false},
		{"two where one is allowed", schemaflux.SuggestOptions{TopN: 1}, `["a","b"]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Suggest[string](operationClient(answeringWith(tc.answer)), "x", tc.opts)
			if !tc.refused {
				if err != nil || len(got) == 0 {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "suggest")
			if got != nil {
				t.Errorf("a refused answer left %q beside the error", got)
			}
		})
	}
}

func TestSuggestRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.SuggestOptions{
		"a negative TopN":                {TopN: -1},
		"a very negative TopN":           {TopN: math.MinInt32},
		"a strategy of no kind":          {Strategy: "bogus"},
		"a capitalised strategy":         {Strategy: "Pattern"},
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 4}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: -1}},
		"a bad setting beside good ones": {TopN: 3, Domain: "d", Strategy: "nonsense"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(suggestAnswer)
			got, err := schemaflux.Suggest[string](operationClient(model), "x", opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "suggest")
			if got != nil || model.Calls() != 0 {
				t.Errorf("got %q after %d requests", got, model.Calls())
			}
			for _, repeated := range []string{"bogus", "Pattern", "nonsense"} {
				if strings.Contains(e.Error(), repeated) {
					t.Errorf("the error repeats the caller's setting: %q", e.Error())
				}
			}
		})
	}
}

func TestSuggestMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a zero TopN means 5 and not none", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(suggestAnswer)
		if _, err := schemaflux.Suggest[string](operationClient(model), "x", schemaflux.SuggestOptions{TopN: 0}); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Return top 5 suggestions")
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(suggestAnswer)
		first := schemaflux.SuggestOptions{TopN: 4, Strategy: "goal", Domain: "kept domain", NoRanking: true}
		second := schemaflux.SuggestOptions{TopN: 3}
		if _, err := schemaflux.Suggest[string](operationClient(model), "x", first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Return top 3 suggestions")
		wantText(t, req, "using goal strategy")
		wantText(t, req, "Domain context: kept domain")
		wantNoText(t, req, "Rank suggestions")
	})
	t.Run("a later list replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(suggestAnswer)
		_, err := schemaflux.Suggest[string](operationClient(model), "x",
			schemaflux.SuggestOptions{Categories: []string{"alpha-kind"}}, schemaflux.SuggestOptions{Categories: []string{"beta-kind"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Categories: beta-kind")
		wantNoText(t, model.Last(), "alpha-kind")
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Suggest[string](operationClient(answeringWith(suggestAnswer)), "x",
			schemaflux.SuggestOptions{}, schemaflux.SuggestOptions{TopN: -2})
		wantError(t, err, schemaflux.ErrConfiguration, "suggest")
	})
}

func TestSuggestReturnsAnErrorTheCallerCanClassifyForEachWayTheCallFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(*schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		{"the provider is unavailable", func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}) }, schemaflux.ErrUnavailable},
		{"the provider is rate limited", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429})
		}, schemaflux.ErrRateLimited},
		{"the answer is not JSON", func(p *schemafluxtest.Provider) { p.Reply("Here are some ideas.") }, schemaflux.ErrInvalidAnswer},
		{"the answer is an empty list", func(p *schemafluxtest.Provider) { p.Reply(`[]`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is null", func(p *schemafluxtest.Provider) { p.Reply(`null`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is an error object", func(p *schemafluxtest.Provider) { p.Reply(`{"error":"no"}`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Suggest[suggestedFix](operationClient(&model), "x")
			wantError(t, err, tc.kind, "suggest")
			if got != nil {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
}

func TestSuggestRefusesAnInputThatCannotBeWrittenAsJSON(t *testing.T) {
	t.Parallel()
	model := answeringWith(suggestAnswer)
	got, err := schemaflux.Suggest[string](operationClient(model), make(chan int))
	wantError(t, err, schemaflux.ErrInvalidRequest, "suggest")
	if got != nil || model.Calls() != 0 {
		t.Errorf("got %q after %d requests", got, model.Calls())
	}
}

func TestSuggestWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(suggestAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Suggest[string](ctx, "x")
	wantError(t, err, schemaflux.ErrCanceled, "suggest")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestSuggestKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-66b0c2"
	opts := schemaflux.SuggestOptions{
		Options:     schemaflux.Options{Steering: "steer " + canary},
		Domain:      "domain " + canary,
		Constraints: []string{"constraint " + canary},
		Categories:  []string{"category " + canary},
		TopN:        2,
	}
	input := map[string]string{canary + "-key": "text " + canary}
	call := func(o schemaflux.SuggestOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Suggest[suggestedFix](ctx, input, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`[{"name":"fine","priority":1}]`) }, true, call(opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the idea is " + canary + " not json") }, true, call(opts))
	})
	t.Run("an answer with too many suggestions that quotes the input", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`[{"name":"` + canary + `"},{"name":"b"},{"name":"c"}]`)
		}, true, call(opts))
	})
	t.Run("an answer of the wrong shape", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a refused strategy", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.Strategy = canary
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("[]") }, true, call(bad))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxOutputTokens = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("[]") }, true, call(bad))
	})
}
