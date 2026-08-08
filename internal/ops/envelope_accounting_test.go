package ops

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// The envelope reported exactly double the tokens and double the money for
// every request this library made, because MetaFrom seeded Usage and Cost from
// the last record and the loop then added every record including that one.
//
// It went unnoticed because nothing compared the envelope against the figures
// that went into it. Every test here does exactly that: it states the inputs
// and the arithmetic they imply, so an accumulator that counts a record twice
// -- or misses one -- fails rather than merely producing a plausible number.

func TestASingleAttemptReportsExactlyWhatTheProviderReported(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{{
		TokenUsage: &types.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 10,
			TotalTokens:      110,
			CachedTokens:     40,
			ReasoningTokens:  5,
		},
		CostInfo: &types.CostInfo{TotalCost: 0.5, PromptCost: 0.4, CompletionCost: 0.1, Priced: true},
	}}, "extract")

	if meta.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want the 100 reported", meta.Usage.PromptTokens)
	}
	if meta.Usage.CompletionTokens != 10 {
		t.Errorf("CompletionTokens = %d, want 10", meta.Usage.CompletionTokens)
	}
	if meta.Usage.TotalTokens != 110 {
		t.Errorf("TotalTokens = %d, want 110", meta.Usage.TotalTokens)
	}
	if meta.Usage.CachedTokens != 40 {
		t.Errorf("CachedTokens = %d, want 40", meta.Usage.CachedTokens)
	}
	if meta.Usage.ReasoningTokens != 5 {
		t.Errorf("ReasoningTokens = %d, want 5", meta.Usage.ReasoningTokens)
	}
	if meta.Cost.TotalCost != 0.5 {
		t.Errorf("TotalCost = %v, want the 0.5 that was actually charged", meta.Cost.TotalCost)
	}
	if meta.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", meta.Attempts)
	}
}

func TestThreeAttemptsSumToTheThreeAttempts(t *testing.T) {
	records := []*types.ResultMetadata{
		{TokenUsage: &types.TokenUsage{PromptTokens: 100, CompletionTokens: 5, TotalTokens: 105},
			CostInfo: &types.CostInfo{TotalCost: 0.10, Priced: true}},
		{TokenUsage: &types.TokenUsage{PromptTokens: 200, CompletionTokens: 10, TotalTokens: 210},
			CostInfo: &types.CostInfo{TotalCost: 0.20, Priced: true}},
		{TokenUsage: &types.TokenUsage{PromptTokens: 300, CompletionTokens: 15, TotalTokens: 315},
			CostInfo: &types.CostInfo{TotalCost: 0.30, Priced: true}},
	}

	meta := envelopeFrom(records, "extract")

	if meta.Usage.PromptTokens != 600 {
		t.Errorf("PromptTokens = %d, want 600 (100+200+300)", meta.Usage.PromptTokens)
	}
	if meta.Usage.CompletionTokens != 30 {
		t.Errorf("CompletionTokens = %d, want 30", meta.Usage.CompletionTokens)
	}
	if meta.Usage.TotalTokens != 630 {
		t.Errorf("TotalTokens = %d, want 630", meta.Usage.TotalTokens)
	}
	// Floating point, so compare with a tolerance rather than pretending.
	if diff := meta.Cost.TotalCost - 0.60; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalCost = %v, want 0.60 (0.10+0.20+0.30)", meta.Cost.TotalCost)
	}
	if meta.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", meta.Attempts)
	}
}

// The identity of the answer comes from the last attempt -- that is the call
// that produced it -- while the figures come from all of them. Seeding the
// identity is what caused the double count, so this pins that the seeding
// still happens for the fields it is supposed to.
func TestIdentityComesFromTheAttemptThatAnswered(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{Model: "first-model", Provider: "first", RequestID: "req-1"},
		{Model: "second-model", Provider: "second", RequestID: "req-2"},
	}, "extract")

	if meta.Model != "second-model" || meta.Provider != "second" || meta.RequestID != "req-2" {
		t.Errorf("identity = %s/%s/%s, want the last attempt's",
			meta.Provider, meta.Model, meta.RequestID)
	}
}

func TestPricingSourceSurvivesTheAccumulation(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{{
		TokenUsage: &types.TokenUsage{PromptTokens: 10, TotalTokens: 10},
		CostInfo:   &types.CostInfo{TotalCost: 0.01, Priced: true, PricingSource: "builtin"},
	}}, "extract")

	if !meta.Cost.Priced {
		t.Fatal("a priced call came out unpriced")
	}
	if meta.Cost.PricingSource != "builtin" {
		t.Errorf("PricingSource = %q, want %q -- it names where the rate card came from, and the loop never sets it",
			meta.Cost.PricingSource, "builtin")
	}
}

// One unpriced attempt makes the whole figure an underestimate, so the total
// is marked unpriced rather than reported as exact.
func TestOneUnpricedAttemptMakesTheWholeFigureUnpriced(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{TokenUsage: &types.TokenUsage{PromptTokens: 100}, CostInfo: &types.CostInfo{TotalCost: 0.10, Priced: true}},
		{TokenUsage: &types.TokenUsage{PromptTokens: 100}, CostInfo: &types.CostInfo{Priced: false}},
	}, "extract")

	if meta.Cost.Priced {
		t.Error("a request containing an unpriced attempt reported an exact cost")
	}
	if meta.Cost.PricingSource != "" {
		t.Errorf("PricingSource = %q on an unpriced total", meta.Cost.PricingSource)
	}
	// The attempts still count, which is the defect the current comment in
	// callrecord.go describes: costs stop accumulating, the count does not.
	if meta.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", meta.Attempts)
	}
}

func TestAnAttemptThatReportedNoUsageContributesNothing(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{TokenUsage: &types.TokenUsage{PromptTokens: 100, TotalTokens: 100},
			CostInfo: &types.CostInfo{TotalCost: 0.10, Priced: true}},
		{}, // a failed attempt the provider told us nothing about
	}, "extract")

	if meta.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", meta.Usage.PromptTokens)
	}
	if meta.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 -- an attempt happened even if nothing was reported", meta.Attempts)
	}
}

func TestNoRecordsProducesAnEmptyEnvelopeNamingTheOperation(t *testing.T) {
	meta := envelopeFrom(nil, "extract")

	if meta.Operation != "extract" {
		t.Errorf("Operation = %q", meta.Operation)
	}
	if meta.Attempts != 0 || meta.Usage.TotalTokens != 0 || meta.Cost.TotalCost != 0 {
		t.Errorf("an envelope with no records reported activity: %+v", meta)
	}
}

// RetryCount inside a record is retries beyond the first, so a record that
// retried twice is three attempts.
func TestRetriesInsideARecordCountAsAttempts(t *testing.T) {
	meta := envelopeFrom([]*types.ResultMetadata{
		{RetryCount: 2, TokenUsage: &types.TokenUsage{PromptTokens: 10}},
	}, "extract")

	if meta.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3 for a record with RetryCount 2", meta.Attempts)
	}
}
