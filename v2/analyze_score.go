package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ScoreOptions are Score's settings. The zero value is the default: a scale of 0
// to 10, no criteria, and a per-criterion breakdown asked for.
//
// The common settings of Options come first and are set without naming the
// embedded type: ScoreOptions{Criteria: []string{"clarity"}, ScaleMax: 5, Model: "gpt-6-luna"}.
type ScoreOptions struct {
	Options

	// Criteria are what to score on, in the order they are listed to the model.
	// Nil means the model's own judgement of quality.
	Criteria []string

	// Rubric says what a criterion means, keyed by criterion. When Criteria is
	// empty the keys of Rubric are the criteria, in alphabetical order. Nil means
	// none.
	Rubric map[string]string

	// Weights give how much a criterion counts, keyed by criterion, and the model
	// is told them; Go does not apply them. Every key has to be in Criteria, or
	// in Rubric when Criteria is empty. A weight that is negative, infinite or not
	// a number is a configuration error, and so is a key that is not a criterion.
	// Nil means equal weights.
	Weights map[string]float64

	// ScaleMin and ScaleMax are the ends of the scale the score is given on, and
	// are read together: both zero means 0 to 10. Otherwise ScaleMin has to be
	// below ScaleMax, either may be negative, both have to be finite numbers, and
	// the distance between them has to be finite too. Anything else is a
	// configuration error.
	ScaleMin, ScaleMax float64

	// OmitBreakdown leaves the request for a per-criterion breakdown out of the
	// steering. The model is still shown the field, and Go does not enforce the
	// omission. False asks for the breakdown.
	OmitBreakdown bool

	// Normalize asks the model to normalise the score to the scale. Go does not
	// enforce it, and the result has the same shape either way: NormalizedValue is
	// always worked out here. False makes no such request.
	Normalize bool
}

// ScoreResult is a score and the model's account of it.
type ScoreResult struct {
	// Value is the overall score, on the scale asked for. An answer outside the
	// scale is refused and never clamped.
	Value float64 `json:"value"`

	// NormalizedValue is (Value - ScaleMin) / (ScaleMax - ScaleMin), worked out
	// here, always: from 0 to 1 whatever the scale was.
	NormalizedValue float64 `json:"normalized_value"`

	// Breakdown is the model's score for each criterion, on the same scale. It is
	// empty, never nil, when the model gave none.
	Breakdown map[string]float64 `json:"breakdown,omitempty"`

	// Reasoning is why, in the model's words. Empty when it gave none.
	Reasoning string `json:"reasoning,omitempty"`

	// Strengths are what the model found good. Empty when it named none.
	Strengths []string `json:"strengths,omitempty"`

	// Weaknesses are what the model found wanting. Empty when it named none.
	Weaknesses []string `json:"weaknesses,omitempty"`
}

// mergeScoreOptions merges the values of a call (M0).
func mergeScoreOptions(opts []ScoreOptions) ScoreOptions {
	return mergeValues(opts, func(o, later ScoreOptions) ScoreOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Criteria, later.Criteria)
		layMap(&o.Rubric, later.Rubric)
		layMap(&o.Weights, later.Weights)
		lay(&o.ScaleMin, later.ScaleMin)
		lay(&o.ScaleMax, later.ScaleMax)
		lay(&o.OmitBreakdown, later.OmitBreakdown)
		lay(&o.Normalize, later.Normalize)
		return o
	})
}

