package schemaflux

import (
	"context"
	"math"
	"slices"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The operations of this file work on lists the caller gave: Choose picks one
// item, Filter keeps some, Sort puts them in order, Rank scores each against a
// query and SemanticMatch pairs the items of two lists. None of them declares a
// schema to the provider. Each reads the model's answer as ids or positions of
// the caller's own items and checks it against the list in Go, so what comes
// back is always the caller's own values; but a level above well formed JSON
// needs a declared schema, and so a client whose DataPolicy sets a floor above
// ContractJSONWellFormed refuses all five with ErrPolicy before anything is
// sent.

// checkWordOf returns the internal error of kind configuration for a value that
// is not one of the words allowed, and nil for an empty value or one that is. The
// error names the field and the words and never the value: the value is the
// caller's text, and the message goes wherever the caller's logs go.
func checkWordOf(op, field, value string, allowed ...string) error {
	if value == "" || slices.Contains(allowed, value) {
		return nil
	}
	return types.Newf(types.KindConfiguration, op, "%s is not one of %s", field, wordList(allowed))
}

// wordList writes words as a person would in a sentence: a, b and c.
func wordList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// layFloor lays a number whose zero means the default and whose negative means
// none onto dst: a positive value replaces the default, a negative one becomes
// zero, and zero leaves the default as it is. The number has been checked.
func layFloor(dst *float64, v float64) {
	switch {
	case v > 0:
		*dst = v
	case v < 0:
		*dst = 0
	}
}

// layLimit is layFloor for a count: a positive value replaces the default, a
// negative one becomes zero, which the internal option reads as no limit, and
// zero leaves the default.
func layLimit(dst *int, v int) {
	switch {
	case v > 0:
		*dst = v
	case v < 0:
		*dst = 0
	}
}

// ChooseOptions are Choose's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ChooseOptions{Model: "gpt-6-luna", Criteria: []string{"cheapest"}}.
type ChooseOptions struct {
	Options

	// Criteria are what to choose by. Nil means the model's judgement of "best".
	Criteria []string

	// Strategy is "sequential", "tournament" or "scoring". Empty means
	// "scoring". It is guidance to the model and nothing checks that it was
	// followed. Any other value is a configuration error.
	Strategy string

	// OmitReasoning withholds the instruction to reason about the choice. False
	// keeps it. The reasoning is not returned either way: the answer is the id of
	// the option and nothing else.
	OmitReasoning bool
}

// mergeChooseOptions merges the values of a call (M0).
func mergeChooseOptions(opts []ChooseOptions) ChooseOptions {
	return mergeValues(opts, func(o, later ChooseOptions) ChooseOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Criteria, later.Criteria)
		lay(&o.Strategy, later.Strategy)
		lay(&o.OmitReasoning, later.OmitReasoning)
		return o
	})
}

// checkChooseOptions returns the internal error of a setting no choice can use,
// or nil.
func checkChooseOptions(o ChooseOptions) error {
	if err := o.common().Check("choose"); err != nil {
		return err
	}
	return checkWordOf("choose", "Strategy", o.Strategy, "sequential", "tournament", "scoring")
}

// chooseInternal builds the internal options of a Choose call on ctx. The
// constructor's TopN of 1 stays: Choose returns one option.
func chooseInternal(ctx context.Context, o ChooseOptions) ops.ChooseOptions {
	opts := ops.NewChooseOptions()
	laySlice(&opts.Criteria, o.Criteria)
	lay(&opts.Strategy, o.Strategy)
	turnOff(&opts.RequireReasoning, o.OmitReasoning)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Choose returns the one option that best meets the criteria. It is always one
// of the options given: the model answers with an id the call assigned, and an id
// that was not offered is an error of kind ErrInvalidAnswer. One option is
// returned without a call. Default tier Fast, mode Transform.
//
//	best, err := schemaflux.Choose(ctx, laptops, schemaflux.ChooseOptions{Criteria: []string{"under $1500", "lightest"}})
//
// No options is an error of kind ErrInvalidRequest, returned before anything is
// sent, and so is an option that cannot be written as JSON. The Record of the
// call is "choose", delivered at ContractJSONWellFormed: the answer is parsed and
// its id is checked against the options offered, but no schema is declared to the
// provider, so a client whose DataPolicy sets a higher floor refuses the call with
// ErrPolicy before anything is sent.
func Choose[T any](ctx context.Context, options []T, opts ...ChooseOptions) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "choose")
	if err != nil {
		return zero, err
	}
	o := mergeChooseOptions(opts)
	if err := refuseEmpty(&call, "options", len(options)); err != nil {
		return zero, err
	}
	if err := checkChooseOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		best, err := ops.Choose(options, chooseInternal(call.Context(), o))
		return best, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (T, error) {
		return ops.Choose(options, chooseInternal(ctx, o))
	})
}

