package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-105 at the provider's own entry point. The test beside this one checks
// the argument list built from a turn request; nothing checked which effort a
// completion request puts into the turn, so the rule that an operation's
// preference is not passed on could be taken out with every test still green.
func TestWhichEffortACompletionRequestPassesToTheTurn(t *testing.T) {
	cases := []struct {
		name      string
		effort    string
		preferred bool
		want      string
		refused   bool
	}{
		{"nothing asked for", "", false, "", false},
		{"a caller's none", "none", false, "none", false},
		{"a caller's minimal", "minimal", false, "minimal", false},
		{"a caller's low", "low", false, "low", false},
		{"a caller's medium", "medium", false, "medium", false},
		{"a caller's high", "high", false, "high", false},
		{"a caller's xhigh", "xhigh", false, "xhigh", false},
		{"a caller's max", "max", false, "max", false},
		{"a caller's value with space around it", "  high ", false, "high", false},
		// An operation's preference was measured on another model, and this
		// provider's own setting is the operator's.
		{"an operation's preference for none", "none", true, "", false},
		{"an operation's preference for low", "low", true, "", false},
		{"the preferred flag with no effort", "", true, "", false},
		// A word that is not an effort is refused here. It used to be handed
		// to the CLI as configuration, which fails a turn later and says less.
		{"a misspelt effort", "hgih", false, "", true},
		{"an effort that is a sentence", "as much as it takes", false, "", true},
		{"an effort with a quote in it", `high" x="y`, false, "", true},
		{"a preference that is not an effort is not this provider's to refuse", "hgih", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := &scriptedTurn{transcript: turnTranscript{
				reply:    []byte(`{"summary":"ok"}`),
				events:   []byte(completedEventLine),
				exitCode: 0,
			}}
			provider := providerWith(script, Options{Model: "gpt-5.6-sol", ReasoningEffort: "medium"})

			_, err := provider.Complete(context.Background(), llm.CompletionRequest{
				UserPrompt: "summarise", ReasoningEffort: tc.effort, ReasoningEffortPreferred: tc.preferred,
			})
			if tc.refused {
				if !errors.Is(err, types.ErrInvalidRequest) {
					t.Fatalf("err = %v, want an invalid request", err)
				}
				if script.calls != 0 {
					t.Fatal("a turn was run for a request that was refused")
				}
				return
			}
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if script.seen.effort != tc.want {
				t.Errorf("the turn was given effort %q, want %q", script.seen.effort, tc.want)
			}
		})
	}
}

func TestIsEffortWord(t *testing.T) {
	cases := []struct {
		word string
		want bool
	}{
		{"none", true}, {"minimal", true}, {"low", true}, {"medium", true}, {"high", true}, {"xhigh", true}, {"max", true},
		{"", false}, {"None", false}, {"HIGH", false}, {" high", false}, {"high ", false}, {"hgih", false},
		{"very high", false}, {"default", false}, {"0", false}, {`high"`, false},
	}
	for _, tc := range cases {
		if got := isEffortWord(tc.word); got != tc.want {
			t.Errorf("isEffortWord(%q) = %v, want %v", tc.word, got, tc.want)
		}
	}
}
