package ops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 12, Decide, Arbitrate and the subject of a decision.

type lx12Hidden struct {
	Secret string `json:"-"`
	Token  string `json:"-"`
}

type lx12PartlyHidden struct {
	Secret  string `json:"-"`
	Subject string `json:"subject"`
}

// lx12Named has nothing JSON can see and says what it is.
type lx12Named struct{ name string }

func (n lx12Named) String() string { return "the " + n.name + " plan" }

type lx12DashName struct {
	Value string `json:"-,"`
}

// A subject is shown to the model as what it says. An error says its message,
// and was being written as JSON, where it is an empty object or, for one with
// exported fields, an object with the cause blanked: the model was shown
// {"Op":"Get","URL":"...","Err":{}} for a timeout. A value with nothing JSON
// can see is shown as its String when it has one, and is refused when it has
// not.
func TestASubjectIsShownAsWhatItSays(t *testing.T) {
	wrapped := fmt.Errorf("charging the card: %w", errors.New("declined by the bank"))
	cases := []struct {
		name    string
		subject any
		want    string // what the message holds
		refused bool
	}{
		{"text", "a ticket about a refund", `"a ticket about a refund"`, false},
		{"an error", errors.New("the card was declined"), `"the card was declined"`, false},
		{"a wrapped error", wrapped, `"charging the card: declined by the bank"`, false},
		{"an error with exported fields", &url.Error{Op: "Get", URL: "https://pay.example", Err: errors.New("timeout")}, `timeout`, false},
		{"a classified error", types.Newf(types.KindTimeout, "charge", "the provider took too long"), `the provider took too long`, false},
		{"a struct with exported fields", lx11Named{Subject: "refund"}, `{"subject":"refund"}`, false},
		{"a struct that is partly hidden", lx12PartlyHidden{Secret: "s3cret", Subject: "refund"}, `{"subject":"refund"}`, false},
		{"a struct whose fields are all json:\"-\"", lx12Hidden{Secret: "s3cret"}, "", true},
		{"a pointer to one", &lx12Hidden{Secret: "s3cret"}, "", true},
		{"a struct with nothing exported", lx11Opaque{secret: "s3cret"}, "", true},
		{"one with nothing exported that names itself", lx12Named{name: "premium"}, `"the premium plan"`, false},
		{"a field named dash", lx12DashName{Value: "kept"}, `{"-":"kept"}`, false},
		{"a type that writes itself as text", lx12ID{1, 2, 3, 4}, `"01020304"`, false},
		{"a type that writes itself as JSON", lx11Marshals{text: "refund"}, `"refund"`, false},
		{"a map", map[string]int{"open": 3}, `{"open":3}`, false},
		{"nil", nil, `null`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent := answering(t, `{"q1":true}`)
			_, err := Is(context.Background(), tc.subject, "Is this about a payment?")
			if tc.refused {
				if types.KindOf(err) != types.KindInvalidRequest || sent.calls != 0 {
					t.Fatalf("err %v after %d calls; want an invalid-request error before any", err, sent.calls)
				}
				if strings.Contains(err.Error(), "s3cret") {
					t.Errorf("the error carries the subject: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Is: %v", err)
			}
			if !strings.Contains(sent.user, tc.want) {
				t.Errorf("the message does not hold %s:\n%s", tc.want, sent.user)
			}
			if strings.Contains(sent.user, "s3cret") {
				t.Errorf("a field marked json:\"-\" reached the model:\n%s", sent.user)
			}

			// Decide shows its situation the same way.
			sent = answering(t, `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`)
			if _, _, err := Decide(context.Background(), tc.subject, routes()); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if !strings.Contains(sent.user, tc.want) {
				t.Errorf("Decide's message does not hold %s:\n%s", tc.want, sent.user)
			}
		})
	}

	// Refused by Decide too, with or without a fallback.
	for _, opts := range []DecideOptions{NewDecideOptions(), NewDecideOptions().WithFallback(0)} {
		sent := answering(t, `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`)
		_, result, err := Decide(context.Background(), lx12Hidden{Secret: "s3cret"}, routes(), opts)
		if types.KindOf(err) != types.KindInvalidRequest || sent.calls != 0 || result.Fallback {
			t.Errorf("err %v after %d calls, fallback %v", err, sent.calls, result.Fallback)
		}
	}
}

// The bound on what an arbitration can declare is the provider's documented
// ceiling, 5,000 properties. It was half that, which refused arbitrations the
// version before this accepted (a hundred options against eight rules). The
// boundary is tested on both sides.
func TestArbitrateBudgetBoundary(t *testing.T) {
	cases := []struct {
		name      string
		options   int
		rules     int
		reasoning bool
		refused   bool
	}{
		{"a hundred by eight", 100, 8, false, false},
		{"a hundred by six with reasons", 100, 6, true, false},
		{"fifty by twenty with reasons", 50, 20, true, false},
		{"exactly the budget", 1, 1249, true, false},
		{"four over the budget", 1, 1250, true, true},
		{"sixty-two by twenty with reasons", 62, 20, true, true},
		{"sixty-one by twenty with reasons", 61, 20, true, false},
		{"just under, without reasons", 1, 1665, false, false},
		{"just over, without reasons", 1, 1666, false, true},
		{"two thousand by one", 2000, 1, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			declared := arbitrateSchemaProperties(tc.options, tc.rules, tc.reasoning)
			if over := declared > strictPropertyBudget; over != tc.refused {
				t.Fatalf("%d declared properties against a budget of %d: the case is mislabelled", declared, strictPropertyBudget)
			}
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
			// Every rule required, so that one option is still evaluated.
			_, sent, err := arbitrateWith(t, lx024Answer("0.8", answers...), options,
				ArbitrateOptions{Rules: rules, IncludeReasoning: tc.reasoning, RequireAllRules: true, Tiebreaker: "first"})
			if tc.refused {
				if types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
					t.Errorf("err %v after %d calls; want a configuration error before any", err, sent.calls)
				}
				return
			}
			if err != nil {
				t.Errorf("an arbitration within the budget was refused: %v", err)
			}
		})
	}
}

