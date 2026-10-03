package pricing

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

func ledgerMetadata(requestID string, custom map[string]any) *types.ResultMetadata {
	return &types.ResultMetadata{
		RequestID: requestID,
		Operation: "extract",
		Model:     "gpt-5.6-luna",
		Provider:  "openai",
		EndTime:   time.Now(),
		Custom:    custom,
		TokenUsage: &types.TokenUsage{
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		},
	}
}

func pricedCost(amount float64) *types.CostInfo {
	return &types.CostInfo{TotalCost: amount, Currency: "USD", Priced: true}
}

// Fifty goroutines recording at once lose nothing: the history, the totals and
// the lookup by request ID all hold every call.
func TestConcurrentRecordingLosesNothing(t *testing.T) {
	resetAll(t)
	SetCostHistoryLimit(100000)

	const goroutines, each = 50, 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				TrackCost(pricedCost(0.001), ledgerMetadata(fmt.Sprintf("req-%d-%d", g, i), nil))
			}
		}()
	}
	wg.Wait()

	if got := CostHistoryLen(); got != goroutines*each {
		t.Fatalf("CostHistoryLen = %d, want %d", got, goroutines*each)
	}
	if total, complete := GetTotalSpend(time.Time{}, nil); !complete || math.Abs(total-0.001*goroutines*each) > 1e-9 {
		t.Errorf("GetTotalSpend = %v (complete %v), want %v", total, complete, 0.001*goroutines*each)
	}
	for _, id := range []string{"req-0-0", "req-49-199", "req-17-100"} {
		if _, ok := GetRequestCost(id); !ok {
			t.Errorf("request %s is not in the ledger", id)
		}
	}
}

// A call that finds a reader or a writer in the ledger waits for it, and has
// recorded its cost and delivered its alerts when it returns.
func TestARecordingCallWaitsForTheLedgerAndThenCompletes(t *testing.T) {
	for name, hold := range map[string]struct{ lock, unlock func() }{
		"a reader": {costMutex.RLock, costMutex.RUnlock},
		"a writer": {costMutex.Lock, costMutex.Unlock},
	} {
		t.Run(name, func(t *testing.T) {
			resetAll(t)
			hold.lock()
			returned := trackInGoroutine("req-while-held", 0.5)
			select {
			case <-returned:
				hold.unlock()
				t.Fatal("a call recorded a cost while the ledger was held")
			case <-time.After(50 * time.Millisecond):
			}
			hold.unlock()
			waitFor(t, "the call returning once the ledger was free", returned)

			record, ok := GetRequestCost("req-while-held")
			if !ok || record.Cost.TotalCost != 0.5 {
				t.Errorf("the call's record is %+v (found %v)", record, ok)
			}
		})
	}
}

// Every reader sees a call that returned before it began.
func TestEveryReaderSeesTheCallsThatReturnedBeforeIt(t *testing.T) {
	readers := map[string]func() bool{
		"GetTotalSpend":    func() bool { total, _ := GetTotalSpend(time.Time{}, nil); return total == 0.5 },
		"GetTotalCost":     func() bool { return GetTotalCost(time.Time{}, nil) == 0.5 },
		"GetCostSummary":   func() bool { return GetCostSummary(time.Time{}, nil).RequestCount == 1 },
		"GetRequestCosts":  func() bool { return len(GetRequestCosts(time.Time{}, nil)) == 1 },
		"GetRequestCost":   func() bool { _, ok := GetRequestCost("req-recorded"); return ok },
		"GetCostBreakdown": func() bool { return GetCostBreakdown(time.Time{})["total"] == 0.5 },
		"CostHistoryLen":   func() bool { return CostHistoryLen() == 1 },
		"CheckBudget": func() bool {
			SetBudget(0.25, 0, 0, nil)
			SetBudgetEnforcement(true)
			return CheckBudget() != nil
		},
		"ExportCostReport": func() bool {
			report, _ := ExportCostReport(time.Time{}, "csv")
			return strings.Contains(report, "req-recorded")
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			resetAll(t)
			TrackCost(pricedCost(0.5), ledgerMetadata("req-recorded", nil))
			if !read() {
				t.Errorf("%s did not see a call that returned before it", name)
			}
		})
	}
}

