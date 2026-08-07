package pricing

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// track records one request at the given cost, the way CallLLM does.
func track(t *testing.T, requestID string, cost float64) {
	t.Helper()
	TrackCost(
		&types.CostInfo{TotalCost: cost, Currency: "USD", Priced: true},
		&types.ResultMetadata{
			RequestID: requestID,
			Operation: "extract",
			Model:     "gpt-5.6-luna",
			Provider:  "openai",
			EndTime:   time.Now(),
			TokenUsage: &types.TokenUsage{
				PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
			},
		},
	)
}

func resetAll(t *testing.T) {
	t.Helper()
	ResetCostTracking()
	ResetBudget()
	SetCostHistoryLimit(DefaultCostHistoryLimit)
	t.Cleanup(func() {
		ResetCostTracking()
		ResetBudget()
		SetCostHistoryLimit(DefaultCostHistoryLimit)
	})
}

// The history was an unbounded slice appended to on every call and never
// evicted, in a library whose purpose is to be called in a loop.
func TestCostHistoryIsBounded(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(100)

	for i := 0; i < 5000; i++ {
		track(t, fmt.Sprintf("req-%d", i), 0.001)
	}

	if got := CostHistoryLen(); got != 100 {
		t.Fatalf("CostHistoryLen = %d, want 100 -- the history is not bounded", got)
	}
	if got := len(costHistory); got != 100 {
		t.Errorf("the backing slice grew to %d; the ring should reuse its slots", got)
	}
}

// Eviction is oldest-first, and the records that survive are the recent ones.
func TestCostHistoryEvictsOldestFirst(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(10)

	for i := 0; i < 25; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.01)
	}

	records := GetRequestCosts(time.Time{}, nil)
	if len(records) != 10 {
		t.Fatalf("got %d records, want 10", len(records))
	}
	for i, record := range records {
		want := fmt.Sprintf("req-%02d", 15+i)
		if record.RequestID != want {
			t.Errorf("record %d is %s, want %s -- eviction is not oldest-first, or order was lost",
				i, record.RequestID, want)
		}
	}
}

// An evicted record must leave the index with it, or a lookup returns the slot
// its replacement now occupies -- somebody else's request, reported as yours.
func TestEvictedRecordsLeaveTheIndex(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(4)

	for i := 0; i < 12; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.01)
	}

	for i := 0; i < 8; i++ {
		if record, ok := GetRequestCost(fmt.Sprintf("req-%02d", i)); ok {
			t.Errorf("evicted request req-%02d still resolves, to %s", i, record.RequestID)
		}
	}
	for i := 8; i < 12; i++ {
		id := fmt.Sprintf("req-%02d", i)
		record, ok := GetRequestCost(id)
		if !ok {
			t.Errorf("retained request %s does not resolve", id)
			continue
		}
		if record.RequestID != id {
			t.Errorf("GetRequestCost(%s) returned %s", id, record.RequestID)
		}
	}
}

// GetRequestCost was a linear scan under a lock. It is a map lookup now, and
// the property that matters is that it still finds the right record.
func TestGetRequestCostFindsTheRightRecord(t *testing.T) {
	resetAll(t)

	for i := 0; i < 500; i++ {
		track(t, fmt.Sprintf("req-%03d", i), float64(i)/1000)
	}

	for _, i := range []int{0, 1, 250, 498, 499} {
		id := fmt.Sprintf("req-%03d", i)
		record, ok := GetRequestCost(id)
		if !ok {
			t.Fatalf("GetRequestCost(%s) not found", id)
		}
		if want := float64(i) / 1000; record.Cost.TotalCost != want {
			t.Errorf("%s cost = %v, want %v", id, record.Cost.TotalCost, want)
		}
	}

	if _, ok := GetRequestCost("never-tracked"); ok {
		t.Error("an unknown request ID resolved")
	}
}

// A retried request keeps its ID, and the caller asking what it cost means the
// most recent attempt.
func TestRepeatedRequestIDResolvesToTheLatestRecord(t *testing.T) {
	resetAll(t)

	track(t, "req-retry", 0.01)
	track(t, "req-retry", 0.02)

	record, ok := GetRequestCost("req-retry")
	if !ok {
		t.Fatal("not found")
	}
	if record.Cost.TotalCost != 0.02 {
		t.Errorf("cost = %v, want the later 0.02", record.Cost.TotalCost)
	}
}

// The totals and the summaries must agree with the retained history rather than
// with everything ever recorded, or a bounded history silently makes them lie.
func TestAggregatesReflectTheRetainedHistory(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(5)

	for i := 0; i < 20; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.10)
	}

	if got, want := GetTotalCost(time.Time{}, nil), 0.50; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("GetTotalCost = %v, want %v (five retained records at $0.10)", got, want)
	}

	summary := GetCostSummary(time.Time{}, nil)
	if summary.RequestCount != 5 {
		t.Errorf("RequestCount = %d, want 5", summary.RequestCount)
	}

	breakdown := GetCostBreakdown(time.Time{})
	if got, want := breakdown["total"], 0.50; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("breakdown total = %v, want %v", got, want)
	}
}

