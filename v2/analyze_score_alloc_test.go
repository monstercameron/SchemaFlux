package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

func TestScoreAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"value":8,"breakdown":{"clarity":7},"reasoning":"clear","strengths":["focused"],"weaknesses":["short"]}`
	many := []string{"c0", "c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11"}
	manyWeights := map[string]float64{"c0": 1, "c3": 2, "c11": 3}
	cases := map[string]ScoreOptions{
		"no options":           {},
		"criteria":             {Criteria: []string{"clarity"}},
		"a scale":              {ScaleMin: 1, ScaleMax: 10},
		"normalized":           {Normalize: true},
		"every option":         {Criteria: []string{"clarity"}, Rubric: map[string]string{"clarity": "plain"}, Weights: map[string]float64{"clarity": 2}, ScaleMax: 10, Normalize: true, OmitBreakdown: true},
		"weights on a rubric":  {Rubric: map[string]string{"clarity": "plain", "tone": "polite"}, Weights: map[string]float64{"clarity": 2, "tone": 1}},
		"many criteria":        {Criteria: many, Weights: manyWeights},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "mark harshly", MaxOutputTokens: 100, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Score(ctx, "an essay", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Score("an essay", scoreInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestScoreWithSeveralOptionsValuesAllocatesAsTheInternalCallWithTheirMerge(t *testing.T) {
	const reply = `{"value":8,"breakdown":{"clarity":7},"reasoning":"clear","strengths":["focused"],"weaknesses":["short"]}`
	first, second := ScoreOptions{Criteria: []string{"clarity"}}, ScoreOptions{ScaleMax: 10, Options: Options{Steering: "mark harshly"}}
	merged := mergeScoreOptions([]ScoreOptions{first, second})
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: reply,
		call: func(ctx context.Context) error {
			_, err := Score(ctx, "an essay", first, second)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Score("an essay", scoreInternal(ctx, merged))
			return err
		},
	})
}

// The result of the internal call is converted to the type of the root with no
// list copied: the strengths, the weaknesses and the breakdown are the internal
// result's own, so a result that has all three allocates what the internal call
// does, and so does one that has none.
func TestScoreAllocatesAsTheInternalCallWhateverTheResultHolds(t *testing.T) {
	for name, reply := range map[string]string{
		"nothing but a value": `{"value":8,"breakdown":{},"reasoning":"","strengths":[],"weaknesses":[]}`,
		"every list":          `{"value":8,"breakdown":{"a":1,"b":2,"c":3},"reasoning":"clear","strengths":["x","y","z"],"weaknesses":["p","q"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Score(ctx, "an essay")
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Score("an essay", scoreInternal(ctx, ScoreOptions{}))
					return err
				},
			})
		})
	}
}

func TestMergeScoreOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeScoreOptions)
}