// FilterOptions are Filter's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: FilterOptions{Model: "gpt-6-luna", Exclude: true}.
type FilterOptions struct {
	Options

	// Exclude removes the items that meet the criteria and returns the rest.
	// False keeps the items that meet them.
	Exclude bool

	// MinConfidence asks the model to act only on items it is at least this sure
	// about. It is an instruction and is not checked: the answer is a list of ids
	// and carries no confidence to check it against. It is a number up to 1. Zero
	// means 0.7, and a negative value leaves the instruction out. A value above 1,
	// or one that is not a number, is a configuration error.
	MinConfidence float64

	// IncludeReasons asks the model to reason about each item. The reasons are not
	// returned. False does not ask.
	IncludeReasons bool
}

// mergeFilterOptions merges the values of a call (M0).
func mergeFilterOptions(opts []FilterOptions) FilterOptions {
	return mergeValues(opts, func(o, later FilterOptions) FilterOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Exclude, later.Exclude)
		lay(&o.MinConfidence, later.MinConfidence)
		lay(&o.IncludeReasons, later.IncludeReasons)
		return o
	})
}

// checkFilterOptions returns the internal error of a setting no filter can use,
// or nil. The number is checked on the caller's own value, before the meaning of
// its zero and of a negative is applied.
func checkFilterOptions(o FilterOptions) error {
	if err := o.common().Check("filter"); err != nil {
		return err
	}
	return checkFloat("filter", "MinConfidence", o.MinConfidence, math.Inf(-1), 1)
}

// filterInternal builds the internal options of a Filter call on ctx. The
// constructor's BatchSize stays: nothing reads it.
func filterInternal(ctx context.Context, criteria string, o FilterOptions) ops.FilterOptions {
	opts := ops.NewFilterOptions()
	opts.Criteria = criteria
	turnOff(&opts.KeepMatching, o.Exclude)
	layFloor(&opts.MinConfidence, o.MinConfidence)
	lay(&opts.IncludeReasons, o.IncludeReasons)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Filter returns the items that meet the criteria, in their original order. The
// result is always a subset of the items given, made of the caller's own values:
// the model answers with ids the call assigned, and an id that was not offered or
// that is repeated is an error of kind ErrInvalidAnswer. A list too long for one
// answer is split into several calls, each held to its own ids. For no items the
// result is the input slice itself, and nothing is sent. Default tier Fast, mode
// Transform.
//
//	open, err := schemaflux.Filter(ctx, tasks, "not yet completed")
//	rest, err := schemaflux.Filter(ctx, emails, "is a newsletter", schemaflux.FilterOptions{Exclude: true})
//
// Blank criteria are an error of kind ErrInvalidRequest, returned before anything
// is sent, and so is an item that cannot be written as JSON. The Record of the
// call is "filter", delivered at ContractJSONWellFormed: the answer is parsed and
// each id in it is checked against the items offered, but no schema is declared to
// the provider, so a client whose DataPolicy sets a higher floor refuses the call
// with ErrPolicy before anything is sent.
func Filter[T any](ctx context.Context, items []T, criteria string, opts ...FilterOptions) ([]T, error) {
	call, err := bind.Begin(ctx, "filter")
	if err != nil {
		return nil, err
	}
	o := mergeFilterOptions(opts)
	if err := refuseBlank(&call, "criteria", criteria); err != nil {
		return nil, err
	}
	if err := checkFilterOptions(o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		kept, err := ops.Filter(items, filterInternal(call.Context(), criteria, o))
		return kept, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) ([]T, error) {
		return ops.Filter(items, filterInternal(ctx, criteria, o))
	})
}

