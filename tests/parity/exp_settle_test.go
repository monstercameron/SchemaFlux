package parity

import (
	"context"
	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
	"math"
	"testing"
)

type settleParityTarget struct {
	Answer string `json:"answer"`
}

const expSettleAnswer = `{"solution":{"answer":"ok"},"satisfaction":{"time":0.8},"overall_satisfaction":0.8,"tradeoffs":[],"alternatives":[],"reasoning":"balanced","confidence":0.9}`

func expSettleCase(name string, old oldroot.NegotiateOptions, v2 exp.SettleOptions) parityCase {
	return parityCase{name: "Settle/" + name, op: "exp.settle", replies: []string{expSettleAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.Settle[settleParityTarget]("deadline", old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := exp.Settle[settleParityTarget](ctx, "deadline", v2)
			return err
		}}
}

func TestExpSettleSendsTheRequestTheOldRootSends(t *testing.T) {
	cases := []parityCase{expSettleCase("default", oldroot.NegotiateOptions{}, exp.SettleOptions{}), expSettleCase("priorities", oldroot.NegotiateOptions{Priorities: map[string]float64{"time": 0.8}}, exp.SettleOptions{Priorities: map[string]float64{"time": 0.8}}), expSettleCase("constraints", oldroot.NegotiateOptions{Constraints: []string{"deadline"}}, exp.SettleOptions{Constraints: []string{"deadline"}}), expSettleCase("minimum", oldroot.NegotiateOptions{MinSatisfaction: 0.7}, exp.SettleOptions{MinSatisfaction: 0.7}), expSettleCase("no minimum floor", oldroot.NegotiateOptions{MinSatisfaction: math.SmallestNonzeroFloat64}, exp.SettleOptions{MinSatisfaction: -1}), expSettleCase("alternatives", oldroot.NegotiateOptions{MaxAlternatives: 2}, exp.SettleOptions{MaxAlternatives: 2}), expSettleCase("strategy", oldroot.NegotiateOptions{Strategy: "pareto"}, exp.SettleOptions{Strategy: "pareto"}), expSettleCase("model", oldroot.NegotiateOptions{Model: "parity-model"}, exp.SettleOptions{Options: schemaflux.Options{Model: "parity-model"}}), expSettleCase("tier", oldroot.NegotiateOptions{Intelligence: oldroot.Fast}, exp.SettleOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}), expSettleCase("effort", oldroot.NegotiateOptions{Effort: oldroot.EffortLow}, exp.SettleOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}), expSettleCase("mode", oldroot.NegotiateOptions{Mode: oldroot.TransformMode}, exp.SettleOptions{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}), expSettleCase("steering", oldroot.NegotiateOptions{Steering: "carefully"}, exp.SettleOptions{Options: schemaflux.Options{Steering: "carefully"}}), expSettleCase("max tokens", oldroot.NegotiateOptions{MaxOutputTokens: 256}, exp.SettleOptions{Options: schemaflux.Options{MaxOutputTokens: 256}}), expSettleCase("web search", oldroot.NegotiateOptions{WebSearch: true}, exp.SettleOptions{Options: schemaflux.Options{WebSearch: true}})}
	runParity(t, cases)
}

const expSettleAdversarialAnswer = `{"deal":{"answer":"ok"},"deal_reached":true,"term_movements":[],"who_conceded_more":"neither","our_satisfaction":0.8,"their_satisfaction":0.7,"reasoning":"balanced","confidence":0.9}`

func expSettleAdversarialCase(name string, old oldroot.AdversarialOptions, v2 exp.SettleAdversarialOptions) parityCase {
	party := oldroot.AdversarialContext[settleParityTarget]{Ours: oldroot.AdversarialPosition[settleParityTarget]{Position: settleParityTarget{"ours"}}, Theirs: oldroot.AdversarialPosition[settleParityTarget]{Position: settleParityTarget{"theirs"}}, OurLeverage: "balanced"}
	ours := exp.AdversarialContext[settleParityTarget]{Ours: exp.AdversarialPosition[settleParityTarget]{Position: settleParityTarget{"ours"}}, Theirs: exp.AdversarialPosition[settleParityTarget]{Position: settleParityTarget{"theirs"}}, OurLeverage: "balanced"}
	return parityCase{name: "SettleAdversarial/" + name, op: "exp.settleadversarial", replies: []string{expSettleAdversarialAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.Context = ctx
			_, err := oldroot.SettleAdversarial[settleParityTarget](party, old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := exp.SettleAdversarial(ctx, ours, v2); return err }}
}

func TestExpSettleAdversarialSendsTheRequestTheOldRootSends(t *testing.T) {
	cases := []parityCase{
		expSettleAdversarialCase("default", oldroot.AdversarialOptions{}, exp.SettleAdversarialOptions{}),
		expSettleAdversarialCase("strategy", oldroot.AdversarialOptions{Strategy: "aggressive"}, exp.SettleAdversarialOptions{Strategy: "aggressive"}),
		expSettleAdversarialCase("model", oldroot.AdversarialOptions{Model: "parity-model"}, exp.SettleAdversarialOptions{Options: schemaflux.Options{Model: "parity-model"}}),
		expSettleAdversarialCase("tier", oldroot.AdversarialOptions{Intelligence: oldroot.Fast}, exp.SettleAdversarialOptions{Options: schemaflux.Options{Tier: schemaflux.TierFast}}),
		expSettleAdversarialCase("effort", oldroot.AdversarialOptions{Effort: oldroot.EffortLow}, exp.SettleAdversarialOptions{Options: schemaflux.Options{Effort: schemaflux.EffortLow}}),
		expSettleAdversarialCase("mode", oldroot.AdversarialOptions{Mode: oldroot.TransformMode}, exp.SettleAdversarialOptions{Options: schemaflux.Options{Mode: schemaflux.ModeTransform}}),
		expSettleAdversarialCase("steering", oldroot.AdversarialOptions{Steering: "carefully"}, exp.SettleAdversarialOptions{Options: schemaflux.Options{Steering: "carefully"}}),
		expSettleAdversarialCase("max tokens", oldroot.AdversarialOptions{MaxOutputTokens: 256}, exp.SettleAdversarialOptions{Options: schemaflux.Options{MaxOutputTokens: 256}}),
		expSettleAdversarialCase("web search", oldroot.AdversarialOptions{WebSearch: true}, exp.SettleAdversarialOptions{Options: schemaflux.Options{WebSearch: true}}),
	}
	runParity(t, cases)
}
