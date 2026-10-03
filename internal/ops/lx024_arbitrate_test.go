package ops

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

type lx024Vendor struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Note  string  `json:"note"`
}

var lx024Vendors = []lx024Vendor{
	{Name: "Acme", Price: 100, Note: "R&D <partner>"},
	{Name: "Bolt", Price: 80},
	{Name: "Core", Price: 120},
}

type arbitrateCall struct {
	system, user string
	opts         types.OpOptions
	calls        int
}

func arbitrateWith(t *testing.T, response string, options []lx024Vendor, opts ArbitrateOptions) (ArbitrateResult[lx024Vendor], *arbitrateCall, error) {
	t.Helper()
	sent := &arbitrateCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent.system, sent.user, sent.opts = system, user, opt
		sent.calls++
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Arbitrate(options, opts)
	return result, sent, err
}

// lx024Answer builds a scripted answer from what each option scored on each
// rule. A score below zero stands for a rule the option failed, at that score's
// absolute value.
func lx024Answer(confidence string, options ...[]float64) string {
	var body strings.Builder
	body.WriteString(`{"options":{`)
	for i, rules := range options {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString(`"o` + strconv.Itoa(i+1) + `":{`)
		for j, score := range rules {
			if j > 0 {
				body.WriteString(",")
			}
			passed := "true"
			if score < 0 {
				passed, score = "false", -score
			}
			body.WriteString(`"r` + strconv.Itoa(j+1) + `":{"passed":` + passed + `,"score":` + strconv.FormatFloat(score, 'f', -1, 64) + `,"reasoning":"because"}`)
		}
		body.WriteString("}")
	}
	body.WriteString(`},"reasoning":"overall","confidence":` + confidence + `}`)
	return body.String()
}

