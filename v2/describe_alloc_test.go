package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

type allocReport struct {
	Region  string  `json:"region"`
	Revenue float64 `json:"revenue"`
}

func TestAnnotateAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const (
		input = "Ada Lovelace wrote the first program"
		reply = `{"annotations":[{"type":"entity","value":"Ada Lovelace","text":"Ada Lovelace","start":0,"end":12,"confidence":0.9}],"summary":{"n":1}}`
	)
	cases := map[string]AnnotateOptions{
		"no options":           {},
		"one options value":    {Types: []string{"topics"}, CustomSchema: map[string]string{"risks": "identify risks"}, MinConfidence: 0.7, Language: "English", Domain: "history", Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			// The result is converted from the internal one: the annotations are
			// copied and the map by type is built again from the copies, which the
			// internal call does not do. The internal side of the comparison is the
			// internal call and that conversion of its result, kept in a variable
			// so that the compiler cannot place the copies on the stack.
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Annotate(ctx, input, o)
					return err
				},
				internal: func(ctx context.Context) error {
					res, err := ops.Annotate(input, annotateInternal(ctx, o))
					allocAnnotateSink = annotateResult(res)
					return err
				},
			})
		})
	}
}

// allocAnnotateSink holds the converted result of the internal call in the
// allocation test of Annotate.
var allocAnnotateSink AnnotateResult

func TestExplainAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"explanation":"The plan reads a table.","summary":"A read.","key_points":["table"]}`
	data := allocReport{Region: "EU", Revenue: 12}
	cases := map[string]ExplainOptions{
		"no options":           {},
		"one options value":    {Audience: "executive", Depth: 3, Format: "bullet-points", Focus: "usage", Background: "a slow query", Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Explain(ctx, data, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Explain(data, explainInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestQuestionAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"answer":"Revenue grew","confidence":0.9,"reasoning":"stated","evidence":["up 12%"]}`
	data := allocReport{Region: "EU", Revenue: 12}
	cases := map[string]QuestionOptions{
		"no options":           {},
		"one options value":    {OmitEvidence: true, OmitReasoning: true, Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Question[string](ctx, data, "What is the main finding?", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Question[allocReport, string](data, questionInternal(ctx, "What is the main finding?", o))
					return err
				},
			})
		})
	}
}
