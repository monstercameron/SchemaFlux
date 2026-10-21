package exp

import (
	"context"
	"math"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SettleOptions configures Settle. Its zero value uses balanced strategy,
// minimum satisfaction 0.6, and at most three alternatives.
type SettleOptions struct {
	schemaflux.Options
	// Priorities assigns importance weights from 0 to 1 by constraint name. Nil means equal weights.
	Priorities map[string]float64
	// Constraints adds natural language requirements. Nil means none.
	Constraints []string
	// MinSatisfaction is the minimum accepted overall satisfaction from 0 to 1. Zero means 0.6; a negative value disables the floor. A value above 1 or a non-finite value is a configuration error.
	MinSatisfaction float64
	// MaxAlternatives limits alternative solutions. Zero means 3. A negative value is a configuration error.
	MaxAlternatives int
	// Strategy guides the model with balanced, maximize_primary, or pareto. Empty means balanced.
	Strategy string
}

// SettleResult is the model's proposed solution, claimed satisfaction, tradeoffs and alternatives.
type SettleResult[T any] struct {
	// Solution is the proposed result.
	Solution T
	// Satisfaction contains the model's own satisfaction claims by constraint.
	Satisfaction map[string]float64
	// OverallSatisfaction is the model's own overall satisfaction claim; Settle enforces its minimum.
	OverallSatisfaction float64
	// Tradeoffs lists the model's reported sacrifices and gains.
	Tradeoffs []Tradeoff
	// Alternatives lists other solutions the model reported.
	Alternatives []T
	// Reasoning is the model's explanation.
	Reasoning string
	// ModelConfidence is the model's own confidence claim.
	ModelConfidence float64
}

// Tradeoff describes what the proposed solution sacrifices and gains.
type Tradeoff struct {
	// Sacrificed is what was reduced or given up.
	Sacrificed string
	// Gained is what was improved or obtained.
	Gained string
	// Impact describes the magnitude of the tradeoff.
	Impact string
	// Reasoning explains the tradeoff.
	Reasoning string
}

func mergeSettleOptions(values []SettleOptions) SettleOptions {
	return mergeExpValues(values, func(out, later SettleOptions) SettleOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Priorities != nil {
			out.Priorities = later.Priorities
		}
		if later.Constraints != nil {
			out.Constraints = later.Constraints
		}
		if later.MinSatisfaction != 0 {
			out.MinSatisfaction = later.MinSatisfaction
		}
		if later.MaxAlternatives != 0 {
			out.MaxAlternatives = later.MaxAlternatives
		}
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		return out
	})
}

