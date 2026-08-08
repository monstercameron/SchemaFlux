package types

import (
	"errors"
	"testing"
)

// PL-013: per-item metrics. These exercise BatchResult.Metrics() directly,
// by hand-building BatchResult/ItemResult values -- no provider, no engine
// -- because the function under test is a pure derivation from
// r.Summary/r.Items (see its own doc comment) and every one of PL-013's named
// numbers (valid-item ratio, omissions, repairs, atomic fallbacks, cost per
// accepted item) needs to be provable in isolation from RunOpManyPartial and
// RunOpManyRecover, which internal/ops's own tests already cover end to end.

// succeeded is a small constructor to keep the cases below focused on the
// field each one is testing rather than repeating every ItemResult field.
func succeeded(index int, mode string, attempts, repairs int, cost CostInfo) ItemResult[string] {
	return ItemResult[string]{Index: index, Status: ItemSucceeded, Value: "v", Mode: mode, Attempts: attempts, Repairs: repairs, Cost: cost}
}

func failed(index int, mode string, attempts int, cost CostInfo) ItemResult[string] {
	return ItemResult[string]{Index: index, Status: ItemFailed, Mode: mode, Attempts: attempts, Cost: cost, Err: errors.New("boom")}
}

func TestBatchMetricsEmptyBatchReportsZeroRatioNotDivideByZero(t *testing.T) {
	r := BatchResult[string]{Summary: BatchSummary{Total: 0}}
	m := r.Metrics()
	if m.Total != 0 || m.ValidItemRatio != 0 {
		t.Fatalf("m = %+v, want Total 0 and ValidItemRatio 0 for an empty batch", m)
	}
	if m.CostPriced {
		t.Fatal("CostPriced = true for a batch with no items")
	}
	if m.CostQuality != PricingUnknown {
		t.Fatalf("CostQuality = %q, want PricingUnknown for a batch with no items", m.CostQuality)
	}
}

func TestBatchMetricsAllSucceededUnpricedRatioIsOneCostIsUnknown(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 3, Succeeded: 3},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{}),
			succeeded(1, "atomic", 1, 0, CostInfo{}),
			succeeded(2, "atomic", 1, 0, CostInfo{}),
		},
	}
	m := r.Metrics()
	if m.ValidItemRatio != 1 {
		t.Fatalf("ValidItemRatio = %v, want 1", m.ValidItemRatio)
	}
	if m.CostPriced {
		t.Fatal("CostPriced = true from items that never carried a rate card")
	}
	if m.CostPerAcceptedItem != 0 {
		t.Fatalf("CostPerAcceptedItem = %v, want 0 (unset) on an unpriced batch, per PL-013's own trap about types.PricingQuality", m.CostPerAcceptedItem)
	}
	if m.CostQuality != PricingUnknown {
		t.Fatalf("CostQuality = %q, want PricingUnknown -- a zero here reads as free, which is the lie this field exists to prevent", m.CostQuality)
	}
}

func TestBatchMetricsMixedOutcomesValidRatioIsSucceededOverTotal(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 5, Succeeded: 2, Failed: 2, Cancelled: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{}),
			succeeded(1, "atomic", 1, 0, CostInfo{}),
			failed(2, "atomic", 1, CostInfo{}),
			failed(3, "atomic", 1, CostInfo{}),
			{Index: 4, Status: ItemCancelled},
		},
	}
	m := r.Metrics()
	if m.ValidItemRatio != 0.4 {
		t.Fatalf("ValidItemRatio = %v, want 0.4 (2/5)", m.ValidItemRatio)
	}
	if m.Succeeded != 2 || m.Failed != 2 || m.Cancelled != 1 {
		t.Fatalf("m = %+v, want Succeeded 2 Failed 2 Cancelled 1", m)
	}
}