// SortOptions are Sort's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SortOptions{Model: "gpt-6-luna", Direction: "descending"}.
//
// A long list is not sorted in one answer: with more than 50 items, or after a
// whole-list answer was refused, each item is scored on its own and the order is
// worked out in Go. On that path equal items always keep their order, whatever
// NoStableOrder says, and the model is asked the Direction for each item.
type SortOptions struct {
	Options

	// Direction is "ascending" or "descending". Empty means "ascending". Any
	// other value is a configuration error.
	Direction string

	// SecondaryCriteria break ties, in order. Nil means none.
	SecondaryCriteria []string

	// ComparisonLogic says how to compare two items, in plain language. Empty
	// means none.
	ComparisonLogic string

	// NoStableOrder withholds the instruction to keep equal items in their
	// original order. False keeps it.
	NoStableOrder bool

	// IncludeScores asks the model to weigh a score for each item. The scores are
	// not returned. False does not ask.
	IncludeScores bool
}

// mergeSortOptions merges the values of a call (M0).
func mergeSortOptions(opts []SortOptions) SortOptions {
	return mergeValues(opts, func(o, later SortOptions) SortOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Direction, later.Direction)
		laySlice(&o.SecondaryCriteria, later.SecondaryCriteria)
		lay(&o.ComparisonLogic, later.ComparisonLogic)
		lay(&o.NoStableOrder, later.NoStableOrder)
		lay(&o.IncludeScores, later.IncludeScores)
		return o
	})
}

// checkSortOptions returns the internal error of a setting no sort can use, or
// nil.
func checkSortOptions(o SortOptions) error {
	if err := o.common().Check("sort"); err != nil {
		return err
	}
	return checkWordOf("sort", "Direction", o.Direction, "ascending", "descending")
}