// Every threshold is announced once, however many calls cross it together.
func TestBudgetNoticesAreDeliveredOnceUnderConcurrency(t *testing.T) {
	resetAll(t)
	var delivered atomic.Int32
	SetBudget(10, 0, 0, func(current, limit float64, period string) { delivered.Add(1) })

	var wg sync.WaitGroup
	for g := 0; g < 40; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				TrackCost(pricedCost(0.05), ledgerMetadata("", nil))
			}
		}()
	}
	wg.Wait()

	// Every call has returned and nothing has read the ledger since, so each
	// notice has been delivered by one of the calls. 2000 calls at 0.05 is 100,
	// ten times the limit: the 80 percent and the 100 percent thresholds were
	// each crossed, once.
	if got := delivered.Load(); got != 2 {
		t.Errorf("the callback ran %d times, want 2 (the 80 and 100 percent thresholds, once each)", got)
	}
}

func TestACallThatFindsTheLedgerFreeAppliesItselfAndItsCallback(t *testing.T) {
	resetAll(t)
	var periods []string
	SetBudget(1, 0, 0, func(current, limit float64, period string) { periods = append(periods, period) })

	TrackCost(pricedCost(0.9), ledgerMetadata("req-a", nil))
	if len(periods) != 1 || periods[0] != "daily" {
		t.Fatalf("callbacks after a call that crossed 80 percent: %v, want one for daily before TrackCost returned", periods)
	}
	TrackCost(pricedCost(0.2), ledgerMetadata("req-b", nil))
	if len(periods) != 2 {
		t.Errorf("callbacks after crossing 100 percent: %v, want two", periods)
	}
}

// What one call costs the ledger, so that the win cannot rot. Measured: the
// record with its tags (a record and a slice of tag pairs).
func TestTrackCostAllocationsStayWithinBudget(t *testing.T) {
	resetAll(t)
	cost := pricedCost(0.001)
	custom := map[string]any{"response_format": "json", "schema_id": "extract/benchInvoice/ab12cd34"}
	metadata := ledgerMetadata("req-budget", custom)
	TrackCost(cost, metadata) // the first call builds the maps and the period keys

	if got := testing.AllocsPerRun(200, func() { TrackCost(cost, metadata) }); got > 3 {
		t.Errorf("TrackCost allocates %v times, budget 3", got)
	}
	if got := testing.AllocsPerRun(200, func() { _ = CheckBudget() }); got != 0 {
		t.Errorf("CheckBudget with enforcement off allocates %v times, want 0", got)
	}
	now := time.Now()
	if got := testing.AllocsPerRun(200, func() { _ = periodKeysFor(now) }); got != 0 {
		t.Errorf("periodKeysFor for the day it already holds allocates %v times, want 0", got)
	}
}

