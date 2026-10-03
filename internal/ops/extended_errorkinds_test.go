package ops

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// reachesUnsupportedType reports whether err still wraps encoding/json's
// refusal to write a value, which is what a caller who matched the cause saw
// before the refusal had a kind.
func reachesUnsupportedType(err error) bool {
	var unsupported *json.UnsupportedTypeError
	return errors.As(err, &unsupported)
}

// The operations of extended.go refuse what the caller passed before they ask a
// model: options they cannot use, no sources, a value JSON cannot write, rules
// a model would have to judge. Each refusal is the text it was and now has a
// kind. The ones that were classified already keep the kind they had.
func TestExtendedOperationsRefuseBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest
	unwritable := make(chan int)

	validateOptions := func(edit func(*ValidateOptions)) ValidateOptions {
		opts := NewValidateOptions()
		opts.FieldRules = map[string]string{"email": "email"}
		edit(&opts)
		return opts
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "validate deterministically with a failOn it does not know",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", validateOptions(func(o *ValidateOptions) { o.FailOn = "fatal" }))
				return err
			},
			text: "invalid options: invalid failOn: fatal",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate deterministically with a threshold above one",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", validateOptions(func(o *ValidateOptions) { o.CommonOptions.Threshold = 4 }))
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got 4.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate deterministically with free-text rules",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", validateOptions(func(o *ValidateOptions) { o.Rules = "looks plausible" }))
				return err
			},
			text: "validate deterministically: Rules is free text and requires model judgment; use ValidateHybrid",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate deterministically with schema hints",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", validateOptions(func(o *ValidateOptions) { o.SchemaHints = map[string]string{"x": "a number"} }))
				return err
			},
			text: "validate deterministically: SchemaHints requires model judgment; use ValidateHybrid",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate deterministically with a rule Go cannot decide",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", validateOptions(func(o *ValidateOptions) {
					o.FieldRules = map[string]string{"tone": "sounds friendly"}
				}))
				return err
			},
			text: "validate deterministically: field rules for [tone] are not decidable in Go; use ValidateHybrid",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate hybrid with a failOn it does not know",
			run: func() error {
				_, err := ValidateHybrid("a@b.c", validateOptions(func(o *ValidateOptions) { o.FailOn = "fatal" }))
				return err
			},
			text: "invalid options: invalid failOn: fatal",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate with a negative threshold",
			run: func() error {
				_, err := Validate("a@b.c", validateOptions(func(o *ValidateOptions) { o.CommonOptions.Threshold = -1 }))
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got -1.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate with nothing to validate against keeps the kind it had",
			run: func() error {
				opts := NewValidateOptions()
				_, err := Validate("a@b.c", opts)
				return err
			},
			text: "validate: configuration: there is nothing to validate against: give Rules, FieldRules or SchemaHints",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate deterministically with no rules keeps the kind it had",
			run: func() error {
				_, err := ValidateDeterministically("a@b.c", NewValidateOptions())
				return err
			},
			text: "validate: configuration: there is nothing to validate against: give FieldRules",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "validate legacy with data JSON cannot write",
			run: func() error {
				_, err := ValidateLegacy(unwritable, "any rule")
				return err
			},
			text: "failed to marshal data: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "merge with no sources",
			run: func() error {
				_, err := Merge([]string{}, "newest wins")
				return err
			},
			text: "no sources to merge",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "merge with a nil list of sources",
			run: func() error {
				_, err := Merge[map[string]any](nil, "newest wins")
				return err
			},
			text: "no sources to merge",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "merge with a source JSON cannot write",
			run: func() error {
				_, err := Merge([]chan int{unwritable, unwritable}, "newest wins")
				return err
			},
			text: "failed to marshal source 0: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "merge with metadata and no sources",
			run: func() error {
				_, err := MergeWithMetadata([]string{}, "newest wins")
				return err
			},
			text: "no sources to merge",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "merge with metadata and a source JSON cannot write",
			run: func() error {
				_, err := MergeWithMetadata([]chan int{unwritable, unwritable}, "newest wins")
				return err
			},
			text: "failed to marshal source 0: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "question with a blank question",
			run: func() error {
				_, err := Question[string, string]("data", NewQuestionOptions("   "))
				return err
			},
			text: "invalid options: question cannot be empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "question with a threshold above one",
			run: func() error {
				opts := NewQuestionOptions("what is it?")
				opts.CommonOptions.Threshold = 9
				_, err := Question[string, string]("data", opts)
				return err
			},
			text: "invalid options: threshold must be between 0 and 1, got 9.000000",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "question about data JSON cannot write",
			run: func() error {
				_, err := Question[chan int, string](unwritable, NewQuestionOptions("what is it?"))
				return err
			},
			text: "failed to marshal data: json: unsupported type: chan int",
			kind: types.KindInvalidRequest, matches: []error{invalid}, chain: reachesUnsupportedType,
		},
		{
			name: "deduplicate with a threshold above one keeps the kind it had",
			run: func() error {
				_, err := Deduplicate([]string{"a", "b"}, 1.5)
				return err
			},
			text: "deduplicate: configuration: the threshold must be between 0 and 1, got 1.5",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