// LX-024. The model says how each option does on each rule, and this library
// works out the winner: it applies the caller's weights, disqualifies what
// fails a rule when every rule is required, and breaks ties by the caller's
// rule. The model used to be asked for the winner's index, which was a plain
// integer, so an answer that left it out chose option 0.
func TestArbitrateWorksOutTheWinner(t *testing.T) {
	two := []string{"is cheap", "is reliable"}
	cases := []struct {
		name     string
		options  int
		opts     ArbitrateOptions
		response string
		winner   int
		scores   map[int]float64
		ties     bool
		out      []int // options disqualified
	}{
		{"the higher score wins", 2, ArbitrateOptions{Rules: two[:1]},
			lx024Answer("0.8", []float64{0.9}, []float64{0.4}), 0, map[int]float64{0: 0.9, 1: 0.4}, false, nil},
		{"the second option wins", 2, ArbitrateOptions{Rules: two[:1]},
			lx024Answer("0.8", []float64{0.2}, []float64{0.7}), 1, map[int]float64{0: 0.2, 1: 0.7}, false, nil},
		{"two rules, equal weight", 2, ArbitrateOptions{Rules: two},
			lx024Answer("0.8", []float64{0.9, 0.3}, []float64{0.5, 0.5}), 0, map[int]float64{0: 0.6, 1: 0.5}, false, nil},
		{"the caller's weights decide", 2, ArbitrateOptions{Rules: two, Weights: []float64{1, 3}},
			lx024Answer("0.8", []float64{0.9, 0.2}, []float64{0.3, 0.8}), 1, map[int]float64{0: 0.375, 1: 0.675}, false, nil},
		{"a weight left out is one", 2, ArbitrateOptions{Rules: two, Weights: []float64{3}},
			lx024Answer("0.8", []float64{0.8, 0.0}, []float64{0.4, 1.0}), 0, map[int]float64{0: 0.6, 1: 0.55}, false, nil},
		{"a rule weighted zero does not count", 2, ArbitrateOptions{Rules: two, Weights: []float64{1, 0}},
			lx024Answer("0.8", []float64{0.6, 0.0}, []float64{0.5, 1.0}), 0, map[int]float64{0: 0.6, 1: 0.5}, false, nil},
		{"three options", 3, ArbitrateOptions{Rules: two[:1]},
			lx024Answer("0.8", []float64{0.3}, []float64{0.4}, []float64{0.95}), 2, map[int]float64{0: 0.3, 1: 0.4, 2: 0.95}, false, nil},
		{"a tie goes to the first", 2, ArbitrateOptions{Rules: two[:1], Tiebreaker: "first"},
			lx024Answer("0.8", []float64{0.5}, []float64{0.5}), 0, map[int]float64{0: 0.5, 1: 0.5}, true, nil},
		{"a tie by default goes to the option that passed more rules", 2, ArbitrateOptions{Rules: two},
			lx024Answer("0.8", []float64{1.0, -0.2}, []float64{0.6, 0.6}), 1, map[int]float64{0: 0.6, 1: 0.6}, true, nil},
		{"a tie between options that passed as many goes to the first", 2, ArbitrateOptions{Rules: two},
			lx024Answer("0.8", []float64{0.6, 0.6}, []float64{0.6, 0.6}), 0, map[int]float64{0: 0.6, 1: 0.6}, true, nil},
		{"a failed rule does not disqualify when not every rule is required", 2, ArbitrateOptions{Rules: two},
			lx024Answer("0.8", []float64{0.9, -0.5}, []float64{0.5, 0.5}), 0, map[int]float64{0: 0.7, 1: 0.5}, false, nil},
		{"a failed rule disqualifies when every rule is required", 2, ArbitrateOptions{Rules: two, RequireAllRules: true},
			lx024Answer("0.8", []float64{0.9, -0.5}, []float64{0.5, 0.5}), 1, map[int]float64{0: 0.7, 1: 0.5}, false, []int{0}},
		{"two of three disqualified", 3, ArbitrateOptions{Rules: two, RequireAllRules: true},
			lx024Answer("0.8", []float64{0.9, -0.9}, []float64{-0.9, 0.9}, []float64{0.2, 0.2}), 2, map[int]float64{0: 0.9, 1: 0.9, 2: 0.2}, false, []int{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.IncludeReasoning = true
			result, _, err := arbitrateWith(t, tc.response, lx024Vendors[:tc.options], tc.opts)
			if err != nil {
				t.Fatalf("Arbitrate: %v", err)
			}
			if result.WinnerIndex != tc.winner || result.Winner != lx024Vendors[tc.winner] {
				t.Errorf("winner %d (%+v), want %d", result.WinnerIndex, result.Winner, tc.winner)
			}
			for index, want := range tc.scores {
				if got := result.Scores[index]; math.Abs(got-want) > 1e-9 {
					t.Errorf("Scores[%d] = %v, want %v", index, got, want)
				}
			}
			if len(result.Scores) != tc.options {
				t.Errorf("Scores has %d entries for %d options", len(result.Scores), tc.options)
			}
			if result.TiesBroken != tc.ties {
				t.Errorf("TiesBroken = %v, want %v", result.TiesBroken, tc.ties)
			}
			if result.ModelConfidence != 0.8 || result.Reasoning != "overall" {
				t.Errorf("confidence %v, reasoning %q", result.ModelConfidence, result.Reasoning)
			}

			// The audit trail is this library's: one evaluation per option, in
			// order, each rule named in the caller's own words.
			if len(result.Evaluations) != tc.options {
				t.Fatalf("%d evaluations for %d options", len(result.Evaluations), tc.options)
			}
			var out []int
			for i, evaluation := range result.Evaluations {
				if evaluation.Index != i {
					t.Errorf("evaluation %d has index %d", i, evaluation.Index)
				}
				if math.Abs(evaluation.TotalScore-result.Scores[i]) > 1e-9 {
					t.Errorf("evaluation %d totals %v and Scores says %v", i, evaluation.TotalScore, result.Scores[i])
				}
				if len(evaluation.RuleResults) != len(tc.opts.Rules) {
					t.Fatalf("evaluation %d has %d rule results for %d rules", i, len(evaluation.RuleResults), len(tc.opts.Rules))
				}
				for j, rule := range evaluation.RuleResults {
					if rule.Rule != tc.opts.Rules[j] {
						t.Errorf("rule %d is named %q, want the caller's %q", j, rule.Rule, tc.opts.Rules[j])
					}
				}
				if evaluation.Disqualified {
					out = append(out, i)
					if evaluation.DisqualifyReason == "" {
						t.Errorf("option %d is disqualified with no reason", i)
					}
				}
			}
			if !reflect.DeepEqual(out, tc.out) {
				t.Errorf("disqualified %v, want %v", out, tc.out)
			}
		})
	}
}

