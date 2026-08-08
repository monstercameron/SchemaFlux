package pricing

// Tests filling coverage gaps left by the other test files: EstimateCost
// (0% -- never called at all), pricingQualityFor's PricingFree branch (only
// reachable with a rate card that is not obtainable through the exported
// RegisterPricingModel, since that function refuses an all-zero card --
// tested here by writing directly into the package-level table, which only
// an in-package test can do), matchesFilters' branches beyond "operation",
// and cloneCostRecord's tag-copying branch.

import (
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// EstimateCost prices a call that has not been made yet and marks the result
// as a projection. Reusing CalculateCost's arithmetic is the point; the only
// thing worth asserting here is that the quality is downgraded to Estimated
// rather than left at Exact, and that an unpriced model still reports
// Unknown rather than being upgraded into looking like a real estimate.
func TestEstimateCostMarksAProjectionRatherThanAMeasurement(t *testing.T) {
	resetPricingTestState(t)

	projected := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500}

	priced := EstimateCost(projected, "gpt-5-mini-2025-08-07", "openai")
	if priced == nil {
		t.Fatal("EstimateCost returned nil for a priced model")
	}
	if !priced.Priced {
		t.Error("a priced model's estimate should still be marked Priced")
	}
	if priced.Quality != types.PricingEstimated {
		t.Errorf("Quality = %v, want PricingEstimated for a projected cost", priced.Quality)
	}
	if priced.TotalCost <= 0 {
		t.Error("expected a positive projected cost for a priced model with nonzero usage")
	}

	unpriced := EstimateCost(projected, "a-model-nobody-has-priced", "openai")
	if unpriced == nil {
		t.Fatal("EstimateCost returned nil for an unpriced model")
	}
	if unpriced.Priced {
		t.Error("an unpriced model must not become Priced just because it was estimated")
	}
	if unpriced.Quality != types.PricingUnknown {
		t.Errorf("Quality = %v, want PricingUnknown for an unpriced projection", unpriced.Quality)
	}

	if got := EstimateCost(nil, "gpt-5", "openai"); got != nil {
		t.Errorf("EstimateCost(nil, ...) = %+v, want nil", got)
	}
}

// pricingQualityFor's PricingFree branch exists for a rate card that is a
// fact -- a local double or a genuinely free tier -- rather than an absent
// one. RegisterPricingModel refuses to register a card priced at zero on
// both prompt and completion (that refusal is tested in register_test.go),
// so the only way to exercise the branch it guards against misreading is to
// place a card directly in the table, the way a free tier's provider
// integration would have to if it ever ships one.
func TestAllZeroRateCardReportsFreeNotUnknown(t *testing.T) {
	resetPricingTestState(t)

	const name = "gap-test-free-tier-model"
	pricingMu.Lock()
	pricingModels[name] = PricingModel{
		Provider: "test",
		Model:    name,
		Currency: "USD",
	}
	pricingMu.Unlock()
	t.Cleanup(func() {
		pricingMu.Lock()
		delete(pricingModels, name)
		pricingMu.Unlock()
	})

	usage := &types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	cost := CalculateCost(usage, name, "test")
	if cost == nil {
		t.Fatal("CalculateCost returned nil for a registered (if free) model")
	}
	if !cost.Priced {
		t.Error("a found rate card -- even an all-zero one -- is a known fact, not an absence: Priced should be true")
	}
	if cost.Quality != types.PricingFree {
		t.Errorf("Quality = %v, want PricingFree for an all-zero rate card", cost.Quality)
	}
	if cost.TotalCost != 0 {
		t.Errorf("TotalCost = %v, want 0 for an all-zero rate card", cost.TotalCost)
	}
}

