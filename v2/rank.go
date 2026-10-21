package schemaflux

import (
	"context"
	"math"
	"slices"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

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