// When every rule is required and no option meets them all, there is no winner
// to return, and that is an error a caller can test for. The model used to be
// trusted to pick one anyway.
func TestArbitrateHasNoWinnerWhenEveryOptionIsDisqualified(t *testing.T) {
	result, _, err := arbitrateWith(t,
		lx024Answer("0.8", []float64{0.9, -0.5}, []float64{-0.5, 0.9}),
		lx024Vendors[:2], ArbitrateOptions{Rules: []string{"a", "b"}, RequireAllRules: true})
	if !errors.Is(err, types.ErrReviewRequired) {
		t.Fatalf("err = %v, want review required", err)
	}
	if result.Winner != (lx024Vendor{}) || len(result.Scores) != 0 || len(result.Evaluations) != 0 {
		t.Errorf("a result came back with the error: %+v", result)
	}
}

// An answer that does not report on every option and every rule is refused.
// The first case is the defect: an answer with no evaluation in it at all used
// to select option 0.
func TestArbitrateRefusesAnAnswerThatDoesNotReport(t *testing.T) {
	rule := func(extra string) string { return `{"passed":true,"score":0.5,"reasoning":"x"` + extra + `}` }
	full := `{"options":{"o1":{"r1":` + rule("") + `,"r2":` + rule("") + `},"o2":{"r1":` + rule("") + `,"r2":` + rule("") + `}},"reasoning":"overall","confidence":0.8}`
	replace := func(old, new string) string {
		if !strings.Contains(full, old) {
			t.Fatalf("the full answer does not contain %q", old)
		}
		return strings.Replace(full, old, new, 1)
	}

	cases := []struct {
		name     string
		response string
	}{
		{"the old answer, a winner and nothing else", `{"winner_index":1,"reasoning":"x","confidence":0.9}`},
		{"an empty object", `{}`},
		{"not JSON", `option 2, R&D <partner> is the best`},
		{"options null", `{"options":null,"reasoning":"x","confidence":0.8}`},
		{"options a list", `{"options":[{},{}],"reasoning":"x","confidence":0.8}`},
		{"an option left out", `{"options":{"o1":{"r1":` + rule("") + `,"r2":` + rule("") + `}},"reasoning":"x","confidence":0.8}`},
		{"an option that was not offered", replace(`"o2":`, `"o3":`)},
		{"an option beyond those offered", replace(`},"reasoning":"overall"`, `,"o3":{"r1":`+rule("")+`,"r2":`+rule("")+`}},"reasoning":"overall"`)},
		{"a rule left out", replace(`,"r2":`+rule("")+`}}`, `}}`)},
		{"a rule that was not asked", replace(`"r2":`+rule("")+`}}`, `"r9":`+rule("")+`}}`)},
		{"a rule beyond those asked", replace(`"r2":`+rule("")+`}}`, `"r2":`+rule("")+`,"r3":`+rule("")+`}}`)},
		{"passed left out", replace(`{"passed":true,"score":0.5`, `{"score":0.5`)},
		{"passed that is not true or false", replace(`{"passed":true`, `{"passed":"yes"`)},
		{"a score left out", replace(`"passed":true,"score":0.5,`, `"passed":true,`)},
		{"a score over the scale", replace(`"score":0.5`, `"score":1.5`)},
		{"a score under the scale", replace(`"score":0.5`, `"score":-0.5`)},
		{"blank reasoning for a rule", replace(`"reasoning":"x"`, `"reasoning":" "`)},
		{"no confidence", replace(`,"confidence":0.8`, ``)},
		{"a confidence off the scale", replace(`"confidence":0.8`, `"confidence":8`)},
		{"an option that is null", replace(`"o2":{"r1":`+rule("")+`,"r2":`+rule("")+`}`, `"o2":null`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := arbitrateWith(t, tc.response, lx024Vendors[:2],
				ArbitrateOptions{Rules: []string{"a", "b"}, IncludeReasoning: true})
			if err == nil {
				t.Fatalf("the answer was accepted, winner %d", result.WinnerIndex)
			}
			kind := types.KindOf(err)
			if kind != types.KindSchemaViolation && kind != types.KindMalformedOutput && kind != types.KindInvariantViolation {
				t.Errorf("the error is classified %v", kind)
			}
			if !reflect.DeepEqual(result, ArbitrateResult[lx024Vendor]{}) {
				t.Errorf("a refused arbitration returned a result: %+v", result)
			}
			if strings.Contains(err.Error(), "R&D") || strings.Contains(err.Error(), "partner") {
				t.Errorf("the error carries the model's or the caller's text: %v", err)
			}
		})
	}
}

