package ops

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type refusalSource struct {
	Name string `json:"name"`
}

type refusalTarget struct {
	Title string `json:"title"`
}

// Extract, Transform and Generate turn away what the caller passed before they
// ask a model. Each refusal is the text it was and has a kind: a plain refusal
// of the options or the arguments is a configuration error, and what the
// operations classified already keeps the kind it had.
func TestCoreOperationsRefuseBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	extract := func(input any, edit func(*ExtractOptions)) func() error {
		return func() error {
			opts := NewExtractOptions()
			edit(&opts)
			_, err := Extract[refusalTarget](input, opts)
			return err
		}
	}
	transform := func(edit func(*TransformOptions)) func() error {
		return func() error {
			opts := NewTransformOptions()
			edit(&opts)
			_, err := Transform[refusalSource, refusalTarget](refusalSource{Name: "Ada"}, opts)
			return err
		}
	}
	generate := func(prompt string, edit func(*GenerateOptions)) func() error {
		return func() error {
			opts := NewGenerateOptions()
			edit(&opts)
			_, err := Generate[refusalTarget](prompt, opts)
			return err
		}
	}
	keepExtract := func(*ExtractOptions) {}
	keepGenerate := func(*GenerateOptions) {}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "extract with strict schema and partial extraction both on",
			run: extract("text", func(o *ExtractOptions) {
				o.StrictSchema = true
				o.AllowPartial = true
			}),
			text: "invalid options: cannot have both StrictSchema and AllowPartial",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "extract with a threshold above one",
			run:  extract("text", func(o *ExtractOptions) { o.CommonOptions.Threshold = 3 }),
			text: "invalid options: threshold must be between 0 and 1, got 3.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "extract with a threshold that is not a number",
			run:  extract("text", func(o *ExtractOptions) { o.CommonOptions.Threshold = math.NaN() }),
			text: "invalid options: threshold must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "extract with no input keeps the kind it had",
			run:  extract(nil, keepExtract),
			text: "extraction failed: input cannot be nil",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "transform with a merge strategy it does not know",
			run:  transform(func(o *TransformOptions) { o.MergeStrategy = "sideways" }),
			text: "invalid options: invalid merge strategy: sideways",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "transform with a threshold below zero",
			run:  transform(func(o *TransformOptions) { o.CommonOptions.Threshold = -1 }),
			text: "invalid options: threshold must be between 0 and 1, got -1.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "transform of input JSON cannot write",
			run: func() error {
				_, err := Transform[chan int, refusalTarget](make(chan int), NewTransformOptions())
				return err
			},
			text: "transformation failed: failed to marshal input: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "transform of a struct holding a function",
			run: func() error {
				_, err := Transform[struct{ F func() }, refusalTarget](struct{ F func() }{}, NewTransformOptions())
				return err
			},
			text: "transformation failed: failed to marshal input: json: unsupported type: func()",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "generate with a count of zero",
			run:  generate("a product", func(o *GenerateOptions) { o.Count = 0 }),
			text: "invalid options: count must be at least 1, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate with a negative count",
			run:  generate("a product", func(o *GenerateOptions) { o.Count = -2 }),
			text: "invalid options: count must be at least 1, got -2",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate with a threshold above one",
			run:  generate("a product", func(o *GenerateOptions) { o.CommonOptions.Threshold = 8 }),
			text: "invalid options: threshold must be between 0 and 1, got 8.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate more than one at a time",
			run:  generate("a product", func(o *GenerateOptions) { o.Count = 3 }),
			text: "batch generation not yet supported - use Count=1",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate from an empty prompt",
			run:  generate("", keepGenerate),
			text: "generation failed: prompt cannot be empty",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "generate from a prompt of spaces",
			run:  generate("  \n ", keepGenerate),
			text: "generation failed: prompt cannot be empty",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "generate from seed data JSON cannot write",
			run:  generate("a product", func(o *GenerateOptions) { o.SeedData = make(chan int) }),
			text: "generate: configuration: the seed data cannot be written as JSON",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate from examples JSON cannot write",
			run:  generate("a product", func(o *GenerateOptions) { o.Examples = []any{func() {}} }),
			text: "generate: configuration: the examples cannot be written as JSON",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "generate from a prompt over the limit keeps the kind it had",
			run:  generate(strings.Repeat("a", generateMaxPromptLength+1), keepGenerate),
			text: "generation failed: the prompt is 10001 characters, over the limit of 10000; shorten it or send less seed data and fewer examples",
			kind: types.KindContextTooLarge, matches: []error{types.ErrContextTooLarge},
		},
	})
}

// A refusal that has a kind still reaches the concrete error type it was made
// from, which a caller of the old root may match with errors.As.
func TestCoreRefusalsStillReachTheirErrorTypes(t *testing.T) {
	swapLLMCaller(t, func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		t.Error("a refused call reached the model")
		return "", errors.New("the model was not to be asked")
	})

	t.Run("an empty prompt is a GenerateError", func(t *testing.T) {
		_, err := Generate[refusalTarget]("", NewGenerateOptions())
		var generation types.GenerateError
		if !errors.As(err, &generation) || generation.Reason != "prompt cannot be empty" {
			t.Fatalf("no types.GenerateError in the chain of %v", err)
		}
		if _, direct := err.(types.GenerateError); !direct {
			t.Errorf("the error is no longer a types.GenerateError: %T", err)
		}
	})
	t.Run("input that cannot be written is a TransformError", func(t *testing.T) {
		_, err := Transform[chan int, refusalTarget](make(chan int), NewTransformOptions())
		var transformation types.TransformError
		if !errors.As(err, &transformation) {
			t.Fatalf("no types.TransformError in the chain of %v", err)
		}
		if _, direct := err.(types.TransformError); !direct {
			t.Errorf("the error is no longer a types.TransformError: %T", err)
		}
	})
}
