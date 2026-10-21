package llm

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// threeKinds is one question of each kind, the shape most cases below start
// from.
func threeKinds() *Decision {
	return &Decision{
		State: `"Please refund my order."`,
		Questions: []DecisionQuestion{
			{Kind: DecisionYesNo, Ask: "Is the customer asking for a refund?"},
			{Kind: DecisionChoice, Ask: "Which team should handle this?", Options: []string{"billing", "technical", "shipping"}},
			{Kind: DecisionScale, Ask: "How urgent is this?", Low: 1, High: 5},
		},
	}
}

func TestRenderDecisionAnswersWritesTheFormTheReaderTakes(t *testing.T) {
	reported := func(answer DecisionAnswer, confidence float64) DecisionAnswer {
		answer.ModelConfidence, answer.ConfidenceReported = confidence, true
		return answer
	}
	yes := DecisionAnswer{Kind: DecisionYesNo, Yes: true}
	no := DecisionAnswer{Kind: DecisionYesNo}
	shipping := DecisionAnswer{Kind: DecisionChoice, Label: "shipping"}
	four := DecisionAnswer{Kind: DecisionScale, Level: 4}

	one := func(question DecisionQuestion) *Decision {
		return &Decision{State: `"x"`, Questions: []DecisionQuestion{question}}
	}
	cases := []struct {
		name     string
		decision *Decision
		answers  []DecisionAnswer
		want     string
	}{
		{"a yes", one(threeKinds().Questions[0]), []DecisionAnswer{yes}, `{"q1":true}`},
		{"a no", one(threeKinds().Questions[0]), []DecisionAnswer{no}, `{"q1":false}`},
		{"the first label", one(threeKinds().Questions[1]), []DecisionAnswer{{Kind: DecisionChoice, Label: "billing"}}, `{"q1":1}`},
		{"the last label", one(threeKinds().Questions[1]), []DecisionAnswer{shipping}, `{"q1":3}`},
		{"a level", one(threeKinds().Questions[2]), []DecisionAnswer{four}, `{"q1":4}`},
		{"the bottom of a scale that starts below zero", one(DecisionQuestion{Kind: DecisionScale, Ask: "?", Low: -2, High: 2}),
			[]DecisionAnswer{{Kind: DecisionScale, Level: -2}}, `{"q1":-2}`},
		{"three questions, none with a probability", threeKinds(), []DecisionAnswer{yes, shipping, four}, `{"q1":true,"q2":3,"q3":4}`},
		{"three questions, the lowest probability is the confidence", threeKinds(),
			[]DecisionAnswer{reported(yes, 0.96), reported(shipping, 0.67), reported(four, 0.99)},
			`{"q1":true,"q2":3,"q3":4,"confidence":0.67}`},
		{"one answer without a probability leaves the confidence out", threeKinds(),
			[]DecisionAnswer{reported(yes, 0.96), shipping, reported(four, 0.99)}, `{"q1":true,"q2":3,"q3":4}`},
		{"a probability of zero is a probability", one(threeKinds().Questions[0]), []DecisionAnswer{reported(no, 0)}, `{"q1":false,"confidence":0}`},
		{"a probability of one", one(threeKinds().Questions[0]), []DecisionAnswer{reported(yes, 1)}, `{"q1":true,"confidence":1}`},
		{"a label that needs quoting is still a number in the answer", one(DecisionQuestion{Kind: DecisionChoice, Ask: "?", Options: []string{`say "hi"`, "none of these"}}),
			[]DecisionAnswer{{Kind: DecisionChoice, Label: "none of these"}}, `{"q1":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderDecisionAnswers(tc.decision, tc.answers)
			if err != nil {
				t.Fatalf("RenderDecisionAnswers: %v", err)
			}
			if got != tc.want {
				t.Errorf("rendered %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRenderDecisionAnswersRefusesAnAnswerThatIsNotOneToTheQuestion(t *testing.T) {
	yes := DecisionAnswer{Kind: DecisionYesNo, Yes: true}
	shipping := DecisionAnswer{Kind: DecisionChoice, Label: "shipping"}
	four := DecisionAnswer{Kind: DecisionScale, Level: 4}
	withConfidence := func(answer DecisionAnswer, confidence float64) DecisionAnswer {
		answer.ModelConfidence, answer.ConfidenceReported = confidence, true
		return answer
	}
	const secret = "a label nobody offered"

	cases := []struct {
		name     string
		decision *Decision
		answers  []DecisionAnswer
		say      string
	}{
		{"no decision", nil, []DecisionAnswer{yes}, "no decision"},
		{"a decision with no questions", &Decision{State: `"x"`}, nil, "no decision"},
		{"no answers at all", threeKinds(), nil, "3 questions"},
		{"one answer short", threeKinds(), []DecisionAnswer{yes, shipping}, "3 questions"},
		{"one answer too many", threeKinds(), []DecisionAnswer{yes, shipping, four, yes}, "3 questions"},
		{"an answer left at its zero value", threeKinds(), []DecisionAnswer{yes, {}, four}, "question 2"},
		{"a choice answered as a yes or no", threeKinds(), []DecisionAnswer{yes, yes, four}, "question 2"},
		{"a yes or no answered as a level", threeKinds(), []DecisionAnswer{four, shipping, four}, "question 1"},
		{"a label that was not offered", threeKinds(), []DecisionAnswer{yes, {Kind: DecisionChoice, Label: secret}, four}, "question 2"},
		{"a label in another letter case", threeKinds(), []DecisionAnswer{yes, {Kind: DecisionChoice, Label: "Shipping"}, four}, "question 2"},
		{"an empty label", threeKinds(), []DecisionAnswer{yes, {Kind: DecisionChoice}, four}, "question 2"},
		{"a level above the scale", threeKinds(), []DecisionAnswer{yes, shipping, {Kind: DecisionScale, Level: 6}}, "question 3"},
		{"a level below the scale", threeKinds(), []DecisionAnswer{yes, shipping, {Kind: DecisionScale, Level: 0}}, "question 3"},
		{"a probability above one", threeKinds(), []DecisionAnswer{withConfidence(yes, 1.2), shipping, four}, "question 1"},
		{"a probability below zero", threeKinds(), []DecisionAnswer{yes, withConfidence(shipping, -0.1), four}, "question 2"},
		{"a probability that is not a number", threeKinds(), []DecisionAnswer{yes, shipping, withConfidence(four, math.NaN())}, "question 3"},
		{"a probability that is infinite", threeKinds(), []DecisionAnswer{withConfidence(yes, math.Inf(1)), shipping, four}, "question 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderDecisionAnswers(tc.decision, tc.answers)
			if err == nil {
				t.Fatalf("rendered %s, want a refusal", got)
			}
			if got != "" {
				t.Errorf("a refusal came with %q beside it", got)
			}
			if kind := types.KindOf(err); kind != types.KindSchemaViolation {
				t.Errorf("kind %v, want a schema violation", kind)
			}
			if !strings.Contains(err.Error(), tc.say) {
				t.Errorf("the refusal %q does not say %q", err, tc.say)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "refund") {
				t.Errorf("the refusal repeats the caller's or the model's text: %q", err)
			}
		})
	}
}

func TestDecisionLimitsCheck(t *testing.T) {
	jev := DecisionLimits{MaxQuestions: 4, MaxOptions: 3, MaxLevels: 5, MaxStateTokens: 10}
	options := func(n int) []string {
		labels := make([]string, n)
		for i := range labels {
			labels[i] = "label " + string(rune('a'+i))
		}
		return labels
	}
	one := func(question DecisionQuestion) *Decision {
		return &Decision{State: `"short"`, Questions: []DecisionQuestion{question}}
	}
	yesNo := DecisionQuestion{Kind: DecisionYesNo, Ask: "Is it?"}

	cases := []struct {
		name     string
		limits   DecisionLimits
		decision *Decision
		want     types.ErrorKind
		say      string
	}{
		{"a yes or no within the limits", jev, one(yesNo), types.KindUnknown, ""},
		{"a choice at the limit", jev, one(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: options(3)}), types.KindUnknown, ""},
		{"a scale at the limit", jev, one(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 5}), types.KindUnknown, ""},
		{"no limits declared takes anything well formed", DecisionLimits{}, one(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: options(26)}), types.KindUnknown, ""},
		{"a state at the limit", jev, &Decision{State: strings.Repeat("x", 40), Questions: []DecisionQuestion{yesNo}}, types.KindUnknown, ""},

		{"no decision on the request", jev, nil, types.KindUnsupportedCapability, "carries no decision"},
		{"a decision with no question", jev, &Decision{State: `"x"`}, types.KindUnsupportedCapability, "carries no decision"},
		{"one option too many", jev, one(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: options(4)}), types.KindUnsupportedCapability, "4 options"},
		{"one level too many", jev, one(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 1, High: 6}), types.KindUnsupportedCapability, "6 levels"},
		{"a scale as wide as the ints", jev, one(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: math.MinInt, High: math.MaxInt}), types.KindUnsupportedCapability, "levels"},
		{"one question too many", jev, &Decision{State: `"x"`, Questions: []DecisionQuestion{yesNo, yesNo, yesNo, yesNo, yesNo}}, types.KindUnsupportedCapability, "5 questions"},
		{"a state longer than the context", jev, &Decision{State: strings.Repeat("x", 41), Questions: []DecisionQuestion{yesNo}}, types.KindUnsupportedCapability, "state"},

		{"a question of no kind", jev, one(DecisionQuestion{Ask: "?"}), types.KindInvalidRequest, "question 1"},
		{"a choice with one option", jev, one(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: options(1)}), types.KindInvalidRequest, "question 1"},
		{"criteria that do not match the options", jev, one(DecisionQuestion{Kind: DecisionChoice, Ask: "Which?", Options: options(2), Criteria: []string{"only one"}}), types.KindInvalidRequest, "question 1"},
		{"a scale with nothing on it", jev, one(DecisionQuestion{Kind: DecisionScale, Ask: "How?", Low: 3, High: 3}), types.KindInvalidRequest, "question 1"},
		{"a blank question", jev, one(DecisionQuestion{Kind: DecisionYesNo, Ask: "  "}), types.KindInvalidRequest, "question 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.limits.Check("jev", "jev-latest", tc.decision)
			if tc.want == types.KindUnknown {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed, want a refusal")
			}
			if kind := types.KindOf(err); kind != tc.want {
				t.Errorf("kind %v, want %v", kind, tc.want)
			}
			if !strings.Contains(err.Error(), tc.say) {
				t.Errorf("the refusal %q does not say %q", err, tc.say)
			}
			if strings.Contains(err.Error(), "label a") || strings.Contains(err.Error(), "xxxx") {
				t.Errorf("the refusal repeats the caller's text: %q", err)
			}
			var refusal *types.OperationError
			if !errors.As(err, &refusal) || refusal.Provider != "jev" || refusal.Model != "jev-latest" {
				t.Errorf("the refusal does not name the provider and the model: %#v", err)
			}
		})
	}
}

// limitedProvider is a provider that declares limits, and wrappedProvider one
// that wraps another the way a middleware does.
type limitedProvider struct{ limits DecisionLimits }

func (limitedProvider) Complete(context.Context, CompletionRequest) (CompletionResponse, error) {
	return CompletionResponse{}, nil
}
func (limitedProvider) Name() string                           { return "limited" }
func (limitedProvider) EstimateCost(CompletionRequest) float64 { return 0 }
func (limitedProvider) RetryPolicy() (int, time.Duration)      { return 0, 0 }
func (p limitedProvider) DecisionLimits() DecisionLimits       { return p.limits }

type wrappedProvider struct{ Provider }

func (w wrappedProvider) Unwrap() Provider { return w.Provider }

func TestDecisionLimitsOfReachesThroughAWrapper(t *testing.T) {
	limits := DecisionLimits{MaxOptions: 7}
	decider := limitedProvider{limits: limits}
	generator, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var loop wrappedProvider
	loop.Provider = &loop

	cases := []struct {
		name     string
		provider Provider
		want     DecisionLimits
		declared bool
	}{
		{"a provider that declares limits", decider, limits, true},
		{"the same held by pointer", &decider, limits, true},
		{"one wrapper around it", wrappedProvider{decider}, limits, true},
		{"two wrappers around it", wrappedProvider{wrappedProvider{decider}}, limits, true},
		{"a provider that declares limits of nothing", limitedProvider{}, DecisionLimits{}, true},
		{"a generating provider", generator, DecisionLimits{}, false},
		{"a wrapper around a generating provider", wrappedProvider{generator}, DecisionLimits{}, false},
		{"no provider", nil, DecisionLimits{}, false},
		{"a wrapper around nothing", wrappedProvider{}, DecisionLimits{}, false},
		{"a wrapper that wraps itself", &loop, DecisionLimits{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, declared := DecisionLimitsOf(tc.provider)
			if got != tc.want || declared != tc.declared {
				t.Errorf("DecisionLimitsOf = %+v, %v; want %+v, %v", got, declared, tc.want, tc.declared)
			}
		})
	}
}

func TestDecisionKindNamesItself(t *testing.T) {
	cases := map[DecisionKind]string{
		DecisionYesNo:    "yes or no",
		DecisionChoice:   "choice",
		DecisionScale:    "scale",
		0:                "no kind",
		DecisionKind(9):  "no kind",
		DecisionKind(77): "no kind",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("DecisionKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestLowestConfidence(t *testing.T) {
	with := func(confidence float64) DecisionAnswer {
		return DecisionAnswer{Kind: DecisionYesNo, ModelConfidence: confidence, ConfidenceReported: true}
	}
	without := DecisionAnswer{Kind: DecisionYesNo}
	cases := []struct {
		name     string
		answers  []DecisionAnswer
		want     float64
		reported bool
	}{
		{"no answers", nil, 0, false},
		{"one answer with a probability", []DecisionAnswer{with(0.8)}, 0.8, true},
		{"one answer without", []DecisionAnswer{without}, 0, false},
		{"the lowest of three", []DecisionAnswer{with(0.9), with(0.4), with(0.7)}, 0.4, true},
		{"the lowest is first", []DecisionAnswer{with(0.1), with(0.4)}, 0.1, true},
		{"the lowest is last", []DecisionAnswer{with(0.6), with(0.2)}, 0.2, true},
		{"one of three without", []DecisionAnswer{with(0.9), without, with(0.7)}, 0, false},
		{"the last without", []DecisionAnswer{with(0.9), with(0.7), without}, 0, false},
		{"all at one", []DecisionAnswer{with(1), with(1)}, 1, true},
		{"a zero is the lowest", []DecisionAnswer{with(0), with(1)}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reported := LowestConfidence(tc.answers)
			if got != tc.want || reported != tc.reported {
				t.Errorf("LowestConfidence = %v, %v; want %v, %v", got, reported, tc.want, tc.reported)
			}
		})
	}
}
