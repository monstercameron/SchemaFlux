package schemaflux

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// The operations on lists are held to the allocation rule of the family (C3): a
// call in the plain state allocates what the internal call it wraps allocates, and
// on the default client one more. The results that carry a list convert it into
// the root's own type, which is one allocation more when the list is not empty:
// those are measured twice, once with nothing to convert, where the two are equal,
// and once with a list, where the function is one more.

var (
	allocChoices = []string{"alpha", "beta", "gamma"}
	allocMatches = []string{"j1", "j2", "j3"}
)

const (
	allocChooseReply  = `{"id":"i-000002"}`
	allocFilterReply  = `{"ids":["i-000001","i-000003"]}`
	allocSortReply    = `{"ids":["i-000003","i-000001","i-000002"]}`
	allocRankReply    = `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":2,"score":0.1}]}`
	allocMatchReply   = `{"matches":[{"source_index":0,"target_index":1,"score":0.9,"explanation":"e","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0,2]}`
	allocNoMatchReply = `{"matches":[],"unmatched_sources":[0,1],"unmatched_targets":[0,1,2]}`
)

var allocEveryCommonSetting = Options{Model: "alloc-model", Tier: TierSmart, Effort: EffortLow, Mode: ModeStrict, Steering: "prefer the first", MaxOutputTokens: 100, WebSearch: true}

func TestChooseAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]ChooseOptions{
		"no options":           {},
		"one options value":    {Criteria: []string{"cheap"}, Strategy: "tournament", OmitReasoning: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocChooseReply,
				call: func(ctx context.Context) error {
					_, err := Choose(ctx, allocChoices, o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Choose(allocChoices, chooseInternal(ctx, o))
					return err
				},
			})
		})
	}
}

func TestFilterAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]FilterOptions{
		"no options":           {},
		"one options value":    {Exclude: true, MinConfidence: 0.9, IncludeReasons: true},
		"a confidence of none": {MinConfidence: -1},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocFilterReply,
				call: func(ctx context.Context) error {
					_, err := Filter(ctx, allocChoices, "short names", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Filter(allocChoices, filterInternal(ctx, "short names", o))
					return err
				},
			})
		})
	}
}

func TestSortAllocatesAsTheInternalCallInThePlainState(t *testing.T) {
	cases := map[string]SortOptions{
		"no options":           {},
		"one options value":    {Direction: "descending", SecondaryCriteria: []string{"by name"}, ComparisonLogic: "numeric", NoStableOrder: true, IncludeScores: true},
		"every common setting": {Options: allocEveryCommonSetting},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			requireAllocsLikeTheInternalCall(t, allocCase{
				reply: allocSortReply,
				call: func(ctx context.Context) error {
					_, err := Sort(ctx, allocChoices, "most urgent first", o)
					return err
				},
				internal: func(ctx context.Context) error {
					_, err := ops.Sort(allocChoices, sortInternal(ctx, "most urgent first", o))
					return err
				},
			})
		})
	}
}

func TestRankAllocatesAsTheInternalCallWhenNoItemIsKept(t *testing.T) {
	o := RankOptions{MinScore: 0.95}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: allocRankReply,
		call: func(ctx context.Context) error {
			_, err := Rank(ctx, allocChoices, "postgres pooling", o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.Rank(allocChoices, rankInternal(ctx, "postgres pooling", o))
			return err
		},
	})
}

func TestRankWithItemsAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	model := &allocProvider{reply: allocRankReply}
	ctx := WithClient(context.Background(), Client{Provider: model})
	o := RankOptions{TopK: 2, Factors: []string{"price"}, Options: allocEveryCommonSetting}
	call := func() { _, _ = Rank(ctx, allocChoices, "postgres pooling", o) }
	internal := func() { _, _ = ops.Rank(allocChoices, rankInternal(ctx, "postgres pooling", o)) }
	call()
	internal()
	wrapped, direct := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal)
	t.Logf("with items: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and the internal call %v: the copy of the items is the one allocation more", wrapped, direct)
	}
}

func TestSemanticMatchAllocatesAsTheInternalCallWhenNothingMatches(t *testing.T) {
	o := SemanticMatchOptions{Threshold: -1, MaxMatches: 2}
	requireAllocsLikeTheInternalCall(t, allocCase{
		reply: allocNoMatchReply,
		call: func(ctx context.Context) error {
			_, err := SemanticMatch(ctx, allocChoices[:2], allocMatches, o)
			return err
		},
		internal: func(ctx context.Context) error {
			_, err := ops.SemanticMatch(allocChoices[:2], allocMatches, semanticMatchInternal(ctx, o))
			return err
		},
	})
}

func TestSemanticMatchWithPairsAllocatesOneMoreThanTheInternalCall(t *testing.T) {
	model := &allocProvider{reply: allocMatchReply}
	ctx := WithClient(context.Background(), Client{Provider: model})
	o := SemanticMatchOptions{Criteria: "same role", Options: allocEveryCommonSetting}
	call := func() { _, _ = SemanticMatch(ctx, allocChoices[:2], allocMatches, o) }
	internal := func() { _, _ = ops.SemanticMatch(allocChoices[:2], allocMatches, semanticMatchInternal(ctx, o)) }
	call()
	internal()
	wrapped, direct := testing.AllocsPerRun(allocRuns, call), testing.AllocsPerRun(allocRuns, internal)
	t.Logf("with pairs: the function %v allocations, the internal call %v", wrapped, direct)
	if wrapped != direct+1 {
		t.Errorf("the function allocates %v times and the internal call %v: the copy of the pairs is the one allocation more", wrapped, direct)
	}
}
