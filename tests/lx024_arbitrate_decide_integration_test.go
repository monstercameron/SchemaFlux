package tests

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type lx024Candidate struct {
	Name  string `json:"name"`
	Years int    `json:"years"`
	City  string `json:"city"`
}

var lx024Candidates = []lx024Candidate{
	{Name: "Ada", Years: 6, City: "Lisbon"},
	{Name: "Bo", Years: 2, City: "Porto"},
}

func lx024Rule(passed bool, score string) string {
	word := "false"
	if passed {
		word = "true"
	}
	return `{"passed":` + word + `,"score":` + score + `,"reasoning":"because"}`
}

func lx024Evaluations(first, second [2]string) string {
	return `{"options":{"o1":{"r1":` + first[0] + `,"r2":` + first[1] + `},"o2":{"r1":` + second[0] + `,"r2":` + second[1] + `}},"reasoning":"overall","confidence":0.8}`
}

// LX-024 through the exported API and the real OpenAI provider. The model says
// how each option does on each rule; the winner is worked out here from the
// caller's weights, disqualifications and tiebreaker; and an answer with no
// evaluation in it is an error, where it used to select the first option.
func TestIntegrationArbitrateWorksOutTheWinner(t *testing.T) {
	rules := []string{"has three or more years of experience", "is based in Porto"}
	yes, no := true, false

	cases := []struct {
		name    string
		opts    schemaflux.ArbitrateOptions
		body    string
		wantErr error
		winner  int
		scores  [2]float64
	}{
		{"equal weights", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "0.9"), lx024Rule(no, "0.1")}, [2]string{lx024Rule(no, "0.2"), lx024Rule(yes, "1")}),
			nil, 1, [2]float64{0.5, 0.6}},
		{"the caller's weights favour experience", schemaflux.ArbitrateOptions{Rules: rules, Weights: []float64{3, 1}, IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "0.9"), lx024Rule(no, "0.1")}, [2]string{lx024Rule(no, "0.2"), lx024Rule(yes, "1")}),
			nil, 0, [2]float64{0.7, 0.4}},
		{"every rule required: the one that fails none wins", schemaflux.ArbitrateOptions{Rules: rules, RequireAllRules: true, IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "0.9"), lx024Rule(no, "0.1")}, [2]string{lx024Rule(yes, "0.5"), lx024Rule(yes, "0.5")}),
			nil, 1, [2]float64{0.5, 0.5}},
		{"every rule required and nobody meets them all", schemaflux.ArbitrateOptions{Rules: rules, RequireAllRules: true, IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "0.9"), lx024Rule(no, "0.1")}, [2]string{lx024Rule(no, "0.2"), lx024Rule(yes, "1")}),
			schemaflux.ErrReviewRequired, 0, [2]float64{}},
		{"a tie goes to the first when asked", schemaflux.ArbitrateOptions{Rules: rules, Tiebreaker: "first", IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "0.5"), lx024Rule(yes, "0.5")}, [2]string{lx024Rule(yes, "0.5"), lx024Rule(yes, "0.5")}),
			nil, 0, [2]float64{0.5, 0.5}},
		{"the old answer, a winner and no evaluation", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			`{"winner_index":1,"reasoning":"x","confidence":0.9}`, schemaflux.ErrSchemaViolation, 0, [2]float64{}},
		{"an answer that leaves an option out", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			`{"options":{"o1":{"r1":` + lx024Rule(yes, "0.9") + `,"r2":` + lx024Rule(yes, "0.9") + `}},"reasoning":"x","confidence":0.9}`,
			schemaflux.ErrSchemaViolation, 0, [2]float64{}},
		{"an answer that leaves a rule out", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			`{"options":{"o1":{"r1":` + lx024Rule(yes, "0.9") + `},"o2":{"r1":` + lx024Rule(yes, "0.9") + `}},"reasoning":"x","confidence":0.9}`,
			schemaflux.ErrSchemaViolation, 0, [2]float64{}},
		{"a score off the scale", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			lx024Evaluations([2]string{lx024Rule(yes, "9"), lx024Rule(yes, "0.5")}, [2]string{lx024Rule(yes, "0.5"), lx024Rule(yes, "0.5")}),
			schemaflux.ErrInvariantViolation, 0, [2]float64{}},
		{"an empty answer", schemaflux.ArbitrateOptions{Rules: rules, IncludeReasoning: true},
			`{}`, schemaflux.ErrSchemaViolation, 0, [2]float64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.Arbitrate(lx024Candidates, tc.opts)
			if len(*captured) != 1 {
				t.Fatalf("%d requests reached the provider, want 1", len(*captured))
			}

			// On the wire: a strict schema, and instructions with nothing of
			// the caller's in them.
			body := (*captured)[0]
			text, _ := body["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "arbitration" {
				t.Fatalf("expected the strict arbitration schema on the wire, got %v", format)
			}
			schema, _ := format["schema"].(map[string]any)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
			}
			instructions, _ := body["instructions"].(string)
			for _, theirs := range []string{rules[0], rules[1], "Ada", "Lisbon"} {
				if strings.Contains(instructions, theirs) {
					t.Errorf("the instructions carry the caller's %q", theirs)
				}
			}

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if result.Winner != (lx024Candidate{}) || len(result.Scores) != 0 || len(result.Evaluations) != 0 {
					t.Fatalf("a failed arbitration returned a usable result: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("Arbitrate: %v", err)
			}
			if result.WinnerIndex != tc.winner || result.Winner != lx024Candidates[tc.winner] {
				t.Errorf("winner %d (%+v), want %d", result.WinnerIndex, result.Winner, tc.winner)
			}
			for i, want := range tc.scores {
				if math.Abs(result.Scores[i]-want) > 1e-9 {
					t.Errorf("Scores[%d] = %v, want %v", i, result.Scores[i], want)
				}
			}
		})
	}
}

