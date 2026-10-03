package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// scoreParityAnswer is on every scale the cases use, so that no case is refused
// for its answer.
const scoreParityAnswer = `{"value":1.5,"breakdown":{"clarity":1.5,"tone":1.5},"reasoning":"clear","strengths":[],"weaknesses":[]}`

func scoreParityCase(name string, old oldroot.ScoreOptions, v2 schemaflux.ScoreOptions) parityCase {
	return scoreParityCaseAnswering(name, scoreParityAnswer, old, v2)
}

// scoreParityCaseAnswering is a case whose answer is the one given, for a scale
// the usual answer is not on.
func scoreParityCaseAnswering(name, answer string, old oldroot.ScoreOptions, v2 schemaflux.ScoreOptions) parityCase {
	return parityCase{name: "Score/" + name, op: "score", replies: []string{answer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Score("essay", old)
			return err
		},
		v2: func(ctx context.Context) error { _, err := schemaflux.Score(ctx, "essay", v2); return err }}
}

func TestScoreSendsTheRequestTheOldRootSends(t *testing.T) {
	base := func() oldroot.ScoreOptions { return oldroot.NewScoreOptions() }
	with := func(set func(o *oldroot.ScoreOptions)) oldroot.ScoreOptions {
		o := base()
		set(&o)
		return o
	}
	two := []string{"tone", "clarity"}
	rubric := map[string]string{"clarity": "plain", "tone": "polite"}
	cases := []parityCase{
		scoreParityCase("default", base(), schemaflux.ScoreOptions{}),
		scoreParityCase("criteria", with(func(o *oldroot.ScoreOptions) { o.Criteria = two }), schemaflux.ScoreOptions{Criteria: two}),
		scoreParityCase("rubric", with(func(o *oldroot.ScoreOptions) { o.Rubric = rubric }), schemaflux.ScoreOptions{Rubric: rubric}),
		scoreParityCase("criteria and rubric", with(func(o *oldroot.ScoreOptions) { o.Criteria, o.Rubric = two, rubric }), schemaflux.ScoreOptions{Criteria: two, Rubric: rubric}),
		scoreParityCase("weights", with(func(o *oldroot.ScoreOptions) {
			o.Criteria, o.Weights = two, map[string]float64{"clarity": 2, "tone": 0.5}
		}), schemaflux.ScoreOptions{Criteria: two, Weights: map[string]float64{"clarity": 2, "tone": 0.5}}),
		scoreParityCase("weights on a rubric", with(func(o *oldroot.ScoreOptions) {
			o.Rubric, o.Weights = rubric, map[string]float64{"clarity": 2}
		}), schemaflux.ScoreOptions{Rubric: rubric, Weights: map[string]float64{"clarity": 2}}),
		scoreParityCase("a scale of 1 to 5", with(func(o *oldroot.ScoreOptions) { o.ScaleMin, o.ScaleMax = 1, 5 }), schemaflux.ScoreOptions{ScaleMin: 1, ScaleMax: 5}),
		scoreParityCase("a maximum alone", with(func(o *oldroot.ScoreOptions) { o.ScaleMax = 5 }), schemaflux.ScoreOptions{ScaleMax: 5}),
		scoreParityCase("a negative minimum", with(func(o *oldroot.ScoreOptions) { o.ScaleMin, o.ScaleMax = -2, 2 }), schemaflux.ScoreOptions{ScaleMin: -2, ScaleMax: 2}),
		scoreParityCaseAnswering("a minimum below zero and a maximum of zero", `{"value":-1,"breakdown":{},"reasoning":"clear","strengths":[],"weaknesses":[]}`,
			with(func(o *oldroot.ScoreOptions) { o.ScaleMin, o.ScaleMax = -2, 0 }), schemaflux.ScoreOptions{ScaleMin: -2}),
		scoreParityCase("omit breakdown", with(func(o *oldroot.ScoreOptions) { o.IncludeBreakdown = false }), schemaflux.ScoreOptions{OmitBreakdown: true}),
		scoreParityCase("normalize", with(func(o *oldroot.ScoreOptions) { o.Normalize = true }), schemaflux.ScoreOptions{Normalize: true}),
		scoreParityCase("normalize on a scale", with(func(o *oldroot.ScoreOptions) { o.Normalize, o.ScaleMin, o.ScaleMax = true, 1, 5 }), schemaflux.ScoreOptions{Normalize: true, ScaleMin: 1, ScaleMax: 5}),
		scoreParityCase("every option", with(func(o *oldroot.ScoreOptions) {
			o.Criteria, o.Rubric, o.Weights = two, rubric, map[string]float64{"clarity": 2}
			o.ScaleMin, o.ScaleMax, o.Normalize, o.IncludeBreakdown = 1, 5, true, false
		}), schemaflux.ScoreOptions{Criteria: two, Rubric: rubric, Weights: map[string]float64{"clarity": 2}, ScaleMin: 1, ScaleMax: 5, Normalize: true, OmitBreakdown: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, scoreParityCase(c.name, old, schemaflux.ScoreOptions{Options: c.v2}))
	}
	runParity(t, cases)
}