// sortInternal builds the internal options of a Sort call on ctx.
func sortInternal(ctx context.Context, criteria string, o SortOptions) ops.SortOptions {
	opts := ops.NewSortOptions()
	opts.Criteria = criteria
	lay(&opts.Direction, o.Direction)
	laySlice(&opts.SecondaryCriteria, o.SecondaryCriteria)
	lay(&opts.ComparisonLogic, o.ComparisonLogic)
	turnOff(&opts.Stable, o.NoStableOrder)
	lay(&opts.IncludeScores, o.IncludeScores)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// Sort returns the items in the order the criteria describe. The result is always
// a permutation of the items given, made of the caller's own values: the model
// answers with ids the call assigned, and an answer that leaves one out or repeats
// one is refused. A long list is scored item by item and ordered in Go; the
// Record's Strategy says which path ran: "trivial" for fewer than two items,
// "whole-list", "scoring", or "scoring-fallback" when a whole-list answer was
// refused. Default tier Fast, mode Transform.
//
//	ranked, err := schemaflux.Sort(ctx, issues, "most urgent first")
//	ranked, err := schemaflux.Sort(ctx, issues, "severity", schemaflux.SortOptions{Direction: "descending"})
//
// Blank criteria are an error of kind ErrInvalidRequest, returned before anything
// is sent, and so is an item that cannot be written as JSON. The Record of the
// call is "sort", delivered at ContractJSONWellFormed: the answer is parsed and
// held to a permutation in Go, but no schema is declared to the provider, so a
// client whose DataPolicy sets a higher floor refuses the call with ErrPolicy
// before anything is sent.
func Sort[T any](ctx context.Context, items []T, criteria string, opts ...SortOptions) ([]T, error) {
	call, err := bind.Begin(ctx, "sort")
	if err != nil {
		return nil, err
	}
	o := mergeSortOptions(opts)
	if err := refuseBlank(&call, "criteria", criteria); err != nil {
		return nil, err
	}
	if err := checkSortOptions(o); err != nil {
		return nil, call.End(nil, err)
	}
	if call.Plain() {
		sorted, err := ops.Sort(items, sortInternal(call.Context(), criteria, o))
		return sorted, call.End(nil, err)
	}
	// The twin runs the same execution and reports which path it took and what it
	// checked, which the Record carries.
	res, err := ops.SortResult(items, sortInternal(call.Context(), criteria, o))
	return res.Value, call.End(&res.Meta, err)
}

// RankOptions are Rank's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: RankOptions{Model: "gpt-6-luna", TopK: 5}.
type RankOptions struct {
	Options

	// TopK is the most items to return. Zero means all. A negative value is a
	// configuration error.
	TopK int

	// MinScore drops an item scored below it, from 0 to 1. Zero keeps all. A value
	// outside 0 to 1, or one that is not a number, is a configuration error.
	MinScore float64

	// Factors are what to weigh. Nil means the model's judgement of relevance.
	Factors []string

	// FactorWeights asks the model to weigh a factor by this much, keyed by
	// factor; every key has to be in Factors. Go does not enforce the weights. Nil
	// means equal. A key that is not in Factors, or a weight that is not a number,
	// is a configuration error.
	FactorWeights map[string]float64

	// BoostFields asks the model to raise items by an attribute, keyed by field;
	// Go does not enforce it. Nil means none. A weight that is not a number is a
	// configuration error.
	BoostFields map[string]float64

	// PenalizeFields asks the model to lower items by an attribute, keyed by
	// field; Go does not enforce it. Nil means none. A weight that is not a
	// number is a configuration error.
	PenalizeFields map[string]float64

	// IncludeExplanation asks for a reason for each item and refuses an answer
	// that leaves one out. False does not ask.
	IncludeExplanation bool
}

// RankResult is the items scored against a query, best first.
type RankResult[T any] struct {
	// Items are the items kept, best first: ordered here by the model's scores,
	// and cut by TopK and MinScore.
	Items []RankedItem[T] `json:"items"`

	// Query is the query that was ranked against.
	Query string `json:"query"`

	// TotalItems is the number of items given, len(items).
	TotalItems int `json:"total_items"`

	// ReturnedItems is the number of items kept, len(Items).
	ReturnedItems int `json:"returned_items"`
}

// RankedItem is one item of a ranking.
type RankedItem[T any] struct {
	// Item is the caller's own value.
	Item T `json:"item"`

	// Index is the item's position in the input.
	Index int `json:"index"`

	// Rank is the item's place in the result, from 1, which is best.
	Rank int `json:"rank"`

	// Score is the model's own score for the item, from 0 to 1. It is not a
	// measurement.
	Score float64 `json:"score"`

	// Explanation is why, in the model's words. Empty when the model gave none.
	Explanation string `json:"explanation,omitempty"`

	// FactorScores are the model's own scores for each factor, when it gave them.
	// Nil when it gave none.
	FactorScores map[string]float64 `json:"factor_scores,omitempty"`
}

// mergeRankOptions merges the values of a call (M0).
func mergeRankOptions(opts []RankOptions) RankOptions {
	return mergeValues(opts, func(o, later RankOptions) RankOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.TopK, later.TopK)
		lay(&o.MinScore, later.MinScore)
		laySlice(&o.Factors, later.Factors)
		layMap(&o.FactorWeights, later.FactorWeights)
		layMap(&o.BoostFields, later.BoostFields)
		layMap(&o.PenalizeFields, later.PenalizeFields)
		lay(&o.IncludeExplanation, later.IncludeExplanation)
		return o
	})
}