func TestPeriodKeysAreTheOnesTheFormattedDatesGave(t *testing.T) {
	cases := []struct {
		day                    time.Time
		daily, weekly, monthly string
		description            string
	}{
		{time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local), "daily_2026-10-03", "weekly_2026-W40", "monthly_2026-10", "an ordinary day"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local), "daily_2026-01-01", "weekly_2026-W01", "monthly_2026-01", "the first day of a year"},
		{time.Date(2027, 1, 1, 23, 59, 0, 0, time.Local), "daily_2027-01-01", "weekly_2026-W53", "monthly_2027-01", "a year's first day in the last week of the one before"},
		{time.Date(2024, 12, 30, 8, 0, 0, 0, time.Local), "daily_2024-12-30", "weekly_2025-W01", "monthly_2024-12", "a December day in the first week of the next year"},
		{time.Date(2024, 2, 29, 8, 0, 0, 0, time.Local), "daily_2024-02-29", "weekly_2024-W09", "monthly_2024-02", "a leap day"},
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local), "daily_2026-12-31", "weekly_2026-W53", "monthly_2026-12", "the last second of a year"},
		{time.Date(2026, 3, 8, 3, 0, 0, 0, time.UTC), "daily_2026-03-08", "weekly_2026-W10", "monthly_2026-03", "a day in UTC"},
		{time.Date(2026, 7, 4, 9, 0, 0, 0, time.FixedZone("east", 9*3600)), "daily_2026-07-04", "weekly_2026-W27", "monthly_2026-07", "a day in a zone east of UTC"},
		{time.Date(2026, 7, 4, 9, 0, 0, 0, time.FixedZone("west", -9*3600)), "daily_2026-07-04", "weekly_2026-W27", "monthly_2026-07", "a day in a zone west of UTC"},
		{time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local), "daily_2026-09-30", "weekly_2026-W40", "monthly_2026-09", "the last day of a month"},
		{time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), "daily_2026-10-01", "weekly_2026-W40", "monthly_2026-10", "the first day of a month"},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			keys := periodKeysFor(tc.day)
			got := [3]string{keys.periods[0].key, keys.periods[1].key, keys.periods[2].key}
			want := [3]string{tc.daily, tc.weekly, tc.monthly}
			if got != want {
				t.Errorf("keys = %v, want %v", got, want)
			}
			if keys.periods[0].name != "daily" || keys.periods[1].name != "weekly" || keys.periods[2].name != "monthly" {
				t.Errorf("period names = %v", keys.periods)
			}
			// And they are what the formatting they replace gives.
			if keys.periods[0].key != "daily_"+tc.day.Format("2006-01-02") ||
				keys.periods[1].key != "weekly_"+formattedWeek(tc.day) ||
				keys.periods[2].key != "monthly_"+tc.day.Format("2006-01") {
				t.Errorf("keys %v differ from the formatted dates of %v", got, tc.day)
			}
		})
	}
}

func TestPeriodKeysChangeWhenTheDayDoes(t *testing.T) {
	first := periodKeysFor(time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local))
	same := periodKeysFor(time.Date(2026, 10, 3, 0, 0, 1, 0, time.Local))
	next := periodKeysFor(time.Date(2026, 10, 4, 0, 0, 0, 0, time.Local))
	if first != same {
		t.Error("two instants of one day did not share their keys")
	}
	if next.periods[0].key != "daily_2026-10-04" {
		t.Errorf("the next day's key is %q", next.periods[0].key)
	}
	if first.periods[0].key != "daily_2026-10-03" {
		t.Errorf("the earlier keys were changed: %q", first.periods[0].key)
	}
}

func TestTagsAreBuiltForTheReaderAndMatchWhatTheCallCarried(t *testing.T) {
	resetAll(t)
	TrackCost(pricedCost(0.1), ledgerMetadata("req-tags", map[string]any{
		"response_format": "json", "attempts": 3, "ok": true, "nothing": nil,
	}))
	TrackCost(pricedCost(0.1), ledgerMetadata("req-empty", map[string]any{}))
	TrackCost(pricedCost(0.1), ledgerMetadata("req-none", nil))

	tagged, _ := GetRequestCost("req-tags")
	want := map[string]string{"response_format": "json", "attempts": "3", "ok": "true", "nothing": "<nil>"}
	if len(tagged.Tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", tagged.Tags, want)
	}
	for key, value := range want {
		if tagged.Tags[key] != value {
			t.Errorf("Tags[%q] = %q, want %q", key, tagged.Tags[key], value)
		}
	}

	empty, _ := GetRequestCost("req-empty")
	if empty.Tags == nil || len(empty.Tags) != 0 {
		t.Errorf("a call with an empty set of tags has Tags %#v, want an empty map", empty.Tags)
	}
	none, _ := GetRequestCost("req-none")
	if none.Tags != nil {
		t.Errorf("a call with no tags has Tags %#v, want nil", none.Tags)
	}
	if tagged.pairs != nil || tagged.hasPairs {
		t.Error("the copy a reader gets carries the ledger's internal form of the tags")
	}
}

func TestFilteringByATagWorksOnWhatTheLedgerHolds(t *testing.T) {
	resetAll(t)
	TrackCost(pricedCost(0.1), ledgerMetadata("req-a", map[string]any{"team": "billing"}))
	TrackCost(pricedCost(0.2), ledgerMetadata("req-b", map[string]any{"team": "search"}))
	TrackCost(pricedCost(0.4), ledgerMetadata("req-c", nil))

	if got := GetTotalCost(time.Time{}, map[string]string{"team": "billing"}); math.Abs(got-0.1) > 1e-12 {
		t.Errorf("billing total = %v, want 0.1", got)
	}
	if got := len(GetRequestCosts(time.Time{}, map[string]string{"team": "search"})); got != 1 {
		t.Errorf("search records = %d, want 1", got)
	}
	if got := len(GetRequestCosts(time.Time{}, map[string]string{"team": "absent"})); got != 0 {
		t.Errorf("records for a tag value nobody used = %d, want 0", got)
	}
}

