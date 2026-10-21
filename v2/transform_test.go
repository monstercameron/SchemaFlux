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

// transformSource and transformTarget are the types the Transform tests turn one
// into the other.
type transformSource struct {
	First string `json:"first"`
	Last  string `json:"last"`
	ID    int    `json:"id"`
}

type transformTarget struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

const transformAnswer = `{"name":"Ada Lovelace","id":7}`

func TestTransformReadsTheAnswerIntoTheTargetType(t *testing.T) {
	t.Parallel()
	model := answeringWith(transformAnswer)
	got, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{First: "Ada", Last: "Lovelace", ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got != (transformTarget{Name: "Ada Lovelace", ID: 7}) {
		t.Errorf("got %+v", got)
	}
	if model.Calls() != 1 {
		t.Fatalf("%d requests were sent, want 1", model.Calls())
	}
	req := model.Last()
	if req.Format != "json" || req.SchemaName != "transformTarget" || req.Schema == nil {
		t.Errorf("request = format %q schema name %q schema %v, want a declared JSON schema of the target", req.Format, req.SchemaName, req.Schema != nil)
	}
	wantText(t, req, "Transform this data")
	wantText(t, req, `"first":"Ada"`)
}

func TestTransformInfersTheInputTypeAndTakesTheOutputTypeFirst(t *testing.T) {
	t.Parallel()
	// The first type argument is the type asked for; the input's type is inferred.
	model := answeringWith(`{"count":3}`)
	got, err := schemaflux.Transform[struct {
		Count int `json:"count"`
	}](operationClient(model), []string{"a", "b", "c"})
	if err != nil || got.Count != 3 {
		t.Fatalf("got %+v, %v", got, err)
	}
	wantText(t, model.Last(), `["a","b","c"]`)
}

func TestTransformWithNoOptionsAsksForTheFastTierInTransformMode(t *testing.T) {
	t.Parallel()
	model := answeringWith(transformAnswer)
	if _, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{}); err != nil {
		t.Fatal(err)
	}
	req := model.Last()
	if math.Abs(req.Temperature-0.3) > 1e-6 || req.MaxTokens != 2000 || req.Model != "test-model" {
		t.Errorf("temperature %v, ceiling %d, model %q: want 0.3, 2000, test-model", req.Temperature, req.MaxTokens, req.Model)
	}
	if req.Effort != "" || req.WebSearch {
		t.Errorf("a default call carried an effort or web search: %+v", req)
	}
	wantText(t, req, "Use replace merge strategy")
	wantNoText(t, req, "Field mappings")
	wantNoText(t, req, "Preserve these fields")
	wantNoText(t, req, "Apply this transformation")
}

func TestTransformEachOptionFieldChangesTheRequestAsItsDocumentationSays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		opts  schemaflux.TransformOptions
		check func(t *testing.T, req schemaflux.Request)
	}{
		{"MappingRules", schemaflux.TransformOptions{MappingRules: map[string]string{"name": "first + last", "id": "copy"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Field mappings: id <- copy; name <- first + last")
		}},
		{"PreserveFields", schemaflux.TransformOptions{PreserveFields: []string{"id", "created"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Preserve these fields: id, created")
		}},
		{"MergeStrategy merge", schemaflux.TransformOptions{MergeStrategy: "merge"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use merge merge strategy")
			wantNoText(t, req, "replace merge")
		}},
		{"MergeStrategy append", schemaflux.TransformOptions{MergeStrategy: "append"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use append merge strategy")
		}},
		{"MergeStrategy replace", schemaflux.TransformOptions{MergeStrategy: "replace"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Use replace merge strategy")
		}},
		{"Logic", schemaflux.TransformOptions{Logic: "uppercase the name"}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "Apply this transformation: uppercase the name")
		}},
		{"Steering", schemaflux.TransformOptions{Options: schemaflux.Options{Steering: "keep titles"}}, func(t *testing.T, req schemaflux.Request) {
			wantText(t, req, "keep titles")
		}},
		{"Model", schemaflux.TransformOptions{Options: schemaflux.Options{Model: "pinned-model"}}, func(t *testing.T, req schemaflux.Request) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"ModeStrict", schemaflux.TransformOptions{Options: schemaflux.Options{Mode: schemaflux.ModeStrict}}, func(t *testing.T, req schemaflux.Request) {
			if math.Abs(req.Temperature-0.1) > 1e-6 {
				t.Errorf("temperature = %v, want 0.1", req.Temperature)
			}
		}},
		{"TierSmart", schemaflux.TransformOptions{Options: schemaflux.Options{Tier: schemaflux.TierSmart}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 4000 {
				t.Errorf("ceiling = %d, want the smart tier's 4000", req.MaxTokens)
			}
		}},
		{"MaxOutputTokens", schemaflux.TransformOptions{Options: schemaflux.Options{MaxOutputTokens: 77}}, func(t *testing.T, req schemaflux.Request) {
			if req.MaxTokens != 77 {
				t.Errorf("ceiling = %d, want 77", req.MaxTokens)
			}
		}},
		{"WebSearch", schemaflux.TransformOptions{Options: schemaflux.Options{WebSearch: true}}, func(t *testing.T, req schemaflux.Request) {
			if !req.WebSearch {
				t.Error("the request does not declare web search")
			}
		}},
		{"Effort", schemaflux.TransformOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}, func(t *testing.T, req schemaflux.Request) {
			if req.Effort != "low" || req.EffortPreferred {
				t.Errorf("effort = %q preferred %v, want low, asked for by the caller", req.Effort, req.EffortPreferred)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(transformAnswer)
			if _, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{First: "Ada", ID: 7}, tc.opts); err != nil {
				t.Fatal(err)
			}
			tc.check(t, model.Last())
		})
	}
}