// checkRankOptions returns the internal error of a setting no ranking can use, or
// nil. The messages name the field and never a key of a map, which is the
// caller's text.
func checkRankOptions(o RankOptions) error {
	if err := o.common().Check("rank"); err != nil {
		return err
	}
	if err := checkCount("rank", "TopK", o.TopK); err != nil {
		return err
	}
	if err := checkFloat("rank", "MinScore", o.MinScore, 0, 1); err != nil {
		return err
	}
	for factor := range o.FactorWeights {
		if !slices.Contains(o.Factors, factor) {
			return types.Newf(types.KindConfiguration, "rank", "FactorWeights has a key that is not in Factors")
		}
	}
	for _, weights := range []struct {
		field   string
		numbers map[string]float64
	}{{"FactorWeights", o.FactorWeights}, {"BoostFields", o.BoostFields}, {"PenalizeFields", o.PenalizeFields}} {
		for _, weight := range weights.numbers {
			if math.IsNaN(weight) || math.IsInf(weight, 0) {
				return types.Newf(types.KindConfiguration, "rank", "%s has a weight that is not a finite number", weights.field)
			}
		}
	}
	return nil
}

// rankInternal builds the internal options of a Rank call on ctx. The
// constructor's IncludeScores stays: Rank always returns each item's score.
func rankInternal(ctx context.Context, query string, o RankOptions) ops.RankOptions {
	opts := ops.NewRankOptions()
	opts.Query = query
	lay(&opts.TopK, o.TopK)
	lay(&opts.MinScore, o.MinScore)
	laySlice(&opts.RankingFactors, o.Factors)
	layMap(&opts.FactorWeights, o.FactorWeights)
	layMap(&opts.BoostFields, o.BoostFields)
	layMap(&opts.PenalizeFields, o.PenalizeFields)
	lay(&opts.IncludeExplanation, o.IncludeExplanation)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// rankResultOf is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result. The items are the one part of it that is a list: converting them
// is one allocation, made only when there are some.
func rankResultOf[T any](res ops.RankResult[T]) RankResult[T] {
	out := RankResult[T]{Query: res.Query, TotalItems: res.TotalItems, ReturnedItems: res.ReturnedItems}
	if len(res.Items) > 0 {
		out.Items = make([]RankedItem[T], len(res.Items))
		for i, item := range res.Items {
			out.Items[i] = RankedItem[T](item)
		}
	}
	return out
}

// Rank scores every item for its relevance to a query and returns them best first.
// Every item is covered exactly once, or the answer is an error of kind
// ErrInvalidAnswer, and the order is worked out here from the model's scores and
// not from the order it listed the items in. The result holds the caller's own
// values. For no items the result is empty and nothing is sent. Default tier Fast,
// mode Transform.
//
//	res, err := schemaflux.Rank(ctx, docs, "postgres connection pooling")
//	res, err := schemaflux.Rank(ctx, docs, query, schemaflux.RankOptions{TopK: 5, IncludeExplanation: true})
//	top := res.Items[0].Item
//
// A blank query is an error of kind ErrInvalidRequest, returned before anything is
// sent, and so is an item that cannot be written as JSON. The Record of the call
// is "rank", delivered at ContractJSONWellFormed: the answer is parsed and checked
// against the items offered, but no schema is declared to the provider, so a
// client whose DataPolicy sets a higher floor refuses the call with ErrPolicy
// before anything is sent.
func Rank[T any](ctx context.Context, items []T, query string, opts ...RankOptions) (RankResult[T], error) {
	var zero RankResult[T]
	call, err := bind.Begin(ctx, "rank")
	if err != nil {
		return zero, err
	}
	o := mergeRankOptions(opts)
	if err := refuseBlank(&call, "query", query); err != nil {
		return zero, err
	}
	if err := checkRankOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Rank(items, rankInternal(call.Context(), query, o))
		return rankResultOf(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.RankResult[T], error) {
		return ops.Rank(items, rankInternal(ctx, query, o))
	})
	return rankResultOf(res), err
}