// What is sent: one fixed set of instructions with nothing of the caller's in
// it; the rules and the options in the message, each under a key; and a schema
// that requires an answer for every option on every rule. The weights, the
// tiebreaker and whether every rule is required are this library's to apply,
// and the model is told none of them.
func TestArbitrateRequest(t *testing.T) {
	cases := []struct {
		name      string
		opts      ArbitrateOptions
		options   int
		rules     []string
		reasoning bool
	}{
		{"two options, one rule", ArbitrateOptions{Rules: []string{"is cheap"}, IncludeReasoning: true}, 2, []string{"r1"}, true},
		{"three options, two rules", ArbitrateOptions{Rules: []string{"is cheap", "is reliable"}, IncludeReasoning: true}, 3, []string{"r1", "r2"}, true},
		{"no reasoning asked for", ArbitrateOptions{Rules: []string{"is cheap"}}, 2, []string{"r1"}, false},
		{"weights and a tiebreaker", ArbitrateOptions{Rules: []string{"is cheap", "is reliable"}, Weights: []float64{0.37, 2}, Tiebreaker: "first", IncludeReasoning: true}, 2, []string{"r1", "r2"}, true},
		{"every rule required", ArbitrateOptions{Rules: []string{"is cheap"}, RequireAllRules: true, IncludeReasoning: true}, 2, []string{"r1"}, true},
		{"guidance", ArbitrateOptions{Rules: []string{"is cheap"}, Steering: "Prices are in <USD> & exclude tax", IncludeReasoning: true}, 2, []string{"r1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scores := make([][]float64, tc.options)
			for i := range scores {
				scores[i] = make([]float64, len(tc.rules))
				for j := range scores[i] {
					scores[i][j] = 0.1 * float64(i+j+1)
				}
			}
			tc.opts.RequestID, tc.opts.CorrelationID = "req-1", "corr-1"
			_, sent, err := arbitrateWith(t, lx024Answer("0.8", scores...), lx024Vendors[:tc.options], tc.opts)
			if err != nil {
				t.Fatalf("Arbitrate: %v", err)
			}
			if sent.calls != 1 {
				t.Fatalf("%d calls, want 1", sent.calls)
			}

			if sent.system != arbitrateSystemPrompt {
				t.Error("the instructions are not the one fixed prompt")
			}
			for _, theirs := range []string{"is cheap", "is reliable", "Acme", "R&D", "0.37", "first", "<USD>", "RequireAllRules", "weight"} {
				if strings.Contains(sent.system, theirs) {
					t.Errorf("the instructions carry %q", theirs)
				}
			}
			for _, kept := range []string{"0.37", "tiebreak", "Tiebreaker", "disqualif", "weight"} {
				if strings.Contains(sent.user, kept) {
					t.Errorf("the model is told %q, which is this library's to apply", kept)
				}
			}
			for i, rule := range tc.opts.Rules {
				if want := "r" + strconv.Itoa(i+1) + `: "` + rule + `"`; !strings.Contains(sent.user, want) {
					t.Errorf("the message does not carry %s:\n%s", want, sent.user)
				}
			}
			for i := range tc.options {
				if want := "o" + strconv.Itoa(i+1) + `: {"name":"` + lx024Vendors[i].Name + `"`; !strings.Contains(sent.user, want) {
					t.Errorf("the message does not carry %s:\n%s", want, sent.user)
				}
			}
			if !strings.Contains(sent.user, "R&D <partner>") {
				t.Errorf("the option's own punctuation was escaped:\n%s", sent.user)
			}
			if tc.opts.Steering != "" && !strings.Contains(sent.user, `"Prices are in <USD> & exclude tax"`) {
				t.Errorf("the guidance is not in the message as written:\n%s", sent.user)
			}
			if sent.opts.RequestID != "req-1" || sent.opts.CorrelationID != "corr-1" {
				t.Errorf("request id %q, correlation id %q", sent.opts.RequestID, sent.opts.CorrelationID)
			}

			schema := sent.opts.JSONSchema
			if schema == nil || sent.opts.SchemaName != "arbitration" || sent.opts.ResponseFormat != "json" {
				t.Fatalf("schema %v, name %q, format %q", schema != nil, sent.opts.SchemaName, sent.opts.ResponseFormat)
			}
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
			}
			options := schema["properties"].(map[string]any)["options"].(map[string]any)
			wantOptions := make([]string, tc.options)
			for i := range wantOptions {
				wantOptions[i] = "o" + strconv.Itoa(i+1)
			}
			if got := options["required"]; !reflect.DeepEqual(got, wantOptions) {
				t.Errorf("the schema requires options %v, want %v", got, wantOptions)
			}
			first := options["properties"].(map[string]any)["o1"].(map[string]any)
			if got := first["required"]; !reflect.DeepEqual(got, tc.rules) {
				t.Errorf("the schema requires rules %v, want %v", got, tc.rules)
			}
			rule := first["properties"].(map[string]any)["r1"].(map[string]any)["properties"].(map[string]any)
			if _, asked := rule["reasoning"]; asked != tc.reasoning {
				t.Errorf("reasoning asked for = %v, want %v", asked, tc.reasoning)
			}
		})
	}
}

