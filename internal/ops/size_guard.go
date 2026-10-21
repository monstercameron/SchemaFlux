package ops

import (
	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Every collection operation marshals the whole slice into one prompt with no
// chunking and no size guard, while output tokens are capped at 1000–4000 by
// tier. Sort and Filter have to echo every item back, so a Sort over a few
// hundred modest objects cannot physically return a complete result: the
// completion truncates, the JSON fails to parse, and the caller gets a parse
// error that says nothing about the real cause.
//
// The documented examples all use three-item slices, which is the only size
// range where that design works.
//
// Chunking with a merge step is the real fix and it is scheduled as OP-108.
// What this does is refuse the call that cannot succeed, and say why — which is
// the difference between a limit and a trap.

// charactersPerToken is the approximation estimateTokens divides bytes by.
// Counting tokens exactly needs the model's tokeniser, which this library does
// not have. The figure is for English prose; dense JSON and text in scripts
// that need several bytes a character carry more tokens than this counts, so the
// estimate can be low and a call it admits can still be truncated.
const charactersPerToken = 4

// estimateTokens approximates the token count of a rendered payload: its bytes
// over charactersPerToken, rounded up.
func estimateTokens(payload string) int {
	return (len(payload) + charactersPerToken - 1) / charactersPerToken
}

// checkEchoBudget refuses a collection call whose result cannot fit in the
// output budget.
//
// echoFactor is the share of the input the operation has to return: 1.0 for
// Sort and a full-pass Filter, which echo everything, and smaller for
// operations that return a subset or a single item.
func checkEchoBudget(operation string, payload string, itemCount int, intelligence types.Speed, echoFactor float64) error {
	if itemCount == 0 {
		return nil
	}

	maxTokens := config.GetMaxTokens(intelligence)
	if maxTokens <= 0 {
		return nil
	}

	needed := int(float64(estimateTokens(payload)) * echoFactor)
	if needed <= maxTokens {
		return nil
	}

	// Roughly how many items would fit, so the error names a number the caller
	// can act on rather than only a number they cannot.
	//
	// It is the share of the items the budget covers, itemCount*maxTokens/needed.
	// Dividing needed by itemCount first rounded the per-item cost down to zero
	// for a small echo factor over many items, and the error then said every
	// item would fit.
	fits := int(int64(itemCount) * int64(maxTokens) / int64(needed))

	return types.Newf(types.KindContextTooLarge, operation,
		"cannot return %d items within the %d-token output budget for the %s tier: "+
			"the result needs roughly %d tokens, so about %d items would fit. "+
			"Reduce the batch, raise the tier, or set SCHEMAFLUX_MODEL to a model with a larger output budget",
		itemCount, maxTokens, intelligence.String(), needed, fits)
}
