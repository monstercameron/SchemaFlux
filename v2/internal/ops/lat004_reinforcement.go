package ops

import (
	"context"
	"strings"
)

// LAT-004. A decision is sent without the reinforcement block.
//
// The block is three lines for a request that asks for text and six for one
// that asks for JSON, added to every request (strengthenSystemPrompt). A
// decision's own instructions already say what it needs: how to read the
// questions, what each kind answers with, and what the object holds. Measured
// live on gpt-6-luna, leaving the block off changed no answer and took 16% off
// the input tokens at reasoning effort none, and 31% off the output tokens and
// 12% off the wait at the default effort.
//
// It is the decision's own choice and not a setting. It travels on the context
// the decision's provider call runs under, so it reaches no other operation and
// the environment switch, which turns the block off for every operation, is
// left as it was.

type dec12WithoutReinforcementKey struct{}

// dec12WithoutReinforcement returns a context whose request is sent without the
// reinforcement block.
func dec12WithoutReinforcement(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, dec12WithoutReinforcementKey{}, true)
}

// dec12ReinforcementDeclined reports whether the context's request is sent
// without the reinforcement block.
func dec12ReinforcementDeclined(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	declined, _ := ctx.Value(dec12WithoutReinforcementKey{}).(bool)
	return declined
}

// dec12StrengthenSystemPrompt is the system prompt a request sends: the
// operation's own, with the reinforcement block in front of it unless the
// operation declined it or the environment turned it off.
func dec12StrengthenSystemPrompt(ctx context.Context, systemPrompt, responseFormat string) string {
	if dec12ReinforcementDeclined(ctx) {
		return strings.TrimSpace(systemPrompt)
	}
	return strengthenSystemPrompt(ctx, systemPrompt, responseFormat)
}
