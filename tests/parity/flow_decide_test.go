package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// flow.Decide and flow.Match against the old root's Decide and Match. Each pair
// has to send the same requests, in the four states of the call-path table. The
// old root's Decide is given no fallback index: the fallback of version 2 is
// taken in Go after the call, and the request it sends is the same either way,
// which the cases that mark a decision show.

const (
	flowDecideSituation = "I was charged twice for my order."
	flowDecideAnswer    = `{"selected":1,"explanation":"a charge","confidence":0.9,"alternatives":[0]}`
	flowMatchInput      = "Please refund my order."
)

// flowSituation is a situation that is a struct, which both write as JSON.
type flowSituation struct {
	Subject string `json:"subject"`
	Urgent  bool   `json:"urgent"`
}

// flowDecisions are the decisions both roots are asked to choose among, with the
// fallback marking given to version 2's.
func flowDecisions(marked int) ([]oldroot.Decision[string], []flow.Decision[string]) {
	descriptions := []string{"a question about an invoice or a payment", "something is broken", "anything else"}
	values := []string{"billing", "technical", "general"}
	var old []oldroot.Decision[string]
	var v2 []flow.Decision[string]
	for i := range descriptions {
		old = append(old, oldroot.Decision[string]{Value: values[i], Description: descriptions[i]})
		v2 = append(v2, flow.Decision[string]{Value: values[i], Description: descriptions[i], Fallback: i == marked})
	}
	return old, v2
}

// flowDecideCase is one Decide pair.
func flowDecideCase(name string, situation any, marked int, old oldroot.DecideOptions, v2 ...schemaflux.Options) parityCase {
	oldDecisions, v2Decisions := flowDecisions(marked)
	return parityCase{
		name: "Decide/" + name, op: "flow.decide", replies: []string{flowDecideAnswer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			_, _, err := oldroot.Decide(ctx, situation, oldDecisions, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := flow.Decide(ctx, situation, v2Decisions, v2...)
			return err
		},
	}
}

func flowDecideCases() []parityCase {
	fallback := 2
	cases := []parityCase{
		flowDecideCase("the default", flowDecideSituation, -1, oldroot.DecideOptions{}),
		flowDecideCase("a struct for a situation", flowSituation{Subject: flowDecideSituation, Urgent: true}, -1, oldroot.DecideOptions{}),
		flowDecideCase("a decision marked as the fallback", flowDecideSituation, 2, oldroot.DecideOptions{Fallback: &fallback}),
		flowDecideCase("the first decision marked as the fallback", flowDecideSituation, 0, oldroot.DecideOptions{}),
		flowDecideCase("two options values", flowDecideSituation, -1,
			oldroot.DecideOptions{OpOptions: types.OpOptions{Model: pinnedModel, Steering: "answer plainly", Intelligence: types.Quick}},
			schemaflux.Options{Model: pinnedModel, Tier: schemaflux.TierSmart}, schemaflux.Options{Steering: "answer plainly", Tier: schemaflux.TierQuick}),
		flowDecideCase("a later zero leaves the earlier value", flowDecideSituation, -1,
			oldroot.DecideOptions{OpOptions: types.OpOptions{Model: pinnedModel, WebSearch: true, MaxOutputTokens: 123}},
			schemaflux.Options{Model: pinnedModel, WebSearch: true, MaxOutputTokens: 123}, schemaflux.Options{}),
		{
			name: "Decide/a condition that does not hold", op: "flow.decide", replies: []string{flowDecideAnswer}, floor: schemaflux.ContractSchemaAndInvariantChecked,
			old: func(ctx context.Context) error {
				old, _ := flowDecisions(-1)
				old[0].Condition = func(any) bool { return false }
				_, _, err := oldroot.Decide(ctx, flowDecideSituation, old)
				return err
			},
			v2: func(ctx context.Context) error {
				_, v2 := flowDecisions(-1)
				v2[0].Condition = func(any) bool { return false }
				_, err := flow.Decide(ctx, flowDecideSituation, v2)
				return err
			},
		},
	}
	for _, c := range commonCases {
		var old oldroot.DecideOptions
		c.decision(&old.OpOptions)
		cases = append(cases, flowDecideCase(c.name, flowDecideSituation, -1, old, c.v2))
	}
	return cases
}

// flowMatchCase is one Match pair. replies are what the model says to each
// string condition, in order.
func flowMatchCase(name string, input any, replies []string, cases func() []oldroot.Case, v2 func() []flow.Case) parityCase {
	return parityCase{
		name: "Match/" + name, op: "flow.match", replies: replies,
		old: func(ctx context.Context) error {
			_, err := oldroot.Match(ctx, input, cases()...)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := flow.Match(ctx, input, v2()...)
			return err
		},
	}
}

func flowMatchCases() []parityCase {
	act := func() {}
	one := func() []oldroot.Case { return []oldroot.Case{oldroot.When("a billing request", act)} }
	oneV2 := func() []flow.Case { return []flow.Case{flow.When("a billing request", act)} }
	return []parityCase{
		flowMatchCase("a string condition that holds", flowMatchInput, []string{"true"}, one, oneV2),
		flowMatchCase("a string condition that does not", flowMatchInput, []string{"false"}, one, oneV2),
		flowMatchCase("a struct for an input", flowSituation{Subject: flowMatchInput}, []string{"true"}, one, oneV2),
		flowMatchCase("two string conditions", flowMatchInput, []string{"false", "true"},
			func() []oldroot.Case {
				return []oldroot.Case{oldroot.When("a billing request", act), oldroot.When("a shipping request", act)}
			},
			func() []flow.Case {
				return []flow.Case{flow.When("a billing request", act), flow.When("a shipping request", act)}
			}),
		flowMatchCase("a default written first", flowMatchInput, []string{"false", "false"},
			func() []oldroot.Case {
				return []oldroot.Case{oldroot.Otherwise(act), oldroot.When("a billing request", act), oldroot.When("a shipping request", act)}
			},
			func() []flow.Case {
				return []flow.Case{flow.Otherwise(act), flow.When("a billing request", act), flow.When("a shipping request", act)}
			}),
		flowMatchCase("a Go condition before a string one", flowMatchInput, []string{"true"},
			func() []oldroot.Case {
				return []oldroot.Case{oldroot.When(42, act), oldroot.When("a billing request", act)}
			},
			func() []flow.Case { return []flow.Case{flow.When(42, act), flow.When("a billing request", act)} }),
		flowMatchCase("a condition that is written in awkward characters", flowMatchInput, []string{"false"},
			func() []oldroot.Case { return []oldroot.Case{oldroot.When("a \"quoted\"\nline <b>&</b>", act)} },
			func() []flow.Case { return []flow.Case{flow.When("a \"quoted\"\nline <b>&</b>", act)} }),
	}
}

// The tests are not parallel: runParity installs an observer, which is process
// state, while it runs its last group.

func TestFlowDecideSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, flowDecideCases()) }

func TestFlowMatchSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, flowMatchCases()) }
