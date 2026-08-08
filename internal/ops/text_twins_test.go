package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
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
