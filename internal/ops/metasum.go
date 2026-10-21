package ops

import (
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// spend collects the envelope of every request a run of many calls makes, so
// that what the run cost can be reported once, summed, beside the per-item
// accounting. It is added to from the goroutines of a bounded pool.
type spend struct {
	mu    sync.Mutex
	metas []types.Meta
}

// add records one call's envelope. What it keeps is only what sumMetas adds up:
// the checks, the lineage and the model's claims of one call say nothing about a
// total, and a run of thousands of items would otherwise hold all of them.
func (s *spend) add(meta types.Meta) {
	meta.Checks = nil
	meta.ModelClaims = nil
	meta.Provenance = types.Provenance{}
	s.mu.Lock()
	s.metas = append(s.metas, meta)
	s.mu.Unlock()
}

// total is the sum of everything recorded, for the operation.
func (s *spend) total(operation string) types.Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sumMetas(operation, s.metas...)
}

// sumMetas adds the envelopes of the requests one call made into one envelope: a
// batched pass's, each item's own, a retry's. It is the summing that the call
// path does for the requests inside one call (envelopeFrom), at the level of a
// call that was itself made of calls, and it follows the same rules.
//
// Usage, attempts, retries, repairs and cache hits are added. The cost is added
// over the requests that were priced, and is priced only when every request that
// was made was: one unpriced request makes the whole figure an underestimate,
// and its quality is the worst quality of its parts (combinedPricingQuality). A
// request that made no attempt, a refusal before anything was sent, has no vote,
// and neither has one that reported no usage and no price, which is what a failed
// request leaves.
// The timings are added (Timing.Add), which is a sum and not a duration on the
// clock.
//
// What belongs to no single request is left out. Provider, Model, RequestedModel,
// SchemaID and RequestedTier are kept only when every request that was made
// agrees on them, and are empty when they differ; RequestID and CorrelationID are
// the request's own and are always empty. Elapsed is not summed, since requests
// that ran together add up to more time than passed, and the contract levels,
// the strategy, the checks and the lineage describe the call and not its parts:
// the caller that knows them sets them.
func sumMetas(operation string, metas ...types.Meta) types.Meta {
	total := types.Meta{Operation: operation}

	var made []types.Meta
	for _, meta := range metas {
		total.Attempts += meta.Attempts
		total.Repairs += meta.Repairs
		total.Retries += meta.Retries
		total.CacheHits += meta.CacheHits
		total.Hedged = total.Hedged || meta.Hedged
		if meta.ServiceTier != "" {
			total.ServiceTier = meta.ServiceTier
		}
		total.Usage.PromptTokens += meta.Usage.PromptTokens
		total.Usage.CompletionTokens += meta.Usage.CompletionTokens
		total.Usage.TotalTokens += meta.Usage.TotalTokens
		total.Usage.CachedTokens += meta.Usage.CachedTokens
		total.Usage.ReasoningTokens += meta.Usage.ReasoningTokens
		total.Timing = total.Timing.Add(meta.Timing)
		if meta.Attempts > 0 {
			made = append(made, meta)
		}
	}
	if total.Usage.PromptTokens > 0 {
		total.CacheHitRatio = float64(total.Usage.CachedTokens) / float64(total.Usage.PromptTokens)
	}
	if len(made) == 0 {
		return total
	}

	priced := true
	var voters []*types.ResultMetadata
	for i := range made {
		cost := &made[i].Cost
		if !cost.Priced && made[i].Usage == (types.TokenUsage{}) {
			// A request that reported nothing about what it cost casts no vote
			// either way. A failed request's record carries nothing, and the call
			// path leaves it out of its own sums in the same way (envelopeFrom):
			// what a failed request was billed is not known, and is not guessed.
			continue
		}
		voters = append(voters, &types.ResultMetadata{CostInfo: cost})
		if !cost.Priced {
			priced = false
			continue
		}
		if len(voters) == 1 || total.Cost.Currency == "" {
			total.Cost.Currency = cost.Currency
			total.Cost.PricingSource = cost.PricingSource
		}
		total.Cost.TotalCost += cost.TotalCost
		total.Cost.PromptCost += cost.PromptCost
		total.Cost.CompletionCost += cost.CompletionCost
		total.Cost.CachedCost += cost.CachedCost
		total.Cost.ReasoningCost += cost.ReasoningCost
	}
	total.Cost.Priced = priced && len(voters) > 0
	if !total.Cost.Priced {
		total.Cost.PricingSource = ""
	}
	total.Cost.Quality = combinedPricingQuality(voters)

	total.Provider = agreed(made, func(m types.Meta) string { return m.Provider })
	total.Model = agreed(made, func(m types.Meta) string { return m.Model })
	total.RequestedModel = agreed(made, func(m types.Meta) string { return m.RequestedModel })
	total.SchemaID = agreed(made, func(m types.Meta) string { return m.SchemaID })
	if tier := made[0].RequestedTier; tier != types.TierUnset {
		same := true
		for _, meta := range made[1:] {
			same = same && meta.RequestedTier == tier
		}
		if same {
			total.RequestedTier = tier
		}
	}
	for _, meta := range made {
		total.ModelDrifted = total.ModelDrifted || meta.ModelDrifted
		total.ModelDriftUnknown = total.ModelDriftUnknown || meta.ModelDriftUnknown
	}
	return total
}

// agreed is the value every meta has for a field, or "" when they differ or none
// has one.
func agreed(metas []types.Meta, field func(types.Meta) string) string {
	first := field(metas[0])
	for _, meta := range metas[1:] {
		if field(meta) != first {
			return ""
		}
	}
	return first
}
