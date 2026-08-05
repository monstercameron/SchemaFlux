package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// scriptedBodies replaces the LLM caller with one that returns each body in
// turn, recording the prompts it was given.
func scriptedBodies(t *testing.T, bodies ...string) (*[]string, func()) {
	t.Helper()

	var prompts []string
	var index int32

	previous := customLLMCaller
	setLLMCaller(func(_ context.Context, _, user string, _ types.OpOptions) (string, error) {
		prompts = append(prompts, user)
		i := int(atomic.AddInt32(&index, 1)) - 1
		if i >= len(bodies) {
			i = len(bodies) - 1
		}
		return bodies[i], nil
	})

	return &prompts, func() { customLLMCaller = previous }
}

type repairTarget struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// The defining feature of a typed LLM library: the parse fails, the error goes
// back to the model, the retry succeeds. A malformed body used to be terminal.
func TestExtractRepairsAnUnparseableAnswer(t *testing.T) {
	cases := []struct {
		name  string
		first string
	}{
		{"prose", "I think the name is Ada and she is 36."},
		{"fenced_prose", "```\nAda, 36\n```"},
		{"truncated", `{"name": "Ada", "ag`},
		{"wrong_shape", `{"unrelated": true}`},
		{"empty", ""},
		{"array_not_object", `[{"name":"Ada","age":36}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompts, restore := scriptedBodies(t, tc.first, `{"name":"Ada","age":36}`)
			defer restore()

			result, err := Extract[repairTarget]("Ada Lovelace, 36", NewExtractOptions())
			if err != nil {
				t.Fatalf("the second attempt should have succeeded: %v", err)
			}
			if result.Name != "Ada" || result.Age != 36 {
				t.Errorf("result = %+v", result)
			}

			if len(*prompts) != 2 {
				t.Fatalf("made %d calls, want 2", len(*prompts))
			}

			// The repair has to tell the model what was wrong, or it is just a
			// retry with extra steps.
			repair := (*prompts)[1]
			if !strings.Contains(repair, "previous answer could not be used") {
				t.Errorf("the repair prompt does not say the answer failed:\n%s", repair)
			}
			if !strings.Contains(repair, "Ada Lovelace, 36") {
				t.Error("the repair prompt lost the original task")
			}
		})
	}
}

// A repair is exhausted, not infinite.
func TestExtractStopsRepairingAtTheBudget(t *testing.T) {
	prompts, restore := scriptedBodies(t, "not json", "still not json", "never json")
	defer restore()

	_, err := Extract[repairTarget]("input", NewExtractOptions())
	if err == nil {
		t.Fatal("an answer that never becomes usable must fail")
	}
	if !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("the error should say how many attempts were made: %v", err)
	}

	// One attempt plus one repair, which is the default budget.
	if len(*prompts) != 2 {
		t.Errorf("made %d calls, want 2 (one attempt, one repair)", len(*prompts))
	}
}

// A first-try success costs one call, so repair is not a tax on the happy path.
func TestNoRepairWhenTheFirstAnswerIsGood(t *testing.T) {
	prompts, restore := scriptedBodies(t, `{"name":"Ada","age":36}`)
	defer restore()

	if _, err := Extract[repairTarget]("Ada, 36", NewExtractOptions()); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(*prompts) != 1 {
		t.Errorf("made %d calls, want 1", len(*prompts))
	}
}

// Strict mode's post-condition is a repair trigger too, not only the parse.
func TestExtractRepairsAMissingRequiredField(t *testing.T) {
	prompts, restore := scriptedBodies(t,
		`{"name":"Ada"}`, // age missing, which Strict mode rejects
		`{"name":"Ada","age":36}`)
	defer restore()

	opts := NewExtractOptions()
	// CommonOptions, not OpOptions: mergeEmbeddedOpOptions takes Mode from
	// CommonOptions unconditionally, so writing the other one is ignored. That
	// is X-07, and it has now caught this session twice.
	opts.CommonOptions.Mode = types.Strict

	result, err := Extract[repairTarget]("Ada Lovelace, 36", opts)
	if err != nil {
		t.Fatalf("the repair should have satisfied Strict mode: %v", err)
	}
	if result.Age != 36 {
		t.Errorf("result = %+v", result)
	}
	if len(*prompts) != 2 {
		t.Fatalf("made %d calls, want 2", len(*prompts))
	}
	if !strings.Contains((*prompts)[1], "age") {
		t.Errorf("the repair prompt does not name the missing field:\n%s", (*prompts)[1])
	}
}

// A transport failure is not a repair case: the model produced no answer, so
// there is nothing to show it.
func TestTransportFailuresAreNotRepaired(t *testing.T) {
	var calls int32
	previous := customLLMCaller
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", errors.New("provider unavailable")
	})
	defer func() { customLLMCaller = previous }()

	if _, err := Extract[repairTarget]("x", NewExtractOptions()); err == nil {
		t.Fatal("a provider failure must reach the caller")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("made %d calls; a transport failure must not be repaired", got)
	}
}

// withRepair directly, including the shapes Extract does not reach.
func TestWithRepair(t *testing.T) {
	always := func(string) error { return errors.New("never acceptable") }
	never := func(string) error { return nil }

	t.Run("first_attempt_accepted", func(t *testing.T) {
		_, restore := scriptedBodies(t, "body")
		defer restore()

		body, result, err := withRepair(context.Background(), "sys", "user",
			types.OpOptions{}, RepairPolicy{}, never)
		if err != nil {
			t.Fatalf("withRepair: %v", err)
		}
		if body != "body" || result.Attempts != 1 || result.Repaired {
			t.Errorf("body=%q result=%+v", body, result)
		}
	})

	t.Run("second_attempt_accepted", func(t *testing.T) {
		_, restore := scriptedBodies(t, "bad", "good")
		defer restore()

		seen := 0
		validate := func(body string) error {
			seen++
			if body == "good" {
				return nil
			}
			return errors.New("body was not good")
		}

		body, result, err := withRepair(context.Background(), "sys", "user",
			types.OpOptions{}, RepairPolicy{}, validate)
		if err != nil {
			t.Fatalf("withRepair: %v", err)
		}
		if body != "good" {
			t.Errorf("body = %q", body)
		}
		if result.Attempts != 2 || !result.Repaired {
			t.Errorf("result = %+v", result)
		}
		if len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "not good") {
			t.Errorf("Failures = %v", result.Failures)
		}
	})

	t.Run("budget_zero_disables_repair", func(t *testing.T) {
		prompts, restore := scriptedBodies(t, "bad", "good")
		defer restore()

		_, result, err := withRepair(context.Background(), "sys", "user",
			types.OpOptions{}, RepairPolicy{Attempts: -1}, always)
		if err == nil {
			t.Fatal("expected a failure")
		}
		if result.Attempts != 1 {
			t.Errorf("Attempts = %d, want 1 with repair disabled", result.Attempts)
		}
		if len(*prompts) != 1 {
			t.Errorf("made %d calls", len(*prompts))
		}
	})

	t.Run("explicit_budget_is_honoured", func(t *testing.T) {
		prompts, restore := scriptedBodies(t, "bad", "bad", "bad", "bad", "bad")
		defer restore()

		_, result, err := withRepair(context.Background(), "sys", "user",
			types.OpOptions{}, RepairPolicy{Attempts: 3}, always)
		if err == nil {
			t.Fatal("expected a failure")
		}
		if result.Attempts != 4 {
			t.Errorf("Attempts = %d, want 4 (one try plus three repairs)", result.Attempts)
		}
		if len(*prompts) != 4 {
			t.Errorf("made %d calls", len(*prompts))
		}
	})

	t.Run("every_failure_is_reported", func(t *testing.T) {
		_, restore := scriptedBodies(t, "a", "b", "c")
		defer restore()

		_, result, _ := withRepair(context.Background(), "sys", "user",
			types.OpOptions{}, RepairPolicy{Attempts: 2},
			func(body string) error { return fmt.Errorf("rejected %s", body) })

		if len(result.Failures) != 3 {
			t.Errorf("Failures = %v, want one per attempt", result.Failures)
		}
	})
}

// The repair prompt keeps the original task first, so the cacheable prefix does
// not move. A repair that rewrote the whole prompt would miss the cache every
// time, which is the opposite of what CA-001 was for.
func TestRepairPromptKeepsThePrefixStable(t *testing.T) {
	original := "Extract the customer from this invoice."

	once := repairPrompt(original, []string{"the response was not JSON"})
	twice := repairPrompt(original, []string{"the response was not JSON", "a field was missing"})

	for _, prompt := range []string{once, twice} {
		if !strings.HasPrefix(prompt, original) {
			t.Errorf("the repair prompt does not begin with the original task:\n%s", prompt)
		}
	}

	if !strings.Contains(once, "The problem was:") {
		t.Errorf("a single failure should read as one problem:\n%s", once)
	}
	if !strings.Contains(twice, "The problems were:") {
		t.Errorf("several failures should be enumerated:\n%s", twice)
	}
	if !strings.Contains(twice, "1.") || !strings.Contains(twice, "2.") {
		t.Errorf("the failures are not numbered:\n%s", twice)
	}
}

// The budget is configurable, because one repair is a default and not a law.
func TestRepairBudgetIsConfigurable(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 1},
		{"0", 0},
		{"3", 3},
		{"-1", 1}, // negative is not a budget; fall back to the default
		{"nonsense", 1},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", tc.env)
			if got := repairAttempts(RepairPolicy{}); got != tc.want {
				t.Errorf("repairAttempts = %d, want %d", got, tc.want)
			}
		})
	}
}

// X-07 has caught this session twice, so the precedence is pinned here until
// M06 removes the duplication. A type embedding both CommonOptions and
// types.OpOptions has two Mode fields and two Intelligence fields; the merge
// takes both from CommonOptions unconditionally, because their zero values --
// Strict and Smart -- are real choices that a "copy if non-zero" rule could not
// express (F-01).
//
// Context, on the same struct, does fall back to the embedded copy. That
// inconsistency is the trap: nothing tells a caller which fields fall back.
func TestEmbeddedOptionsPrecedenceIsPinned(t *testing.T) {
	t.Run("mode_comes_from_common", func(t *testing.T) {
		opts := NewExtractOptions()
		opts.CommonOptions.Mode = types.Creative
		opts.OpOptions.Mode = types.Strict

		if got := opts.toOpOptions().Mode; got != types.Creative {
			t.Errorf("Mode = %v, want the CommonOptions value Creative", got)
		}
	})

	t.Run("intelligence_comes_from_common", func(t *testing.T) {
		opts := NewExtractOptions()
		opts.CommonOptions.Intelligence = types.Quick
		opts.OpOptions.Intelligence = types.Smart

		if got := opts.toOpOptions().Intelligence; got != types.Quick {
			t.Errorf("Intelligence = %v, want the CommonOptions value Quick", got)
		}
	})

	t.Run("zero_values_are_expressible_from_common", func(t *testing.T) {
		opts := NewExtractOptions()
		opts.CommonOptions.Mode = types.Strict        // the zero value
		opts.CommonOptions.Intelligence = types.Smart // the zero value

		merged := opts.toOpOptions()
		if merged.Mode != types.Strict {
			t.Errorf("Mode = %v; Strict is the zero value and must still be settable", merged.Mode)
		}
		if merged.Intelligence != types.Smart {
			t.Errorf("Intelligence = %v; Smart is the zero value and must still be settable", merged.Intelligence)
		}
	})

	t.Run("context_falls_back_to_embedded", func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(context.Background(), key{}, "marker")

		opts := NewExtractOptions()
		opts.OpOptions.Context = ctx // CommonOptions.Context left nil

		if got := opts.toOpOptions().Context.Value(key{}); got != "marker" {
			t.Error("Context does not fall back to the embedded copy, contradicting the documented behaviour")
		}
	})
}