// checkScoreOptions returns the internal error of a setting no call can use, or
// nil. The numbers are checked on the caller's own values, before the meaning of
// a zero scale is applied. No message repeats a criterion the caller wrote.
func checkScoreOptions(o ScoreOptions) error {
	if err := o.common().Check("score"); err != nil {
		return err
	}
	for _, end := range [...]struct {
		field string
		value float64
	}{{"ScaleMin", o.ScaleMin}, {"ScaleMax", o.ScaleMax}} {
		if math.IsNaN(end.value) || math.IsInf(end.value, 0) {
			return types.Newf(types.KindConfiguration, "score", "%s must be a finite number", end.field)
		}
	}
	if o.ScaleMin != 0 || o.ScaleMax != 0 {
		if o.ScaleMin >= o.ScaleMax {
			return types.Newf(types.KindConfiguration, "score", "ScaleMin must be below ScaleMax, or both must be zero")
		}
		if math.IsInf(o.ScaleMax-o.ScaleMin, 0) {
			return types.Newf(types.KindConfiguration, "score", "ScaleMin and ScaleMax are too far apart")
		}
	}
	for criterion, weight := range o.Weights {
		if !(weight >= 0) || math.IsInf(weight, 1) {
			return types.Newf(types.KindConfiguration, "score", "Weights has a weight that is negative or not a finite number")
		}
		if !isScoredCriterion(o, criterion) {
			return types.Newf(types.KindConfiguration, "score", "Weights has a key that is not in Criteria, or in Rubric when Criteria is empty")
		}
	}
	return nil
}

// isScoredCriterion reports whether name is one of the criteria the call scores
// on: those of Criteria, or the keys of Rubric when Criteria is empty. It reads
// the options in place and allocates nothing.
func isScoredCriterion(o ScoreOptions, name string) bool {
	if len(o.Criteria) == 0 {
		_, ok := o.Rubric[name]
		return ok
	}
	for _, criterion := range o.Criteria {
		if criterion == name {
			return true
		}
	}
	return false
}

// scoreInternal builds the internal options of a Score call on ctx.
func scoreInternal(ctx context.Context, o ScoreOptions) ops.ScoreOptions {
	opts := ops.NewScoreOptions()
	laySlice(&opts.Criteria, o.Criteria)
	layMap(&opts.Rubric, o.Rubric)
	layMap(&opts.Weights, o.Weights)
	if o.ScaleMin != 0 || o.ScaleMax != 0 {
		opts.ScaleMin, opts.ScaleMax = o.ScaleMin, o.ScaleMax
	}
	turnOff(&opts.IncludeBreakdown, o.OmitBreakdown)
	lay(&opts.Normalize, o.Normalize)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// scoreResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result. The internal call has already worked out NormalizedValue from the
// scale it asked for, and has made Breakdown an empty map when the model gave
// none.
func scoreResult(res ops.ScoreResult) ScoreResult {
	return ScoreResult{
		Value:           res.Value,
		NormalizedValue: res.NormalizedValue,
		Breakdown:       res.Breakdown,
		Reasoning:       res.Reasoning,
		Strengths:       res.Strengths,
		Weaknesses:      res.Weaknesses,
	}
}

// Score rates a value on a scale against criteria. Default tier Fast, mode
// Transform.
//
//	res, err := schemaflux.Score(ctx, essay)
//	res, err := schemaflux.Score(ctx, essay, schemaflux.ScoreOptions{Criteria: []string{"clarity", "evidence"}, ScaleMax: 5})
//	fmt.Println(res.Value, res.Breakdown["clarity"])
//
// An answer that has no value, has a value or a criterion's score outside the
// scale, or is not the JSON that was asked for is an error of kind
// ErrInvalidAnswer, never a clamped or defaulted number. On any error the result
// is empty. The Record of the call is "score", delivered at ContractJSONWellFormed:
// the answer is parsed and held to the scale, but no schema is declared to the
// provider.
func Score[T any](ctx context.Context, input T, opts ...ScoreOptions) (ScoreResult, error) {
	var zero ScoreResult
	call, err := bind.Begin(ctx, "score")
	if err != nil {
		return zero, err
	}
	o := mergeScoreOptions(opts)
	if err := checkScoreOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Score(input, scoreInternal(call.Context(), o))
		return scoreResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ScoreResult, error) {
		return ops.Score(input, scoreInternal(ctx, o))
	})
	return scoreResult(res), err
}
