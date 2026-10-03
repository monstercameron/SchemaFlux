package ops

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 11, Arbitrate and Decide.

// The prompt left the shape of the answer to "the schema". A provider that is
// sent no schema, Anthropic's and the plain JSON mode among them, never showed
// the model what to answer with, so every such answer was refused. The shape
// is in the instructions, with an example, and the example is an answer this
// library accepts.
func TestArbitratePromptStatesTheAnswerShape(t *testing.T) {
	for _, want := range []string{`"options"`, `"o1"`, `"r1"`, `"passed"`, `"score"`, `"reasoning"`, `"confidence"`} {
		if !strings.Contains(arbitrateSystemPrompt, want) {
			t.Errorf("the instructions do not name %s", want)
		}
	}

	// Each top-level key is described, and not only shown in the example.
	for _, described := range []string{`- "options":`, `- "reasoning":`, `- "confidence":`} {
		if !strings.Contains(arbitrateSystemPrompt, described) {
			t.Errorf("the instructions do not describe %s", described)
		}
	}

	const marker = "For two options and two rules, with reasons asked for:\n"
	at := strings.Index(arbitrateSystemPrompt, marker)
	if at < 0 {
		t.Fatal("the instructions carry no example of an answer")
	}
	example := arbitrateSystemPrompt[at+len(marker):]
	if end := strings.Index(example, "\n"); end >= 0 {
		example = example[:end]
	}
	rules := []string{"cheap", "fast"}
	for _, reasoning := range []bool{true, false} {
		evaluations, _, _, err := arbitrateAnswer(example, 2, rules, reasoning)
		if err != nil {
			t.Fatalf("the example in the instructions is refused (reasoning %v): %v", reasoning, err)
		}
		if len(evaluations) != 2 || len(evaluations[0].RuleResults) != 2 {
			t.Errorf("the example reports on %d options", len(evaluations))
		}
	}
}

// No instruction says "see the schema": a model that was sent none has nothing
// to see.
func TestNoPromptLeavesTheAnswerToTheSchema(t *testing.T) {
	prompts := map[string]string{
		"arbitrate": arbitrateSystemPrompt,
		"audit":     auditSystemPrompt,
		"decide":    decideSystemPrompt,
		"validate":  validateSystemPrompt,
	}
	for name, prompt := range prompts {
		if strings.Contains(strings.ToLower(prompt), "schema") {
			t.Errorf("%s: the instructions refer the model to a schema", name)
		}
		if !strings.Contains(prompt, "JSON object") {
			t.Errorf("%s: the instructions do not say the answer is a JSON object", name)
		}
	}
}

// Whether a reason is wanted with every evaluation is said in the message. It
// was said only by the schema having the field.
func TestArbitrateAsksForReasonsInTheMessage(t *testing.T) {
	answer := lx024Answer("0.8", []float64{0.9}, []float64{0.2}, []float64{0.4})
	rules := []string{"the cheapest"}

	_, sent, err := arbitrateWith(t, answer, lx024Vendors, ArbitrateOptions{Rules: rules, IncludeReasoning: true})
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if !strings.Contains(sent.user, "Give a reason with every evaluation.") {
		t.Errorf("reasons were asked for, and the message does not say so:\n%s", sent.user)
	}

	_, sent, err = arbitrateWith(t, answer, lx024Vendors, ArbitrateOptions{Rules: rules})
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if strings.Contains(sent.user, "Give a reason") {
		t.Errorf("no reasons were asked for, and the message asks for them:\n%s", sent.user)
	}
}

// A reason with every evaluation is required only when it was asked for. No
// test left it out without asking, so making it always required passed them
// all.
func TestArbitratePerRuleReasoningIsRequiredOnlyWhenAsked(t *testing.T) {
	bare := `{"options":{"o1":{"r1":{"passed":true,"score":0.9}},"o2":{"r1":{"passed":true,"score":0.2}},"o3":{"r1":{"passed":false,"score":0.1}}},"reasoning":"overall","confidence":0.8}`
	rules := []string{"the cheapest"}

	result, _, err := arbitrateWith(t, bare, lx024Vendors, ArbitrateOptions{Rules: rules})
	if err != nil {
		t.Fatalf("an answer with no reasons was refused, and none were asked for: %v", err)
	}
	if result.WinnerIndex != 0 {
		t.Errorf("WinnerIndex = %d", result.WinnerIndex)
	}

	if result, _, err := arbitrateWith(t, bare, lx024Vendors, ArbitrateOptions{Rules: rules, IncludeReasoning: true}); err == nil {
		t.Errorf("an answer with no reasons was accepted, and reasons were asked for: %+v", result.Evaluations)
	}

	// NewArbitrateOptions asks for the same thing a struct does.
	if NewArbitrateOptions().IncludeReasoning {
		t.Error("NewArbitrateOptions asks for a reason with every evaluation, and ArbitrateOptions{} does not")
	}
}