// Options that cannot mean anything are refused before a call is made.
func TestArbitrateRefusesOptionsThatCannotMeanAnything(t *testing.T) {
	rules := []string{"is cheap", "is reliable"}
	cases := []struct {
		name    string
		options int
		opts    ArbitrateOptions
	}{
		{"no options", 0, ArbitrateOptions{Rules: rules}},
		{"no rules", 2, ArbitrateOptions{}},
		{"a blank rule", 2, ArbitrateOptions{Rules: []string{"is cheap", "  "}}},
		{"more weights than rules", 2, ArbitrateOptions{Rules: rules, Weights: []float64{1, 2, 3}}},
		{"a negative weight", 2, ArbitrateOptions{Rules: rules, Weights: []float64{1, -1}}},
		{"a weight that is not a number", 2, ArbitrateOptions{Rules: rules, Weights: []float64{math.NaN(), 1}}},
		{"an infinite weight", 2, ArbitrateOptions{Rules: rules, Weights: []float64{math.Inf(1), 1}}},
		{"every weight zero", 2, ArbitrateOptions{Rules: rules, Weights: []float64{0, 0}}},
		{"a tiebreaker that is not one", 2, ArbitrateOptions{Rules: rules, Tiebreaker: "coin-toss"}},
		{"a tiebreaker in other capitals", 2, ArbitrateOptions{Rules: rules, Tiebreaker: "First"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, sent, err := arbitrateWith(t, lx024Answer("0.8", []float64{0.5, 0.5}, []float64{0.5, 0.5}), lx024Vendors[:tc.options], tc.opts)
			if err == nil {
				t.Fatalf("accepted, winner %d", result.WinnerIndex)
			}
			if sent.calls != 0 {
				t.Errorf("%d calls were made for options that were refused", sent.calls)
			}
			if kind := types.KindOf(err); kind != types.KindConfiguration && kind != types.KindInvalidRequest {
				t.Errorf("the error is classified %v", kind)
			}
			if !reflect.DeepEqual(result, ArbitrateResult[lx024Vendor]{}) {
				t.Errorf("a refused arbitration returned a result: %+v", result)
			}
		})
	}
}