// Export walks the ring in order too.
func TestExportWalksTheRingInOrder(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(3)

	for i := 0; i < 6; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.01)
	}

	report, err := ExportCostReport(time.Time{}, "csv")
	if err != nil {
		t.Fatalf("ExportCostReport: %v", err)
	}
	for _, id := range []string{"req-03", "req-04", "req-05"} {
		if !contains(report, id) {
			t.Errorf("the report is missing the retained record %s", id)
		}
	}
	for _, id := range []string{"req-00", "req-01", "req-02"} {
		if contains(report, id) {
			t.Errorf("the report contains the evicted record %s", id)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// PR-004: clearing history used to null the budget limits and the callback with
// it, so a service that reset its history on a schedule silently stopped
// alerting on spend.
func TestResetCostTrackingKeepsTheBudget(t *testing.T) {
	resetAll(t)

	var fired int
	SetBudget(1.0, 0, 0, func(float64, float64, string) { fired++ })

	ResetCostTracking()

	// The budget must still be configured: spending past it alerts.
	track(t, "req-after-reset", 0.9)
	if fired == 0 {
		t.Fatal("the callback did not fire; ResetCostTracking disabled the budget")
	}
}

// ResetBudget is the function that clears it, and it leaves the history alone.
func TestResetBudgetKeepsTheHistory(t *testing.T) {
	resetAll(t)

	var fired int
	SetBudget(1.0, 0, 0, func(float64, float64, string) { fired++ })
	track(t, "req-1", 0.5)

	ResetBudget()
	track(t, "req-2", 0.9)

	if fired != 0 {
		t.Errorf("the callback fired %d times after ResetBudget", fired)
	}
	if CostHistoryLen() != 2 {
		t.Errorf("CostHistoryLen = %d, want 2 -- ResetBudget dropped history", CostHistoryLen())
	}
}

// PR-005: one notification per threshold crossing. The old check fired on every
// request once spend passed 80%, with no debounce and no state.
func TestBudgetAlertsAreEdgeTriggered(t *testing.T) {
	resetAll(t)

	var fired int
	SetBudget(1.0, 0, 0, func(current, limit float64, period string) { fired++ })

	// Twenty requests that together cross 80% once.
	for i := 0; i < 20; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.045) // 0.90 total
	}

	if fired != 1 {
		t.Fatalf("callback fired %d times, want exactly 1 for the 80%% crossing", fired)
	}

	// Crossing 100% is a second, distinct edge.
	track(t, "req-over", 0.2)
	if fired != 2 {
		t.Fatalf("callback fired %d times, want 2 after crossing the limit itself", fired)
	}

	// And nothing after that, however much more is spent.
	for i := 0; i < 10; i++ {
		track(t, fmt.Sprintf("req-extra-%02d", i), 0.5)
	}
	if fired != 2 {
		t.Errorf("callback fired %d times; both edges had already been reported", fired)
	}
}

// Enforcement is off by default, because budgets in this library have always
// been advisory and quietly starting to refuse calls would be the worse
// surprise.
func TestBudgetEnforcementIsOffByDefault(t *testing.T) {
	resetAll(t)

	SetBudget(0.01, 0, 0, nil)
	track(t, "req-over", 1.00)

	if err := CheckBudget(); err != nil {
		t.Fatalf("CheckBudget = %v, want nil with enforcement off", err)
	}
}

// With enforcement on, an exhausted budget refuses the call before it is made.
func TestBudgetEnforcementRefusesWhenExhausted(t *testing.T) {
	resetAll(t)

	SetBudget(0.50, 0, 0, nil)
	SetBudgetEnforcement(true)

	track(t, "req-1", 0.20)
	if err := CheckBudget(); err != nil {
		t.Fatalf("CheckBudget = %v, want nil while under the limit", err)
	}

	track(t, "req-2", 0.35) // 0.55 total, over the 0.50 daily limit
	err := CheckBudget()
	if err == nil {
		t.Fatal("CheckBudget = nil, want ErrBudgetExceeded once spend passed the limit")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("errors.Is(err, ErrBudgetExceeded) = false; err = %v", err)
	}
}

// A zero limit means "no limit", not "refuse everything".
func TestZeroBudgetMeansUnlimited(t *testing.T) {
	resetAll(t)

	SetBudget(0, 0, 0, nil)
	SetBudgetEnforcement(true)
	track(t, "req-1", 100.0)

	if err := CheckBudget(); err != nil {
		t.Errorf("CheckBudget = %v, want nil when no limit is configured", err)
	}
}

// SetCostHistoryLimit(0) restores the default rather than disabling recording.
func TestZeroHistoryLimitRestoresTheDefault(t *testing.T) {
	resetAll(t)

	SetCostHistoryLimit(7)
	SetCostHistoryLimit(0)

	for i := 0; i < 20; i++ {
		track(t, fmt.Sprintf("req-%02d", i), 0.01)
	}
	if got := CostHistoryLen(); got != 20 {
		t.Errorf("CostHistoryLen = %d, want 20 -- a zero limit should mean the default, not seven", got)
	}
}