func TestArbitrateSchemaProperties(t *testing.T) {
	cases := []struct {
		options, rules int
		reasoning      bool
		want           int
	}{
		{1, 1, false, 3 + 1 + 1 + 2},
		{1, 1, true, 3 + 1 + 1 + 3},
		{2, 1, false, 3 + 2 + 2 + 4},
		{2, 2, false, 3 + 2 + 4 + 8},
		{2, 2, true, 3 + 2 + 4 + 12},
		{3, 5, false, 3 + 3 + 15 + 30},
		{3, 5, true, 3 + 3 + 15 + 45},
		{10, 10, true, 3 + 10 + 100 + 300},
		{20, 10, true, 3 + 20 + 200 + 600},
		{50, 20, true, 3 + 50 + 1000 + 3000},
		{62, 20, true, 3 + 62 + 1240 + 3720},
		{100, 1, false, 3 + 100 + 100 + 200},
	}
	for _, tc := range cases {
		if got := arbitrateSchemaProperties(tc.options, tc.rules, tc.reasoning); got != tc.want {
			t.Errorf("arbitrateSchemaProperties(%d, %d, %v) = %d, want %d", tc.options, tc.rules, tc.reasoning, got, tc.want)
		}
		// The count is of the schema that is built, not a formula beside it.
		if tc.options*tc.rules <= 200 {
			if counted := countSchemaProperties(arbitrateSchema(tc.options, tc.rules, tc.reasoning)); counted != tc.want {
				t.Errorf("the schema for %d options and %d rules declares %d properties, and the count says %d",
					tc.options, tc.rules, counted, tc.want)
			}
		}
	}
}

// countSchemaProperties counts every property a schema declares, at any depth.
func countSchemaProperties(schema map[string]any) int {
	total := 0
	if properties, ok := schema["properties"].(map[string]any); ok {
		total += len(properties)
		for _, property := range properties {
			if nested, ok := property.(map[string]any); ok {
				total += countSchemaProperties(nested)
			}
		}
	}
	return total
}

// An arbitration that would declare more than a provider's structured output
// takes is refused here, with the count, before anything is sent. It used to
// go out and come back as a provider's 400.
func TestArbitrateRefusesMoreThanItCanDeclare(t *testing.T) {
	cases := []struct {
		name      string
		options   int
		rules     int
		reasoning bool
		refused   bool
	}{
		{"three by one", 3, 1, false, false},
		{"ten by ten with reasons", 10, 10, true, false},
		{"twenty by ten with reasons", 20, 10, true, false},
		{"thirty by twenty", 30, 20, false, false},
		{"thirty by twenty with reasons", 30, 20, true, false},
		// Round 12: the bound is the documented 5,000, where it was 2,500.
		{"fifty by twenty", 50, 20, false, false},
		{"fifty by twenty with reasons", 50, 20, true, false},
		{"sixty-two by twenty with reasons", 62, 20, true, true},
		{"two thousand by one", 2000, 1, false, true},
		{"two by seven hundred with reasons", 2, 700, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options := make([]lx024Vendor, tc.options)
			rules := make([]string, tc.rules)
			scores := make([]float64, tc.rules)
			for j := range rules {
				rules[j] = "rule " + strconv.Itoa(j+1)
				scores[j] = 0.5
			}
			answers := make([][]float64, tc.options)
			for i := range answers {
				answers[i] = scores
			}
			_, sent, err := arbitrateWith(t, lx024Answer("0.8", answers...), options,
				ArbitrateOptions{Rules: rules, IncludeReasoning: tc.reasoning, Tiebreaker: "first"})
			if !tc.refused {
				if err != nil {
					t.Fatalf("Arbitrate: %v", err)
				}
				return
			}
			if err == nil || types.KindOf(err) != types.KindConfiguration {
				t.Fatalf("err = %v, want a configuration error", err)
			}
			if sent.calls != 0 {
				t.Errorf("%d calls were made for an arbitration that was refused", sent.calls)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(arbitrateSchemaProperties(tc.options, tc.rules, tc.reasoning))) {
				t.Errorf("the error does not give the count: %v", err)
			}
		})
	}
}

