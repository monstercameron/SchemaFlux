package ops

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Suggest refuses options it cannot use, and input JSON cannot write, before it
// asks a model. Each refusal is the text it was and has a kind: the caller
// passed something that cannot be used.
func TestSuggestRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	suggest := func(input any, edit func(*SuggestOptions)) func() error {
		return func() error {
			opts := NewSuggestOptions()
			edit(&opts)
			_, err := Suggest[string](input, opts)
			return err
		}
	}
	keep := func(*SuggestOptions) {}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "a top N of zero",
			run:  suggest("state", func(o *SuggestOptions) { o.TopN = 0 }),
			text: "invalid options: topN must be at least 1, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative top N",
			run:  suggest("state", func(o *SuggestOptions) { o.TopN = -3 }),
			text: "invalid options: topN must be at least 1, got -3",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a strategy that is not one of the four",
			run:  suggest("state", func(o *SuggestOptions) { o.Strategy = "random" }),
			text: "invalid options: suggestion strategy must be contextual, pattern, goal, or hybrid",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "no strategy",
			run:  suggest("state", func(o *SuggestOptions) { o.Strategy = "" }),
			text: "invalid options: suggestion strategy must be contextual, pattern, goal, or hybrid",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold above one",
			run:  suggest("state", func(o *SuggestOptions) { o.CommonOptions.Threshold = 3 }),
			text: "invalid options: threshold must be between 0 and 1, got 3.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold below zero",
			run:  suggest("state", func(o *SuggestOptions) { o.CommonOptions.Threshold = -0.5 }),
			text: "invalid options: threshold must be between 0 and 1, got -0.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold that is not a number",
			run:  suggest("state", func(o *SuggestOptions) { o.CommonOptions.Threshold = math.NaN() }),
			text: "invalid options: threshold must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the threshold is read before the top N",
			run: suggest("state", func(o *SuggestOptions) {
				o.CommonOptions.Threshold = 2
				o.TopN = 0
			}),
			text: "invalid options: threshold must be between 0 and 1, got 2.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "input that is a channel",
			run:  suggest(make(chan int), keep),
			text: "failed to marshal input: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "input that is a function",
			run:  suggest(func() {}, keep),
			text: "failed to marshal input: json: unsupported type: func()",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "input that is a map holding a channel",
			run:  suggest(map[string]any{"c": make(chan int)}, keep),
			text: "failed to marshal input: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "input that is a complex number",
			run:  suggest(complex(1, 2), keep),
			text: "failed to marshal input: json: unsupported type: complex128",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "the result form refuses the same options",
			run: func() error {
				opts := NewSuggestOptions()
				opts.TopN = 0
				_, err := SuggestWithResult[string]("state", opts)
				return err
			},
			text: "invalid options: topN must be at least 1, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
