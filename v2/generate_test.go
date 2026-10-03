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

// generatedMeal is the type the Generate tests ask for.
type generatedMeal struct {
	Name     string `json:"name"`
	Calories int    `json:"calories"`
}

const generateAnswer = `{"name":"Lentil soup","calories":320}`

func TestGenerateIntoAStringReturnsTheModelsTextAsItCame(t *testing.T) {
	t.Parallel()
	model := answeringWith("  Ada writes Go.\nShe likes proofs.  ")
	got, err := schemaflux.Generate[string](operationClient(model), "Write a two-line bio for a Go developer")
	if err != nil {
		t.Fatal(err)
	}
	if got != "  Ada writes Go.\nShe likes proofs.  " {
		t.Errorf("got %q, want the text exactly as the model wrote it", got)
	}
	req := model.Last()
	if req.Schema != nil || req.Format == "json" {
		t.Errorf("a string was asked for as JSON: format %q schema %v", req.Format, req.Schema != nil)
	}
	wantText(t, req, "Write a two-line bio for a Go developer")
}

func TestGenerateIntoAStructReadsTheAnswerIntoIt(t *testing.T) {
	t.Parallel()
	model := answeringWith(generateAnswer)
	got, err := schemaflux.Generate[generatedMeal](operationClient(model), "A vegetarian dinner")
	if err != nil {
		t.Fatal(err)
	}
	if got != (generatedMeal{Name: "Lentil soup", Calories: 320}) {
		t.Errorf("got %+v", got)
	}
	req := model.Last()
	if req.Format != "json" || req.SchemaName != "generatedMeal" || req.Schema == nil {
		t.Errorf("request = format %q schema name %q schema %v, want a declared JSON schema", req.Format, req.SchemaName, req.Schema != nil)
	}
}

func TestGenerateIntoASliceOfStructs(t *testing.T) {
	t.Parallel()
	model := answeringWith(`[{"name":"Soup","calories":1},{"name":"Stew","calories":2}]`)
	got, err := schemaflux.Generate[[]generatedMeal](operationClient(model), "Two dishes")
	if err != nil || len(got) != 2 || got[1].Name != "Stew" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestGenerateWithNoOptionsAsksForTheFastTierInCreativeMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(generateAnswer)
	if _, err := schemaflux.Generate[generatedMeal](operationClient(model), "A dinner"); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.7) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.7, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	if req.Effort != "" || req.WebSearch {
		t.Errorf("a default call carried an effort or web search: %+v", req)
	}
	for _, absent := range []string{"Use this template", "Style:", "Constraints:", "distinct", "seed data", "Follow these examples"} {
		wantNoText(t, req, absent)
	}
}

func TestGenerateEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.GenerateOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"Template", schemaflux.GenerateOptions{Template: "name: <dish>"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use this template: name: <dish>")
		}},
		{"Style", schemaflux.GenerateOptions{Style: "dry and brief"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Style: dry and brief")
		}},
		{"Constraints in key order", schemaflux.GenerateOptions{Constraints: map[string]any{"vegan": true, "calories": "under 400"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Constraints: calories=under 400; vegan=true")
		}},
		{"SeedData", schemaflux.GenerateOptions{SeedData: generatedMeal{Name: "Seed dish", Calories: 9}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Base on this seed data: {"name":"Seed dish","calories":9}`)
		}},
		{"SeedData that is a map", schemaflux.GenerateOptions{SeedData: map[string]int{"a": 1}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Base on this seed data: {"a":1}`)
		}},
		{"Examples", schemaflux.GenerateOptions{Examples: []any{generatedMeal{Name: "Z-9", Calories: 1}}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, `Follow these examples: [{"name":"Z-9","calories":1}]`)
		}},
		{"EnsureUnique", schemaflux.GenerateOptions{EnsureUnique: true}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Every generated value must be distinct")
		}},
		{"Steering", schemaflux.GenerateOptions{Options: schemaflux.Options{Steering: "keep it short"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "keep it short")
		}},
		{"Model", schemaflux.GenerateOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.GenerateOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.GenerateOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.GenerateOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.GenerateOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.GenerateOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(generateAnswer)
			if _, err := schemaflux.Generate[generatedMeal](operationClient(model), "A dinner", tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestGenerateRefusesWhatCannotBeSentBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prompt string
		opts   schemaflux.GenerateOptions
		kind   schemaflux.Kind
	}{
		{"a negative output ceiling", "x", schemaflux.GenerateOptions{Options: schemaflux.Options{MaxOutputTokens: -1}}, schemaflux.ErrConfiguration},
		{"a tier that is not one", "x", schemaflux.GenerateOptions{Options: schemaflux.Options{Tier: -1}}, schemaflux.ErrConfiguration},
		{"a mode that is not one", "x", schemaflux.GenerateOptions{Options: schemaflux.Options{Mode: 9}}, schemaflux.ErrConfiguration},
		{"seed data that cannot be written as JSON", "x", schemaflux.GenerateOptions{SeedData: make(chan int)}, schemaflux.ErrConfiguration},
		{"examples that cannot be written as JSON", "x", schemaflux.GenerateOptions{Examples: []any{func() {}}}, schemaflux.ErrConfiguration},
		{"a blank prompt", "   ", schemaflux.GenerateOptions{}, schemaflux.ErrInvalidRequest},
		{"an empty prompt", "", schemaflux.GenerateOptions{}, schemaflux.ErrInvalidRequest},
		{"a prompt over the limit", strings.Repeat("a", 10001), schemaflux.GenerateOptions{}, schemaflux.ErrTooLarge},
		{"a prompt made too long by its options", strings.Repeat("a", 9990), schemaflux.GenerateOptions{Style: strings.Repeat("b", 50)}, schemaflux.ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(generateAnswer)
			got, err := schemaflux.Generate[generatedMeal](operationClient(model), tc.prompt, tc.opts)
			wantError(t, err, tc.kind, "generate")
			if got != (generatedMeal{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
		})
	}
}

func TestGenerateTakesAPromptOfExactlyTheLimit(t *testing.T) {
	t.Parallel()
	model := answeringWith("fine")
	if _, err := schemaflux.Generate[string](operationClient(model), strings.Repeat("a", 10000)); err != nil {
		t.Fatalf("a prompt of 10000 characters was refused: %v", err)
	}
}

func TestGenerateMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("no value is the default", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(generateAnswer)
		if _, err := schemaflux.Generate[generatedMeal](operationClient(model), "x"); err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Style:")
	})
	t.Run("one value is used as it is", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(generateAnswer)
		if _, err := schemaflux.Generate[generatedMeal](operationClient(model), "x", schemaflux.GenerateOptions{Style: "only"}); err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Style: only")
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(generateAnswer)
		first := schemaflux.GenerateOptions{Style: "first style", Template: "kept template", EnsureUnique: true}
		second := schemaflux.GenerateOptions{Style: "second style"}
		if _, err := schemaflux.Generate[generatedMeal](operationClient(model), "x", first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "Style: second style")
		wantNoText(t, req, "first style")
		wantText(t, req, "Use this template: kept template")
		wantText(t, req, "must be distinct")
	})
	t.Run("a later seed value replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(generateAnswer)
		_, err := schemaflux.Generate[generatedMeal](operationClient(model), "x",
			schemaflux.GenerateOptions{SeedData: "first seed"}, schemaflux.GenerateOptions{SeedData: "second seed"})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "second seed")
		wantNoText(t, model.Last(), "first seed")
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Generate[generatedMeal](operationClient(answeringWith(generateAnswer)), "x",
			schemaflux.GenerateOptions{}, schemaflux.GenerateOptions{Options: schemaflux.Options{MaxOutputTokens: -1}})
		wantError(t, err, schemaflux.ErrConfiguration, "generate")
	})
}