// SemanticMatchOptions are SemanticMatch's settings. The zero value is the
// default.
//
// The common settings of Options come first and are set without naming the
// embedded type: SemanticMatchOptions{Model: "gpt-6-luna", MaxMatches: 3}.
type SemanticMatchOptions struct {
	Options

	// Criteria say what makes a match, in plain language. Empty means similarity of
	// meaning. It is guidance to the model and is not checked.
	Criteria string

	// Strategy is "best-fit", "all-matches", "one-to-one" or "one-to-many". Only
	// "one-to-one" is checked against the answer; the others are guidance. Empty
	// means "best-fit". Any other value is a configuration error.
	Strategy string

	// Threshold is the lowest score a pair may have, from 0 to 1. A pair scored
	// below it is refused, not dropped. Zero means 0.5, and a negative value means
	// 0. A value above 1, or one that is not a number, is a configuration error.
	Threshold float64

	// MaxMatches is the most targets one source may be paired with, and a source
	// given more is refused. Zero means 1, and a negative value means no limit.
	MaxMatches int

	// Fields are the fields to match on. It is guidance to the model and is not
	// checked. Nil means all.
	Fields []string

	// FieldWeights ask the model to weigh a field by this much, keyed by field; Go
	// does not enforce them. Nil means equal.
	FieldWeights map[string]float64

	// OmitExplanations leaves the per-pair explanation out of the request. False
	// asks for it.
	OmitExplanations bool

	// NoPartial refuses a pair the model flags as partial: such a pair is an error
	// of kind ErrInvalidAnswer, not a pair left out. It also leaves the sentence
	// that allows partial matches out of the request. False allows one.
	NoPartial bool

	// Bidirectional refuses a pair that another returned pair beats for its source
	// or for its target. False does not check.
	Bidirectional bool
}

// SemanticMatchResult is the pairs found between two lists, and the items left
// without one.
type SemanticMatchResult[S any, T any] struct {
	// Matches are the pairs.
	Matches []MatchPair[S, T] `json:"matches"`

	// UnmatchedSources are the positions of the sources with no pair. They are
	// exactly the sources that appear in no pair.
	UnmatchedSources []int `json:"unmatched_sources"`

	// UnmatchedTargets are the positions of the targets with no pair.
	UnmatchedTargets []int `json:"unmatched_targets"`

	// TotalMatches is the number of pairs, len(Matches).
	TotalMatches int `json:"total_matches"`

	// AverageScore is the mean of the pairs' scores, worked out here. Zero with no
	// pairs.
	AverageScore float64 `json:"average_score"`
}

// MatchPair is one source paired with one target.
type MatchPair[S any, T any] struct {
	// Source is the caller's own value from the first list.
	Source S `json:"source"`

	// SourceIndex is the source's position in the first list.
	SourceIndex int `json:"source_index"`

	// Target is the caller's own value from the second list.
	Target T `json:"target"`

	// TargetIndex is the target's position in the second list.
	TargetIndex int `json:"target_index"`

	// Score is the model's own score for the pair, from 0 to 1. It is not a
	// measurement.
	Score float64 `json:"score"`

	// Explanation is why the two match, in the model's words. Empty when the model
	// gave none, and when OmitExplanations is set.
	Explanation string `json:"explanation,omitempty"`

	// FieldScores are the model's own scores for each field, when it gave them. Nil
	// when it gave none.
	FieldScores map[string]float64 `json:"field_scores,omitempty"`

	// IsPartial reports that the model flagged the pair as a partial match.
	IsPartial bool `json:"is_partial,omitempty"`
}

// mergeSemanticMatchOptions merges the values of a call (M0).
func mergeSemanticMatchOptions(opts []SemanticMatchOptions) SemanticMatchOptions {
	return mergeValues(opts, func(o, later SemanticMatchOptions) SemanticMatchOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Criteria, later.Criteria)
		lay(&o.Strategy, later.Strategy)
		lay(&o.Threshold, later.Threshold)
		lay(&o.MaxMatches, later.MaxMatches)
		laySlice(&o.Fields, later.Fields)
		layMap(&o.FieldWeights, later.FieldWeights)
		lay(&o.OmitExplanations, later.OmitExplanations)
		lay(&o.NoPartial, later.NoPartial)
		lay(&o.Bidirectional, later.Bidirectional)
		return o
	})
}

