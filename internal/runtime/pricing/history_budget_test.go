package pricing

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// lockedLog is a log destination safe to write from any goroutine.
type lockedLog struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// captureLog sends the library's log to a buffer for the length of a test and
// returns a function reading what was logged so far.
func captureLog(t *testing.T) func() string {
	t.Helper()
	buffer := &lockedLog{}
	previous := logger.GetLogger()
	logger.ConfigureLogger(logger.LoggerConfig{Level: logger.DebugLevel, Format: "text", Output: buffer})
	t.Cleanup(func() { logger.SetLogger(previous) })
	return buffer.String
}

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

// A negative or NaN limit is a configuration mistake, not a budget: it must not
// refuse every call (a negative number is always exceeded) nor fire an alert on
// every cost (a comparison against NaN is never false the right way round).
func TestANegativeOrNaNBudgetLimitIsNoLimit(t *testing.T) {
	for name, limit := range map[string]float64{"negative": -5, "NaN": math.NaN(), "negative infinity": math.Inf(-1)} {
		t.Run(name, func(t *testing.T) {
			resetAll(t)
			var alerts int
			SetBudget(limit, limit, limit, func(float64, float64, string) { alerts++ })
			SetBudgetEnforcement(true)
			track(t, "req-1", 0.9)
			track(t, "req-2", 0.9)

			if err := CheckBudget(); err != nil {
				t.Errorf("CheckBudget refused under a limit of %v: %v", limit, err)
			}
			if alerts != 0 {
				t.Errorf("%d budget alerts under a limit of %v, want none", alerts, limit)
			}
		})
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

// The callback used to run while TrackCost held the ledger's write lock, so a
// callback that read a total, checked the budget or tracked a cost -- the first
// things an alert hook reaches for -- waited on a lock its own caller held.
func TestTheBudgetCallbackMayCallBackIntoThePackage(t *testing.T) {
	resetAll(t)
	SetBudgetEnforcement(true)

	var seen struct {
		total    float64
		checkErr error
		fired    int
	}
	SetBudget(1.0, 0, 0, func(current, limit float64, period string) {
		seen.fired++
		seen.total = GetTotalCost(time.Time{}, nil)
		seen.checkErr = CheckBudget()
		_ = GetCostSummary(time.Time{}, nil)
		_ = GetCostBreakdown(time.Time{})
		track(t, fmt.Sprintf("from-callback-%d", seen.fired), 0)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		track(t, "req-1", 0.9)
		track(t, "req-2", 0.2)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TrackCost did not return: the budget callback ran under the ledger lock")
	}

	if seen.fired != 2 {
		t.Fatalf("callback fired %d times, want 2 (the 80%% and 100%% crossings)", seen.fired)
	}
	if seen.total < 1.0 {
		t.Errorf("the callback read a total of %v, want the 1.1 just recorded", seen.total)
	}
	if !errors.Is(seen.checkErr, ErrBudgetExceeded) {
		t.Errorf("CheckBudget inside the callback = %v, want ErrBudgetExceeded", seen.checkErr)
	}
}

// The callback is the caller's code. A panic in it must not unwind through the
// operation that recorded the cost, and must not stop later costs recording.
func TestAPanickingBudgetCallbackDoesNotReachTheCaller(t *testing.T) {
	resetAll(t)

	var calls int
	SetBudget(1.0, 0, 0, func(float64, float64, string) {
		calls++
		panic("alert hook failed with customer data in the message")
	})

	logs := captureLog(t)
	track(t, "req-1", 0.9)
	track(t, "req-2", 0.2)

	if calls != 2 {
		t.Errorf("callback ran %d times, want 2", calls)
	}
	text := logs()
	if !strings.Contains(text, "Budget callback panicked") {
		t.Errorf("the panic was not logged at all\nlog:\n%s", text)
	}
	if strings.Contains(text, "customer data") {
		t.Errorf("the panic value reached the log\nlog:\n%s", text)
	}
	if CostHistoryLen() != 2 {
		t.Errorf("CostHistoryLen = %d, want 2: a panic in the callback lost a record", CostHistoryLen())
	}
	if usd, _ := GetTotalSpend(time.Time{}, nil); !closeEnough(usd, 1.1) {
		t.Errorf("total = %v, want 1.1", usd)
	}
}

// trackUnpriced records one call whose cost is not known.
func trackUnpriced(t *testing.T, requestID string) {
	t.Helper()
	TrackCost(
		&types.CostInfo{Currency: "USD", Priced: false, Quality: types.PricingUnknown},
		&types.ResultMetadata{RequestID: requestID, Operation: "extract", Model: "gpt-5.6-sol", Provider: "openai", EndTime: time.Now()},
	)
}

// A total that includes a call of unknown cost is a floor, and says so.
func TestATotalHoldingAnUnpricedCallIsMarkedIncomplete(t *testing.T) {
	resetAll(t)

	if usd, complete := GetTotalSpend(time.Time{}, nil); usd != 0 || !complete {
		t.Fatalf("an empty ledger reported (%v, %v), want (0, true): nothing is missing from it", usd, complete)
	}

	track(t, "req-priced", 0.25)
	if usd, complete := GetTotalSpend(time.Time{}, nil); !closeEnough(usd, 0.25) || !complete {
		t.Fatalf("priced calls only: (%v, %v), want (0.25, true)", usd, complete)
	}

	trackUnpriced(t, "req-unpriced")
	usd, complete := GetTotalSpend(time.Time{}, nil)
	if !closeEnough(usd, 0.25) || complete {
		t.Errorf("with an unpriced call: (%v, %v), want (0.25, false)", usd, complete)
	}
	if total := GetTotalCost(time.Time{}, nil); !closeEnough(total, 0.25) {
		t.Errorf("GetTotalCost = %v, want the same floor, 0.25", total)
	}

	// The flag follows the range asked about, not the whole history.
	cases := []struct {
		name     string
		filters  map[string]string
		complete bool
	}{
		{"every call", nil, false},
		{"only the priced call", map[string]string{"request_id": "req-priced"}, true},
		{"only the unpriced call", map[string]string{"request_id": "req-unpriced"}, false},
		{"a model the unpriced call used", map[string]string{"model": "gpt-5.6-sol"}, false},
		{"a model nothing used", map[string]string{"model": "gpt-4"}, true},
	}
	for _, tc := range cases {
		if _, got := GetTotalSpend(time.Time{}, tc.filters); got != tc.complete {
			t.Errorf("%s: complete = %v, want %v", tc.name, got, tc.complete)
		}
	}
	if _, got := GetTotalSpend(time.Now().Add(time.Hour), nil); !got {
		t.Error("a range after every record reported incomplete")
	}
}

// With enforcement on, a refusal says how many calls of unknown cost the
// period holds, because the spend it names is then a floor.
func TestRefusingABudgetNamesTheUnpricedCalls(t *testing.T) {
	resetAll(t)
	SetBudget(0.50, 0, 0, nil)
	SetBudgetEnforcement(true)

	track(t, "req-1", 0.60)
	err := CheckBudget()
	if !errors.Is(err, ErrBudgetExceeded) || strings.Contains(err.Error(), "unknown cost") {
		t.Fatalf("with no unpriced call: %v", err)
	}

	trackUnpriced(t, "req-2")
	trackUnpriced(t, "req-3")
	err = CheckBudget()
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("CheckBudget = %v, want ErrBudgetExceeded", err)
	}
	if !strings.Contains(err.Error(), "2 call(s) of unknown cost") {
		t.Errorf("the refusal does not say the spend is a floor: %v", err)
	}
}

// Unpriced calls do not unlock the gate, and do not close it either: only
// priced spend is compared with the limit.
func TestUnpricedCallsAloneNeitherRefuseNorHideASpentBudget(t *testing.T) {
	resetAll(t)
	SetBudget(0.50, 0, 0, nil)
	SetBudgetEnforcement(true)

	for i := 0; i < 5; i++ {
		trackUnpriced(t, fmt.Sprintf("req-%d", i))
	}
	if err := CheckBudget(); err != nil {
		t.Fatalf("CheckBudget = %v: unpriced calls alone must not lock out a subscription transport", err)
	}
	track(t, "req-priced", 0.5)
	if err := CheckBudget(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("CheckBudget = %v, want ErrBudgetExceeded once the priced spend reaches the limit", err)
	}
}

// A cost that is not a finite non-negative number used to be added to the
// running totals as it was. NaN made every later total NaN, and NaN is never
// at or above a limit, so the budget stopped ever refusing.
func TestAnInvalidCostAmountDoesNotPoisonTheTotals(t *testing.T) {
	resetAll(t)
	SetBudget(0.50, 0, 0, nil)
	SetBudgetEnforcement(true)

	bad := []struct {
		name string
		cost types.CostInfo
	}{
		{"NaN total", types.CostInfo{TotalCost: math.NaN(), Priced: true}},
		{"positive infinity", types.CostInfo{TotalCost: math.Inf(1), Priced: true}},
		{"negative infinity", types.CostInfo{TotalCost: math.Inf(-1), Priced: true}},
		{"negative total", types.CostInfo{TotalCost: -3, Priced: true}},
		{"NaN prompt cost", types.CostInfo{TotalCost: 1, PromptCost: math.NaN(), Priced: true}},
		{"negative completion cost", types.CostInfo{TotalCost: 1, CompletionCost: -1, Priced: true}},
		{"infinite cached cost", types.CostInfo{TotalCost: 1, CachedCost: math.Inf(1), Priced: true}},
		{"NaN reasoning cost", types.CostInfo{TotalCost: 1, ReasoningCost: math.NaN(), Priced: true}},
		{"negative cached cost", types.CostInfo{TotalCost: 1, CachedCost: -0.5, Priced: true}},
		{"infinite reasoning cost", types.CostInfo{TotalCost: 1, ReasoningCost: math.Inf(1), Priced: true}},
	}
	for i, tc := range bad {
		cost := tc.cost
		TrackCost(&cost, &types.ResultMetadata{RequestID: fmt.Sprintf("bad-%d", i), EndTime: time.Now()})
	}

	usd, complete := GetTotalSpend(time.Time{}, nil)
	if usd != 0 || complete {
		t.Fatalf("after %d invalid costs: (%v, %v), want (0, false): recorded as unknown, adding nothing", len(bad), usd, complete)
	}
	if summary := GetCostSummary(time.Time{}, nil); summary.UnpricedRequests != len(bad) {
		t.Errorf("UnpricedRequests = %d, want %d", summary.UnpricedRequests, len(bad))
	}
	record, ok := GetRequestCost("bad-0")
	if !ok || record.Cost.Priced || math.IsNaN(record.Cost.TotalCost) {
		t.Errorf("the stored record of a NaN cost = %+v, want an unpriced record holding no NaN", record.Cost)
	}

	track(t, "req-real", 0.6)
	if err := CheckBudget(); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("CheckBudget = %v: a real cost over the limit must still refuse after invalid ones", err)
	}
}

// When more than one period is over its limit the one named is the same every
// time; it used to be whichever the map iteration reached first.
func TestRefusalNamesTheShortestPeriodOverItsLimit(t *testing.T) {
	resetAll(t)
	SetBudget(0.10, 0.10, 0.10, nil)
	SetBudgetEnforcement(true)
	track(t, "req-1", 0.5)

	for i := 0; i < 50; i++ {
		err := CheckBudget()
		if err == nil || !strings.Contains(err.Error(), "daily spend") {
			t.Fatalf("attempt %d: %v, want the refusal to name the daily period", i, err)
		}
	}
}

// Only the first call of an unpriced model logs a warning; a loop over such a
// model logged once per call.
func TestAnUnpricedModelIsWarnedAboutOnce(t *testing.T) {
	unpricedWarnMu.Lock()
	saved := unpricedWarned
	unpricedWarned = map[string]bool{}
	unpricedWarnMu.Unlock()
	t.Cleanup(func() {
		unpricedWarnMu.Lock()
		unpricedWarned = saved
		unpricedWarnMu.Unlock()
	})

	logs := captureLog(t)
	for i := 0; i < 100; i++ {
		CalculateCost(usage(), "warned-once-model", "openai")
	}
	CalculateCost(usage(), "warned-once-model", "anthropic")
	CalculateCost(usage(), "another-warned-model", "openai")

	if got := strings.Count(logs(), "No pricing information available"); got != 3 {
		t.Errorf("%d warnings logged for 102 calls on three (provider, model) pairs, want 3\nlog:\n%s", got, logs())
	}

	unpricedWarnMu.Lock()
	remembered := len(unpricedWarned)
	unpricedWarnMu.Unlock()
	if remembered != 3 {
		t.Errorf("%d distinct (provider, model) pairs remembered, want 3", remembered)
	}

	for i := 0; i < unpricedWarnLimit+50; i++ {
		CalculateCost(usage(), fmt.Sprintf("flood-%d", i), "openai")
	}
	unpricedWarnMu.Lock()
	remembered = len(unpricedWarned)
	unpricedWarnMu.Unlock()
	if remembered > unpricedWarnLimit {
		t.Errorf("%d pairs remembered, want at most %d", remembered, unpricedWarnLimit)
	}
}

// The error of an exhausted budget has a kind, so a caller that branches on
// types.KindOf, and anything that reads errors.Is against the library's own
// sentinel, sees a refused budget. Its text and the sentinel it always matched
// are what they were.
func TestAnExhaustedBudgetsErrorHasAKindAndKeepsItsTextAndItsSentinel(t *testing.T) {
	unpriced := func(t *testing.T) {
		t.Helper()
		TrackCost(&types.CostInfo{Priced: false, Currency: "USD"}, &types.ResultMetadata{
			RequestID: "req-unpriced", Operation: "extract", Model: "unlisted", Provider: "openai",
			EndTime: time.Now(), TokenUsage: &types.TokenUsage{PromptTokens: 10, TotalTokens: 10},
		})
	}
	cases := []struct {
		name                   string
		daily, weekly, monthly float64
		spend                  []float64
		withUnpricedCall       bool
		want                   string
	}{
		{"the daily limit", 0.50, 0, 0, []float64{0.55}, false,
			"cost budget exceeded: daily spend $0.5500 has reached the $0.50 limit"},
		{"the weekly limit alone", 0, 0.50, 0, []float64{0.55}, false,
			"cost budget exceeded: weekly spend $0.5500 has reached the $0.50 limit"},
		{"the monthly limit alone", 0, 0, 0.50, []float64{0.55}, false,
			"cost budget exceeded: monthly spend $0.5500 has reached the $0.50 limit"},
		{"spend exactly at the limit", 0.50, 0, 0, []float64{0.30, 0.20}, false,
			"cost budget exceeded: daily spend $0.5000 has reached the $0.50 limit"},
		{"two limits over: the shortest period is the one reported", 0.50, 0.50, 0.50, []float64{0.60}, false,
			"cost budget exceeded: daily spend $0.6000 has reached the $0.50 limit"},
		{"the weekly one when the daily is not over", 5.00, 0.50, 0, []float64{0.60}, false,
			"cost budget exceeded: weekly spend $0.6000 has reached the $0.50 limit"},
		{"a limit with cents", 0.125, 0, 0, []float64{0.20}, false,
			"cost budget exceeded: daily spend $0.2000 has reached the $0.12 limit"},
		{"a spend of many small calls", 0.10, 0, 0, []float64{0.03, 0.03, 0.03, 0.03}, false,
			"cost budget exceeded: daily spend $0.1200 has reached the $0.10 limit"},
		{"a floor, with a call of unknown cost", 0.50, 0, 0, []float64{0.55}, true,
			"cost budget exceeded: daily spend $0.5500 has reached the $0.50 limit (plus 1 call(s) of unknown cost, so the spend is a floor)"},
		{"a large limit", 1000, 0, 0, []float64{1200}, false,
			"cost budget exceeded: daily spend $1200.0000 has reached the $1000.00 limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAll(t)
			SetBudget(tc.daily, tc.weekly, tc.monthly, nil)
			SetBudgetEnforcement(true)
			for i, cost := range tc.spend {
				track(t, fmt.Sprintf("req-%d", i), cost)
			}
			if tc.withUnpricedCall {
				unpriced(t)
			}

			err := CheckBudget()
			if err == nil {
				t.Fatal("CheckBudget = nil, want a refusal")
			}
			if err.Error() != tc.want {
				t.Errorf("the text is %q, want %q", err.Error(), tc.want)
			}
			if got := types.KindOf(err); got != types.KindBudgetExceeded {
				t.Errorf("the kind is %v, want %v", got, types.KindBudgetExceeded)
			}
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Error("the error no longer matches pricing.ErrBudgetExceeded")
			}
			if !errors.Is(err, types.ErrBudgetExceeded) {
				t.Error("the error does not match the library's budget sentinel")
			}
			if errors.Is(err, types.ErrRateLimited) || errors.Is(err, types.ErrConfiguration) {
				t.Error("the error matches the sentinel of another kind")
			}
		})
	}
}
