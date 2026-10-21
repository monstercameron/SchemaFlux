package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

type completeTargetInner struct{ Name string }

type completeTarget struct {
	Body   string
	Count  int
	hidden string
}

type completeTargetEmbedding struct{ *completeTargetInner }

// Complete and CompleteField turn away a bad option, an empty input and a field
// that cannot be completed before they ask a model. Each refusal is the text it
// was and has a kind: it is about what the caller passed.
func TestCompleteOperationsRefuseBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	ctx := context.Background()

	complete := func(text string, edit func(*CompleteOptions)) func() error {
		return func() error {
			opts := NewCompleteOptions()
			edit(&opts)
			_, err := Complete(ctx, text, opts)
			return err
		}
	}
	field := func(data any, name string) func() error {
		return func() error {
			_, err := CompleteField(ctx, data, NewCompleteFieldOptions(name))
			return err
		}
	}
	keep := func(*CompleteOptions) {}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "complete with a maximum length of zero",
			run:  complete("The", func(o *CompleteOptions) { o.MaxLength = 0 }),
			text: "invalid options: MaxLength must be positive",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a negative maximum length",
			run:  complete("The", func(o *CompleteOptions) { o.MaxLength = -5 }),
			text: "invalid options: MaxLength must be positive",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a temperature above two",
			run:  complete("The", func(o *CompleteOptions) { o.Temperature = 2.5 }),
			text: "invalid options: temperature must be between 0.0 and 2.0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a negative temperature",
			run:  complete("The", func(o *CompleteOptions) { o.Temperature = -0.1 }),
			text: "invalid options: temperature must be between 0.0 and 2.0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a TopP of zero",
			run:  complete("The", func(o *CompleteOptions) { o.TopP = 0 }),
			text: "invalid options: TopP must be between 0.0 and 1.0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a TopP above one",
			run:  complete("The", func(o *CompleteOptions) { o.TopP = 1.5 }),
			text: "invalid options: TopP must be between 0.0 and 1.0",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with a TopK of zero",
			run:  complete("The", func(o *CompleteOptions) { o.TopK = 0 }),
			text: "invalid options: TopK must be positive",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with no text",
			run:  complete("", keep),
			text: "partial text cannot be empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete with only spaces",
			run:  complete(" \n\t ", keep),
			text: "partial text cannot be empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field with no field name",
			run:  field(completeTarget{Body: "text"}, ""),
			text: "field name is required",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field of a value that is not a struct",
			run:  field(42, "Body"),
			text: "data must be a struct, got int",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field of a nil pointer to a struct",
			run:  field((*completeTarget)(nil), "Body"),
			text: "data must be a struct, got *ops.completeTarget",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field that the struct does not have",
			run:  field(completeTarget{Body: "text"}, "Missing"),
			text: `field "Missing" not found in struct`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field that is not exported",
			run:  field(completeTarget{Body: "text", hidden: "x"}, "hidden"),
			text: `field "hidden" is not exported, so it cannot be completed`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field promoted through a nil embedded pointer",
			run:  field(completeTargetEmbedding{}, "Name"),
			text: `field "Name" cannot be read: it is promoted through an embedded pointer that is nil`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field that is not a string",
			run:  field(completeTarget{Body: "text", Count: 3}, "Count"),
			text: `field "Count" must be a string, got int`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field that is empty",
			run:  field(completeTarget{}, "Body"),
			text: `field "Body" is empty, nothing to complete`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "complete field that is only spaces, through a pointer",
			run:  field(&completeTarget{Body: "  "}, "Body"),
			text: `field "Body" is empty, nothing to complete`,
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}

// copyWithUpdatedField is called after CompleteField has checked the field, so
// its own refusals cannot be reached through the operation. Each is the text it
// was and has a kind.
func TestCopyWithUpdatedFieldRefusesWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	copyOf := func(data any, name string) func() error {
		return func() error {
			_, err := copyWithUpdatedField(data, name, "new")
			return err
		}
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "a nil pointer",
			run:  copyOf((*completeTarget)(nil), "Body"),
			text: "data is a nil pointer",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an integer",
			run:  copyOf(7, "Body"),
			text: "data must be a struct, got int",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a string",
			run:  copyOf("text", "Body"),
			text: "data must be a struct, got string",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a slice",
			run:  copyOf([]string{"a"}, "Body"),
			text: "data must be a struct, got []string",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a field the struct does not have",
			run:  copyOf(completeTarget{}, "Missing"),
			text: `field "Missing" is not an exported field of the struct`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a field that is not exported",
			run:  copyOf(completeTarget{}, "hidden"),
			text: `field "hidden" is not an exported field of the struct`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a field promoted through a nil embedded pointer",
			run:  copyOf(completeTargetEmbedding{}, "Name"),
			text: `field "Name" is promoted through an embedded pointer that is nil`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a field that is not a string",
			run:  copyOf(completeTarget{}, "Count"),
			text: `field "Count" cannot be set`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "a pointer to a struct with a field that is not a string",
			run:  copyOf(&completeTarget{}, "Count"),
			text: `field "Count" cannot be set`,
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty field name",
			run:  copyOf(completeTarget{}, ""),
			text: `field "" is not an exported field of the struct`,
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}
