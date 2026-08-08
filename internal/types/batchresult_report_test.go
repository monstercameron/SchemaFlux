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

// DefaultPolicyFor's cutover: at or below the threshold gets FailFast, above
// it gets RetryThenCollect, and the reason names both the count and the
// threshold so a caller can tell a deliberate choice from a fallback.
func TestDefaultPolicyForCutover(t *testing.T) {
	cases := []struct {
		itemCount  int
		wantPolicy BatchPolicy
	}{
		{1, PolicyFailFast},
		{LongBatchThreshold, PolicyFailFast},
		{LongBatchThreshold + 1, PolicyRetryThenCollect},
		{500, PolicyRetryThenCollect},
	}
	for _, tc := range cases {
		policy, reason := DefaultPolicyFor(tc.itemCount)
		if policy != tc.wantPolicy {
			t.Errorf("DefaultPolicyFor(%d) = %v, want %v", tc.itemCount, policy, tc.wantPolicy)
		}
		if reason == "" {
			t.Errorf("DefaultPolicyFor(%d) gave no reason", tc.itemCount)
		}
		if !strings.Contains(reason, "no policy specified") {
			t.Errorf("reason = %q, does not say the policy was unspecified", reason)
		}
	}
}
