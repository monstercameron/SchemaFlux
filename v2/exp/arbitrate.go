package exp

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// ArbitrateOptions configures Arbitrate. Its zero value uses equal rule
// weights, allows partial rule satisfaction, omits per-evaluation reasoning,
// and breaks ties by the most rules passed and then input order.
type ArbitrateOptions struct {
	schemaflux.Options
	// Weights gives each rule's weight in order. Nil assigns weight 1 to every rule; negative weights are invalid.
	Weights []float64
	// RequireAllRules disqualifies an option that fails any rule. False allows partial satisfaction.
	RequireAllRules bool
	// IncludeReasoning asks for a reason with every evaluation. False omits those reasons.
	IncludeReasoning bool
	// Tiebreaker is first, random, or most-confident. Empty means most-confident.
	Tiebreaker string
}

// ArbitrateResult contains the winning option and its checked evaluations.
type ArbitrateResult[T any] struct {
	// Winner is the selected option.
	Winner T `json:"winner"`
	// WinnerIndex is the position of Winner in the input.
	WinnerIndex int `json:"winner_index"`
	// Scores holds each option's weighted total by index; it is empty when no model evaluated one option.
	Scores map[int]float64 `json:"scores"`
	// Evaluations contains each option's rule evaluations.
	Evaluations []OptionEvaluation `json:"evaluations"`
	// Reasoning is the model's account of how the options compare.
	Reasoning string `json:"reasoning"`
	// ModelConfidence is the model's claimed confidence, zero when not evaluated.
	ModelConfidence float64 `json:"confidence"`
	// TiesBroken reports that multiple options had the highest total.
	TiesBroken bool `json:"ties_broken"`
	// Tied contains the indices of options tied for the highest total.
	Tied []int `json:"tied,omitempty"`
	// Evaluated reports whether a model evaluated the options.
	Evaluated bool `json:"evaluated"`
}

// OptionEvaluation describes one option's checked score and rule results.
type OptionEvaluation struct {
	// Index is this option's position in the input.
	Index int `json:"index"`
	// TotalScore is its weighted mean, calculated by the library.
	TotalScore float64 `json:"total_score"`
	// RuleResults contains one evaluation for each supplied rule.
	RuleResults []RuleEvaluation `json:"rule_results"`
	// Disqualified reports whether RequireAllRules disqualified this option.
	Disqualified bool `json:"disqualified"`
	// DisqualifyReason describes the rule positions that disqualified it.
	DisqualifyReason string `json:"disqualify_reason,omitempty"`
}

// RuleEvaluation is the model's assessment of one option against one rule.
type RuleEvaluation struct {
	// Rule is the caller's rule text.
	Rule string `json:"rule"`
	// Passed reports the model's yes-or-no assessment.
	Passed bool `json:"passed"`
	// Score is the model's score from zero to one.
	Score float64 `json:"score"`
	// Reasoning is the requested explanation, empty unless IncludeReasoning is set.
	Reasoning string `json:"reasoning,omitempty"`
}

func mergeArbitrateOptions(values []ArbitrateOptions) ArbitrateOptions {
	return mergeExpValues(values, func(out, later ArbitrateOptions) ArbitrateOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Weights != nil {
			out.Weights = later.Weights
		}
		if later.RequireAllRules {
			out.RequireAllRules = true
		}
		if later.IncludeReasoning {
			out.IncludeReasoning = true
		}
		if later.Tiebreaker != "" {
			out.Tiebreaker = later.Tiebreaker
		}
		return out
	})
}

func arbitrateCommon(o ArbitrateOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.arbitrate"); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

func arbitrateInternal(ctx context.Context, o ArbitrateOptions, c bind.Common) ops.ArbitrateOptions {
	in := ops.NewArbitrateOptions()
	if o.Weights != nil {
		in.Weights = o.Weights
	}
	if o.RequireAllRules {
		in.RequireAllRules = true
	}
	if o.IncludeReasoning {
		in.IncludeReasoning = true
	}
	if o.Tiebreaker != "" {
		in.Tiebreaker = o.Tiebreaker
	}
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
	in.Context = ctx
	return in
}

func arbitrateResult[T any](in ops.ArbitrateResult[T]) ArbitrateResult[T] {
	out := ArbitrateResult[T]{Winner: in.Winner, WinnerIndex: in.WinnerIndex, Scores: in.Scores, Reasoning: in.Reasoning, ModelConfidence: in.ModelConfidence, TiesBroken: in.TiesBroken, Evaluated: in.Metadata["evaluated"] == true}
	if tied, ok := in.Metadata["tied"].([]int); ok {
		out.Tied = tied
	}
	if len(in.Evaluations) != 0 {
		out.Evaluations = make([]OptionEvaluation, len(in.Evaluations))
		for i, v := range in.Evaluations {
			out.Evaluations[i] = OptionEvaluation{Index: v.Index, TotalScore: v.TotalScore, Disqualified: v.Disqualified, DisqualifyReason: v.DisqualifyReason}
			if len(v.RuleResults) != 0 {
				out.Evaluations[i].RuleResults = make([]RuleEvaluation, len(v.RuleResults))
				for j, r := range v.RuleResults {
					out.Evaluations[i].RuleResults[j] = RuleEvaluation{Rule: r.Rule, Passed: r.Passed, Score: r.Score, Reasoning: r.Reasoning}
				}
			}
		}
	}
	return out
}

// Arbitrate selects an option against rules. Go applies weights,
// disqualification and tie breaking to the model's per-rule evaluations.
// At least one option and one nonblank rule are required. A call with more
// options and rules than one answer can declare is refused before sending.
// Default tier Fast, mode Transform. Its record is exp.arbitrate at
// SchemaAndInvariantChecked.
func Arbitrate[T any](ctx context.Context, options []T, rules []string, values ...ArbitrateOptions) (ArbitrateResult[T], error) {
	var zero ArbitrateResult[T]
	call, err := bind.Begin(ctx, "exp.arbitrate")
	if err != nil {
		return zero, err
	}
	o := mergeArbitrateOptions(values)
	if err := refuseExpEmpty(&call, "options", len(options)); err != nil {
		return zero, err
	}
	if err := refuseExpEmpty(&call, "rules", len(rules)); err != nil {
		return zero, err
	}
	for _, rule := range rules {
		if err := refuseExpBlank(&call, "rule", rule); err != nil {
			return zero, err
		}
	}
	c, err := arbitrateCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		in := arbitrateInternal(call.Context(), o, c)
		in.Rules = rules
		res, err := ops.Arbitrate[T](options, in)
		return arbitrateResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (ops.ArbitrateResult[T], error) {
		in := arbitrateInternal(ctx, o, c)
		in.Rules = rules
		return ops.Arbitrate[T](options, in)
	})
	return arbitrateResult(res), err
}
