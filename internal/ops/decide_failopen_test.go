package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func routes() []Decision[string] {
	return []Decision[string]{
		{Value: "technical", Description: "Technical support"},
		{Value: "billing", Description: "Billing support"},
		{Value: "success", Description: "Customer success"},
	}
}

// Decide used to return decisions[0] with a nil error and a fabricated
// confidence whenever anything went wrong: a provider failure, an unparseable
// answer, or an out-of-range index. Every one of those silently routed the
// caller down the first branch.
func TestDecideDoesNotSilentlyTakeBranchZero(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		fails bool
	}{
		{"provider_error", "", true},
		{"prose", "I think billing is best.", false},
		{"empty", "", false},
		{"truncated_json", `{"selected": `, false},
		{"error_page", "<html><body>502 Bad Gateway</body></html>", false},
		{"index_too_high", `{"selected":7,"confidence":0.9}`, false},
		{"index_negative", `{"selected":-1,"confidence":0.9}`, false},
		{"index_off_by_one", `{"selected":3,"confidence":0.9}`, false},
		{"wrong_shape", `[{"selected":1}]`, false},
		{"refusal", "I'm sorry, I can't help with that.", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var restore func()
			if tc.fails {
				restore = stubLLMError(errors.New("provider unavailable"))
			} else {
				restore = stubLLM(tc.body)
			}
			defer restore()

			value, result, err := Decide(context.Background(), "a ticket", routes())
			if err == nil {
				t.Fatalf("expected an error, got %q at index %d", value, result.SelectedIndex)
			}
			if value != "" {
				t.Errorf("a failed decision must not return a branch value, got %q", value)
			}
			if result.SelectedIndex != -1 {
				t.Errorf("SelectedIndex = %d, want -1 on failure", result.SelectedIndex)
			}
			if result.Confidence != 0 {
				t.Errorf("a failed decision must not carry a confidence, got %v", result.Confidence)
			}
		})
	}
}

// A fallback is available, but only when the caller asks for it, and it says so
// on the result rather than passing itself off as a decision.
func TestDecideFallbackIsExplicitAndLabelled(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"prose", "billing, probably"},
		{"index_out_of_range", `{"selected":9}`},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			value, result, err := Decide(context.Background(), "a ticket", routes(),
				NewDecideOptions().WithFallback(2))
			if err != nil {
				t.Fatalf("a configured fallback must not error: %v", err)
			}
			if value != "success" || result.SelectedIndex != 2 {
				t.Errorf("fallback selected %q at %d, want the configured index 2", value, result.SelectedIndex)
			}
			if !result.Fallback {
				t.Error("Fallback must be set so the caller can tell a default from a decision")
			}
			if result.Confidence != 0 {
				t.Errorf("a fallback carries no confidence, got %v", result.Confidence)
			}
			if !strings.Contains(result.Explanation, "fallback") {
				t.Errorf("the explanation should say it was a fallback, got %q", result.Explanation)
			}
		})
	}
}

// A fallback index that does not exist is a configuration error, caught before
// any call is made.
func TestDecideRejectsAnOutOfRangeFallback(t *testing.T) {
	for _, index := range []int{-1, 3, 99} {
		t.Run(fmt.Sprintf("index_%d", index), func(t *testing.T) {
			called := false
			previous := customLLMCaller
			setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
				called = true
				return "", nil
			})
			defer func() { customLLMCaller = previous }()

			if _, _, err := Decide(context.Background(), "x", routes(), NewDecideOptions().WithFallback(index)); err == nil {
				t.Fatalf("fallback index %d must be rejected", index)
			}
			if called {
				t.Error("a bad configuration must be caught before the provider is called")
			}
		})
	}
}

// A well-formed answer is honoured, and the model's own confidence is reported
// unchanged.
func TestDecideHonoursAWellFormedAnswer(t *testing.T) {
	restore := stubLLM(`{"selected":1,"explanation":"the customer is asking about an invoice","confidence":0.82,"alternatives":[2]}`)
	defer restore()

	value, result, err := Decide(context.Background(), "invoice question", routes())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if value != "billing" || result.SelectedIndex != 1 {
		t.Errorf("selected %q at %d, want billing at 1", value, result.SelectedIndex)
	}
	if result.Confidence != 0.82 {
		t.Errorf("Confidence = %v, want the model's 0.82", result.Confidence)
	}
	if result.Fallback {
		t.Error("a real decision must not be labelled a fallback")
	}
	if len(result.Alternatives) != 1 || result.Alternatives[0] != 2 {
		t.Errorf("Alternatives = %v, want [2]", result.Alternatives)
	}
}

// A fenced answer is still an answer.
func TestDecideAcceptsAFencedAnswer(t *testing.T) {
	restore := stubLLM("```json\n{\"selected\":0,\"confidence\":0.5}\n```")
	defer restore()

	value, result, err := Decide(context.Background(), "x", routes())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if value != "technical" || result.SelectedIndex != 0 {
		t.Errorf("selected %q at %d", value, result.SelectedIndex)
	}
}

// A programmatic condition short-circuits before any call is made, so it works
// with the provider down.
func TestDecideProgrammaticConditionNeedsNoProvider(t *testing.T) {
	restore := stubLLMError(errors.New("provider unavailable"))
	defer restore()

	decisions := routes()
	decisions[1].Condition = func(situation any) bool {
		text, ok := situation.(string)
		return ok && strings.Contains(text, "invoice")
	}

	value, result, err := Decide(context.Background(), "an invoice question", decisions)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if value != "billing" || result.SelectedIndex != 1 || result.Confidence != 1.0 {
		t.Errorf("selected %q at %d with confidence %v", value, result.SelectedIndex, result.Confidence)
	}
}

// No decisions is an error whether or not a fallback is configured.
func TestDecideWithNoDecisions(t *testing.T) {
	if _, _, err := Decide[string](context.Background(), "x", nil); err == nil {
		t.Error("no decisions must be an error")
	}
	if _, _, err := Decide(context.Background(), "x", []Decision[string]{}, NewDecideOptions().WithFallback(0)); err == nil {
		t.Error("a fallback cannot rescue an empty decision set")
	}
}

// A cancelled context is reported rather than turned into branch zero.
func TestDecideHonoursACancelledContext(t *testing.T) {
	previous := customLLMCaller
	setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return "", ctx.Err()
	})
	defer func() { customLLMCaller = previous }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	value, result, err := Decide(ctx, "x", routes())
	if err == nil {
		t.Fatalf("a cancelled context must be reported, got %q at %d", value, result.SelectedIndex)
	}
}