// One option. With nothing required of it, it wins without a call, and nothing
// is made up about it: it used to come back with a score of 1.0 and a model
// confidence of 1.0 that no model gave. When every rule is required it is
// evaluated like any other, because it can fail.
func TestArbitrateWithOneOption(t *testing.T) {
	one := lx024Vendors[:1]

	t.Run("nothing required of it", func(t *testing.T) {
		result, sent, err := arbitrateWith(t, `not used`, one, ArbitrateOptions{Rules: []string{"is cheap"}})
		if err != nil {
			t.Fatalf("Arbitrate: %v", err)
		}
		if sent.calls != 0 {
			t.Errorf("%d calls for a choice of one", sent.calls)
		}
		if result.WinnerIndex != 0 || result.Winner != one[0] {
			t.Errorf("winner %d", result.WinnerIndex)
		}
		if len(result.Scores) != 0 || result.ModelConfidence != 0 || len(result.Evaluations) != 0 {
			t.Errorf("figures no model gave: scores %v, confidence %v, %d evaluations", result.Scores, result.ModelConfidence, len(result.Evaluations))
		}
		if evaluated, _ := result.Metadata["evaluated"].(bool); evaluated {
			t.Error("Metadata says the option was evaluated")
		}
	})

	t.Run("every rule required, and it meets them", func(t *testing.T) {
		result, sent, err := arbitrateWith(t, lx024Answer("0.7", []float64{0.9}), one, ArbitrateOptions{Rules: []string{"is cheap"}, RequireAllRules: true, IncludeReasoning: true})
		if err != nil || sent.calls != 1 {
			t.Fatalf("err %v, %d calls", err, sent.calls)
		}
		if result.WinnerIndex != 0 || result.Scores[0] != 0.9 || result.ModelConfidence != 0.7 {
			t.Errorf("winner %d, scores %v, confidence %v", result.WinnerIndex, result.Scores, result.ModelConfidence)
		}
	})

	t.Run("every rule required, and it fails one", func(t *testing.T) {
		_, sent, err := arbitrateWith(t, lx024Answer("0.7", []float64{-0.9}), one, ArbitrateOptions{Rules: []string{"is cheap"}, RequireAllRules: true, IncludeReasoning: true})
		if !errors.Is(err, types.ErrReviewRequired) || sent.calls != 1 {
			t.Fatalf("err %v, %d calls; want review required after one call", err, sent.calls)
		}
	})
}