func TestBatchMetricsAcceptedCostSumsOnlySucceededItems(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Succeeded: 1, Failed: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.10, Quality: PricingExact}),
			failed(1, "atomic", 1, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.07, Quality: PricingExact}),
		},
	}
	m := r.Metrics()
	if !m.CostPriced {
		t.Fatal("CostPriced = false with a priced item present")
	}
	if m.AcceptedCost != 0.10 {
		t.Fatalf("AcceptedCost = %v, want 0.10 (the succeeded item only)", m.AcceptedCost)
	}
	if m.FailedCost != 0.07 {
		t.Fatalf("FailedCost = %v, want 0.07 -- a failed item's call was still billed, and folding it into AcceptedCost would make a worse batch look cheaper per unit of value", m.FailedCost)
	}
	if m.CostPerAcceptedItem != 0.10 {
		t.Fatalf("CostPerAcceptedItem = %v, want 0.10 (AcceptedCost / 1 succeeded)", m.CostPerAcceptedItem)
	}
}

func TestBatchMetricsCostPerAcceptedItemDividesByAcceptedCountNotTotal(t *testing.T) {
	// The trap PL-013 exists to catch: a batch where half the items came
	// back invalid costs twice as much per useful answer as the totals
	// suggest. AcceptedCost / Total would understate that; the field must
	// divide by Succeeded.
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 4, Succeeded: 2, Failed: 2},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 1.00, Quality: PricingExact}),
			succeeded(1, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 1.00, Quality: PricingExact}),
			failed(2, "atomic", 1, CostInfo{Priced: true, Currency: "USD", TotalCost: 1.00, Quality: PricingExact}),
			failed(3, "atomic", 1, CostInfo{Priced: true, Currency: "USD", TotalCost: 1.00, Quality: PricingExact}),
		},
	}
	m := r.Metrics()
	if m.CostPerAcceptedItem != 1.0 {
		t.Fatalf("CostPerAcceptedItem = %v, want 1.0 (AcceptedCost 2.0 / Succeeded 2), not 1.0 (4.0 / Total 4) which would hide that half the spend produced nothing usable", m.CostPerAcceptedItem)
	}
}

func TestBatchMetricsWhollyFailedPricedBatchLeavesCostPerAcceptedItemUnknown(t *testing.T) {
	// A batch can be priced overall (every call was billed) while having
	// zero succeeded items -- the denominator PL-013's own number needs is
	// zero, and dividing AcceptedCost (also zero here) by zero must not
	// silently read as "free".
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Failed: 2},
		Items: []ItemResult[string]{
			failed(0, "atomic", 1, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.50, Quality: PricingExact}),
			failed(1, "atomic", 1, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.50, Quality: PricingExact}),
		},
	}
	m := r.Metrics()
	if !m.CostPriced {
		t.Fatal("CostPriced = false despite two priced failed items")
	}
	if m.CostPerAcceptedItem != 0 {
		t.Fatalf("CostPerAcceptedItem = %v, want 0 (unset) with zero succeeded items to divide into", m.CostPerAcceptedItem)
	}
	if m.CostQuality != PricingUnknown {
		t.Fatalf("CostQuality = %q, want PricingUnknown -- CostPerAcceptedItem is meaningless here, and CostQuality describes exactly that field", m.CostQuality)
	}
}

func TestBatchMetricsEstimatedAcceptedCostDowngradesCostQuality(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Succeeded: 2},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.10, Quality: PricingExact}),
			succeeded(1, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.20, Quality: PricingEstimated}),
		},
	}
	m := r.Metrics()
	if m.CostQuality != PricingEstimated {
		t.Fatalf("CostQuality = %q, want PricingEstimated -- one accepted item's cost is a projection, so the average is too, and a later Exact value must not paper over it", m.CostQuality)
	}
}

func TestBatchMetricsAllExactAcceptedCostKeepsCostQualityExact(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Succeeded: 2},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.10, Quality: PricingExact}),
			succeeded(1, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0.20, Quality: PricingExact}),
		},
	}
	m := r.Metrics()
	if m.CostQuality != PricingExact {
		t.Fatalf("CostQuality = %q, want PricingExact when every accepted item's cost came from reported usage", m.CostQuality)
	}
}

