package types

import (
	"errors"
	"strings"
	"testing"
)

func TestItemStatusString(t *testing.T) {
	cases := []struct {
		s    ItemStatus
		want string
	}{
		{ItemUnspecified, "unspecified"},
		{ItemSucceeded, "succeeded"},
		{ItemFailed, "failed"},
		{ItemCancelled, "cancelled"},
		{ItemStatus(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.s.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBatchPolicyString(t *testing.T) {
	cases := []struct {
		p    BatchPolicy
		want string
	}{
		{PolicyUnspecified, "unspecified"},
		{PolicyFailFast, "fail_fast"},
		{PolicyCollectFailures, "collect_failures"},
		{PolicyRetryFailedItems, "retry_failed_items"},
		{PolicyRetryThenCollect, "retry_then_collect"},
		{PolicyRequireAll, "require_all"},
		{BatchPolicy(99), "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.p.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Complete is false the moment any item did not succeed -- failed or
// cancelled, either one breaks it.
func TestBatchResultComplete(t *testing.T) {
	cases := []struct {
		name    string
		summary BatchSummary
		want    bool
	}{
		{"all succeeded", BatchSummary{Total: 3, Succeeded: 3}, true},
		{"one failed", BatchSummary{Total: 3, Succeeded: 2, Failed: 1}, false},
		{"one cancelled", BatchSummary{Total: 3, Succeeded: 2, Cancelled: 1}, false},
		{"empty batch", BatchSummary{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := BatchResult[string]{Summary: tc.summary}
			if got := r.Complete(); got != tc.want {
				t.Errorf("Complete() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Values is the point of PL-008: a caller cannot reach the []Out without
// also being handed whether it is all of them.
func TestBatchResultValuesReturnsSucceededInOrderWithCompleteFlag(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 3, Succeeded: 2, Failed: 1},
		Items: []ItemResult[string]{
			{Index: 0, Status: ItemSucceeded, Value: "a"},
			{Index: 1, Status: ItemFailed, Err: errors.New("boom")},
			{Index: 2, Status: ItemSucceeded, Value: "c"},
		},
	}
	values, complete := r.Values()
	if complete {
		t.Error("Values() reported complete=true despite a failed item")
	}
	if len(values) != 2 || values[0] != "a" || values[1] != "c" {
		t.Errorf("Values() = %v, want [a c] in input order", values)
	}

	allOK := BatchResult[string]{
		Summary: BatchSummary{Total: 1, Succeeded: 1},
		Items:   []ItemResult[string]{{Index: 0, Status: ItemSucceeded, Value: "only"}},
	}
	values, complete = allOK.Values()
	if !complete {
		t.Error("Values() reported complete=false for an all-succeeded batch")
	}
	if len(values) != 1 || values[0] != "only" {
		t.Errorf("Values() = %v, want [only]", values)
	}
}

func TestBatchResultFailuresReturnsNonSucceededInOrder(t *testing.T) {
	r := BatchResult[string]{
		Items: []ItemResult[string]{
			{Index: 0, Status: ItemSucceeded, Value: "a"},
			{Index: 1, Status: ItemFailed},
			{Index: 2, Status: ItemCancelled},
		},
	}
	failures := r.Failures()
	if len(failures) != 2 {
		t.Fatalf("Failures() returned %d items, want 2", len(failures))
	}
	if failures[0].Index != 1 || failures[1].Index != 2 {
		t.Errorf("Failures() = %+v, want indexes 1 then 2 in input order", failures)
	}

	allOK := BatchResult[string]{Items: []ItemResult[string]{{Index: 0, Status: ItemSucceeded}}}
	if got := allOK.Failures(); got != nil {
		t.Errorf("Failures() = %v, want nil when every item succeeded", got)
	}
}

func TestBatchResultStringSummarizesCountsAndPolicyNeverAValue(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 5, Succeeded: 2, Failed: 2, Cancelled: 1, Policy: PolicyRetryThenCollect},
	}
	s := r.String()
	for _, want := range []string{"retry_then_collect", "2/5 succeeded", "2 failed", "1 cancelled"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}

	clean := BatchResult[string]{Summary: BatchSummary{Total: 3, Succeeded: 3, Policy: PolicyFailFast}}
	cleanStr := clean.String()
	if strings.Contains(cleanStr, "failed") || strings.Contains(cleanStr, "cancelled") {
		t.Errorf("String() = %q, a fully-succeeded batch must not mention failed/cancelled", cleanStr)
	}
}

// A result whose summary lags its items is not complete: Complete reads both.
func TestBatchResultCompleteReadsTheItemsAsWellAsTheSummary(t *testing.T) {
	cases := []struct {
		name  string
		items []ItemResult[string]
		sum   BatchSummary
		want  bool
	}{
		{"summary and items agree on success", []ItemResult[string]{{Status: ItemSucceeded}, {Status: ItemSucceeded}}, BatchSummary{Total: 2, Succeeded: 2}, true},
		{"a failed item the summary does not count", []ItemResult[string]{{Status: ItemSucceeded}, {Status: ItemFailed}}, BatchSummary{Total: 2, Succeeded: 2}, false},
		{"a cancelled item the summary does not count", []ItemResult[string]{{Status: ItemSucceeded}, {Status: ItemCancelled}}, BatchSummary{Total: 2, Succeeded: 2}, false},
		{"an item with no status", []ItemResult[string]{{Status: ItemSucceeded}, {}}, BatchSummary{Total: 2, Succeeded: 2}, false},
		{"a failure the summary counts and the items do not show", []ItemResult[string]{{Status: ItemSucceeded}}, BatchSummary{Total: 1, Failed: 1}, false},
		{"a cancellation the summary counts alone", nil, BatchSummary{Cancelled: 1}, false},
		{"no items and an empty summary", nil, BatchSummary{}, true},
		{"one item that succeeded", []ItemResult[string]{{Status: ItemSucceeded}}, BatchSummary{Total: 1, Succeeded: 1}, true},
		{"one item that failed", []ItemResult[string]{{Status: ItemFailed}}, BatchSummary{Total: 1, Failed: 1}, false},
		{"every item failed and the summary says none did", []ItemResult[string]{{Status: ItemFailed}, {Status: ItemFailed}}, BatchSummary{Total: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := BatchResult[string]{Items: tc.items, Summary: tc.sum}
			if got := r.Complete(); got != tc.want {
				t.Errorf("Complete() = %v, want %v", got, tc.want)
			}
			if _, values := r.Values(); values != tc.want {
				t.Errorf("Values() complete = %v, want %v", values, tc.want)
			}
			if complete, failures := r.Complete(), len(r.Failures()); complete && failures > 0 {
				t.Errorf("Complete() is true beside %d failure(s)", failures)
			}
		})
	}
}

func TestBatchResultValuesSurvivesANonsenseSummary(t *testing.T) {
	r := BatchResult[string]{
		Summary: BatchSummary{Total: 2, Succeeded: -5},
		Items:   []ItemResult[string]{{Status: ItemSucceeded, Value: "a"}, {Status: ItemSucceeded, Value: "b"}},
	}
	values, _ := r.Values()
	if len(values) != 2 {
		t.Errorf("Values() = %v", values)
	}
}

func TestBatchMetricsCostQualityIgnoresAnUnsetQualityAndMixesFreeWithExact(t *testing.T) {
	priced := func(quality PricingQuality) ItemResult[string] {
		return ItemResult[string]{Status: ItemSucceeded, Cost: CostInfo{Priced: true, TotalCost: 0.01, Currency: "USD", Quality: quality}}
	}
	cases := []struct {
		name  string
		items []ItemResult[string]
		want  PricingQuality
	}{
		{"an unset quality, then exact", []ItemResult[string]{priced(""), priced(PricingExact)}, PricingExact},
		{"exact, then an unset quality", []ItemResult[string]{priced(PricingExact), priced("")}, PricingExact},
		{"only unset qualities", []ItemResult[string]{priced(""), priced("")}, PricingUnknown},
		{"free, then exact", []ItemResult[string]{priced(PricingFree), priced(PricingExact)}, PricingExact},
		{"exact, then free", []ItemResult[string]{priced(PricingExact), priced(PricingFree)}, PricingExact},
		{"free only", []ItemResult[string]{priced(PricingFree), priced(PricingFree)}, PricingFree},
		{"exact then estimated", []ItemResult[string]{priced(PricingExact), priced(PricingEstimated)}, PricingEstimated},
		{"estimated then exact", []ItemResult[string]{priced(PricingEstimated), priced(PricingExact)}, PricingEstimated},
		{"free then estimated", []ItemResult[string]{priced(PricingFree), priced(PricingEstimated)}, PricingEstimated},
		{"unknown quality items", []ItemResult[string]{priced(PricingUnknown)}, PricingUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := BatchResult[string]{Items: tc.items, Summary: BatchSummary{Total: len(tc.items), Succeeded: len(tc.items)}}
			if got := r.Metrics().CostQuality; got != tc.want {
				t.Errorf("CostQuality = %q, want %q", got, tc.want)
			}
		})
	}
}