func TestArbitrateRefusesAnAnswerWithNoReasoning(t *testing.T) {
	good := lx024Answer("0.8", []float64{0.9}, []float64{0.4})
	withoutReasoning := strings.Replace(good, `"reasoning":"overall",`, "", 1)
	if withoutReasoning == good {
		t.Fatal("the scripted answer has no top-level reasoning to remove")
	}
	nullReasoning := strings.Replace(good, `"reasoning":"overall"`, `"reasoning":null`, 1)

	for name, response := range map[string]string{"missing": withoutReasoning, "null": nullReasoning} {
		result, _, err := arbitrateWith(t, response, lx024Vendors[:2], ArbitrateOptions{Rules: []string{"is cheap"}})
		if err == nil {
			t.Errorf("%s: an answer with no reasoning was accepted: %+v", name, result)
			continue
		}
		if types.KindOf(err) != types.KindSchemaViolation {
			t.Errorf("%s: kind = %v", name, types.KindOf(err))
		}
		if result.WinnerIndex != 0 || result.Scores != nil || result.Evaluations != nil {
			t.Errorf("%s: a result came back beside the error: %+v", name, result)
		}
	}

	// An empty reasoning is a reasoning that was given.
	empty := strings.Replace(good, `"reasoning":"overall"`, `"reasoning":""`, 1)
	if _, _, err := arbitrateWith(t, empty, lx024Vendors[:2], ArbitrateOptions{Rules: []string{"is cheap"}}); err != nil {
		t.Errorf("an empty reasoning was refused: %v", err)
	}
}

func TestArbitrateRefusesASecondOptionsValue(t *testing.T) {
	sent := &arbitrateCall{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		sent.calls++
		return "{}", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	rules := ArbitrateOptions{Rules: []string{"is cheap"}}
	result, err := Arbitrate(lx024Vendors[:2], rules, ArbitrateOptions{Rules: []string{"is fast"}})
	if err == nil {
		t.Fatalf("a second options value was ignored: %+v", result)
	}
	if types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
		t.Errorf("kind %v, %d calls: %v", types.KindOf(err), sent.calls, err)
	}
}

// Passed and Score are two claims about one fact, and neither is checked against
// the other: Passed decides whether an option is disqualified when every rule is
// required, Score decides its total. A rule can be failed and scored well, or
// passed and scored zero, and both are used as reported. This pins that as the
// rule, so a change to it is a decision and not an accident.
func TestArbitratePassedAndScoreAreIndependentClaims(t *testing.T) {
	answer := func(passed1 string, score1 string) string {
		return `{"options":{"o1":{"r1":{"passed":` + passed1 + `,"score":` + score1 + `,"reasoning":"x"}},"o2":{"r1":{"passed":true,"score":0.5,"reasoning":"x"}}},"reasoning":"r","confidence":0.8}`
	}
	opts := ArbitrateOptions{Rules: []string{"is cheap"}, RequireAllRules: true, IncludeReasoning: true}

	// Failed with a high score: disqualified whatever the score says, and the
	// score still counts toward its total.
	result, _, err := arbitrateWith(t, answer("false", "0.95"), lx024Vendors[:2], opts)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if result.WinnerIndex != 1 || !result.Evaluations[0].Disqualified || result.Evaluations[0].TotalScore != 0.95 {
		t.Errorf("winner %d, evaluation %+v: a failed rule disqualifies and the score still counts", result.WinnerIndex, result.Evaluations[0])
	}

	// Passed with a score of zero: not disqualified, and loses on its total.
	result, _, err = arbitrateWith(t, answer("true", "0"), lx024Vendors[:2], opts)
	if err != nil {
		t.Fatalf("Arbitrate: %v", err)
	}
	if result.Evaluations[0].Disqualified || result.WinnerIndex != 1 || result.Evaluations[0].TotalScore != 0 {
		t.Errorf("winner %d, evaluation %+v", result.WinnerIndex, result.Evaluations[0])
	}
}