// Weights whose sum is not a number a total can be divided by are refused. Two
// of 1e308 add up to infinity, every total came out not-a-number, nothing was
// ever the best, and the error blamed a requirement the caller had not set.
func TestArbitrateRefusesWeightsThatOverflow(t *testing.T) {
	cases := []struct {
		name    string
		weights []float64
		refused bool
	}{
		{"two of 1e308", []float64{1e308, 1e308}, true},
		{"the largest number, twice", []float64{math.MaxFloat64, math.MaxFloat64}, true},
		{"three of 1e308", []float64{1e308, 1e308, 1e308}, true},
		{"one of 1e308 and the default", []float64{1e308}, false},
		{"the largest number and zero", []float64{math.MaxFloat64, 0, 0}, false},
		{"ordinary", []float64{0.5, 0.3, 0.2}, false},
		{"large and finite", []float64{1e300, 1e300, 1e300}, false},
		{"tiny", []float64{1e-300, 1e-300, 1e-300}, false},
		{"one that underflows to nothing with the others", []float64{math.SmallestNonzeroFloat64, 0, 0}, false},
		{"infinity", []float64{math.Inf(1), 1, 1}, true},
	}
	rules := []string{"a", "b", "c"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weights, err := arbitrateWeights(rules, tc.weights)
			if tc.refused {
				if err == nil || types.KindOf(err) != types.KindConfiguration {
					t.Fatalf("weights %v, err %v; want a configuration error", weights, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("arbitrateWeights: %v", err)
			}
			sum := 0.0
			for _, weight := range weights {
				sum += weight
			}
			if math.IsInf(sum, 0) || math.IsNaN(sum) || sum <= 0 {
				t.Errorf("the accepted weights add up to %v", sum)
			}
		})
	}

	// Through the operation: the answer is fine, and the weights are what is
	// wrong.
	answer := lx024Answer("0.8", []float64{0.9, 0.1}, []float64{0.2, 0.9}, []float64{0.4, 0.4})
	_, sent, err := arbitrateWith(t, answer, lx024Vendors, ArbitrateOptions{Rules: []string{"a", "b"}, Weights: []float64{1e308, 1e308}})
	if err == nil || types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
		t.Errorf("err %v, %d calls", err, sent.calls)
	}
}

// When every option is disqualified, the error says which option failed which
// rule, by position. The evaluations used to come back beside the error and
// were dropped with the rest of the result; a caller was left with "every
// option failed" and nothing to act on.
func TestArbitrateSaysWhichOptionFailedWhichRule(t *testing.T) {
	answer := lx024Answer("0.8", []float64{-0.9, 0.5}, []float64{0.2, -0.9}, []float64{-0.4, -0.4})
	result, _, err := arbitrateWith(t, answer, lx024Vendors,
		ArbitrateOptions{Rules: []string{"the cheapest", "ships this week"}, RequireAllRules: true})
	if !errors.Is(err, types.ErrReviewRequired) {
		t.Fatalf("err = %v, want a review-required error", err)
	}
	for _, want := range []string{"option 1 failed rule 1", "option 2 failed rule 2", "option 3 failed rules 1, 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
	for _, leaked := range []string{"Acme", "Bolt", "cheapest", "ships", "because", "R&D"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("the error carries %q, which is the caller's or the model's: %v", leaked, err)
		}
	}
	if result.Evaluations != nil || result.Winner != (lx024Vendor{}) {
		t.Errorf("a result came back with the error: %+v", result)
	}

	// A long list is cut, and says how many it left out.
	many := make([]lx024Vendor, 30)
	answers := make([][]float64, 30)
	for i := range answers {
		answers[i] = []float64{-0.5}
	}
	_, _, err = arbitrateWith(t, lx024Answer("0.8", answers...), many,
		ArbitrateOptions{Rules: []string{"the cheapest"}, RequireAllRules: true})
	if !errors.Is(err, types.ErrReviewRequired) || !strings.Contains(err.Error(), "and 20 more") {
		t.Errorf("err = %v", err)
	}
	if len(err.Error()) > 600 {
		t.Errorf("the error is %d characters long", len(err.Error()))
	}
}

