package ops

import (
	"fmt"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
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

// charactersPerToken is a deliberately conservative approximation. Counting
// tokens exactly needs the model's tokeniser, which this library does not have;
// under-estimating the budget is the safe direction, because the cost of being
// wrong is a refused call rather than a truncated answer.
const charactersPerToken = 4

// estimateTokens approximates the token count of a rendered payload.
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
	perItem := needed / itemCount
	fits := itemCount
	if perItem > 0 {
		fits = maxTokens / perItem
	}

	return fmt.Errorf(
		"%s cannot return %d items within the %d-token output budget for the %s tier: "+
			"the result needs roughly %d tokens, so about %d items would fit. "+
			"Reduce the batch, raise the tier, or set SCHEMAFLUX_MODEL to a model with a larger output budget",
		operation, itemCount, maxTokens, intelligence.String(), needed, fits)
}