// The verdict of Arbitrate is read member by member. An answer that leaves out
// any one of what it asks for is refused with nothing beside the error: not a
// winner chosen from a zero score, not a rule read as failed or passed.
func TestArbitrateRefusesAnAnswerMissingAnyMemberItAsksFor(t *testing.T) {
	rules := []string{"is cheap", "is reliable"}
	opts := ArbitrateOptions{Rules: rules, IncludeReasoning: true}
	complete := lx024Answer("0.8", []float64{0.9, 0.3}, []float64{0.5, 0.5})

	t.Run("the members of the answer", func(t *testing.T) {
		members := [][]any{{"options"}, {"reasoning"}, {"confidence"}}
		refuseEachMissingMember(t, complete, members, func(response string) (any, error) {
			result, _, err := arbitrateWith(t, response, lx024Vendors[:2], opts)
			return result, err
		})
	})

	// An evaluation is found by position under a key, so a member of it is named
	// by option and rule.
	t.Run("the members of an evaluation", func(t *testing.T) {
		for _, option := range []string{"o1", "o2"} {
			for _, rule := range []string{"r1", "r2"} {
				for _, member := range []string{"passed", "score", "reasoning"} {
					response, name := answerWithout(t, complete, "options", option, rule, member)
					t.Run(name, func(t *testing.T) {
						result, _, err := arbitrateWith(t, response, lx024Vendors[:2], opts)
						if err == nil {
							t.Fatalf("ACCEPTED: the answer without %s was read as if the model had said nothing there: %+v", name, result)
						}
						wantKind(t, err, types.KindSchemaViolation)
						if !reflect.ValueOf(result).IsZero() {
							t.Errorf("a result beside the error: %+v", result)
						}
					})
				}
			}
		}
	})

	t.Run("an option and a rule", func(t *testing.T) {
		for _, path := range [][]any{{"options", "o1"}, {"options", "o2"}, {"options", "o1", "r1"}, {"options", "o2", "r2"}} {
			response, name := answerWithout(t, complete, path...)
			t.Run(name, func(t *testing.T) {
				result, _, err := arbitrateWith(t, response, lx024Vendors[:2], opts)
				wantKind(t, err, types.KindSchemaViolation)
				if !reflect.ValueOf(result).IsZero() {
					t.Errorf("a result beside the error: %+v", result)
				}
			})
		}
	})
}

// The reason of an evaluation is asked for only when IncludeReasoning is set, and
// then it is the one member of an evaluation that may be left out.
func TestArbitrateReadsAnEvaluationWithoutAReasonWhenNoneWasAskedFor(t *testing.T) {
	response, _ := answerWithout(t, lx024Answer("0.8", []float64{0.9}, []float64{0.5}), "options", "o1", "r1", "reasoning")
	result, _, err := arbitrateWith(t, response, lx024Vendors[:2], ArbitrateOptions{Rules: []string{"is cheap"}})
	if err != nil || result.WinnerIndex != 0 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if _, _, err := arbitrateWith(t, response, lx024Vendors[:2], ArbitrateOptions{Rules: []string{"is cheap"}, IncludeReasoning: true}); err == nil {
		t.Error("an evaluation with no reason was accepted although reasons were asked for")
	}
}

func TestArbitrateRefusesANullWhereNothingCanBeNull(t *testing.T) {
	opts := ArbitrateOptions{Rules: []string{"is cheap"}, IncludeReasoning: true}
	good := lx024Answer("0.8", []float64{0.9}, []float64{0.5})
	for name, body := range map[string]string{
		"reasoning":  strings.Replace(good, `"reasoning":"overall"`, `"reasoning":null`, 1),
		"confidence": strings.Replace(good, `"confidence":0.8`, `"confidence":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			result, _, err := arbitrateWith(t, body, lx024Vendors[:2], opts)
			wantKind(t, err, types.KindSchemaViolation)
			if !strings.Contains(err.Error(), name+" is null") || !reflect.ValueOf(result).IsZero() {
				t.Errorf("result = %+v, err = %v", result, err)
			}
		})
	}
}