// A tie is every option within a billionth of the best. It was every option
// within a billionth of the best so far, so a run of totals each just under
// the last were all "tied", and the tiebreaker could pick the lowest of them.
func TestArbitrateTiesAreMeasuredFromTheBest(t *testing.T) {
	one := func(score float64, passed bool) OptionEvaluation {
		return OptionEvaluation{RuleResults: []RuleEvaluation{{Passed: passed, Score: score}}}
	}
	cases := []struct {
		name   string
		scores []float64
		winner int
		tied   []int
	}{
		{"a run just under each other", []float64{0.5, 0.5 + 0.9e-9, 0.5 - 0.9e-9}, 0, []int{0, 1}},
		{"the same run, the other way round", []float64{0.5 - 0.9e-9, 0.5 + 0.9e-9, 0.5}, 1, []int{1, 2}},
		{"three equal", []float64{0.5, 0.5, 0.5}, 0, []int{0, 1, 2}},
		{"a clear best last", []float64{0.1, 0.2, 0.9}, 2, nil},
		{"a clear best first", []float64{0.9, 0.2, 0.1}, 0, nil},
		{"two best, apart", []float64{0.9, 0.2, 0.9}, 0, []int{0, 2}},
		{"within a billionth, best in the middle", []float64{0.7, 0.7 + 0.5e-9, 0.7}, 0, []int{0, 1, 2}},
		{"a step of two billionths is not a tie", []float64{0.7, 0.7 + 2e-9}, 1, nil},
		{"descending by just under a billionth", []float64{0.5 + 1.8e-9, 0.5 + 0.9e-9, 0.5}, 0, []int{0, 1}},
		{"all zero", []float64{0, 0}, 0, []int{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evaluations := make([]OptionEvaluation, len(tc.scores))
			for i, score := range tc.scores {
				evaluations[i] = one(score, true)
			}
			winner, tied := arbitrateDecide(evaluations, []float64{1}, false, tiebreakFirst, nil)
			if winner != tc.winner {
				t.Errorf("winner = %d, want %d", winner, tc.winner)
			}
			if len(tied) != len(tc.tied) {
				t.Fatalf("tied = %v, want %v", tied, tc.tied)
			}
			for i := range tied {
				if tied[i] != tc.tied[i] {
					t.Fatalf("tied = %v, want %v", tied, tc.tied)
				}
			}
		})
	}

	// The case from review: "most-confident" must not pick the lowest total.
	// The lowest is the only one that passed its rule, so a tiebreak that
	// takes it for tied chooses it.
	evaluations := []OptionEvaluation{one(0.5, false), one(0.5+0.9e-9, false), one(0.5-0.9e-9, true)}
	if winner, _ := arbitrateDecide(evaluations, []float64{1}, false, tiebreakMostRulesPassed, nil); winner != 0 {
		t.Errorf("winner = %d: the option with the lowest total was taken for tied with the best", winner)
	}
}

// One option with nothing required of it wins without a call, and what was
// asked for is still checked: an unknown tiebreaker or a weight that is not a
// number was accepted here and refused with a second option.
func TestArbitrateChecksItsOptionsBeforeTheOneOptionShortcut(t *testing.T) {
	single := lx024Vendors[:1]
	cases := []struct {
		name    string
		opts    ArbitrateOptions
		refused bool
	}{
		{"nothing set", ArbitrateOptions{}, false},
		{"rules only", ArbitrateOptions{Rules: []string{"cheap"}}, false},
		{"rules and weights", ArbitrateOptions{Rules: []string{"cheap"}, Weights: []float64{2}}, false},
		{"a known tiebreaker", ArbitrateOptions{Tiebreaker: "random"}, false},
		{"an unknown tiebreaker", ArbitrateOptions{Tiebreaker: "coin"}, true},
		{"a weight that is not a number", ArbitrateOptions{Rules: []string{"cheap"}, Weights: []float64{math.NaN()}}, true},
		{"a negative weight", ArbitrateOptions{Rules: []string{"cheap"}, Weights: []float64{-1}}, true},
		{"more weights than rules", ArbitrateOptions{Rules: []string{"cheap"}, Weights: []float64{1, 1}}, true},
		{"weights and no rules", ArbitrateOptions{Weights: []float64{1}}, true},
		{"a blank rule", ArbitrateOptions{Rules: []string{"cheap", "  "}}, true},
		{"weights of zero", ArbitrateOptions{Rules: []string{"cheap"}, Weights: []float64{0}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, sent, err := arbitrateWith(t, "{}", single, tc.opts)
			if sent.calls != 0 {
				t.Errorf("%d calls were made for one option with nothing required of it", sent.calls)
			}
			if tc.refused {
				if err == nil || types.KindOf(err) != types.KindConfiguration {
					t.Fatalf("err = %v, want a configuration error", err)
				}
				return
			}
			if err != nil || result.WinnerIndex != 0 || result.Winner != single[0] {
				t.Errorf("result %+v, err %v", result, err)
			}
		})
	}
}