func settleCommon(op string, o schemaflux.Options) (bind.Common, error) {
	c, err := commonSettings(o)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check(op); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func settleInternal[T any](ctx context.Context, o SettleOptions, c bind.Common) ops.NegotiateOptions {
	in := ops.NewNegotiateOptions()
	if o.Priorities != nil {
		in.Priorities = o.Priorities
	}
	if o.Constraints != nil {
		in.Constraints = o.Constraints
	}
	if o.MinSatisfaction < 0 {
		in.MinSatisfaction = math.SmallestNonzeroFloat64
	} else if o.MinSatisfaction != 0 {
		in.MinSatisfaction = o.MinSatisfaction
	}
	if o.MaxAlternatives > 0 {
		in.MaxAlternatives = o.MaxAlternatives
	}
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	in.Context = ctx
	if c.Model != "" {
		in.Model = c.Model
	}
	if c.Tier != 0 {
		in.Intelligence = c.Tier
	}
	if c.Effort != "" {
		in.Effort = c.Effort
	}
	if c.Mode != 0 {
		in.Mode = c.Mode
	}
	if c.Steering != "" {
		in.Steering = c.Steering
	}
	if c.MaxOutputTokens > 0 {
		in.MaxOutputTokens = c.MaxOutputTokens
	}
	if c.WebSearch {
		in.WebSearch = true
	}
	return in
}

func settleResult[T any](r ops.NegotiateResult[T]) SettleResult[T] {
	return SettleResult[T]{Solution: r.Solution, Satisfaction: r.Satisfaction, OverallSatisfaction: r.OverallSatisfaction, Tradeoffs: convertSettleTradeoffs(r.Tradeoffs), Alternatives: r.Alternatives, Reasoning: r.Reasoning, ModelConfidence: r.ModelConfidence}
}

func convertSettleTradeoffs(in []ops.Tradeoff) []Tradeoff {
	if len(in) == 0 {
		return nil
	}
	out := make([]Tradeoff, len(in))
	for i, v := range in {
		out[i] = Tradeoff{Sacrificed: v.Sacrificed, Gained: v.Gained, Impact: v.Impact, Reasoning: v.Reasoning}
	}
	return out
}

// Settle produces a T that best satisfies competing constraints in one call.
// The model's claimed overall satisfaction is checked against MinSatisfaction.
// Default tier Fast, mode Transform. NaN, infinity, and values above 1 for
// MinSatisfaction are configuration errors; negative values disable its floor.
// Negative MaxAlternatives is a configuration error.
func Settle[T any](ctx context.Context, constraints any, options ...SettleOptions) (SettleResult[T], error) {
	var zero SettleResult[T]
	call, err := bind.Begin(ctx, "exp.settle")
	if err != nil {
		return zero, err
	}
	o := mergeSettleOptions(options)
	if math.IsNaN(o.MinSatisfaction) || math.IsInf(o.MinSatisfaction, 0) || o.MinSatisfaction > 1 {
		return zero, call.End(nil, types.Newf(types.KindConfiguration, "exp.settle", "MinSatisfaction is outside 0 to 1"))
	}
	if o.MaxAlternatives < 0 {
		return zero, call.End(nil, types.Newf(types.KindConfiguration, "exp.settle", "MaxAlternatives must not be negative"))
	}
	c, err := settleCommon("exp.settle", o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		r, e := ops.Settle[T](constraints, settleInternal[T](call.Context(), o, c))
		if e != nil {
			return zero, call.End(nil, e)
		}
		return settleResult(r), call.End(nil, e)
	}
	r, e := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.NegotiateResult[T], error) {
		result, callErr := ops.Settle[T](constraints, settleInternal[T](ctx, o, c))
		if callErr != nil {
			return ops.NegotiateResult[T]{}, callErr
		}
		return result, callErr
	})
	return settleResult(r), e
}

// AdversarialContext describes two positions and their bargaining relationship.
// It is prompt material, not a context.Context.
type AdversarialContext[T any] struct {
	// Ours is our party's position.
	Ours AdversarialPosition[T]
	// Theirs is the other party's position.
	Theirs AdversarialPosition[T]
	// OurLeverage is strong, weak, or balanced.
	OurLeverage string
	// Relationship is collaborative, competitive, or mixed; empty means unstated.
	Relationship string
}

// AdversarialPosition is one party's starting position and limits.
type AdversarialPosition[T any] struct {
	// Position is the party's starting position.
	Position T
	// MustHaves are requirements the party will not give up. Nil means none.
	MustHaves []string
	// WalkAway is the party's point for leaving. Nil means unstated.
	WalkAway map[string]any
}

// SettleAdversarialOptions configures SettleAdversarial. Its zero value uses balanced strategy.
type SettleAdversarialOptions struct {
	schemaflux.Options
	// Strategy guides the model with aggressive, balanced, or accommodating. Empty means balanced.
	Strategy string
}

// SettleAdversarialResult reports the deal and the model's account of concessions.
type SettleAdversarialResult[T any] struct {
	// Deal is the proposed agreement.
	Deal T
	// DealReached reports whether the model says the parties agreed.
	DealReached bool
	// TermMovements lists the model's account of movement on each term.
	TermMovements []TermMovement
	// WhoConcededMore is the model's account of which side gave more.
	WhoConcededMore string
	// OurSatisfaction is the model's own satisfaction claim for our side.
	OurSatisfaction float64
	// TheirSatisfaction is the model's own satisfaction claim for the other side.
	TheirSatisfaction float64
	// Reasoning is the model's explanation.
	Reasoning string
	// ModelConfidence is the model's own confidence claim.
	ModelConfidence float64
}