func TestBatchMetricsOmissionsCountsIsolatedMDSPItemsAndAtomicFallbacks(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 3, Succeeded: 3},
		Items: []ItemResult[string]{
			// Resolved on the first MDSP pass: attempts == 1, not an omission.
			succeeded(0, modeMDSP, 1, 0, CostInfo{}),
			// Took an isolate pass to resolve: mode is still "mdsp", but
			// attempts > 1 says the first pass dropped it.
			succeeded(1, modeMDSP, 2, 0, CostInfo{}),
			// Never resolved by any MDSP pass; recovered by an isolated
			// atomic call.
			succeeded(2, ModeAtomicFallback, 1, 0, CostInfo{}),
		},
	}
	m := r.Metrics()
	if m.Omissions != 2 {
		t.Fatalf("Omissions = %d, want 2 (the isolate-pass item and the atomic-fallback item)", m.Omissions)
	}
	if m.AtomicFallbacks != 1 {
		t.Fatalf("AtomicFallbacks = %d, want 1", m.AtomicFallbacks)
	}
}

func TestBatchMetricsAtomicFallbackCountsWhetherOrNotItSucceeded(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Succeeded: 1, Failed: 1},
		Items: []ItemResult[string]{
			succeeded(0, ModeAtomicFallback, 1, 0, CostInfo{}),
			failed(1, ModeAtomicFallback, 1, CostInfo{}),
		},
	}
	m := r.Metrics()
	if m.AtomicFallbacks != 2 {
		t.Fatalf("AtomicFallbacks = %d, want 2 -- a batch failing badly enough to fall back on most items must show up here even when a fallback call itself fails", m.AtomicFallbacks)
	}
	if m.Omissions != 2 {
		t.Fatalf("Omissions = %d, want 2", m.Omissions)
	}
}

func TestBatchMetricsAtomicModeItemsAreNeverOmissionsRegardlessOfAttempts(t *testing.T) {
	// RunOpManyPartial's own retry pass can push Attempts above 1 for a
	// plain atomic item -- that is PL-008's retry accounting, not an
	// omission: ShapeAtomic never had a "first pass" that could have
	// dropped it in the first place.
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 1, Succeeded: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 3, 1, CostInfo{}),
		},
	}
	m := r.Metrics()
	if m.Omissions != 0 {
		t.Fatalf("Omissions = %d, want 0 for a plain atomic item retried 3 times", m.Omissions)
	}
}

func TestBatchMetricsTotalAttemptsAndRepairsSumAcrossItems(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 3, Succeeded: 2, Failed: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{}),
			succeeded(1, "atomic", 3, 2, CostInfo{}), // one first pass + two retries, two of which were repairs
			failed(2, "atomic", 2, CostInfo{}),
		},
	}
	m := r.Metrics()
	if m.TotalAttempts != 6 {
		t.Fatalf("TotalAttempts = %d, want 6 (1 + 3 + 2)", m.TotalAttempts)
	}
	if m.TotalRepairs != 2 {
		t.Fatalf("TotalRepairs = %d, want 2", m.TotalRepairs)
	}
}

func TestBatchMetricsCurrencyReadFromPricedItems(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 1, Succeeded: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "EUR", TotalCost: 0.05, Quality: PricingExact}),
		},
	}
	m := r.Metrics()
	if m.Currency != "EUR" {
		t.Fatalf("Currency = %q, want EUR", m.Currency)
	}
}

func TestBatchMetricsFreeModelIsPricedAndKnownNotUnpriced(t *testing.T) {
	// PricingFree (a rate card that prices everything at zero -- a local or
	// mock provider) must read as known and $0.00, distinct from
	// PricingUnknown (no rate card at all, cost fields zero because nothing
	// is known). Collapsing the two would either hide a working free
	// configuration behind an "unknown" warning or, worse, the reverse.
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 1, Succeeded: 1},
		Items: []ItemResult[string]{
			succeeded(0, "atomic", 1, 0, CostInfo{Priced: true, Currency: "USD", TotalCost: 0, Quality: PricingFree}),
		},
	}
	m := r.Metrics()
	if !m.CostPriced {
		t.Fatal("CostPriced = false for a PricingFree item; free is known, not unknown")
	}
	if m.CostQuality != PricingFree {
		t.Fatalf("CostQuality = %q, want PricingFree", m.CostQuality)
	}
	if m.CostPerAcceptedItem != 0 {
		t.Fatalf("CostPerAcceptedItem = %v, want 0 -- a real, known zero this time, not an unset one", m.CostPerAcceptedItem)
	}
}