type lx11Opaque struct {
	secret string
	count  int
}

type lx11Named struct {
	Subject string `json:"subject"`
	hidden  int
}

type lx11Marshals struct{ text string }

func (m lx11Marshals) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(m.text)), nil }

// A situation that cannot reach the model is refused. The situation is written
// as JSON now, and an error, or a struct with no exported field, is "{}": the
// model was asked to choose for a situation it was not shown. With a fallback
// set, the caller's mistake is still reported and the fallback is not taken:
// it would be taken on every call and the mistake never seen.
func TestDecideRefusesASituationThatSaysNothing(t *testing.T) {
	answer := `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`
	cases := []struct {
		name      string
		situation any
		refused   bool
	}{
		{"a string", "a ticket about a refund", false},
		{"a struct with an exported field", lx11Named{Subject: "refund"}, false},
		{"a pointer to one", &lx11Named{Subject: "refund"}, false},
		{"a map", map[string]any{"subject": "refund"}, false},
		{"an empty map", map[string]any{}, false},
		{"nil", nil, false},
		{"a number", 42, false},
		{"a type that writes itself", lx11Marshals{text: "refund"}, false},
		// Round 12: an error is shown as its message, and is no longer refused.
		{"an error", errors.New("the card was declined"), false},
		{"a wrapped error", &types.OperationError{Kind: types.KindTimeout, Op: "x"}, false},
		{"a struct with no exported field", lx11Opaque{secret: "refund", count: 2}, true},
		{"a pointer to one", &lx11Opaque{secret: "refund"}, true},
		{"an empty struct", struct{}{}, false},
		{"a channel", make(chan int), true},
		{"a function", func() {}, true},
	}
	for _, tc := range cases {
		for _, withFallback := range []bool{false, true} {
			name := tc.name
			if withFallback {
				name += ", with a fallback"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				previousCaller, previousProvider := currentHooks()
				setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
					calls++
					return answer, nil
				})
				t.Cleanup(func() {
					setLLMCaller(previousCaller)
					SetDefaultProvider(previousProvider)
				})

				opts := NewDecideOptions()
				if withFallback {
					opts = opts.WithFallback(0)
				}
				value, result, err := Decide(context.Background(), tc.situation, routes(), opts)
				if !tc.refused {
					if err != nil || result.SelectedIndex != 1 || result.Fallback {
						t.Errorf("value %q, result %+v, err %v", value, result, err)
					}
					return
				}
				if err == nil || types.KindOf(err) != types.KindInvalidRequest {
					t.Fatalf("value %q, result %+v, err %v; want an invalid-request error", value, result, err)
				}
				if calls != 0 || result.Fallback || result.SelectedIndex != -1 || value != "" {
					t.Errorf("%d calls, value %q, result %+v", calls, value, result)
				}
				if strings.Contains(err.Error(), "declined") || strings.Contains(err.Error(), "refund") {
					t.Errorf("the error carries the situation: %v", err)
				}
			})
		}
	}
}