func TestTransformRefusesABadOptionBeforeAnythingIsSent(t *testing.T) {
	t.Parallel()
	cases := map[string]schemaflux.TransformOptions{
		"a negative output ceiling":      {Options: schemaflux.Options{MaxOutputTokens: -1}},
		"a tier that is not one":         {Options: schemaflux.Options{Tier: schemaflux.TierQuick + 1}},
		"a mode that is not one":         {Options: schemaflux.Options{Mode: schemaflux.ModeCreative + 1}},
		"a merge strategy of no kind":    {MergeStrategy: "upsert"},
		"a capitalised merge strategy":   {MergeStrategy: "Replace"},
		"a bad setting beside good ones": {Logic: "x", MergeStrategy: "nonsense", Options: schemaflux.Options{Model: "m"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(transformAnswer)
			got, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{}, opts)
			e := wantError(t, err, schemaflux.ErrConfiguration, "transform")
			if got != (transformTarget{}) || model.Calls() != 0 {
				t.Errorf("got %+v after %d requests", got, model.Calls())
			}
			if strings.Contains(e.Error(), "upsert") || strings.Contains(e.Error(), "nonsense") || strings.Contains(e.Error(), "Replace") {
				t.Errorf("the error repeats the caller's setting: %q", e.Error())
			}
		})
	}
}

func TestTransformMergesSeveralOptionsValuesFromLeftToRight(t *testing.T) {
	t.Parallel()
	t.Run("no value is the default", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(transformAnswer)
		if _, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{}); err != nil {
			t.Fatal(err)
		}
		wantNoText(t, model.Last(), "Apply this transformation")
	})
	t.Run("one value is used as it is", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(transformAnswer)
		_, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{}, schemaflux.TransformOptions{Logic: "only"})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "Apply this transformation: only")
	})
	t.Run("a later value that is not zero wins and a later zero leaves the earlier alone", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(transformAnswer)
		first := schemaflux.TransformOptions{Logic: "first logic", MergeStrategy: "append", PreserveFields: []string{"id"}}
		second := schemaflux.TransformOptions{Logic: "second logic"}
		if _, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{ID: 7}, first, second); err != nil {
			t.Fatal(err)
		}
		req := model.Last()
		wantText(t, req, "second logic")
		wantNoText(t, req, "first logic")
		wantText(t, req, "Use append merge strategy")
		wantText(t, req, "Preserve these fields: id")
	})
	t.Run("a later map replaces the earlier one", func(t *testing.T) {
		t.Parallel()
		model := answeringWith(transformAnswer)
		_, err := schemaflux.Transform[transformTarget](operationClient(model), transformSource{},
			schemaflux.TransformOptions{MappingRules: map[string]string{"name": "first"}},
			schemaflux.TransformOptions{MappingRules: map[string]string{"id": "second"}})
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, model.Last(), "id <- second")
		wantNoText(t, model.Last(), "name <- first")
	})
	t.Run("a bad setting in any value is refused", func(t *testing.T) {
		t.Parallel()
		_, err := schemaflux.Transform[transformTarget](operationClient(answeringWith(transformAnswer)), transformSource{},
			schemaflux.TransformOptions{}, schemaflux.TransformOptions{MergeStrategy: "upsert"})
		wantError(t, err, schemaflux.ErrConfiguration, "transform")
	})
}

func TestTransformReturnsAnErrorTheCallerCanClassifyForEachWayTheCallFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(*schemafluxtest.Provider)
		kind  schemaflux.Kind
	}{
		{"the provider is unavailable", func(p *schemafluxtest.Provider) { p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable}) }, schemaflux.ErrUnavailable},
		{"the key is refused", func(p *schemafluxtest.Provider) {
			p.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Status: 401})
		}, schemaflux.ErrUnauthorized},
		{"the answer is not JSON", func(p *schemafluxtest.Provider) { p.Reply("I cannot help with that.") }, schemaflux.ErrInvalidAnswer},
		{"the answer is the wrong shape", func(p *schemafluxtest.Provider) { p.Reply(`["a","b"]`) }, schemaflux.ErrInvalidAnswer},
		{"the answer has a field of the wrong type", func(p *schemafluxtest.Provider) { p.Reply(`{"name":7,"id":"x"}`) }, schemaflux.ErrInvalidAnswer},
		{"the answer is empty", func(p *schemafluxtest.Provider) { p.Reply("") }, schemaflux.ErrInvalidAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			tc.setup(&model)
			got, err := schemaflux.Transform[transformTarget](operationClient(&model), transformSource{})
			wantError(t, err, tc.kind, "transform")
			if got != (transformTarget{}) {
				t.Errorf("a failed call left %+v beside the error", got)
			}
		})
	}
}

