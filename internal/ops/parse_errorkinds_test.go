package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// refusalPerson is the target the refusal tables parse into and infer from.
type refusalPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// Parse refuses what it cannot read before it asks a model, and says why with a
// kind. The options it refuses leave through "invalid options" as they always
// did; the refusals made by the parsers keep the kind they had.
func TestParseRefusesBeforeTheCallWithAKind(t *testing.T) {
	config := types.ErrConfiguration
	invalid := types.ErrInvalidRequest

	parse := func(input any, edit func(*ParseOptions)) func() error {
		return func() error {
			opts := NewParseOptions()
			edit(&opts)
			_, err := Parse[refusalPerson](input, opts)
			return err
		}
	}
	none := func(*ParseOptions) {}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "an empty custom delimiter in first place",
			run:  parse(`{"name":"a"}`, func(o *ParseOptions) { o.CustomDelimiters = []string{""} }),
			text: "invalid options: parse: configuration: custom delimiter 1 is empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty custom delimiter in second place",
			run:  parse(`{"name":"a"}`, func(o *ParseOptions) { o.CustomDelimiters = []string{";", ""} }),
			text: "invalid options: parse: configuration: custom delimiter 2 is empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty custom delimiter in third place",
			run:  parse(`{"name":"a"}`, func(o *ParseOptions) { o.CustomDelimiters = []string{";", ":", ""} }),
			text: "invalid options: parse: configuration: custom delimiter 3 is empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "an empty custom delimiter is refused before the input is read",
			run:  parse(nil, func(o *ParseOptions) { o.CustomDelimiters = []string{""} }),
			text: "invalid options: parse: configuration: custom delimiter 1 is empty",
			kind: types.KindConfiguration, matches: []error{config},
		},
		{
			name: "no input",
			run:  parse(nil, none),
			text: "parse: invalid request: there is no input to parse",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "an empty string",
			run:  parse("", none),
			text: "parse: invalid request: the input is blank",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a string of spaces",
			run:  parse("  \n\t ", none),
			text: "parse: invalid request: the input is blank",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "text in no format it knows, with no fallback",
			run:  parse("just some words", none),
			text: "parsing failed: parse: invalid request: the input is not in a format Parse recognises (consider enabling AllowLLMFallback)",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "JSON that stops short, with no fallback",
			run:  parse(`{"name": oops}`, none),
			text: "parsing failed: parse: invalid request: the input is not valid JSON (syntax error at byte 10) (consider enabling AllowLLMFallback)",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "JSON with a value of the wrong type, with no fallback",
			run:  parse(`{"name":"a","age":"old"}`, none),
			text: "parsing failed: parse: invalid request: the JSON value for field age cannot be read as int (consider enabling AllowLLMFallback)",
			kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a column hint that names no field of the target",
			run: parse("ada|36", func(o *ParseOptions) {
				o.FormatHints = []string{"nickname|age"}
			}),
			text: "parse: configuration: the format hint's column at position 1 of 2 names no field of ops.refusalPerson",
			kind: types.KindConfiguration, matches: []error{config},
		},
	})
}

// Infer refuses data it cannot show a model before it asks one. Its own options
// have nothing to refuse, so the only refusal is the data.
func TestInferRefusesDataItCannotWriteWithAKind(t *testing.T) {
	invalid := types.ErrInvalidRequest
	text := "infer: invalid request: the partial data could not be written as JSON"

	type withChannel struct {
		Name string `json:"name"`
		C    chan int
	}
	type withFunc struct {
		F func()
	}
	type withComplex struct {
		Z complex128
	}
	type nested struct {
		Inner withChannel
	}

	checkEarlyRefusals(t, []earlyRefusal{
		{
			name: "a channel in a struct",
			run: func() error {
				_, err := Infer(withChannel{Name: "a"}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a function in a struct",
			run: func() error {
				_, err := Infer(withFunc{}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a complex number in a struct",
			run: func() error {
				_, err := Infer(withComplex{}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a channel one level down",
			run: func() error {
				_, err := Infer(nested{}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a channel with a background given",
			run: func() error {
				_, err := Infer(withChannel{}, NewInferOptions().WithBackground("someone"))
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a channel at the smart tier",
			run: func() error {
				_, err := Infer(withChannel{}, NewInferOptions().WithIntelligence(types.Smart))
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a bare channel",
			run: func() error {
				_, err := Infer(make(chan int), NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a bare function",
			run: func() error {
				_, err := Infer(func() {}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a map holding a channel",
			run: func() error {
				_, err := Infer(map[string]any{"c": make(chan int)}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
		{
			name: "a slice of channels",
			run: func() error {
				_, err := Infer([]chan int{make(chan int)}, NewInferOptions())
				return err
			},
			text: text, kind: types.KindInvalidRequest, matches: []error{invalid},
		},
	})
}
