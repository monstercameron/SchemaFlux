package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// T-01 / OP-401. Each `X` and `XWithMetadata` pair duplicated its option
// handling verbatim, so a rule added to one silently did not apply to the
// other. The blocks are shared now; this is what keeps them shared.
//
// The test compares the *steering the two twins actually send*, which is the
// thing that drifted. Comparing the source would only prove they look alike.
func TestTextTwinsSendTheSameOptions(t *testing.T) {
	cases := []struct {
		name      string
		runPlain  func() error
		runDetail func() error
	}{
		{
			"summarize",
			func() error {
				_, err := Summarize("some text", summarizeTwinOptions())
				return err
			},
			func() error {
				_, err := SummarizeWithMetadata("some text", summarizeTwinOptions())
				return err
			},
		},
		{
			"rewrite",
			func() error {
				_, err := Rewrite("some text", rewriteTwinOptions())
				return err
			},
			func() error {
				_, err := RewriteWithMetadata("some text", rewriteTwinOptions())
				return err
			},
		},
		{
			"translate",
			func() error {
				_, err := Translate("some text", translateTwinOptions())
				return err
			},
			func() error {
				_, err := TranslateWithMetadata("some text", translateTwinOptions())
				return err
			},
		},
		{
			"expand",
			func() error {
				_, err := Expand("some text", expandTwinOptions())
				return err
			},
			func() error {
				_, err := ExpandWithMetadata("some text", expandTwinOptions())
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain := captureSteering(t, tc.runPlain)
			detailed := captureSteering(t, tc.runDetail)

			if plain == "" {
				t.Fatal("the plain twin sent no steering, so this test proves nothing")
			}
			if plain != detailed {
				t.Errorf("the twins disagree about what to tell the model:\n  plain:    %s\n  detailed: %s",
					plain, detailed)
			}
		})
	}
}

// captureSteering runs an operation against a caller that records what it was
// asked and returns whatever body keeps the operation from erroring first.
func captureSteering(t *testing.T, run func() error) string {
	t.Helper()

	var seen string
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
		seen = opts.Steering
		return `{"text":"a summary","key_points":["one"],"confidence":0.9,` +
			`"rewritten":"x","translated":"x","expanded":"x","changes":[],"detected_language":"en"}`, nil
	})
	defer func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	}()

	// The error is not the subject: several of these refuse the shared body,
	// which is them working. What is being compared is the request.
	_ = run()
	return seen
}

func summarizeTwinOptions() SummarizeOptions {
	opts := NewSummarizeOptions()
	opts.TargetLength = 3
	opts.LengthUnit = "sentences"
	opts.Style = "executive"
	opts.FocusAreas = []string{"cost", "risk"}
	opts.PreserveInfo = []string{"the invoice number"}
	opts.OpOptions.Steering = "keep it dry"
	return opts
}

func rewriteTwinOptions() RewriteOptions {
	opts := NewRewriteOptions()
	opts.TargetTone = "formal"
	opts.Audience = "executives"
	opts.AvoidWords = []string{"synergy"}
	opts.OpOptions.Steering = "keep it dry"
	return opts
}

func translateTwinOptions() TranslateOptions {
	opts := NewTranslateOptions()
	opts.TargetLanguage = "French"
	opts.SourceLanguage = "English"
	opts.Dialect = "Quebec"
	opts.PreserveFormatting = true
	opts.Glossary = map[string]string{"gamma": "c", "alpha": "a", "beta": "b"}
	opts.OpOptions.Steering = "keep it dry"
	return opts
}

func expandTwinOptions() ExpandOptions {
	opts := NewExpandOptions()
	opts.ExpansionFactor = 3
	opts.DetailLevel = 8
	opts.IncludeExamples = true
	opts.ElaborateOn = []string{"the cost model"}
	opts.OpOptions.Steering = "keep it dry"
	return opts
}

// The twins agreeing with each other is not enough: two that dropped the same
// clause would still agree. This pins what is sent, word for word, and sends it
// twice, so an order that changes from one call to the next (a glossary ranged
// over as a map) shows up as a difference between the calls.
func TestTextTwinsSendTheExactSteering(t *testing.T) {
	cases := []struct {
		name      string
		runPlain  func() error
		runDetail func() error
		want      string
	}{
		{"summarize",
			func() error { _, err := Summarize("some text", summarizeTwinOptions()); return err },
			func() error { _, err := SummarizeWithMetadata("some text", summarizeTwinOptions()); return err },
			"keep it dry. Target length: 3 sentences. Style: executive. Focus on: cost, risk. Must preserve: the invoice number"},
		{"rewrite",
			func() error { _, err := Rewrite("some text", rewriteTwinOptions()); return err },
			func() error { _, err := RewriteWithMetadata("some text", rewriteTwinOptions()); return err },
			"keep it dry. Target tone: formal. Target audience: executives. Avoid: synergy. Preserve all factual information"},
		{"translate",
			func() error { _, err := Translate("some text", translateTwinOptions()); return err },
			func() error { _, err := TranslateWithMetadata("some text", translateTwinOptions()); return err },
			"keep it dry. Translate to French. From English. Use Quebec dialect. Preserve formatting. Use glossary: alpha=a, beta=b, gamma=c"},
		{"expand",
			func() error { _, err := Expand("some text", expandTwinOptions()); return err },
			func() error { _, err := ExpandWithMetadata("some text", expandTwinOptions()); return err },
			"keep it dry. Expand by 3.0x. Detail level: 8/10. Style: balanced. Include relevant examples. Elaborate on: the cost model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for round := 0; round < 3; round++ {
				if got := captureSteering(t, tc.runPlain); got != tc.want {
					t.Errorf("round %d, plain: got %q, want %q", round, got, tc.want)
				}
				if got := captureSteering(t, tc.runDetail); got != tc.want {
					t.Errorf("round %d, with metadata: got %q, want %q", round, got, tc.want)
				}
			}
		})
	}
}
