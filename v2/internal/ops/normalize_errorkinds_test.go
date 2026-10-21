package ops

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Normalize refuses options it cannot use, and an input JSON cannot write,
// before it asks a model. Each refusal is the text it was and has a kind. The
// batch form reports the first item's refusal under the item's index and keeps
// its kind.
func TestNormalizeRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	normalize := func(edit func(*NormalizeOptions)) func() error {
		return func() error {
			opts := NewNormalizeOptions()
			edit(&opts)
			_, err := Normalize("some  text", opts)
			return err
		}
	}
	batch := func(items []string, edit func(*NormalizeOptions)) func() error {
		return func() error {
			opts := NewNormalizeOptions()
			edit(&opts)
			_, err := NormalizeBatch(items, opts)
			return err
		}
	}
	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "a case that is not one of the four",
			run:  normalize(func(o *NormalizeOptions) { o.NormalizeCase = "weird" }),
			text: "invalid options: normalize case must be lower, upper, title, or sentence",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a case in the wrong letters",
			run:  normalize(func(o *NormalizeOptions) { o.NormalizeCase = "Lower" }),
			text: "invalid options: normalize case must be lower, upper, title, or sentence",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold above one",
			run:  normalize(func(o *NormalizeOptions) { o.CommonOptions.Threshold = 7 }),
			text: "invalid options: threshold must be between 0 and 1, got 7.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold below zero",
			run:  normalize(func(o *NormalizeOptions) { o.CommonOptions.Threshold = -1 }),
			text: "invalid options: threshold must be between 0 and 1, got -1.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold that is not a number",
			run:  normalize(func(o *NormalizeOptions) { o.CommonOptions.Threshold = math.NaN() }),
			text: "invalid options: threshold must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the threshold is read before the case",
			run: normalize(func(o *NormalizeOptions) {
				o.CommonOptions.Threshold = 7
				o.NormalizeCase = "weird"
			}),
			text: "invalid options: threshold must be between 0 and 1, got 7.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the text form refuses the same options",
			run: func() error {
				opts := NewNormalizeOptions()
				opts.NormalizeCase = "weird"
				_, err := NormalizeText("text", opts)
				return err
			},
			text: "invalid options: normalize case must be lower, upper, title, or sentence",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an input JSON cannot write keeps the kind it had",
			run: func() error {
				_, err := Normalize(make(chan int), NewNormalizeOptions())
				return err
			},
			text: "normalize: invalid request: the input could not be written as JSON",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a struct holding a function keeps the kind it had",
			run: func() error {
				_, err := Normalize(struct{ F func() }{}, NewNormalizeOptions())
				return err
			},
			text: "normalize: invalid request: the input could not be written as JSON",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a batch with a case that is not one of the four",
			run:  batch([]string{"a"}, func(o *NormalizeOptions) { o.NormalizeCase = "weird" }),
			text: "failed to normalize item 0: invalid options: normalize case must be lower, upper, title, or sentence",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a batch of one with a threshold above one",
			run:  batch([]string{"a"}, func(o *NormalizeOptions) { o.CommonOptions.Threshold = 2 }),
			text: "failed to normalize item 0: invalid options: threshold must be between 0 and 1, got 2.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
