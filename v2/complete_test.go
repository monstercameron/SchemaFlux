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

func TestCompleteReturnsTheWholeTextWithTheCompletionAppended(t *testing.T) {
	t.Parallel()
	model := answeringWith("jumps over the lazy dog")
	got, err := schemaflux.Complete(operationClient(model), "The quick brown fox")
	if err != nil {
		t.Fatal(err)
	}
	want := schemaflux.CompleteResult{Text: "The quick brown fox jumps over the lazy dog", Original: "The quick brown fox", Length: 24}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	wantText(t, model.Last(), `Complete this text: "The quick brown fox"`)
}

func TestCompleteDropsTheOriginalWhenTheModelRepeatsIt(t *testing.T) {
	t.Parallel()
	model := answeringWith("The quick brown fox jumps")
	got, err := schemaflux.Complete(operationClient(model), "The quick brown fox")
	if err != nil || got.Text != "The quick brown fox jumps" || got.Length != 6 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestCompleteWithNoOptionsAsksForTheSmartTierInCreativeMode(t *testing.T) {
	t.Parallel()
	model := answeringWith("jumps")
	if _, err := schemaflux.Complete(operationClient(model), "The fox"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.7) > 1e-6 || req.MaxTokens != 4000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.7, 4000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	if req.Effort != "" || req.WebSearch || req.Schema != nil {
		t.Errorf("a default call carried an effort, web search or a schema: %+v", req)
	}
	wantText(t, req, "approximately 100 characters")
	wantNoText(t, req, "Context:")
	wantNoText(t, req, "Stop generation")
}

func TestCompleteEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.CompleteOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Background", schemaflux.CompleteOptions{Background: []string{"earlier note", "second note"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Context:\n1. \"earlier note\"\n2. \"second note\"")
			wantText(t, req, "Use the provided context")
		}},
		{"MaxLength", schemaflux.CompleteOptions{MaxLength: 400}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "approximately 400 characters")
			wantNoText(t, req, "approximately 100 characters")
		}},
		{"StopSequences", schemaflux.CompleteOptions{StopSequences: []string{"END", "\n\n"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Stop generation when you encounter any of these sequences: ["END","\n\n"]`)
		}},
		{"Steering", schemaflux.CompleteOptions{Options: schemaflux.Options{Steering: "write as a pirate"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "write as a pirate")
		}},
		{"Model", schemaflux.CompleteOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.CompleteOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
			wantText(t, req, "strict grammatical and logical rules")
		}},
		{"TierFast", schemaflux.CompleteOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 2000 {
				t.Errorf("ceiling = %d, want the fast tier's 2000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.CompleteOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.CompleteOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.CompleteOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith("jumps over")
			if _, err := schemaflux.Complete(operationClient(model), "The fox", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestCompleteCutsTheAnswerAtTheFirstStopSequenceAndDropsIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		answer string
		stops  []string
		want   string
	}{
		{"one sequence", "jumps over END and more", []string{"END"}, "The fox jumps over "},
		{"the earliest of several, whatever their order", "xAyB", []string{"B", "A"}, "The fox x"},
		{"an empty sequence is ignored", "jumps over", []string{""}, "The fox jumps over"},
		{"no sequence occurs", "jumps over", []string{"END"}, "The fox jumps over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Complete(operationClient(answeringWith(tc.answer)), "The fox", schemaflux.CompleteOptions{StopSequences: tc.stops})
			if err != nil || got.Text != tc.want {
				t.Fatalf("got %q, %v, want %q", got.Text, err, tc.want)
			}
		})
	}
}

func TestCompleteLimitsTheCompletionToMaxLengthCharacters(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 100)
	got, err := schemaflux.Complete(operationClient(answeringWith(long)), "Start", schemaflux.CompleteOptions{MaxLength: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got.Length > 20 || got.Length <= 0 {
		t.Errorf("the completion added %d characters, want at most 20", got.Length)
	}
	if !strings.HasPrefix(got.Text, "Start ") {
		t.Errorf("text = %q", got.Text)
	}
}

func TestCompleteCountsCharactersAndNotBytes(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Complete(operationClient(answeringWith("日本語です")), "こんにちは", schemaflux.CompleteOptions{MaxLength: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got.Length != 6 || got.Original != "こんにちは" {
		t.Errorf("length %d original %q, want 6 characters added", got.Length, got.Original)
	}
}

func TestCompleteRefusesWhatCannotBeSentBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		opts schemaflux.CompleteOptions
		kind schemaflux.Kind
	}{
		{"an empty text", "", schemaflux.CompleteOptions{}, schemaflux.ErrInvalidRequest},
		{"a text of only space", " \n\t ", schemaflux.CompleteOptions{}, schemaflux.ErrInvalidRequest},
		{"a negative MaxLength", "x", schemaflux.CompleteOptions{MaxLength: -1}, schemaflux.ErrConfiguration},
		{"a very negative MaxLength", "x", schemaflux.CompleteOptions{MaxLength: math.MinInt32}, schemaflux.ErrConfiguration},
		{"a negative output ceiling", "x", schemaflux.CompleteOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration},
		{"a tier that is not one", "x", schemaflux.CompleteOptions{Options: schemaflux.Options{Tier: 9}}, schemaflux.ErrConfiguration},
		{"a mode that is not one", "x", schemaflux.CompleteOptions{Options: schemaflux.Options{Mode: 9}}, schemaflux.ErrConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith("more")
			got, err := schemaflux.Complete(operationClient(model), tc.text, tc.opts)
			wantError(t, err, tc.kind, "complete")
			if got != (schemaflux.CompleteResult{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestCompleteMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("no value is the default", func(t *testing.T) {
		t.Parallel()
		model := answeringWith("more")
		if _, err := schemaflux.Complete(operationClient(model), "x"); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "approximately 100 characters")
	})
	t.Run("a zero MaxLength means 100 and not none", func(t *testing.T) {
		t.Parallel()
		model := answeringWith("more")
		if _, err := schemaflux.Complete(operationClient(model), "x", schemaflux.CompleteOptions{MaxLength: 0}); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "approximately 100 characters")
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith("more")
		first := schemaflux.CompleteOptions{MaxLength: 40, Background: []string{"kept"}, Options: schemaflux.Options{Model: "first", MaxOutputTokens: 11}}
		second := schemaflux.CompleteOptions{MaxLength: 60, Options: schemaflux.Options{Model: "second"}}
		if _, err := schemaflux.Complete(operationClient(model), "x", first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		if req.Model != "second" || req.MaxTokens != 11 {
			t.Errorf("model %q ceiling %d, want second and 11", req.Model, req.MaxTokens)
		}
		wantText(t, req, "approximately 60 characters")
		wantText(t, req, `"kept"`)
	})
	t.Run("a later list replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith("more")
		_, err := schemaflux.Complete(operationClient(model), "x",
			schemaflux.CompleteOptions{StopSequences: []string{"FIRST"}}, schemaflux.CompleteOptions{StopSequences: []string{"SECOND"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "SECOND")
		wantNoText(t, model.Last(), "FIRST")
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Complete(operationClient(answeringWith("more")), "x",
			schemaflux.CompleteOptions{}, schemaflux.CompleteOptions{MaxLength: -4})
		wantError(t, err, schemaflux.ErrConfiguration, "complete")
	})
}

func TestCompleteReturnsAnErrorTheCallerCanClassifyForEachWayTheCallFails(t *testing.T) {
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
		{"the key is refused", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401})
		}, schemaflux.ErrUnauthorized},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
		{"the answer adds nothing but the text it was given", func(p *schemafluxtest.Provider) { p.Reply("The fox") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Complete(operationClient(&model), "The fox")
			wantError(t, err, tc.kind, "complete")
			if got != (schemaflux.CompleteResult{}) {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
	t.Run("an answer that is only a stop sequence adds nothing", func(t *testing.T) {
		t.Parallel()
		got, err := schemaflux.Complete(operationClient(answeringWith("END")), "The fox", schemaflux.CompleteOptions{StopSequences: []string{"END"}})
		wantError(t, err, schemaflux.ErrInvalidAnswer, "complete")
		if got != (schemaflux.CompleteResult{}) {
			t.Errorf("got %+v", got)
		}
	})
}

func TestCompleteWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith("more")
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Complete(ctx, "x")
	wantError(t, err, schemaflux.ErrCanceled, "complete")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestCompleteResultCarriesTheJSONTagsOfTheInternalResult(t *testing.T) {
	t.Parallel()
	got, err := schemaflux.Complete(operationClient(answeringWith("jumps")), "The fox")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := marshaledText(got)
	if err != nil || encoded != `{"text":"The fox jumps","original":"The fox","length":6}` {
		t.Errorf("encoded = %s, %v", encoded, err)
	}
}

func TestCompleteKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-81d3a7"
	opts := schemaflux.CompleteOptions{
		Options:       schemaflux.Options{Steering: "steer " + canary},
		Background:    []string{"note " + canary},
		StopSequences: []string{"stop " + canary},
	}
	text := "text " + canary
	call := func(text string, o schemaflux.CompleteOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Complete(ctx, text, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("and then " + canary) }, true, call(text, opts))
	})
	t.Run("an answer that adds nothing", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(text) }, true, call(text, opts))
	})
	t.Run("an answer that is only a stop sequence", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("stop " + canary) }, true, call(text, opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(text, opts))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxLength = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("x") }, true, call(text, bad))
	})
}
