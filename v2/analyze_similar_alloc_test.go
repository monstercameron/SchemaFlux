package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

func TestSimilarAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	const reply = `{"score":0.8,"explanation":"the same meaning"}`
	cases := map[string]SimilarOptions{
		"no options":           {},
		"one options value":    {Threshold: 0.9, Options: Options{Model: "alloc-model"}},
		"a threshold of zero":  {Threshold: -1},
		"every common setting": {Options: Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "ignore greetings", MaxOutputTokens: 100, WebSearch: true}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: reply,
				call: func(ctx context.Context) error {
					_, err := Similar(ctx, "the cat sat", "a cat was sitting", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Similar("the cat sat", "a cat was sitting", similarInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestSimilarWithSeveralOptionsValuesAllocatesAsTheInternalCallWithTheirMerge(t *testing.T) {
	const reply = `{"score":0.8,"explanation":"the same meaning"}`
	first, second := SimilarOptions{Threshold: 0.9}, SimilarOptions{Options: Options{Model: "alloc-model", Steering: "ignore greetings"}}
	merged := mergeSimilarOptions([]SimilarOptions{first, second})
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: reply,
		call: func(ctx context.Context) error {
			_, err := Similar(ctx, "the cat sat", "a cat was sitting", first, second)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Similar("the cat sat", "a cat was sitting", similarInternal(ctx, merged))
			return err
		},
	})
}

// The result of the internal call is copied into the root's own type, and the
// two lists of aspects are the parts of it that are lists: converting each is one
// allocation, made only when the list is not empty. Checking the aspects before
// the call allocates nothing.
func TestSimilarWithAspectsAllocatesOneMoreThanTheInternalCallForEachListThatIsNotEmpty(t *testing.T) {
	const reply = `{"score":0.8,"explanation":"alike","aspects":{"a1":{"score":0.9,"reason":"same claim"},"a2":{"score":0.3,"reason":"other voice"}}}`
	model := &allocProvider{reply: reply}
	ctx := WithClient(context.Background(), Client{Provider: model})
	cases := []struct {
		name  string
		o     SimilarOptions
		lists float64
	}{
		{"one list matched and one differing", SimilarOptions{Aspects: []string{"meaning", "tone"}}, 2},
		{"every aspect matched", SimilarOptions{Aspects: []string{"meaning", "tone"}, Threshold: 0.2}, 1},
		{"every aspect differing", SimilarOptions{Aspects: []string{"meaning", "tone"}, Threshold: 0.95}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := func() { _, _ = Similar(ctx, "the cat sat", "a cat was sitting", tc.o) }
			internal := func() { _, _ = ops.Similar("the cat sat", "a cat was sitting", similarInternal(ctx, tc.o)) }
			call()
			internal()
			wrapped, direct := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal)
			t.Logf("the function %v allocations, the internal call %v", wrapped, direct)
			if wrapped != direct+tc.lists {
				t.Errorf("the function allocates %v times and the internal call %v: the copy of each list of aspects is the %v allocations more", wrapped, direct, tc.lists)
			}
		})
	}
}

func TestMergeSimilarOptionsCopiesEveryField(t *testing.T) {
	t.Parallel()
	requireMergeCopiesEveryField(t, mergeSimilarOptions)
}
