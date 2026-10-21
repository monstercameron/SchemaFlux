package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
