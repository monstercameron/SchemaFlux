package ops

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Annotate refuses options it cannot use before it asks a model. Each refusal
// is the text it was and has a kind: the caller passed an option that cannot be
// used. An input that cannot be turned into text keeps the kind it had.
func TestAnnotateRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	annotate := func(edit func(*AnnotateOptions)) func() error {
		return func() error {
			opts := NewAnnotateOptions()
			edit(&opts)
			_, err := Annotate("Ada met Grace in London.", opts)
			return err
		}
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "no annotation type and no custom schema",
			run:  annotate(func(o *AnnotateOptions) { o.AnnotationTypes = nil }),
			text: "invalid options: at least one annotation type or custom schema is required",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty list of types and an empty schema",
			run: annotate(func(o *AnnotateOptions) {
				o.AnnotationTypes = []string{}
				o.CustomSchema = map[string]string{}
			}),
			text: "invalid options: at least one annotation type or custom schema is required",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a minimum confidence above one",
			run:  annotate(func(o *AnnotateOptions) { o.MinConfidence = 1.5 }),
			text: "invalid options: min confidence must be between 0 and 1, got 1.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative minimum confidence",
			run:  annotate(func(o *AnnotateOptions) { o.MinConfidence = -0.2 }),
			text: "invalid options: min confidence must be between 0 and 1, got -0.200000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a minimum confidence that is not a number",
			run:  annotate(func(o *AnnotateOptions) { o.MinConfidence = math.NaN() }),
			text: "invalid options: min confidence must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a format that is not one of the three",
			run:  annotate(func(o *AnnotateOptions) { o.Format = "xml" }),
			text: "invalid options: annotation format must be inline, standoff, or structured",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold above one",
			run:  annotate(func(o *AnnotateOptions) { o.CommonOptions.Threshold = 2 }),
			text: "invalid options: threshold must be between 0 and 1, got 2.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold below zero",
			run:  annotate(func(o *AnnotateOptions) { o.CommonOptions.Threshold = -3 }),
			text: "invalid options: threshold must be between 0 and 1, got -3.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the threshold is read before the types",
			run: annotate(func(o *AnnotateOptions) {
				o.CommonOptions.Threshold = 2
				o.AnnotationTypes = nil
			}),
			text: "invalid options: threshold must be between 0 and 1, got 2.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an input that is nothing keeps the kind it had",
			run: func() error {
				_, err := Annotate[any](nil, NewAnnotateOptions())
				return err
			},
			text: "annotate: invalid request: the input could not be turned into text",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
	})
}
