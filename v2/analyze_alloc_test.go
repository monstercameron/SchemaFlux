package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

func TestClassifyAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	categories := []string{"billing", "technical"}
	const reply = `{"category":"billing","confidence":0.9,"alternatives":[],"reasoning":"a charge"}`
	cases := map[string]ClassifyOptions{
		"no options":           {},
		"one options value":    {MinConfidence: 0.7, Descriptions: map[string]string{"billing": "money"}, Model: "alloc-model"},
		"a floor of none":      {MinConfidence: -1},
		"every common setting": {Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "prefer billing", MaxOutputTokens: 100, WebSearch: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Classify(ctx, "my card was charged twice", categories, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Classify[string, string]("my card was charged twice", classifyInternal(ctx, categories, o))
					return err
				},
			})
		})
	}
}

// The result of the internal call is copied into the root's own type, and the
// alternatives are the one part of it that is a list: converting them is one
// allocation, made only when the model named any.
func TestClassifyWithAlternativesAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	categories := []string{"billing", "technical"}
	const reply = `{"category":"billing","confidence":0.9,"alternatives":[{"category":"technical","confidence":0.3}],"reasoning":"a charge"}`
	model := &allocProvider{reply: reply}
	ctx := WithClient(context.Background(), Client{Provider: model})
	call := func() { _, _ = Classify(ctx, "my card was charged twice", categories) }
	internal := func() {
		_, _ = ops.Classify[string, string]("my card was charged twice", classifyInternal(ctx, categories, ClassifyOptions{}))
	}
	call()
	internal()
	wrapped, direct := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal)
	t.Logf("with one alternative: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and the internal call %v: the copy of the alternatives is the one allocation more", wrapped, direct)
	}
}