func TestTransformWithACancelledContextReturnsCanceledAndSendsNothing(t *testing.T) {
	t.Parallel()
	model := answeringWith(transformAnswer)
	ctx, cancel := context.WithCancel(operationClient(model))
	cancel()
	_, err := schemaflux.Transform[transformTarget](ctx, transformSource{})
	wantError(t, err, schemaflux.ErrCanceled, "transform")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the context's own error is not reachable: %v", err)
	}
	if model.Calls() != 0 {
		t.Errorf("%d requests were sent", model.Calls())
	}
}

func TestTransformReadsAnyKindOfInput(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		call func(ctx context.Context) error
		seen string
	}{
		"a struct": {func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, transformSource{First: "B-2"})
			return err
		}, "B-2"},
		"a pointer to a struct": {func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, &transformSource{First: "C-3"})
			return err
		}, "C-3"},
		"a map": {func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, map[string]any{"ref": "D-4"})
			return err
		}, "D-4"},
		"a string": {func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, "E-5")
			return err
		}, "E-5"},
		"an interface holding a value": {func(ctx context.Context) error {
			var in any = transformSource{First: "F-6"}
			_, err := schemaflux.Transform[transformTarget](ctx, in)
			return err
		}, "F-6"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			model := answeringWith(transformAnswer)
			if err := tc.call(operationClient(model)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(model.Last().User, tc.seen) {
				t.Errorf("%q is not in the user message: %s", tc.seen, sentTo(model))
			}
		})
	}
}

func TestTransformKeepsThePayloadOutOfEverythingItLeavesBehind(t *testing.T) {
	t.Parallel()
	const canary = "CANARY-7b21d4"
	type canarySource struct {
		Text string `json:"text"`
	}
	type canaryTarget struct {
		Value string `json:"value" desc:"CANARY-7b21d4-tag"`
	}
	opts := schemaflux.TransformOptions{
		Options:        schemaflux.Options{Steering: "steer " + canary},
		MappingRules:   map[string]string{"value": "text " + canary},
		PreserveFields: []string{canary + "-field"},
		Logic:          "logic " + canary,
	}
	input := canarySource{Text: "text " + canary}
	call := func(o schemaflux.TransformOptions) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			_, err := schemaflux.Transform[canaryTarget](ctx, input, o)
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
	t.Run("an answer of the wrong shape that quotes the input", func(t *testing.T) {
		t.Parallel()
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply(`{"value":["` + canary + `"],"` + canary + `":1}`) }, true, call(opts))
	})
	t.Run("a provider that fails with a vendor message beneath its error", func(t *testing.T) {
		t.Parallel()
		vendor := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "the provider is down", Err: errors.New("vendor said " + canary)}
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Fail(vendor) }, false, call(opts))
	})
	t.Run("a refused option", func(t *testing.T) {
		t.Parallel()
		bad := opts
		bad.MergeStrategy = canary
		requireNoPayload(t, canary, func(p *schemafluxtest.Provider) { p.Reply("{}") }, true, call(bad))
	})
}

func TestTransformRefusesAnAnswerThatChangesAFieldItWasToldToPreserve(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		answer  string
		opts    schemaflux.TransformOptions
		refused bool
	}{
		{"the preserved field comes back unchanged", `{"name":"Ada","id":7}`, schemaflux.TransformOptions{PreserveFields: []string{"id"}}, false},
		{"the preserved field is changed", `{"name":"Ada","id":8}`, schemaflux.TransformOptions{PreserveFields: []string{"id"}}, true},
		{"a field that is not preserved may change", `{"name":"Someone else","id":7}`, schemaflux.TransformOptions{PreserveFields: []string{"id"}}, false},
		{"nothing is preserved by default", `{"name":"Ada","id":8}`, schemaflux.TransformOptions{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := schemaflux.Transform[transformTarget](operationClient(answeringWith(tc.answer)), transformSource{First: "Ada", ID: 7}, tc.opts)
			if !tc.refused {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantError(t, err, schemaflux.ErrInvalidAnswer, "transform")
			if got != (transformTarget{}) {
				t.Errorf("a refused answer left %+v beside the error", got)
			}
		})
	}
}

func TestTransformIntoAMapIsDeliveredAsWellFormedAndHeldToThatLevelByAFloor(t *testing.T) {
	t.Parallel()
	source := transformSource{First: "Ada", Last: "Lovelace", ID: 7}
	wantLevelFollowsTheType(t, levelCall{
		op: "transform", mapReply: `{"name":"Ada Lovelace"}`, structReply: transformAnswer,
		intoMap: func(ctx context.Context) error {
			got, err := schemaflux.Transform[map[string]string](ctx, source)
			if err == nil && got["name"] != "Ada Lovelace" {
				t.Errorf("got %v", got)
			}
			return err
		},
		intoStruct: func(ctx context.Context) error {
			_, err := schemaflux.Transform[transformTarget](ctx, source)
			return err
		},
	})
}
