package ops

import (
	"context"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Carrying the execution record back to the caller.
//
// An operation returns `(T, error)`, and the record of how the answer was
// produced -- attempts, usage, cost, which contract was delivered -- was built
// inside CallLLM and thrown away. Getting it out meant changing the return type
// of every operation at once.
//
// So it travels on the context instead. An operation that wants an envelope
// attaches a collector before it calls; the ones that do not are unchanged, and
// both run the identical path. That property is the whole reason to do it this
// way rather than writing a second code path for detailed results: two return
// types that execute differently is how the two drift, and this library has
// four such pairs already (T-01).
//
// This is a bridge to A-001's descriptor, not a destination. When every
// operation lowers to one executor, the executor returns the envelope and this
// goes away.

type callRecordKey struct{}

// callRecords collects what one logical request's provider calls reported.
type callRecords struct {
	mu      sync.Mutex
	records []*types.ResultMetadata
}

func (c *callRecords) add(metadata *types.ResultMetadata) {
	if c == nil || metadata == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, metadata)
}

// collect returns the records observed so far, oldest first.
func (c *callRecords) collect() []*types.ResultMetadata {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*types.ResultMetadata(nil), c.records...)
}

// withCallRecording returns a context that collects provider-call records.
func withCallRecording(ctx context.Context) (context.Context, *callRecords) {
	if ctx == nil {
		ctx = context.Background()
	}
	records := &callRecords{}
	return context.WithValue(ctx, callRecordKey{}, records), records
}

// publishCallRecord hands a record to the collector, if there is one.
func publishCallRecord(ctx context.Context, metadata *types.ResultMetadata) {
	if ctx == nil {
		return
	}
	if records, ok := ctx.Value(callRecordKey{}).(*callRecords); ok {
		records.add(metadata)
	}
}

// envelopeFrom folds a request's call records into one Meta.
//
// Usage and cost sum across attempts, because a caller asking what an answer
// cost means the answer, not the last try at it. That is the difference between
// a bill a caller recognises and one they dispute.
func envelopeFrom(records []*types.ResultMetadata, operation string) types.Meta {
	if len(records) == 0 {
		return types.Meta{Operation: operation}
	}

	// The last record carries the identity of the call that actually produced
	// the answer -- model, provider, request ID.
	meta := types.MetaFrom(records[len(records)-1])
	meta.Operation = operation

	// Everything the loop below accumulates has to start at zero, because
	// MetaFrom already copied the last record's figures in as the seed. It did
	// not for Attempts -- that reset was here from the start -- but Usage and
	// Cost were left seeded and then added again, so the final attempt was
	// counted twice: every single-call operation reported exactly double its
	// tokens and double its money, and a caller reading Meta.Cost was reading a
	// number that was wrong for every request this library has ever made.
	//
	// It went unnoticed because nothing compared the envelope against the
	// provider's own usage figure; the tests asserted that a cost appeared, not
	// that it was the right one.
	meta.Attempts = 0
	meta.Usage = types.TokenUsage{}
	// PricingSource is carried across the reset rather than recomputed: it
	// names where the rate card came from, which is a property of the run and
	// not a figure to be summed, and the loop below never sets it.
	pricingSource := meta.Cost.PricingSource
	meta.Cost = types.CostInfo{PricingSource: pricingSource}

	// Every attempt counts toward the total, and one unpriced attempt makes the
	// whole figure an underestimate.
	//
	// These are two separate facts and were briefly one loop with a `break` in
	// it, which stopped counting attempts the moment an unpriced one appeared:
	// a request that retried twice against a model with no rate card reported
	// one attempt. Costs stop accumulating; the count does not.
	priced := true
	sawCost := false

	for _, record := range records {
		meta.Attempts += record.RetryCount + 1

		if record.TokenUsage != nil {
			meta.Usage.PromptTokens += record.TokenUsage.PromptTokens
			meta.Usage.CompletionTokens += record.TokenUsage.CompletionTokens
			meta.Usage.TotalTokens += record.TokenUsage.TotalTokens
			meta.Usage.CachedTokens += record.TokenUsage.CachedTokens
			meta.Usage.ReasoningTokens += record.TokenUsage.ReasoningTokens
		}

		if record.CostInfo == nil {
			continue
		}
		sawCost = true

		if !record.CostInfo.Priced {
			// Reporting the sum as priced would be the PR-001 mistake one level
			// up: a number that looks exact and is not.
			priced = false
			continue
		}

		meta.Cost.TotalCost += record.CostInfo.TotalCost
		meta.Cost.PromptCost += record.CostInfo.PromptCost
		meta.Cost.CompletionCost += record.CostInfo.CompletionCost
		meta.Cost.CachedCost += record.CostInfo.CachedCost
		meta.Cost.ReasoningCost += record.CostInfo.ReasoningCost
	}

	meta.Cost.Priced = sawCost && priced
	if !meta.Cost.Priced {
		meta.Cost.PricingSource = ""
	}

	// CA-006. Measured from what the provider reported, not estimated: the
	// share of prompt tokens it said came from its own prefix cache. Guarded
	// against a provider that reports usage with no prompt tokens at all,
	// where the ratio is undefined rather than zero.
	if meta.Usage.PromptTokens > 0 {
		meta.CacheHitRatio = float64(meta.Usage.CachedTokens) / float64(meta.Usage.PromptTokens)
	}

	return meta
}
