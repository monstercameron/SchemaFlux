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

// Compose ran only its first operation. Its old signature made chaining
// impossible: func(T) (U, error) cannot feed an operation that takes T.
func TestComposeThreadsEveryOperation(t *testing.T) {
	appendStep := func(name string) func(string) (string, error) {
		return func(s string) (string, error) { return s + ">" + name, nil }
	}

	cases := []struct {
		name  string
		steps []string
		want  string
	}{
		{"one", []string{"a"}, "in>a"},
		{"two", []string{"a", "b"}, "in>a>b"},
		{"three", []string{"a", "b", "c"}, "in>a>b>c"},
		{"five", []string{"a", "b", "c", "d", "e"}, "in>a>b>c>d>e"},
		{"ten", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, "in>1>2>3>4>5>6>7>8>9>10"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operations := make([]func(string) (string, error), 0, len(tc.steps))
			for _, step := range tc.steps {
				operations = append(operations, appendStep(step))
			}

			got, err := Compose(operations...)("in")
			if err != nil {
				t.Fatalf("Compose: %v", err)
			}
			if got != tc.want {
				t.Errorf("Compose = %q, want %q", got, tc.want)
			}
		})
	}
}

// A failure stops the chain at the operation that failed, and names it.
func TestComposeStopsAtTheFailingOperation(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("fails_at_%d", failAt), func(t *testing.T) {
			var reached []int
			operations := make([]func(int) (int, error), 0, 4)
			for i := 0; i < 4; i++ {
				index := i
				operations = append(operations, func(v int) (int, error) {
					reached = append(reached, index)
					if index == failAt {
						return 0, errors.New("boom")
					}
					return v + 1, nil
				})
			}

			_, err := Compose(operations...)(0)
			if err == nil {
				t.Fatal("a failing operation must be reported")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("operation %d", failAt)) {
				t.Errorf("the error should name operation %d, got %v", failAt, err)
			}
			if len(reached) != failAt+1 {
				t.Errorf("ran %v; nothing after operation %d should run", reached, failAt)
			}
		})
	}
}

// The degenerate inputs are errors, not panics.
func TestComposeDegenerateInputs(t *testing.T) {
	t.Run("no_operations", func(t *testing.T) {
		if _, err := Compose[string]()("in"); err == nil {
			t.Error("composing nothing must be an error")
		}
	})
	t.Run("nil_operation_first", func(t *testing.T) {
		if _, err := Compose[string](nil)("in"); err == nil {
			t.Error("a nil operation must be reported")
		}
	})
	t.Run("nil_operation_later", func(t *testing.T) {
		ok := func(s string) (string, error) { return s, nil }
		if _, err := Compose(ok, nil)("in"); err == nil {
			t.Error("a nil operation must be reported")
		}
	})
	t.Run("zero_value_on_failure", func(t *testing.T) {
		got, err := Compose(func(s string) (string, error) { return "partial", errors.New("boom") })("in")
		if err == nil {
			t.Fatal("expected an error")
		}
		if got != "" {
			t.Errorf("a failed compose must return the zero value, got %q", got)
		}
	})
}

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

			result, err := Complete(context.Background(), nil, "Hello", NewCompleteOptions())
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
			previousCaller := customLLMCaller
			previousProvider := defaultProvider
			customLLMCaller = nil
			defaultProvider = nil
			defer func() {
				customLLMCaller = previousCaller
				defaultProvider = previousProvider
			}()

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
