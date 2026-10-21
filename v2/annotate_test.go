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

// annotateInput is 36 characters long, so a span may end as late as 36.
const annotateInput = "Ada Lovelace wrote the first program"

const annotateAnswer = `{"annotations":[
 {"type":"entity","value":"Ada Lovelace","text":"Ada Lovelace","start":0,"end":12,"confidence":0.9,"metadata":{"kind":"person"}},
 {"type":"entity","value":"program","text":"program","start":29,"end":36,"confidence":0.6},
 {"type":"sentiment","value":"neutral","start":0,"end":0,"confidence":0.3}
],"summary":{"overall_sentiment":"neutral","entity_count":2}}`

func TestAnnotateReturnsTheAnnotationsInOrderAndByType(t *testing.T) {
	t.Parallel()
	model := answeringWith(annotateAnswer)
	got, err := schemaflux.Annotate(operationClient(model), annotateInput)
	if err != nil {
		t.Fatal(err)
	}
	if got.Original != annotateInput {
		t.Errorf("Original = %q", got.Original)
	}
	// The sentiment is below the default floor of 0.5 and is left out.
	if len(got.Annotations) != 2 || got.Annotations[0].Value != "Ada Lovelace" || got.Annotations[1].Value != "program" {
		t.Fatalf("annotations = %+v", got.Annotations)
	}
	first := got.Annotations[0]
	if first.Type != "entity" || first.Text != "Ada Lovelace" || first.Start != 0 || first.End != 12 || first.ModelConfidence != 0.9 || first.Attributes["kind"] != "person" {
		t.Errorf("first = %+v", first)
	}
	if len(got.ByType["entity"]) != 2 || got.ByType["entity"][1].Value != "program" || len(got.ByType) != 1 {
		t.Errorf("ByType = %+v", got.ByType)
	}
	if got.Summary["overall_sentiment"] != "neutral" || got.Summary["entity_count"] != float64(2) {
		t.Errorf("Summary = %+v", got.Summary)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	wantText(t, model.Last(), `Annotate this text:`)
	wantText(t, model.Last(), `"Ada Lovelace wrote the first program"`)
}

func TestAnnotateWithNoOptionsAsksForEntitiesAndSentimentWithConfidenceOnTheFastTier(t *testing.T) {
	t.Parallel()
	model := answeringWith(annotateAnswer)
	if _, err := schemaflux.Annotate(operationClient(model), annotateInput); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	wantText(t, req, "entities: Identify named entities")
	wantText(t, req, "sentiment: Determine overall sentiment")
	wantText(t, req, "Only include annotations with confidence >= 0.50")
	wantNoText(t, req, "topics:")
	wantNoText(t, req, "Domain context")
	if req.Schema != nil {
		t.Error("Annotate declares no schema")
	}
}

func TestAnnotateEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.AnnotateOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Types", schemaflux.AnnotateOptions{Types: []string{"topics", "keywords"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "topics: Extract main topics")
			wantText(t, req, "keywords: Identify important keywords")
			wantNoText(t, req, "entities: Identify")
			wantNoText(t, req, "sentiment: Determine")
		}},
		{"Types the library names, intent and language", schemaflux.AnnotateOptions{Types: []string{"intent", "language"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "intent: Determine the intent")
			wantText(t, req, "language: Detect the language")
		}},
		{"a Type of the caller's own is quoted", schemaflux.AnnotateOptions{Types: []string{"urgency"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `"urgency": Identify "urgency"`)
		}},
		{"an empty list of Types with a CustomSchema asks for the schema alone", schemaflux.AnnotateOptions{Types: []string{}, CustomSchema: map[string]string{"risks": "identify risks"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `"risks": "identify risks"`)
			wantNoText(t, req, "entities: Identify")
			wantNoText(t, req, "sentiment: Determine")
		}},
		{"CustomSchema beside the default Types", schemaflux.AnnotateOptions{CustomSchema: map[string]string{"risks": "identify risks", "actions": "action items"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "entities: Identify")
			wantText(t, req, `"actions": "action items"`)
			wantText(t, req, `"risks": "identify risks"`)
			if strings.Index(req.System, `"actions"`) > strings.Index(req.System, `"risks"`) {
				t.Error("the custom kinds are not in key order")
			}
		}},
		{"OmitConfidence leaves the confidence out of the request", schemaflux.AnnotateOptions{OmitConfidence: true}, func(t *testing.T, req schemaflux.Request) {
			wantNoText(t, req, "Only include annotations with confidence")
			wantNoText(t, req, "Include confidence scores")
		}},
		{"MinConfidence", schemaflux.AnnotateOptions{MinConfidence: 0.8}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "confidence >= 0.80")
		}},
		{"a negative MinConfidence keeps every annotation", schemaflux.AnnotateOptions{MinConfidence: -1}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "confidence >= 0.00")
		}},
		{"Language", schemaflux.AnnotateOptions{Language: "French"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `The text is in "French"; annotate in that language`)
		}},
		{"Domain", schemaflux.AnnotateOptions{Domain: "medical"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Domain context: "medical"`)
		}},
		{"Steering", schemaflux.AnnotateOptions{Options: schemaflux.Options{Steering: "prefer people"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "prefer people")
		}},
		{"Model", schemaflux.AnnotateOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.AnnotateOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.AnnotateOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.AnnotateOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.AnnotateOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.AnnotateOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(annotateAnswer)
			if _, err := schemaflux.Annotate(operationClient(model), annotateInput, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestAnnotateAppliesMinConfidenceToTheAnswerAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts schemaflux.AnnotateOptions
		want []string
	}{
		{"zero means 0.5", schemaflux.AnnotateOptions{}, []string{"Ada Lovelace", "program"}},
		{"a higher floor", schemaflux.AnnotateOptions{MinConfidence: 0.7}, []string{"Ada Lovelace"}},
		{"a floor of exactly a confidence keeps it", schemaflux.AnnotateOptions{MinConfidence: 0.6}, []string{"Ada Lovelace", "program"}},
		{"a floor of 1 keeps none of these", schemaflux.AnnotateOptions{MinConfidence: 1}, nil},
		{"a negative floor keeps every annotation", schemaflux.AnnotateOptions{MinConfidence: -0.1}, []string{"Ada Lovelace", "program", "neutral"}},
		{"OmitConfidence keeps every annotation and no floor applies", schemaflux.AnnotateOptions{OmitConfidence: true, MinConfidence: 0.99}, []string{"Ada Lovelace", "program", "neutral"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Annotate(operationClient(answeringWith(annotateAnswer)), annotateInput, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			var values []string
			for _, a := range got.Annotations {
				values = append(values, a.Value)
			}
			if strings.Join(values, "|") != strings.Join(tc.want, "|") {
				t.Errorf("kept %q, want %q", values, tc.want)
			}
		})
	}
}

func TestAnnotateReadsAStructAndAnyTextTheCallerCanWrite(t *testing.T) {
	t.Parallel()
	type article struct {
		Title string `json:"title"`
	}
	cases := map[string]struct {
		call func(ctx context.Context) error
		seen string
	}{
		"a struct": {func(ctx context.Context) error {
			_, err := schemaflux.Annotate(ctx, article{Title: "B-2"})
			return err
		}, "B-2"},
		"bytes": {func(ctx context.Context) error {
			_, err := schemaflux.Annotate(ctx, []byte("C-3"))
			return err
		}, "C-3"},
		"a map": {func(ctx context.Context) error {
			_, err := schemaflux.Annotate(ctx, map[string]string{"ref": "D-4"})
			return err
		}, "D-4"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(`{"annotations":[],"summary":{}}`)
			if err := tc.call(operationClient(model)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, sentTo(model))
			}
		})
	}
}

func TestAnnotateTakesAnEmptyListOfAnnotationsAsAnAnswer(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Annotate(operationClient(answeringWith(`{"annotations":[],"summary":{"note":"nothing found"}}`)), annotateInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Annotations) != 0 || got.Original != annotateInput || got.Summary["note"] != "nothing found" {
		t.Errorf("got %+v", got)
	}
}

func TestAnnotateRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.AnnotateOptions{
		"a minimum above 1":              {MinConfidence: 1.5},
		"a minimum that is not a number": {MinConfidence: math.NaN()},
		"an infinite minimum":            {MinConfidence: math.Inf(1)},
		"an infinite negative minimum":   {MinConfidence: math.Inf(-1)},
		"no kind at all":                 {Types: []string{}},
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: 5}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: 5}},
		"a bad setting beside good ones": {Domain: "d", Language: "l", MinConfidence: 2},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(annotateAnswer)
			got, err := schemaflux.Annotate(operationClient(model), annotateInput, opts)
			wantError(t, err, schemaflux.ErrConfiguration, "annotate")
			if got.Original != "" || got.Annotations != nil || got.ByType != nil || got.Summary != nil || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestAnnotateMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(annotateAnswer)
		first := schemaflux.AnnotateOptions{Types: []string{"topics"}, Domain: "kept domain", MinConfidence: 0.7, OmitConfidence: false, Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}}
		second := schemaflux.AnnotateOptions{MinConfidence: 0.8, Options: schemaflux.Options{Model: "second"}}
		if _, err := schemaflux.Annotate(operationClient(model), annotateInput, first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 11 {
			t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
		}
		wantText(t, req, "confidence >= 0.80")
		wantText(t, req, "topics: Extract")
		wantText(t, req, `Domain context: "kept domain"`)
	})
	t.Run("a bool set by an earlier value is not cleared by a later one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(annotateAnswer)
		_, err := schemaflux.Annotate(operationClient(model), annotateInput,
			schemaflux.AnnotateOptions{OmitConfidence: true}, schemaflux.AnnotateOptions{OmitConfidence: false})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Include confidence scores")
	})
	t.Run("a later list replaces the earlier one, and a later empty one is a value", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(annotateAnswer)
		_, err := schemaflux.Annotate(operationClient(model), annotateInput,
			schemaflux.AnnotateOptions{Types: []string{"topics"}, CustomSchema: map[string]string{"risks": "r"}},
			schemaflux.AnnotateOptions{Types: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "topics:")
		wantText(t, model.Last(), `"risks"`)
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Annotate(operationClient(answeringWith(annotateAnswer)), annotateInput,
			schemaflux.AnnotateOptions{}, schemaflux.AnnotateOptions{MinConfidence: 7})
		wantError(t, err, schemaflux.ErrConfiguration, "annotate")
	})
}

func TestAnnotateRefusesAnAnswerThatDoesNotReportWhatWasAsked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		opts   schemaflux.AnnotateOptions
		answer string
	}{
		{"it is not JSON", schemaflux.AnnotateOptions{}, "I found two names."},
		{"it is empty", schemaflux.AnnotateOptions{}, ""},
		{"it has no list of annotations", schemaflux.AnnotateOptions{}, `{"summary":{}}`},
		{"its list is null", schemaflux.AnnotateOptions{}, `{"annotations":null,"summary":{}}`},
		{"an annotation has no type", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":" ","value":"x","start":0,"end":1,"confidence":0.9}]}`},
		{"a span ends beyond the text", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","start":0,"end":37,"confidence":0.9}]}`},
		{"a span ends before it starts", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","start":5,"end":2,"confidence":0.9}]}`},
		{"a span starts before the text", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","start":-1,"end":2,"confidence":0.9}]}`},
		{"a span has no start", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","end":2,"confidence":0.9}]}`},
		{"confidence was asked for and is missing", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","start":0,"end":1}]}`},
		{"a confidence above the scale", schemaflux.AnnotateOptions{}, `{"annotations":[{"type":"e","value":"x","start":0,"end":1,"confidence":1.5}]}`},
		{"a confidence below the scale", schemaflux.AnnotateOptions{OmitConfidence: false}, `{"annotations":[{"type":"e","value":"x","start":0,"end":1,"confidence":-0.2}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Annotate(operationClient(answeringWith(tc.answer)), annotateInput, tc.opts)
			e := wantError(t, err, schemaflux.ErrInvalidAnswer, "annotate")
			if got.Original != "" || got.Annotations != nil || got.ByType != nil || got.Summary != nil {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
			if strings.Contains(e.Error(), "I found") {
				t.Errorf("the error repeats the model's text: %q", e.Error())
			}
		})
	}
}

func TestAnnotateReturnsAnErrorTheCallerCanClassifyWhenTheProviderFailsOrTheInputIsUnusable(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]schemaflux.Kind{
		"unavailable":  schemaflux.ErrUnavailable,
		"rate limited": schemaflux.ErrRateLimited,
		"unauthorized": schemaflux.ErrUnauthorized,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Fail(&schemaflux.Error{Kind: kind})
			_, err := schemaflux.Annotate(operationClient(&model), annotateInput)
			wantError(t, err, kind, "annotate")
		})
	}
	t.Run("an input that cannot be turned into text", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(annotateAnswer)
		_, err := schemaflux.Annotate(operationClient(model), make(chan int))
		wantError(t, err, schemaflux.ErrInvalidRequest, "annotate")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	})
}

func TestAnnotateWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(annotateAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Annotate(ctx, annotateInput)
	wantError(t, err, schemaflux.ErrCanceled, "annotate")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestAnnotateResultCarriesTheJSONTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Annotate(operationClient(answeringWith(annotateAnswer)), annotateInput, schemaflux.AnnotateOptions{MinConfidence: 0.7})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshaledText(got)
	want := `{"original":"Ada Lovelace wrote the first program","annotations":[{"type":"entity","value":"Ada Lovelace","text":"Ada Lovelace","end":12,"confidence":0.9,"metadata":{"kind":"person"}}],"by_type":{"entity":[{"type":"entity","value":"Ada Lovelace","text":"Ada Lovelace","end":12,"confidence":0.9,"metadata":{"kind":"person"}}]},"summary":{"entity_count":2,"overall_sentiment":"neutral"}}`
	if err != nil || encoded != want {
		t.Errorf("encoded = %s, %v", encoded, err)
	}
}

func TestAnnotateKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-2e88f1"
	opts := schemaflux.AnnotateOptions{
		Options:      schemaflux.Options{Steering: "steer " + canary},
		Types:        []string{"type " + canary},
		CustomSchema: map[string]string{"key " + canary: "description " + canary},
		Language:     "language " + canary,
		Domain:       "domain " + canary,
	}
	input := map[string]string{canary + "-key": "text " + canary}
	call := func(o schemaflux.AnnotateOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Annotate(ctx, input, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"annotations":[],"summary":{}}`) }, true, call(opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the entity is " + canary + " not json") }, true, call(opts))
	})
	t.Run("an answer with a span outside the text that quotes the input", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) {
			p.Reply(`{"annotations":[{"type":"` + canary + `","value":"` + canary + `","start":0,"end":9999,"confidence":0.9}]}`)
		}, true, call(opts))
	})
	t.Run("an answer of the wrong shape", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"annotations":["` + canary + `"],"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MinConfidence = 9
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
}