// A long list of failed rules is cut like a long list of options: the error is
// for a person to read, and thirty options failing forty rules each is not.
func TestArbitrateFailuresAreCutPerOption(t *testing.T) {
	failing := func(rules int) OptionEvaluation {
		return OptionEvaluation{RuleResults: make([]RuleEvaluation, rules)}
	}
	cases := []struct {
		name  string
		rules int
		want  string
	}{
		{"one", 1, "option 1 failed rule 1"},
		{"two", 2, "option 1 failed rules 1, 2"},
		{"nine", 9, "option 1 failed rules 1, 2, 3, 4, 5, 6, 7, 8, 9"},
		{"ten", 10, "option 1 failed rules 1, 2, 3, 4, 5, 6, 7, 8, 9, 10"},
		{"eleven", 11, "option 1 failed rules 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 and 1 more"},
		{"twenty-five", 25, "option 1 failed rules 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 and 15 more"},
		{"a thousand", 1000, "option 1 failed rules 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 and 990 more"},
	}
	for _, tc := range cases {
		if got := arbitrateFailures([]OptionEvaluation{failing(tc.rules)}); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// At its longest: ten options, each failing a thousand rules.
	many := make([]OptionEvaluation, 30)
	for i := range many {
		many[i] = failing(1000)
	}
	if got := arbitrateFailures(many); len(got) > 900 {
		t.Errorf("the account is %d characters long", len(got))
	}
}

// An option reported on a rule that was not given is refused, as an option
// beyond those offered is.
func TestArbitrateRefusesARuleBeyondThoseGiven(t *testing.T) {
	good := `{"passed":true,"score":0.9}`
	extra := `{"options":{"o1":{"r1":` + good + `,"r2":` + good + `,"r3":` + good + `},"o2":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`
	if evaluations, _, _, err := arbitrateAnswer(extra, 2, []string{"a", "b"}, false); types.KindOf(err) != types.KindSchemaViolation {
		t.Errorf("an answer that reports on a rule nobody gave was accepted: %+v, %v", evaluations, err)
	}
	swapped := `{"options":{"o1":{"r1":` + good + `,"r3":` + good + `},"o2":{"r1":` + good + `,"r2":` + good + `}},"reasoning":"x","confidence":0.8}`
	if _, _, _, err := arbitrateAnswer(swapped, 2, []string{"a", "b"}, false); types.KindOf(err) != types.KindSchemaViolation {
		t.Errorf("an answer with a rule under the wrong key was accepted: %v", err)
	}
}

// What Decide cannot declare an answer for is refused whether or not one of
// the branches has a condition that holds: a request that is refused on one
// input and runs on another is harder to find than one that is always
// refused.
func TestDecideChecksItsSizeBeforeItsConditions(t *testing.T) {
	branches := make([]Decision[string], strictEnumBudget+1)
	for i := range branches {
		branches[i] = Decision[string]{Value: "v" + strconv.Itoa(i), Description: "branch " + strconv.Itoa(i)}
	}
	branches[0].Condition = func(any) bool { return true }
	value, result, err := Decide(context.Background(), "a ticket", branches)
	if types.KindOf(err) != types.KindConfiguration || value != "" || result.ByCondition {
		t.Errorf("value %q, result %+v, err %v; want a configuration error", value, result, err)
	}

	// Within the budget, a condition that holds is still taken with no call.
	sent := answering(t, `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`)
	value, result, err = Decide(context.Background(), "a ticket", branches[:3])
	if err != nil || value != "v0" || !result.ByCondition || sent.calls != 0 {
		t.Errorf("value %q, result %+v, err %v, %d calls", value, result, err, sent.calls)
	}
}

// A missing list of alternatives is read as none. It is the one list this
// library does not require: the decision is the selection, nothing branches on
// the alternatives, and refusing a selection for want of them would trade a
// usable decision for an error. A list that is there and is wrong is refused.
func TestDecideReadsAMissingListOfAlternativesAsNone(t *testing.T) {
	cases := []struct {
		name     string
		response string
		wantErr  bool
		others   int
	}{
		{"an empty list", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[]}`, false, 0},
		{"one alternative", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[0]}`, false, 1},
		{"two alternatives", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[2,0]}`, false, 2},
		{"missing", `{"selected":1,"explanation":"x","confidence":0.9}`, false, 0},
		{"null", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":null}`, false, 0},
		{"a number", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":0}`, true, 0},
		{"text", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":"none"}`, true, 0},
		{"an object", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":{}}`, true, 0},
		{"a list of text", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":["0"]}`, true, 0},
		{"a list with a fraction", `{"selected":1,"explanation":"x","confidence":0.9,"alternatives":[0.5]}`, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, err := readDecideAnswer(tc.response, 3)
			if tc.wantErr {
				if types.KindOf(err) != types.KindSchemaViolation {
					t.Fatalf("answer %+v, err %v", answer, err)
				}
				return
			}
			if err != nil || len(answer.alternatives) != tc.others {
				t.Fatalf("answer %+v, err %v", answer, err)
			}
		})
	}
}