// matchesFilters is checked by GetTotalCost, GetCostBreakdown's callers, and
// GetRequestCosts/GetCostSummary, but only its "operation" branch was
// exercised elsewhere. Each key it understands gets a matching and a
// mismatching case here, plus the fallback to tags for anything else --
// including a tag that is simply absent from the record.
func TestMatchesFiltersCoversEveryFilterKey(t *testing.T) {
	resetPricingTestState(t)

	record := CostRecord{
		RequestID:     "req-77",
		CorrelationID: "corr-77",
		Operation:     "extract",
		Model:         "gpt-5",
		Provider:      "openai",
		Tags:          map[string]string{"team": "billing"},
	}

	cases := []struct {
		name    string
		filters map[string]string
		want    bool
	}{
		{"request_id match", map[string]string{"request_id": "req-77"}, true},
		{"request_id mismatch", map[string]string{"request_id": "req-99"}, false},
		{"correlation_id match", map[string]string{"correlation_id": "corr-77"}, true},
		{"correlation_id mismatch", map[string]string{"correlation_id": "corr-99"}, false},
		{"model match", map[string]string{"model": "gpt-5"}, true},
		{"model mismatch", map[string]string{"model": "gpt-4"}, false},
		{"provider match", map[string]string{"provider": "openai"}, true},
		{"provider mismatch", map[string]string{"provider": "anthropic"}, false},
		{"operation match", map[string]string{"operation": "extract"}, true},
		{"operation mismatch", map[string]string{"operation": "classify"}, false},
		{"tag match", map[string]string{"team": "billing"}, true},
		{"tag mismatch", map[string]string{"team": "support"}, false},
		{"tag absent from record", map[string]string{"nonexistent-key": "anything"}, false},
		{"no filters", nil, true},
		{"empty filters", map[string]string{}, true},
		{"multiple filters all match", map[string]string{"model": "gpt-5", "provider": "openai"}, true},
		{"multiple filters one mismatches", map[string]string{"model": "gpt-5", "provider": "anthropic"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesFilters(record, tc.filters); got != tc.want {
				t.Errorf("matchesFilters(%+v) = %v, want %v", tc.filters, got, tc.want)
			}
		})
	}
}

// A record's tags survive GetRequestCosts as an independent copy, so a
// caller mutating the returned map cannot corrupt the retained history --
// the branch cloneCostRecord takes only when there is something to copy.
func TestClonedCostRecordTagsAreIndependentOfTheOriginal(t *testing.T) {
	resetPricingTestState(t)

	usage := &types.TokenUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}
	cost := CalculateCost(usage, "gpt-5", "openai")
	TrackCost(cost, &types.ResultMetadata{
		RequestID: "req-tagged",
		Operation: "extract",
		Custom:    map[string]any{"team": "billing"},
	})

	got, ok := GetRequestCost("req-tagged")
	if !ok {
		t.Fatal("expected the tagged record to be retained")
	}
	if got.Tags["team"] != "billing" {
		t.Fatalf("Tags[team] = %q, want billing", got.Tags["team"])
	}

	// Mutate the caller's copy and fetch again: the retained history must be
	// unaffected, which is only true if cloneCostRecord actually copied the
	// map rather than sharing it.
	got.Tags["team"] = "mutated"

	again, ok := GetRequestCost("req-tagged")
	if !ok {
		t.Fatal("expected the tagged record still to be retained")
	}
	if again.Tags["team"] != "billing" {
		t.Errorf("the retained record's tags were mutated through the caller's copy: got %q", again.Tags["team"])
	}
}

// GetTotalCost is the one place a caller decides, in Go, whether a request
// stays inside a budget before it commits to acting on it -- exercised here
// across a since-cutoff that excludes an older record and a filter that
// excludes a different provider, the two axes GetTotalCost combines.
func TestGetTotalCostCombinesSinceAndFilters(t *testing.T) {
	resetPricingTestState(t)

	now := time.Now()
	old := CalculateCost(&types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000}, "gpt-5", "openai")
	TrackCost(old, &types.ResultMetadata{RequestID: "old", Operation: "extract", Provider: "openai", EndTime: now.Add(-2 * time.Hour)})

	recent := CalculateCost(&types.TokenUsage{PromptTokens: 1000, CompletionTokens: 1000}, "gpt-5", "anthropic")
	TrackCost(recent, &types.ResultMetadata{RequestID: "recent", Operation: "extract", Provider: "anthropic", EndTime: now})

	sinceCutoff := GetTotalCost(now.Add(-time.Hour), nil)
	if sinceCutoff <= 0 {
		t.Fatal("expected the recent record's cost to be included")
	}
	// The old record's cost is larger than zero too, so if it leaked through
	// the cutoff this total would be roughly double.
	all := GetTotalCost(time.Time{}, nil)
	if sinceCutoff >= all {
		t.Errorf("since-cutoff total (%v) should exclude the older record and be less than the unfiltered total (%v)", sinceCutoff, all)
	}

	byProvider := GetTotalCost(time.Time{}, map[string]string{"provider": "anthropic"})
	if byProvider <= 0 || byProvider >= all {
		t.Errorf("provider-filtered total = %v, want strictly between 0 and the unfiltered total %v", byProvider, all)
	}
}
