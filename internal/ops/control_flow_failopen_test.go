package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// stubLLMError makes every call fail, standing in for an outage.
func stubLLMError(err error) func() {
	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return "", err
	})
	return func() { customLLMCaller = previous }
}

// Match used to swallow a provider failure as "this case did not match", so an
// outage routed every input to the default branch. A caller reading Match as a
// router would silently misroute everything.
func TestMatchReportsProviderFailureInsteadOfDefaulting(t *testing.T) {
	restore := stubLLMError(errors.New("provider unavailable"))
	defer restore()

	defaultRan := false
	matched, err := Match(context.Background(), "anything",
		Like("a billing question", func() { t.Error("the matching case must not run on a failed call") }),
		Otherwise(func() { defaultRan = true }),
	)

	if err == nil {
		t.Fatal("a provider failure must be reported")
	}
	if matched {
		t.Error("no case ran, so matched must be false")
	}
	if defaultRan {
		t.Error("an outage must not be read as 'nothing matched' and routed to the default")
	}
}

// An answer that is neither true nor false is also not a "no".
func TestMatchRejectsAnAmbiguousAnswer(t *testing.T) {
	for _, body := range []string{
		"I'm not sure.",
		"maybe",
		"",
		"It depends on the customer's plan.",
		"<html>502 Bad Gateway</html>",
	} {
		t.Run(strings.ReplaceAll(body, " ", "_"), func(t *testing.T) {
			restore := stubLLM(body)
			defer restore()

			matched, err := Match(context.Background(), "input", Like("some condition", func() {}))
			if err == nil {
				t.Fatalf("an unusable answer must be reported, got matched=%v", matched)
			}
		})
	}
}

// "true" and "false" are honoured, in any of the shapes a model returns them.
func TestMatchAcceptsTrueAndFalse(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"true", true},
		{"TRUE", true},
		{`"true"`, true},
		{" true ", true},
		{"yes", true},
		{"true.", true},
		{"false", false},
		{"FALSE", false},
		{`"false"`, false},
		{"no", false},
	}

	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			restore := stubLLM(tc.body)
			defer restore()

			ran := false
			matched, err := Match(context.Background(), "input", Like("some condition", func() { ran = true }))
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if matched != tc.want || ran != tc.want {
				t.Errorf("body %q: matched=%v ran=%v, want %v", tc.body, matched, ran, tc.want)
			}
		})
	}
}

// Otherwise fired by position: placed first it won outright, so every case
// after it was dead code. It must be evaluated last wherever it is written.
func TestOtherwiseIsEvaluatedLastWhereverItAppears(t *testing.T) {
	restore := stubLLM("true")
	defer restore()

	ran := ""
	matched, err := Match(context.Background(), "input",
		Otherwise(func() { ran = "otherwise" }),
		Like("a matching condition", func() { ran = "case" }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || ran != "case" {
		t.Errorf("a later matching case must win over a leading Otherwise; ran=%q", ran)
	}
}

// And it still runs when nothing else matches.
func TestOtherwiseRunsWhenNothingMatches(t *testing.T) {
	restore := stubLLM("false")
	defer restore()

	ran := ""
	matched, err := Match(context.Background(), "input",
		Otherwise(func() { ran = "otherwise" }),
		Like("a condition that does not hold", func() { ran = "case" }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || ran != "otherwise" {
		t.Errorf("the default must run when no case matched; ran=%q", ran)
	}
}

// The three spellings of the default branch behave alike.
func TestDefaultConditionSpellings(t *testing.T) {
	for _, spelling := range []string{"_", "otherwise", "default"} {
		t.Run(spelling, func(t *testing.T) {
			ran := false
			matched, err := Match(context.Background(), 42, When(spelling, func() { ran = true }))
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if !matched || !ran {
				t.Errorf("%q must name the default branch", spelling)
			}
		})
	}
}

// Non-semantic conditions need no provider at all, so they must work with the
// provider down.
func TestMatchNonSemanticConditionsNeedNoProvider(t *testing.T) {
	restore := stubLLMError(errors.New("provider unavailable"))
	defer restore()

	t.Run("type", func(t *testing.T) {
		ran := false
		matched, err := Match(context.Background(), 42, When(reflect.TypeOf(0), func() { ran = true }))
		if err != nil || !matched || !ran {
			t.Fatalf("matched=%v ran=%v err=%v", matched, ran, err)
		}
	})

	t.Run("value_type", func(t *testing.T) {
		ran := false
		matched, err := Match(context.Background(), "text", When("", func() { ran = true }))
		// An empty string condition never matches, and must not call out.
		if err != nil || matched || ran {
			t.Fatalf("matched=%v ran=%v err=%v", matched, ran, err)
		}
	})

	t.Run("error", func(t *testing.T) {
		ran := false
		target := errors.New("sentinel")
		matched, err := Match(context.Background(), errors.New("other"), When(target, func() { ran = true }))
		if err != nil || !matched || !ran {
			t.Fatalf("same concrete error type must match: matched=%v ran=%v err=%v", matched, ran, err)
		}
	})
}

// No cases at all is not an error, and nothing ran.
func TestMatchWithNoCases(t *testing.T) {
	matched, err := Match(context.Background(), "input")
	if err != nil || matched {
		t.Fatalf("matched=%v err=%v", matched, err)
	}
}

// A case with no action is skipped rather than counted as a match.
func TestMatchSkipsCasesWithNoAction(t *testing.T) {
	restore := stubLLM("true")
	defer restore()

	ran := false
	matched, err := Match(context.Background(), "input",
		Like("first", nil),
		Like("second", func() { ran = true }),
	)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if !matched || !ran {
		t.Error("the first case with an action must run")
	}
}

// A cancelled context must reach the caller rather than becoming a non-match.
func TestMatchHonoursACancelledContext(t *testing.T) {
	previous := customLLMCaller
	setLLMCaller(func(ctx context.Context, _, _ string, _ types.OpOptions) (string, error) {
		return "", ctx.Err()
	})
	defer func() { customLLMCaller = previous }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	defaultRan := false
	matched, err := Match(ctx, "input",
		Like("some condition", func() {}),
		Otherwise(func() { defaultRan = true }),
	)
	if err == nil {
		t.Fatal("a cancelled context must be reported")
	}
	if matched || defaultRan {
		t.Error("cancellation must not route to the default branch")
	}
}
