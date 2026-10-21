package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// RedactLLM refuses options it cannot use before it asks a model, with the kind
// of a caller's mistake and a message that is the options' own text.
func TestRedactLLMRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration

	redact := func(text string, edit func(*RedactLLMOptions)) func() error {
		return func() error {
			opts := NewRedactLLMOptions()
			edit(&opts)
			_, err := RedactLLM(context.Background(), text, opts)
			return err
		}
	}
	const prefix = "redact_llm: configuration: invalid options: "

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "no category",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.Categories = nil }),
			text: prefix + "at least one category must be specified",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty list of categories",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.Categories = []string{} }),
			text: prefix + "at least one category must be specified",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a blank category",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.Categories = []string{"email", "  "} }),
			text: prefix + "category 1 is blank",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty category in first place",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.Categories = []string{""} }),
			text: prefix + "category 0 is blank",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a category with a comma in its name",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.Categories = []string{"name,email"} }),
			text: prefix + "category 0 has a comma in its name, which is what separates two categories",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a minimum mask of zero",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.MinMask = 0 }),
			text: prefix + "MinMask must be at least 1",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative minimum mask",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.MinMask = -4 }),
			text: prefix + "MinMask must be at least 1",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative count of characters to show first",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.ShowFirst = -1 }),
			text: prefix + "ShowFirst and ShowLast must not be negative",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a negative count of characters to show last",
			run:  redact("call jane", func(o *RedactLLMOptions) { o.ShowLast = -1 }),
			text: prefix + "ShowFirst and ShowLast must not be negative",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "options are refused even when there is no text to redact",
			run:  redact("", func(o *RedactLLMOptions) { o.MinMask = 0 }),
			text: prefix + "MinMask must be at least 1",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the categories are read before the mask",
			run: redact("call jane", func(o *RedactLLMOptions) {
				o.Categories = nil
				o.MinMask = 0
			}),
			text: prefix + "at least one category must be specified",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
