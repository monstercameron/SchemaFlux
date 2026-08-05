package ops

import (
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The marker stands in for anything a caller would not want copied: a national
// ID, a card number, a medical note. If it appears anywhere in the error, it
// appears in every log line that error reaches.
const payloadMarker = "SSN-123-45-6789-CARD-4111111111111111"

// An error that stores the input copies the payload wherever the error goes.
// These operations must describe the input instead.
func TestErrorsDoNotCarryThePayload(t *testing.T) {
	type record struct {
		Secret string `json:"secret"`
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"extract_parse_failure", func() error {
			_, err := Extract[record](payloadMarker, NewExtractOptions())
			return err
		}},
		{"extract_provider_failure", func() error {
			restore := stubLLMError(errors.New("provider unavailable"))
			defer restore()
			_, err := Extract[record](payloadMarker, NewExtractOptions())
			return err
		}},
		{"classify", func() error {
			opts := NewClassifyOptions()
			opts.Categories = []string{"alpha", "bravo"}
			_, err := Classify[string, string](payloadMarker, opts)
			return err
		}},
		{"summarize", func() error {
			_, err := SummarizeWithMetadata(payloadMarker, NewSummarizeOptions())
			return err
		}},
		{"rewrite", func() error {
			_, err := RewriteWithMetadata(payloadMarker, NewRewriteOptions())
			return err
		}},
		{"translate", func() error {
			_, err := TranslateWithMetadata(payloadMarker, NewTranslateOptions())
			return err
		}},
		{"expand", func() error {
			_, err := ExpandWithMetadata(payloadMarker, NewExpandOptions())
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM("this is not the JSON you asked for")
			defer restore()

			err := tc.run()
			if err == nil {
				t.Fatal("this configuration produced no error, so it witnesses nothing")
			}
			if strings.Contains(err.Error(), payloadMarker) {
				t.Errorf("the error reproduces the payload:\n%s", err.Error())
			}
			// A partial leak is still a leak.
			for _, fragment := range []string{"123-45-6789", "4111111111111111"} {
				if strings.Contains(err.Error(), fragment) {
					t.Errorf("the error reproduces %q:\n%s", fragment, err.Error())
				}
			}
		})
	}
}

// The description has to be useful, or the redaction just removes information.
func TestDescribeValue(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{"nil", nil, []string{"nil"}},
		{"empty_string", "", []string{"empty"}},
		{"prose", "some ordinary sentence", []string{"text", "22 bytes"}},
		{"json_object", `{"a":1}`, []string{"json object", "7 bytes"}},
		{"json_array", `[1,2,3]`, []string{"json array"}},
		{"markup", "<html></html>", []string{"markup"}},
		{"fenced", "```json\n{}\n```", []string{"fenced block"}},
		{"bytes", []byte("hello"), []string{"5 bytes"}},
		{"slice", []int{1, 2, 3}, []string{"[]int", "of 3"}},
		{"map", map[string]int{"a": 1}, []string{"of 1"}},
		{"struct", struct{ A int }{1}, []string{"struct"}},
		{"int", 42, []string{"int"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := types.DescribeValue(tc.value)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("DescribeValue(%v) = %q, want it to mention %q", tc.value, got, want)
				}
			}
		})
	}
}

// And the description must never reproduce the contents.
func TestDescribeValueNeverReproducesContents(t *testing.T) {
	for _, value := range []any{
		payloadMarker,
		[]byte(payloadMarker),
		[]string{payloadMarker},
		map[string]string{"secret": payloadMarker},
		struct{ Secret string }{payloadMarker},
	} {
		if got := types.DescribeValue(value); strings.Contains(got, "123-45-6789") {
			t.Errorf("DescribeValue reproduced the payload: %q", got)
		}
	}
}

// ClassifyError used to print the input with %q, which put it in the error
// message itself rather than merely retaining it.
func TestClassifyErrorMessageDescribesRatherThanQuotes(t *testing.T) {
	err := types.ClassifyError{
		InputShape: types.DescribeValue(payloadMarker),
		Categories: []string{"alpha"},
		Reason:     "no category returned",
	}

	message := err.Error()
	if strings.Contains(message, payloadMarker) {
		t.Fatalf("the message reproduces the payload: %s", message)
	}
	if !strings.Contains(message, "no category returned") {
		t.Errorf("the message must still say what went wrong: %s", message)
	}
	if !strings.Contains(message, "bytes") {
		t.Errorf("the message should describe the input: %s", message)
	}
}

// An operation run before the library has a provider must say what to do about
// it. "no LLM provider configured" is true and useless.
func TestNoProviderErrorNamesTheWayOut(t *testing.T) {
	previous := customLLMCaller
	customLLMCaller = nil
	previousProvider := defaultProvider
	defaultProvider = nil
	defer func() {
		customLLMCaller = previous
		defaultProvider = previousProvider
	}()

	type record struct {
		Alpha string `json:"alpha"`
	}

	_, err := Extract[record]("input", NewExtractOptions())
	if err == nil {
		t.Fatal("an operation with no provider must fail")
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("the error should wrap ErrNoProvider, got %v", err)
	}

	message := ErrNoProvider.Error()
	for _, expected := range []string{"Init", "InitWithEnv", "SCHEMAFLUX_API_KEY", "OPENAI_API_KEY"} {
		if !strings.Contains(message, expected) {
			t.Errorf("the message should name %q: %s", expected, message)
		}
	}
}