func TestTheRecordAReaderIsGivenIsACopy(t *testing.T) {
	resetAll(t)
	TrackCost(pricedCost(0.1), ledgerMetadata("req-copy", map[string]any{"team": "billing"}))

	first, _ := GetRequestCost("req-copy")
	first.Tags["team"] = "mutated"
	first.Cost.TotalCost = 99

	second, _ := GetRequestCost("req-copy")
	if second.Tags["team"] != "billing" || second.Cost.TotalCost != 0.1 {
		t.Errorf("a reader's change reached the ledger: %+v", second)
	}
	for _, record := range GetRequestCosts(time.Time{}, nil) {
		record.Tags["team"] = "mutated again"
	}
	if third, _ := GetRequestCost("req-copy"); third.Tags["team"] != "billing" {
		t.Errorf("a change to a record from GetRequestCosts reached the ledger: %+v", third)
	}
}

// A call that went well writes nothing at info: the high-cost line is a debug
// line now, and is not built at all when debug is off.
func TestTheHighCostLineIsADebugLine(t *testing.T) {
	resetAll(t)
	for _, tc := range []struct {
		level logger.LogLevel
		want  bool
	}{
		{logger.DebugLevel, true},
		{logger.InfoLevel, false},
		{logger.WarnLevel, false},
	} {
		t.Run(fmt.Sprint(tc.level), func(t *testing.T) {
			buffer := &lockedLog{}
			previous := logger.GetLogger()
			logger.ConfigureLogger(logger.LoggerConfig{Level: tc.level, Format: "text", Output: buffer})
			t.Cleanup(func() { logger.SetLogger(previous) })

			TrackCost(pricedCost(0.5), ledgerMetadata("req-expensive", nil))
			TrackCost(pricedCost(0.01), ledgerMetadata("req-cheap", nil))

			logged := strings.Contains(buffer.String(), "High-cost operation tracked")
			if logged != tc.want {
				t.Errorf("at level %v the high-cost line was logged: %v, want %v\n%s", tc.level, logged, tc.want, buffer.String())
			}
			if strings.Contains(buffer.String(), "req-cheap") {
				t.Error("a call under ten cents was reported as high-cost")
			}
		})
	}
}

func TestACostThatIsNotUsableIsRecordedAsUnpricedWithoutBlockingTheLedger(t *testing.T) {
	resetAll(t)
	for i, amount := range []float64{math.NaN(), math.Inf(1), -1} {
		TrackCost(&types.CostInfo{TotalCost: amount, Priced: true, Currency: "USD"}, ledgerMetadata(fmt.Sprintf("req-bad-%d", i), nil))
	}
	summary := GetCostSummary(time.Time{}, nil)
	if summary.RequestCount != 3 || summary.UnpricedRequests != 3 || summary.TotalCost != 0 {
		t.Errorf("summary = %+v, want three unpriced requests adding nothing", summary)
	}
}

// formattedWeek is the ISO week key as the formatting the ledger's keys replaced
// wrote it.
func formattedWeek(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// budgetAlerts sets a daily budget and returns the channel its callback sends
// every alert to, in the order the callback received them.
func budgetAlerts(daily float64) <-chan budgetNotice {
	alerts := make(chan budgetNotice, 64)
	SetBudget(daily, 0, 0, func(current, limit float64, period string) {
		alerts <- budgetNotice{current: current, limit: limit, period: period}
	})
	return alerts
}

// nextAlert waits a bounded time for the next alert. The wait is only the
// allowance for a failure to show itself: when the ledger is right the alert is
// there as soon as the call that caused it has returned.
func nextAlert(t *testing.T, alerts <-chan budgetNotice) budgetNotice {
	t.Helper()
	select {
	case alert := <-alerts:
		return alert
	case <-time.After(3 * time.Second):
		t.Fatal("a budget alert was not delivered, and no later call or read was due to deliver it")
		return budgetNotice{}
	}
}

// noMoreAlerts fails if an alert is already waiting.
func noMoreAlerts(t *testing.T, alerts <-chan budgetNotice) {
	t.Helper()
	select {
	case alert := <-alerts:
		t.Errorf("an alert nobody expected: %+v", alert)
	default:
	}
}

// trackInGoroutine records one call on its own goroutine and returns a channel
// closed when TrackCost has returned.
func trackInGoroutine(requestID string, amount float64) <-chan struct{} {
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		TrackCost(pricedCost(amount), ledgerMetadata(requestID, nil))
	}()
	return returned
}

