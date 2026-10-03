package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ---------------------------------------------------------------------------
// F-009 — Compose
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CA-001 — prompt determinism
// ---------------------------------------------------------------------------

// sortedKeys is what makes a prompt reproducible; it has to be total.
func TestSortedKeysOrdering(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]int
		want  []string
	}{
		{"empty", map[string]int{}, nil},
		{"nil", nil, nil},
		{"single", map[string]int{"a": 1}, []string{"a"}},
		{"already_sorted", map[string]int{"a": 1, "b": 2, "c": 3}, []string{"a", "b", "c"}},
		{"reverse", map[string]int{"c": 3, "b": 2, "a": 1}, []string{"a", "b", "c"}},
		{"mixed_case_is_byte_ordered", map[string]int{"b": 1, "A": 2}, []string{"A", "b"}},
		{"digits_before_letters", map[string]int{"a": 1, "1": 2}, []string{"1", "a"}},
		{"punctuation", map[string]int{"a-b": 1, "a_b": 2, "a.b": 3}, []string{"a-b", "a.b", "a_b"}},
		{"unicode", map[string]int{"é": 1, "e": 2}, []string{"e", "é"}},
		{"empty_key", map[string]int{"": 1, "a": 2}, []string{"", "a"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sortedKeys(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("sortedKeys = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("sortedKeys = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// And it must be stable across repeated calls on the same map, which is the
// property the prompt cache actually depends on.
func TestSortedKeysIsStableAcrossCalls(t *testing.T) {
	m := map[string]string{
		"alpha": "a", "bravo": "b", "charlie": "c", "delta": "d", "echo": "e",
		"foxtrot": "f", "golf": "g", "hotel": "h", "india": "i", "juliet": "j",
	}

	first := strings.Join(sortedKeys(m), ",")
	for run := 0; run < 50; run++ {
		if got := strings.Join(sortedKeys(m), ","); got != first {
			t.Fatalf("run %d produced %q, want %q", run, got, first)
		}
	}
}

// ---------------------------------------------------------------------------
// F-016..F-019 — fabricated numbers
// ---------------------------------------------------------------------------

// The deleted heuristics all produced a number from the shape of the text. None
// of these bodies may produce one.
func TestNoConfidenceIsInventedFromTextShape(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{"short", "ok"},
		{"long_no_punctuation", strings.Repeat("word ", 40)},
		{"long_with_full_stop", strings.Repeat("word ", 40) + "."},
		{"exclamation", "Done!"},
		{"question", "Really?"},
		{"starts_with_brace", "{not actually json"},
		{"starts_with_bracket", "[not actually json"},
		{"prose", "Here is a considerable amount of prose that goes on for a while."},
		{"empty", ""},
		{"whitespace", "   "},
	}

	for _, tc := range bodies {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM("Hello " + tc.body)
			defer restore()

			result, err := Complete(context.Background(), "Hello", NewCompleteOptions())
			if err != nil {
				// An empty completion is a legitimate error; what matters is
				// that no confidence was invented on the way out.
				return
			}
			if result.ModelConfidence != 0 {
				t.Errorf("body %q produced confidence %v; the heuristic must stay deleted", tc.body, result.ModelConfidence)
			}
		})
	}
}

// A failed extraction reports no confidence at all, whatever the body looked
// like. The old code returned 0.3 for anything brace-shaped and 0.1 otherwise.
func TestFailedExtractionNeverCarriesAConfidence(t *testing.T) {
	type target struct {
		Alpha string `json:"alpha"`
	}

	for _, body := range []string{
		`{"beta": 1}`,
		`{"alpha`,
		`[1,2,3]`,
		"prose",
		"<html>502</html>",
		"",
	} {
		t.Run(body, func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			_, err := Extract[target]("input", NewExtractOptions())
			if err == nil {
				t.Fatal("expected an error")
			}

			var extractErr types.ExtractError
			if errors.As(err, &extractErr) {
				// The field is gone; this asserts the type has not regained it.
				if strings.Contains(fmt.Sprintf("%+v", extractErr), "ModelConfidence") {
					t.Errorf("ExtractError has regained a ModelConfidence field: %+v", extractErr)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-030 — ErrNoProvider
// ---------------------------------------------------------------------------

// Every operation that reaches a provider must report the same actionable error
// when there is none, rather than a bare "no LLM provider configured".
func TestEveryOperationReportsErrNoProvider(t *testing.T) {
	type target struct {
		Alpha string `json:"alpha"`
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"Extract", func() error { _, err := Extract[target]("in", NewExtractOptions()); return err }},
		{"Generate", func() error { _, err := Generate[target]("in", NewGenerateOptions()); return err }},
		{"Summarize", func() error { _, err := Summarize("in", NewSummarizeOptions()); return err }},
		{"Rewrite", func() error { _, err := Rewrite("in", NewRewriteOptions()); return err }},
		{"Translate", func() error {
			_, err := Translate("in", NewTranslateOptions().WithTargetLanguage("fr"))
			return err
		}},
		{"Expand", func() error { _, err := Expand("in", NewExpandOptions()); return err }},
		{"Validate", func() error {
			_, err := Validate(target{}, NewValidateOptions().WithRules("any"))
			return err
		}},
		{"Score", func() error {
			_, err := Score(target{}, NewScoreOptions().WithSteering("quality"))
			return err
		}},
		{"Critique", func() error {
			_, err := Critique(target{}, NewCritiqueOptions().WithCriteria([]string{"clarity"}))
			return err
		}},
		{"Decide", func() error {
			_, _, err := Decide(context.Background(), "situation",
				[]Decision[string]{{Value: "a", Description: "first"}, {Value: "b", Description: "second"}})
			return err
		}},
		{"Match", func() error {
			_, err := Match(context.Background(), "in", Like("some condition", func() {}))
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapNoProvider(t)

			err := tc.run()
			if err == nil {
				t.Fatal("an operation with no provider must fail")
			}
			if !errors.Is(err, ErrNoProvider) {
				t.Errorf("the error should wrap ErrNoProvider, got %v", err)
			}
		})
	}
}

// The message itself has to be actionable: a name to call and a variable to
// set, not just a statement of fact.
func TestErrNoProviderMessageIsActionable(t *testing.T) {
	message := ErrNoProvider.Error()

	for _, expected := range []string{
		"Init", "InitWithEnv",
		"SCHEMAFLUX_API_KEY", "SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI",
	} {
		if !strings.Contains(message, expected) {
			t.Errorf("the message should name %q: %s", expected, message)
		}
	}
	if len(message) < 80 {
		t.Errorf("the message is too terse to be actionable: %q", message)
	}
}
