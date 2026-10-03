package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Explain refuses options it cannot use and data it cannot show a model before
// it asks one. Each refusal is the text it was and has a kind: the caller passed
// something that cannot be used.
func TestExplainRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	explain := func(data any, edit func(*ExplainOptions)) func() error {
		return func() error {
			opts := NewExplainOptions()
			edit(&opts)
			_, err := Explain(data, opts)
			return err
		}
	}
	keep := func(*ExplainOptions) {}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "an audience that is not one of the six",
			run:  explain("data", func(o *ExplainOptions) { o.Audience = "alien" }),
			text: "invalid options: invalid audience: alien",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "no audience",
			run:  explain("data", func(o *ExplainOptions) { o.Audience = "" }),
			text: "invalid options: invalid audience: ",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a depth of zero",
			run:  explain("data", func(o *ExplainOptions) { o.Depth = 0 }),
			text: "invalid options: depth must be between 1 and 4, got 0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a depth above four",
			run:  explain("data", func(o *ExplainOptions) { o.Depth = 5 }),
			text: "invalid options: depth must be between 1 and 4, got 5",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative depth",
			run:  explain("data", func(o *ExplainOptions) { o.Depth = -2 }),
			text: "invalid options: depth must be between 1 and 4, got -2",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a format that is not one of the five",
			run:  explain("data", func(o *ExplainOptions) { o.Format = "haiku" }),
			text: "invalid options: invalid format: haiku",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a focus that is not one of the six",
			run:  explain("data", func(o *ExplainOptions) { o.Focus = "gossip" }),
			text: "invalid options: invalid focus: gossip",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the audience is read before the depth",
			run: explain("data", func(o *ExplainOptions) {
				o.Audience = "alien"
				o.Depth = 0
			}),
			text: "invalid options: invalid audience: alien",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "no data",
			run:  explain(nil, keep),
			text: "data analysis failed: cannot explain nil data",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer",
			run:  explain((*int)(nil), keep),
			text: "data analysis failed: cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a struct",
			run:  explain((*struct{ A int })(nil), keep),
			text: "data analysis failed: cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "data JSON cannot write",
			run:  explain(make(chan int), keep),
			text: "explanation generation failed: failed to marshal data: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "a struct holding a function",
			run:  explain(struct{ F func() }{}, keep),
			text: "explanation generation failed: failed to marshal data: json: unsupported type: func()",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
	})
}

// analyzeDataForExplanation turns away the two values that have nothing to
// explain, with the kind of a caller's mistake.
func TestAnalyzeDataForExplanationRefusesWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	analyze := func(data any) func() error {
		return func() error {
			_, err := analyzeDataForExplanation(data)
			return err
		}
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "nil",
			run:  analyze(nil),
			text: "cannot explain nil data",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to an integer",
			run:  analyze((*int)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a string",
			run:  analyze((*string)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a slice",
			run:  analyze((*[]int)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a map",
			run:  analyze((*map[string]int)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a struct",
			run:  analyze((*completeTarget)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a pointer",
			run:  analyze((**int)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to an array",
			run:  analyze((*[3]int)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a bool",
			run:  analyze((*bool)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a nil pointer to a float",
			run:  analyze((*float64)(nil)),
			text: "cannot explain nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