// waitFor waits a bounded time for a channel to close.
func waitFor(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen", what)
	}
}

// expectCrossing expects the two alerts that a daily spend of 1.5 against a
// limit of 1 raises, the 80 percent one first.
func expectCrossing(t *testing.T, alerts <-chan budgetNotice) {
	t.Helper()
	first, second := nextAlert(t, alerts), nextAlert(t, alerts)
	if first.current != 1.5 || second.current != 1.5 || first.limit != 1 || first.period != "daily" || second.period != "daily" {
		t.Errorf("alerts = %+v then %+v, want two for daily with 1.5 spent against 1", first, second)
	}
	noMoreAlerts(t, alerts)
}

// A call that crosses a limit while a reader has the ledger gets its alerts out
// once the reader lets go. Nothing else happens afterwards: no later call and no
// read, which is the only thing that used to deliver them.
func TestABudgetAlertIsDeliveredWhenAReaderHeldTheLedgerAndNothingFollows(t *testing.T) {
	resetAll(t)
	alerts := budgetAlerts(1)

	costMutex.RLock()
	returned := trackInGoroutine("req-last", 1.5)
	// Let the call reach the ledger, and return if it can, while the reader has it.
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
	}
	costMutex.RUnlock()

	waitFor(t, "the call returning once the reader had let go", returned)
	expectCrossing(t, alerts)
}

// The same with a writer, which is what SetBudget, SetBudgetEnforcement,
// ResetBudget and SetCostHistoryLimit are.
func TestABudgetAlertIsDeliveredWhenAWriterHeldTheLedgerAndNothingFollows(t *testing.T) {
	resetAll(t)
	alerts := budgetAlerts(1)

	costMutex.Lock()
	returned := trackInGoroutine("req-last", 1.5)
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
	}
	costMutex.Unlock()

	waitFor(t, "the call returning once the writer had let go", returned)
	expectCrossing(t, alerts)
}

// Readers that copy a full history over and over keep the ledger busy for most of
// the time. A call that crosses a limit among them still gets its alerts out.
func TestABudgetAlertIsDeliveredWhileReadersKeepCopyingTheHistory(t *testing.T) {
	for trial := 0; trial < 10; trial++ {
		resetAll(t)
		for i := 0; i < 10000; i++ {
			TrackCost(pricedCost(0.0000001), ledgerMetadata("", nil))
		}
		alerts := budgetAlerts(1)

		stop := make(chan struct{})
		var readers sync.WaitGroup
		for r := 0; r < 3; r++ {
			readers.Add(1)
			go func() {
				defer readers.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_ = costRecordsSnapshot()
					}
				}
			}()
		}
		// The readers stop as soon as the call has returned: a reader that went
		// on would apply what the call left waiting, and hide that it was left.
		waitFor(t, "the crossing call returning", trackInGoroutine("crossing", 2))
		close(stop)
		readers.Wait()
		first := nextAlert(t, alerts)
		second := nextAlert(t, alerts)
		if first.current < 2 || second.current < 2 {
			t.Fatalf("trial %d: alerts %+v and %+v, want both to report the 2 just recorded", trial, first, second)
		}
	}
}

