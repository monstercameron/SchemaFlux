package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

type allocPost struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func TestCompleteAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]CompleteOptions{
		"no options":           {},
		"one options value":    {Background: []string{"earlier note"}, MaxLength: 40, StopSequences: []string{"END"}, Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "jumps over",
				call: func(ctx context.Context) error {
					_, err := Complete(ctx, "The quick brown fox", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Complete(ctx, "The quick brown fox", completeInternal(ctx, o))
					return err
				},
			})
		})
	}
	t.Run("two options values are merged without allocating", func(t *testing.T) {
		first, second := CompleteOptions{Model: "alloc-model"}, CompleteOptions{MaxLength: 40}
		merged := mergeCompleteOptions([]CompleteOptions{first, second})
		requireAllocsLikeTheInternalCall(t, allocCase{
			reply: "jumps over",
			call: func(ctx context.Context) error {
				_, err := Complete(ctx, "The quick brown fox", first, second)
				return err
			},
			internal: func(ctx context.Context) error {
				_, err := ops.Complete(ctx, "The quick brown fox", completeInternal(ctx, merged))
				return err
			},
		})
	})
}

func TestCompleteFieldAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	post := allocPost{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming"}
	cases := map[string]CompleteOptions{
		"no options":           {},
		"one options value":    {Background: []string{"house style: plain"}, MaxLength: 40, Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: "how doctors read scans.",
				call: func(ctx context.Context) error {
					_, err := CompleteField(ctx, post, "Body", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.CompleteField[allocPost](ctx, post, completeFieldInternal(ctx, "Body", o))
					return err
				},
			})
		})
	}
}

func TestSuggestAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `["Reply to the customer","Offer a refund"]`
	cases := map[string]SuggestOptions{
		"no options":           {},
		"one options value":    {Strategy: "goal", TopN: 3, NoRanking: true, IncludeScores: true, Domain: "billing", Constraints: []string{"no refunds"}, Categories: []string{"fix"}, Model: "alloc-model"},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierQuick, Effort: EffortLow, Mode: ModeStrict, Steering: "be exact", MaxOutputTokens: 300, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Suggest[string](ctx, "My invoice is wrong", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Suggest[string]("My invoice is wrong", suggestInternal(ctx, o))
					return err
				},
			})
		})
	}
}