// TermMovement tracks the model's account of movement on one term.
type TermMovement struct {
	// Term names the negotiated item.
	Term string
	// OurAsk is our initial position on the term.
	OurAsk any
	// TheirOffer is the other party's initial offer.
	TheirOffer any
	// FinalValue is the proposed agreed value.
	FinalValue any
	// Movement is we_concede, they_concede, split, or deadlock.
	Movement string
}

func mergeSettleAdversarialOptions(values []SettleAdversarialOptions) SettleAdversarialOptions {
	return mergeExpValues(values, func(out, later SettleAdversarialOptions) SettleAdversarialOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		return out
	})
}

func settleAdversarialInternal(ctx context.Context, o SettleAdversarialOptions, c bind.Common) ops.AdversarialOptions {
	in := ops.NewAdversarialOptions()
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	in.Context = ctx
	if c.Model != "" {
		in.Model = c.Model
	}
	if c.Tier != 0 {
		in.Intelligence = c.Tier
	}
	if c.Effort != "" {
		in.Effort = c.Effort
	}
	if c.Mode != 0 {
		in.Mode = c.Mode
	}
	if c.Steering != "" {
		in.Steering = c.Steering
	}
	if c.MaxOutputTokens > 0 {
		in.MaxOutputTokens = c.MaxOutputTokens
	}
	if c.WebSearch {
		in.WebSearch = true
	}
	return in
}

func settleAdversarialResult[T any](r ops.AdversarialResult[T]) SettleAdversarialResult[T] {
	var movements []TermMovement
	if len(r.TermMovements) > 0 {
		movements = make([]TermMovement, len(r.TermMovements))
		for i, v := range r.TermMovements {
			movements[i] = TermMovement{Term: v.Term, OurAsk: v.OurAsk, TheirOffer: v.TheirOffer, FinalValue: v.FinalValue, Movement: v.Movement}
		}
	}
	return SettleAdversarialResult[T]{Deal: r.Deal, DealReached: r.DealReached, TermMovements: movements, WhoConcededMore: r.WhoConcededMore, OurSatisfaction: r.OurSatisfaction, TheirSatisfaction: r.TheirSatisfaction, Reasoning: r.Reasoning, ModelConfidence: r.ModelConfidence}
}

// SettleAdversarial produces a deal between two opposed positions in one call and reports who moved on each term. Default tier Fast, mode Transform.
func SettleAdversarial[T any](ctx context.Context, parties AdversarialContext[T], options ...SettleAdversarialOptions) (SettleAdversarialResult[T], error) {
	var zero SettleAdversarialResult[T]
	call, err := bind.Begin(ctx, "exp.settleadversarial")
	if err != nil {
		return zero, err
	}
	o := mergeSettleAdversarialOptions(options)
	c, err := settleCommon("exp.settleadversarial", o.Options)
	if err != nil {
		return zero, call.End(nil, err)
	}
	convert := func(r ops.AdversarialResult[T]) SettleAdversarialResult[T] { return settleAdversarialResult(r) }
	if call.Plain() {
		r, e := ops.SettleAdversarial[T](ops.AdversarialContext[T]{Ours: ops.AdversarialPosition[T]{Position: parties.Ours.Position, MustHaves: parties.Ours.MustHaves, WalkAway: parties.Ours.WalkAway}, Theirs: ops.AdversarialPosition[T]{Position: parties.Theirs.Position, MustHaves: parties.Theirs.MustHaves, WalkAway: parties.Theirs.WalkAway}, OurLeverage: parties.OurLeverage, Relationship: parties.Relationship}, settleAdversarialInternal(call.Context(), o, c))
		return convert(r), call.End(nil, e)
	}
	r, e := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.AdversarialResult[T], error) {
		return ops.SettleAdversarial[T](ops.AdversarialContext[T]{Ours: ops.AdversarialPosition[T]{Position: parties.Ours.Position, MustHaves: parties.Ours.MustHaves, WalkAway: parties.Ours.WalkAway}, Theirs: ops.AdversarialPosition[T]{Position: parties.Theirs.Position, MustHaves: parties.Theirs.MustHaves, WalkAway: parties.Theirs.WalkAway}, OurLeverage: parties.OurLeverage, Relationship: parties.Relationship}, settleAdversarialInternal(ctx, o, c))
	})
	return convert(r), e
}