// A choice among more branches than an answer can be declared for is refused
// here, with or without a fallback. The alternatives no longer repeat the
// list of branch numbers, which doubled it.
func TestDecideRefusesMoreBranchesThanItCanDeclare(t *testing.T) {
	branches := func(n int) []Decision[string] {
		decisions := make([]Decision[string], n)
		for i := range decisions {
			decisions[i] = Decision[string]{Value: "v" + strconv.Itoa(i), Description: "branch " + strconv.Itoa(i)}
		}
		return decisions
	}
	answer := `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[0]}`
	for _, tc := range []struct {
		n       int
		refused bool
	}{
		{2, false}, {10, false}, {100, false}, {250, false}, {251, false}, {400, false}, {500, false},
		{501, true}, {1000, true}, {5000, true},
	} {
		for _, withFallback := range []bool{false, true} {
			restore := stubLLM(answer)
			opts := NewDecideOptions()
			if withFallback {
				opts = opts.WithFallback(0)
			}
			value, result, err := Decide(context.Background(), "a ticket", branches(tc.n), opts)
			restore()
			if !tc.refused {
				if err != nil || result.SelectedIndex != 1 || result.Fallback {
					t.Errorf("%d branches (fallback %v): value %q, result %+v, err %v", tc.n, withFallback, value, result, err)
				}
				continue
			}
			if err == nil || types.KindOf(err) != types.KindConfiguration || result.Fallback {
				t.Errorf("%d branches (fallback %v): result %+v, err %v; want a configuration error", tc.n, withFallback, result, err)
			}
		}
	}

	schema := decideSchema(40)
	properties := schema["properties"].(map[string]any)
	items := properties["alternatives"].(map[string]any)["items"].(map[string]any)
	if _, repeated := items["enum"]; repeated {
		t.Error("the alternatives repeat the list of branch numbers")
	}
	if selected := properties["selected"].(map[string]any)["enum"].([]any); len(selected) != 40 {
		t.Errorf("the selection is one of %d numbers, want 40", len(selected))
	}
}

// What Decide refuses before it asks anything is classified, so a caller can
// tell its own mistake from a provider's.
func TestDecideClassifiesWhatItRefuses(t *testing.T) {
	cases := []struct {
		name      string
		decisions []Decision[string]
		opts      DecideOptions
		kind      types.ErrorKind
	}{
		{"no decisions", nil, NewDecideOptions(), types.KindInvalidRequest},
		{"an empty list", []Decision[string]{}, NewDecideOptions(), types.KindInvalidRequest},
		{"a fallback past the end", routes(), NewDecideOptions().WithFallback(len(routes())), types.KindConfiguration},
		{"a fallback far past the end", routes(), NewDecideOptions().WithFallback(99), types.KindConfiguration},
		{"a negative fallback", routes(), NewDecideOptions().WithFallback(-1), types.KindConfiguration},
		{"no decisions and a fallback", nil, NewDecideOptions().WithFallback(0), types.KindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubLLM(`{"selected":0,"explanation":"x","confidence":0.9,"alternatives":[]}`)
			defer restore()
			value, result, err := Decide(context.Background(), "a ticket", tc.decisions, tc.opts)
			if err == nil || types.KindOf(err) != tc.kind {
				t.Fatalf("err = %v (%v), want %v", err, types.KindOf(err), tc.kind)
			}
			if value != "" || result.SelectedIndex != -1 || result.Fallback {
				t.Errorf("value %q, result %+v", value, result)
			}
		})
	}
}

// A number the model gave off the scale is refused without being repeated: the
// message goes into errors, logs and a fallback's explanation.
func TestOnScaleDoesNotRepeatTheNumber(t *testing.T) {
	// The message names the scale, "0 to 1", so a digit check cannot tell a
	// repeated 1.0000001 from the scale. What is compared is the message of one
	// value against every other: a message that repeats its number is a
	// different message for each, and this one is the same for all of them.
	reference := onScale("decide", "the confidence", 2)
	if reference == nil {
		t.Fatal("2 is off the scale and was accepted")
	}
	for _, value := range []float64{1.5, -0.25, 7, 1e9, -1e-9, 1.0000001, 42.4242, math.Inf(1), math.Inf(-1), math.NaN()} {
		err := onScale("decide", "the confidence", value)
		if err == nil || types.KindOf(err) != types.KindInvariantViolation {
			t.Errorf("%v: err = %v", value, err)
			continue
		}
		if err.Error() != reference.Error() {
			t.Errorf("%v: the message differs from the one for another off-scale value, so it repeats the number: %v", value, err)
		}
		for _, repeated := range []string{"NaN", "Inf", "e+", "e-", "0.25", "1.5", "42", "1.0000001"} {
			if strings.Contains(err.Error(), repeated) {
				t.Errorf("%v: the error repeats %q: %v", value, repeated, err)
			}
		}
		if !strings.Contains(err.Error(), "the confidence") || !strings.Contains(err.Error(), "0 to 1") {
			t.Errorf("%v: the error does not say what was off which scale: %v", value, err)
		}
	}
	for _, value := range []float64{0, 1, 0.5, 1e-12, 0.999999} {
		if err := onScale("decide", "the confidence", value); err != nil {
			t.Errorf("%v is on the scale: %v", value, err)
		}
	}
}