// LX-024, Decide through the exported API. An answer that selects nothing is
// an error and not the first branch.
func TestIntegrationDecideDoesNotReadAMissingSelectionAsTheFirstBranch(t *testing.T) {
	branches := []schemaflux.Decision[string]{
		{Value: "technical", Description: "Technical support"},
		{Value: "billing", Description: "Billing support"},
		{Value: "success", Description: "Customer success"},
	}
	cases := []struct {
		name     string
		body     string
		wantErr  bool
		selected int
	}{
		{"a selection", `{"selected":1,"explanation":"a refund","confidence":0.9,"alternatives":[]}`, false, 1},
		{"the first branch, said outright", `{"selected":0,"explanation":"an outage","confidence":0.9,"alternatives":[2]}`, false, 0},
		{"no selection at all", `{"explanation":"a refund","confidence":0.9,"alternatives":[]}`, true, -1},
		{"a null selection", `{"selected":null,"explanation":"x","confidence":0.9,"alternatives":[]}`, true, -1},
		{"a selection past the end", `{"selected":3,"explanation":"x","confidence":0.9,"alternatives":[]}`, true, -1},
		{"no confidence", `{"selected":1,"explanation":"x","alternatives":[]}`, true, -1},
		{"an empty answer", `{}`, true, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			value, result, err := schemaflux.Decide(context.Background(), "I was charged twice", branches)
			if len(*captured) != 1 {
				t.Fatalf("%d requests reached the provider, want 1", len(*captured))
			}
			text, _ := (*captured)[0]["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["name"] != "decision" {
				t.Fatalf("expected the decision schema on the wire, got %v", format)
			}

			if tc.wantErr {
				if err == nil {
					t.Fatalf("the answer was accepted: %q at %d", value, result.SelectedIndex)
				}
				if value != "" || result.SelectedIndex != -1 {
					t.Errorf("a refused decision returned %q at %d", value, result.SelectedIndex)
				}
				return
			}
			if err != nil || result.SelectedIndex != tc.selected || value != branches[tc.selected].Value {
				t.Fatalf("value %q at %d, err %v; want branch %d", value, result.SelectedIndex, err, tc.selected)
			}
		})
	}
}

// ExampleArbitrate chooses between two candidates on two rules, with experience
// counting three times as much as location. The model says how each candidate
// does on each rule; the weights are applied here, so the totals are exact. It
// runs under go test with a scripted provider: no credential, no spend.
func ExampleArbitrate() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"options": {
		    "o1": {"r1": {"passed": true,  "score": 0.9, "reasoning": "Six years."},
		           "r2": {"passed": false, "score": 0.1, "reasoning": "Based in Lisbon."}},
		    "o2": {"r1": {"passed": false, "score": 0.2, "reasoning": "Two years."},
		           "r2": {"passed": true,  "score": 1.0, "reasoning": "Based in Porto."}}},
		  "reasoning": "Ada has the experience and Bo has the location.",
		  "confidence": 0.85}`))

	type candidate struct {
		Name  string `json:"name"`
		Years int    `json:"years"`
		City  string `json:"city"`
	}
	result, err := schemaflux.Arbitrate(
		[]candidate{{"Ada", 6, "Lisbon"}, {"Bo", 2, "Porto"}},
		schemaflux.ArbitrateOptions{
			Rules:            []string{"has three or more years of experience", "is based in Porto"},
			Weights:          []float64{3, 1},
			IncludeReasoning: true,
		})
	if err != nil {
		fmt.Println("the arbitration could not be made:", err)
		return
	}

	fmt.Println("winner:", result.Winner.Name)
	for _, evaluation := range result.Evaluations {
		fmt.Printf("option %d scored %.2f\n", evaluation.Index, evaluation.TotalScore)
	}

	// Output:
	// winner: Ada
	// option 0 scored 0.70
	// option 1 scored 0.40
}

// ExampleDecide routes a ticket to a team. An answer that selects nothing would
// be an error, and never the first team. It runs under go test with a scripted
// provider: no credential, no spend.
func ExampleDecide() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"selected": 1, "explanation": "The customer was charged twice.", "confidence": 0.92, "alternatives": []}`))

	team, decision, err := schemaflux.Decide(context.Background(), "I was charged twice for my subscription",
		[]schemaflux.Decision[string]{
			{Value: "technical", Description: "Technical support"},
			{Value: "billing", Description: "Billing support"},
		})
	if err != nil {
		fmt.Println("the ticket could not be routed:", err)
		return
	}
	fmt.Printf("%s: %s\n", team, decision.Explanation)

	// Output:
	// billing: The customer was charged twice.
}