// Two calls that cross different thresholds together are announced one at a
// time and in the order the thresholds were crossed, however long the first
// announcement takes: an integration that pages on the callback must not see 100
// percent before 80, nor two of them at once.
func TestBudgetAlertsAreDeliveredOneAtATimeInTheOrderTheyCameDue(t *testing.T) {
	resetAll(t)
	var active atomic.Int32
	var overlapped atomic.Bool
	entered := make(chan float64, 8)
	release := make(chan struct{})
	delivered := make(chan float64, 8)
	SetBudget(10, 0, 0, func(current, limit float64, period string) {
		if active.Add(1) > 1 {
			overlapped.Store(true)
		}
		entered <- current
		if current < limit {
			<-release // the 80 percent announcement is slow
		}
		active.Add(-1)
		delivered <- current
	})

	first := trackInGoroutine("req-80", 8.5)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the callback for the first crossing never started")
	}
	second := trackInGoroutine("req-100", 2)
	// Give the second call the chance to announce 100 percent while the first
	// announcement is still going on.
	select {
	case <-second:
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	var order []float64
	for len(order) < 2 {
		select {
		case current := <-delivered:
			order = append(order, current)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v of the two alerts were delivered", order)
		}
	}
	waitFor(t, "the first call returning", first)
	waitFor(t, "the second call returning", second)
	if overlapped.Load() {
		t.Error("two budget alerts were delivered at the same time")
	}
	if order[0] != 8.5 || order[1] != 10.5 {
		t.Errorf("alerts were delivered with %v spent, in that order; want 8.5 (80 percent) and then 10.5 (100 percent)", order)
	}
}

// A callback that records a cost has the alerts of that cost delivered after its
// own, not inside it and not never.
func TestACallbackThatRecordsACostHasThatCostsAlertsDeliveredAfterItsOwn(t *testing.T) {
	resetAll(t)
	var seen []string
	var inside atomic.Bool
	SetBudget(10, 0, 0, func(current, limit float64, period string) {
		if inside.Swap(true) {
			t.Error("an alert was delivered from inside another one")
		}
		defer inside.Store(false)
		if current < limit {
			seen = append(seen, "80")
			TrackCost(pricedCost(3), ledgerMetadata("from-callback", nil))
			return
		}
		seen = append(seen, "100")
	})

	done := trackInGoroutine("req-80", 8.5)
	waitFor(t, "the call returning", done)
	if got := fmt.Sprint(seen); got != "[80 100]" {
		t.Errorf("alerts delivered = %v, want [80 100]", got)
	}
	if got := CostHistoryLen(); got != 2 {
		t.Errorf("CostHistoryLen = %d, want 2", got)
	}
}

// A callback that ends its goroutine, as t.Fatal does in a test, must not stop
// the alerts that follow it from being delivered.
func TestACallbackThatEndsItsGoroutineDoesNotStopLaterAlerts(t *testing.T) {
	resetAll(t)
	var calls atomic.Int32
	delivered := make(chan float64, 8)
	SetBudget(10, 0, 0, func(current, limit float64, period string) {
		if calls.Add(1) == 1 {
			runtime.Goexit()
		}
		delivered <- current
	})

	waitFor(t, "the goroutine ending", trackInGoroutine("req-80", 8.5))
	waitFor(t, "the second call returning", trackInGoroutine("req-100", 2))
	select {
	case current := <-delivered:
		if current != 10.5 {
			t.Errorf("the alert delivered after the ended one reports %v, want 10.5", current)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no alert was delivered after a callback ended its goroutine")
	}
}

// A call recorded after every threshold has been announced costs the ledger the
// same as one with no budget at all: the lock is not held across the building of
// a key to look the announcement up under.
func TestACallOverAnAnnouncedLimitAllocatesNoMoreThanOneWithoutABudget(t *testing.T) {
	resetAll(t)
	alerts := budgetAlerts(1)
	TrackCost(pricedCost(5), ledgerMetadata("req-over", nil))
	nextAlert(t, alerts)
	nextAlert(t, alerts)

	cost := pricedCost(0.001)
	metadata := ledgerMetadata("req-after", map[string]any{"response_format": "json", "schema_id": "extract/benchInvoice/ab12cd34"})
	if got := testing.AllocsPerRun(200, func() { TrackCost(cost, metadata) }); got > 3 {
		t.Errorf("TrackCost over an announced limit allocates %v times, budget 3", got)
	}
	noMoreAlerts(t, alerts)
}