// checkSemanticMatchOptions returns the internal error of a setting no match can
// use, or nil. The internal call checks its options only after it has found both
// lists non-empty, so a bad option beside an empty list would pass: the check is
// made here, first.
func checkSemanticMatchOptions(o SemanticMatchOptions) error {
	if err := o.common().Check("semanticmatch"); err != nil {
		return err
	}
	if err := checkWordOf("semanticmatch", "Strategy", o.Strategy, "best-fit", "all-matches", "one-to-one", "one-to-many"); err != nil {
		return err
	}
	return checkFloat("semanticmatch", "Threshold", o.Threshold, math.Inf(-1), 1)
}

// semanticMatchInternal builds the internal options of a SemanticMatch call on
// ctx.
func semanticMatchInternal(ctx context.Context, o SemanticMatchOptions) ops.MatchOptions {
	opts := ops.NewMatchOptions()
	lay(&opts.MatchCriteria, o.Criteria)
	lay(&opts.Strategy, o.Strategy)
	layFloor(&opts.Threshold, o.Threshold)
	layLimit(&opts.MaxMatches, o.MaxMatches)
	laySlice(&opts.MatchFields, o.Fields)
	layMap(&opts.FieldWeights, o.FieldWeights)
	turnOff(&opts.IncludeExplanations, o.OmitExplanations)
	turnOff(&opts.AllowPartial, o.NoPartial)
	lay(&opts.Bidirectional, o.Bidirectional)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// semanticMatchResultOf is the result of the internal call as the root declares
// it. The zero result, which the internal call returns beside an error, converts
// to the zero result. The pairs are the one part of it that is copied: one
// allocation, made only when there are some.
func semanticMatchResultOf[S, T any](res ops.MatchResult[S, T]) SemanticMatchResult[S, T] {
	out := SemanticMatchResult[S, T]{
		UnmatchedSources: res.UnmatchedSources,
		UnmatchedTargets: res.UnmatchedTargets,
		TotalMatches:     res.TotalMatches,
		AverageScore:     res.AverageScore,
	}
	if len(res.Matches) > 0 {
		out.Matches = make([]MatchPair[S, T], len(res.Matches))
		for i, pair := range res.Matches {
			out.Matches[i] = MatchPair[S, T](pair)
		}
	}
	return out
}

// SemanticMatch pairs items of one list with items of another by meaning. It is
// not the control-flow Match of the flow package. The pairs hold the caller's own
// values and their positions; an answer with a position that does not exist, a
// pair twice, a score outside 0 to 1, a pair below Threshold, more than
// MaxMatches for a source, or unmatched lists that are not exactly the items in no
// pair, is an error of kind ErrInvalidAnswer. One source is a list of one. With no
// sources or no targets every item is unmatched and nothing is sent. Default tier
// Fast, mode Transform.
//
//	res, err := schemaflux.SemanticMatch(ctx, resumes, jobs)
//	res, err := schemaflux.SemanticMatch(ctx, []Resume{r}, jobs, schemaflux.SemanticMatchOptions{MaxMatches: 3})
//
// A bad option is an error of kind ErrConfiguration even when a list is empty. The
// Record of the call is "semanticmatch", delivered at ContractJSONWellFormed: the
// answer is parsed and checked against both lists, but no schema is declared to the
// provider, so a client whose DataPolicy sets a higher floor refuses the call with
// ErrPolicy before anything is sent.
func SemanticMatch[S any, T any](ctx context.Context, sources []S, targets []T, opts ...SemanticMatchOptions) (SemanticMatchResult[S, T], error) {
	var zero SemanticMatchResult[S, T]
	call, err := bind.Begin(ctx, "semanticmatch")
	if err != nil {
		return zero, err
	}
	o := mergeSemanticMatchOptions(opts)
	if err := checkSemanticMatchOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.SemanticMatch(sources, targets, semanticMatchInternal(call.Context(), o))
		return semanticMatchResultOf(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.MatchResult[S, T], error) {
		return ops.SemanticMatch(sources, targets, semanticMatchInternal(ctx, o))
	})
	return semanticMatchResultOf(res), err
}
