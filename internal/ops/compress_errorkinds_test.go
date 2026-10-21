package ops

import (
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Compress refuses options it cannot use and an input that is nothing before it
// asks a model. Each refusal is the text it was and has a kind: the caller
// passed something that cannot be used.
func TestCompressRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	compress := func(edit func(*CompressOptions)) func() error {
		return func() error {
			opts := NewCompressOptions()
			edit(&opts)
			_, err := Compress("some text to compress", opts)
			return err
		}
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "a compression ratio of zero",
			run:  compress(func(o *CompressOptions) { o.CompressionRatio = 0 }),
			text: "invalid options: compression ratio must be between 0 and 1, got 0.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a compression ratio above one",
			run:  compress(func(o *CompressOptions) { o.CompressionRatio = 1.5 }),
			text: "invalid options: compression ratio must be between 0 and 1, got 1.500000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative compression ratio",
			run:  compress(func(o *CompressOptions) { o.CompressionRatio = -0.25 }),
			text: "invalid options: compression ratio must be between 0 and 1, got -0.250000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a compression ratio that is not a number",
			run:  compress(func(o *CompressOptions) { o.CompressionRatio = math.NaN() }),
			text: "invalid options: compression ratio must be between 0 and 1, got NaN",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a strategy that is not one of the three",
			run:  compress(func(o *CompressOptions) { o.Strategy = "crush" }),
			text: "invalid options: compression strategy must be lossy, lossless, or semantic",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a priority that is not one of the four",
			run:  compress(func(o *CompressOptions) { o.Priority = "feelings" }),
			text: "invalid options: compression priority must be facts, actions, decisions, or all",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an output format that is not one of the four",
			run:  compress(func(o *CompressOptions) { o.OutputFormat = "haiku" }),
			text: "invalid options: compression output format must be same, summary, key_points, or structured",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a threshold above one",
			run:  compress(func(o *CompressOptions) { o.CommonOptions.Threshold = 5 }),
			text: "invalid options: threshold must be between 0 and 1, got 5.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the threshold is read before the ratio",
			run: compress(func(o *CompressOptions) {
				o.CommonOptions.Threshold = 5
				o.CompressionRatio = 0
			}),
			text: "invalid options: threshold must be between 0 and 1, got 5.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "no input",
			run: func() error {
				_, err := Compress[any](nil, NewCompressOptions())
				return err
			},
			text: "failed to normalize input: input is nil",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "the text form refuses the same options",
			run: func() error {
				opts := NewCompressOptions()
				opts.CompressionRatio = 3
				_, err := CompressText("text", opts)
				return err
			},
			text: "invalid options: compression ratio must be between 0 and 1, got 3.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
