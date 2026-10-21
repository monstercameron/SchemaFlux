package ops

import (
	"errors"
	"regexp/syntax"
	"strconv"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Redact and RedactWithResult refuse options they cannot use, and a nil input,
// before they touch the value. Each refusal is the text it was and has a kind:
// the options leave through "invalid options" and carry the kind they were made
// with, and the input keeps the kind of an invalid request.
func TestRedactRefusesBeforeTheWorkWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	redact := func(edit func(*RedactOptions)) func() error {
		return func() error {
			opts := NewRedactOptions()
			edit(&opts)
			_, err := Redact("jane@example.com", opts)
			return err
		}
	}
	redactWithResult := func(edit func(*RedactOptions)) func() error {
		return func() error {
			opts := NewRedactOptions()
			edit(&opts)
			_, _, err := RedactWithResult("jane@example.com", opts)
			return err
		}
	}
	// reachesRegexpError says the refusal still carries the compiler's error.
	reachesRegexpError := func(err error) bool {
		var syntaxErr *syntax.Error
		return errors.As(err, &syntaxErr)
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "no category",
			run:  redact(func(o *RedactOptions) { o.Categories = nil }),
			text: "invalid options: redact: configuration: at least one category must be specified",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a category it does not know",
			run:  redact(func(o *RedactOptions) { o.Categories = []string{"PII", "gossip"} }),
			text: `invalid options: redact: configuration: category 1 ("gossip") is not one of pii, secrets or financial`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a strategy it does not know",
			run:  redact(func(o *RedactOptions) { o.Strategy = "smudge" }),
			text: "invalid options: redact: configuration: strategy must be nil, mask, remove, jumble, or scramble",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a mask length below minus one",
			run:  redact(func(o *RedactOptions) { o.MaskLength = -2 }),
			text: "invalid options: redact: configuration: MaskLength -2 is out of range: use -1 for the length of the value, or 0 to " + strconv.Itoa(maxRedactMaskLength),
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a mask length above the ceiling",
			run:  redact(func(o *RedactOptions) { o.MaskLength = maxRedactMaskLength + 1 }),
			text: "invalid options: redact: configuration: MaskLength " + strconv.Itoa(maxRedactMaskLength+1) + " is out of range: use -1 for the length of the value, or 0 to " + strconv.Itoa(maxRedactMaskLength),
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a custom pattern that does not compile",
			run:  redact(func(o *RedactOptions) { o.CustomPatterns = []string{`(unclosed`} }),
			text: "invalid options: redact: configuration: custom pattern 0 does not compile",
			kind: types.KindConfiguration, matches: []error{config}, chain: reachesRegexpError,
		},
		{
			name: "a custom pattern that matches the empty string",
			run:  redact(func(o *RedactOptions) { o.CustomPatterns = []string{`a*`} }),
			text: "invalid options: redact: configuration: custom pattern 0 matches the empty string, so it would match between every two characters",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the second custom pattern is the one that fails",
			run:  redact(func(o *RedactOptions) { o.CustomPatterns = []string{`\d+`, `x*`} }),
			text: "invalid options: redact: configuration: custom pattern 1 matches the empty string, so it would match between every two characters",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the result form refuses no category",
			run:  redactWithResult(func(o *RedactOptions) { o.Categories = []string{} }),
			text: "invalid options: redact: configuration: at least one category must be specified",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the result form refuses a strategy it does not know",
			run:  redactWithResult(func(o *RedactOptions) { o.Strategy = "smudge" }),
			text: "invalid options: redact: configuration: strategy must be nil, mask, remove, jumble, or scramble",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "the result form refuses a custom pattern that does not compile",
			run:  redactWithResult(func(o *RedactOptions) { o.CustomPatterns = []string{`[a-`} }),
			text: "invalid options: redact: configuration: custom pattern 0 does not compile",
			kind: types.KindConfiguration, matches: []error{config}, chain: reachesRegexpError,
		},
		{
			name: "a nil interface keeps the kind it had",
			run: func() error {
				_, err := Redact[any](nil, NewRedactOptions())
				return err
			},
			text: "redact: invalid request: invalid input: a nil interface carries nothing to redact",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a nil interface, in the result form, keeps the kind it had",
			run: func() error {
				_, _, err := RedactWithResult[any](nil, NewRedactOptions())
				return err
			},
			text: "redact: invalid request: invalid input: a nil interface carries nothing to redact",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
	})
}