func TestGenerateReturnsAnErrorTheCallerCanClassifyForEachWayTheCallFails(t *testing.T) {
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
		{"the answer is not JSON", func(p *schemafluxtest.Provider) { p.Reply("I cannot help with that.") }, schemaflux.ErrInvalidAnswer},
		{"the answer is the wrong shape", func(p *schemafluxtest.Provider) { p.Reply(`["a","b"]`) }, schemaflux.ErrInvalidAnswer},
		{"the answer has a field of the wrong type", func(p *schemafluxtest.Provider) { p.Reply(`{"name":7,"calories":"x"}`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Generate[generatedMeal](operationClient(&model), "A dinner")
			wantError(t, err, tc.kind, "generate")
			if got != (generatedMeal{}) {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
	t.Run("a string that is asked for and the provider fails", func(t *testing.T) {
		t.Parallel()
		var model schemafluxtest.Provider
		model.Fail(&schemaflux.Error{Kind: schemaflux.ErrTimeout})
		got, err := schemaflux.Generate[string](operationClient(&model), "A bio")
		wantError(t, err, schemaflux.ErrTimeout, "generate")
		if got != "" {
			t.Errorf("a failed call left %q beside the error", got)
		}
	})
}

func TestGenerateWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(generateAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Generate[generatedMeal](ctx, "A dinner")
	wantError(t, err, schemaflux.ErrCanceled, "generate")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestGenerateRecordsTheLevelOfItsTypeArgument(t *testing.T) {
	t.Parallel()
	var asString, asStruct schemaflux.Recorder
	if _, err := schemaflux.Generate[string](operationClient(answeringWith("text"), recordingTo(&asString)), "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := schemaflux.Generate[generatedMeal](operationClient(answeringWith(generateAnswer), recordingTo(&asStruct)), "x"); err != nil {
		t.Fatal(err)
	}
	if r := onlyRecord(t, &asString); r.Requested != schemaflux.ContractPromptOnly || r.Delivered != schemaflux.ContractPromptOnly {
		t.Errorf("a string was held at %v and delivered at %v, want prompt only", r.Requested, r.Delivered)
	}
	if r := onlyRecord(t, &asStruct); r.Requested != schemaflux.ContractSchemaConstrained || r.Delivered != schemaflux.ContractSchemaConstrained {
		t.Errorf("a struct was held at %v and delivered at %v, want schema constrained", r.Requested, r.Delivered)
	}
}

func TestGenerateKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-3c9e55"
	type canaryType struct {
		Value string `json:"value" desc:"CANARY-3c9e55-tag"`
	}
	opts := schemaflux.GenerateOptions{
		Options:      schemaflux.Options{Steering: "steer " + canary},
		Template:     "template " + canary,
		Style:        "style " + canary,
		Constraints:  map[string]any{canary + "-key": "value " + canary},
		SeedData:     map[string]string{canary + "-seed-key": "seed " + canary},
		Examples:     []any{map[string]string{"value": "example " + canary}},
		EnsureUnique: true,
	}
	prompt := "prompt " + canary
	call := func(o schemaflux.GenerateOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Generate[canaryType](ctx, prompt, o)
			return err
		}
	}

	t.Run("a call that succeeds", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"value":"fine"}`) }, true, call(opts))
	})
	t.Run("an answer that is refused", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("the value is " + canary + " not json") }, true, call(opts))
	})
	t.Run("an answer of the wrong shape that quotes the prompt", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"value":["` + canary + `"],"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a prompt that is too long", func(t *testing.T) {
		t.Parallel()
		long := opts
		long.Template = canary + strings.Repeat("a", 10001)
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(long))
	})
	t.Run("seed data that cannot be written", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.SeedData = map[string]any{canary: make(chan int)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MaxOutputTokens = -1
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
}

func TestGenerateIntoATypeWithNoSchemaIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	wantLevelFollowsTheType(t, levelCall{
		op: "generate", mapReply: `{"dish":"soup"}`, structReply: generateAnswer,
		intoMap: func(ctx context.Context) error {
			_, err := schemaflux.Generate[map[string]string](ctx, "x")
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Generate[generatedMeal](ctx, "x")
			return err
		},
	})
	// A recursive type and a slice of maps are no different from a map: a strict
	// schema cannot describe them, and none is sent.
	type tree struct {
		Name string `json:"name"`
		Next *tree  `json:"next"`
	}
	for name, call := range map[string]func(ctx context.Context) error{
		"a recursive type": func(ctx context.Context) error { _, err := schemaflux.Generate[tree](ctx, "x"); return err },
		"a slice of maps": func(ctx context.Context) error {
			_, err := schemaflux.Generate[[]map[string]string](ctx, "x")
			return err
		},
	} {
		t.Run(name+" is refused under a schema-constrained floor", func(t *testing.T) {
			model := namedModel(`{"name":"a","next":null}`)
			wantError(t, call(operationClient(model, underFloor(schemaflux.ContractSchemaConstrained))), schemaflux.ErrPolicy, "generate")
			if model.Calls() != 0 {
				t.Errorf("%d requests were sent", model.Calls())
			}
		})
	}
	t.Run("a string is prompt only and no floor above that admits it", func(t *testing.T) {
		model := namedModel("text")
		_, err := schemaflux.Generate[string](operationClient(model, underFloor(schemaflux.ContractJSONWellFormed)), "x")
		wantError(t, err, schemaflux.ErrPolicy, "generate")
		if model.Calls() != 0 {
			t.Errorf("%d requests were sent", model.Calls())
		}
	})
}
