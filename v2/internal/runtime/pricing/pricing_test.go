package pricing

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func resetPricingTestState(t *testing.T) {
	t.Helper()
	ResetCostTracking()
	t.Cleanup(ResetCostTracking)
}

func TestCalculateCost(t *testing.T) {
	resetPricingTestState(t)

	tests := []struct {
		name             string
		model            string
		provider         string
		promptTokens     int
		completionTokens int
		expectedMin      float64
		expectedMax      float64
	}{
		{
			name:             "GPT-5.4",
			model:            "gpt-5.4",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.008,
			expectedMax:      0.011,
		},
		{
			name:             "GPT-5",
			model:            "gpt-5-2025-08-07",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.006, // Minimum expected cost
			expectedMax:      0.007, // Maximum expected cost
		},
		{
			name:             "GPT-5 Nano",
			model:            "gpt-5-nano-2025-08-07",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.0002,
			expectedMax:      0.0003,
		},
		{
			name:             "GPT-5 Mini",
			model:            "gpt-5-mini-2025-08-07",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.0012,
			expectedMax:      0.0013,
		},
		{
			name:             "GPT-4 Turbo (legacy)",
			model:            "gpt-4-turbo-preview",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.01, // Minimum expected cost
			expectedMax:      0.05, // Maximum expected cost
		},
		{
			name:             "GPT-3.5 Turbo (legacy)",
			model:            "gpt-3.5-turbo",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0.001,
			expectedMax:      0.01,
		},
		{
			name:             "Unknown model",
			model:            "unknown-model",
			provider:         "openai",
			promptTokens:     1000,
			completionTokens: 500,
			expectedMin:      0,
			expectedMax:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := &types.TokenUsage{
				PromptTokens:     tt.promptTokens,
				CompletionTokens: tt.completionTokens,
				TotalTokens:      tt.promptTokens + tt.completionTokens,
			}
			cost := CalculateCost(usage, tt.model, tt.provider)
			if cost == nil {
				t.Fatal("Expected cost to be calculated")
			}
			if cost.TotalCost < tt.expectedMin || cost.TotalCost > tt.expectedMax {
				t.Errorf("CalculateCost() = %v, want between %v and %v",
					cost.TotalCost, tt.expectedMin, tt.expectedMax)
			}
			// A zero ceiling is the unpriced case, which must be unpriced and
			// not a priced zero.
			if wantPriced := tt.expectedMax > 0; cost.Priced != wantPriced {
				t.Errorf("Priced = %v, want %v", cost.Priced, wantPriced)
			}
		})
	}
}

func TestTrackCost(t *testing.T) {
	resetPricingTestState(t)

	// Test tracking costs
	usage1 := &types.TokenUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost1 := CalculateCost(usage1, "gpt-5-2025-08-07", "openai")
	metadata1 := &types.ResultMetadata{
		RequestID: "test-001",
		Operation: "extract",
	}

	TrackCost(cost1, metadata1)

	// Test getting total cost
	total := GetTotalCost(time.Now().Add(-1*time.Hour), nil)
	if total <= 0 {
		t.Error("Expected positive total cost")
	}

	// A filter on the operation the call was tracked under keeps it, and a
	// filter on another operation drops it.
	extractCost := GetTotalCost(time.Now().Add(-1*time.Hour), map[string]string{
		"operation": "extract",
	})
	if extractCost != total {
		t.Errorf("total for operation extract = %v, want the whole total %v", extractCost, total)
	}
	if other := GetTotalCost(time.Now().Add(-1*time.Hour), map[string]string{"operation": "classify"}); other != 0 {
		t.Errorf("total for operation classify = %v, want 0", other)
	}
}

func TestGetCostBreakdown(t *testing.T) {
	resetPricingTestState(t)

	// Add some costs first
	usage := &types.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}

	cost := CalculateCost(usage, "gpt-5-mini-2025-08-07", "openai")
	metadata := &types.ResultMetadata{
		RequestID: "test-002",
		Operation: "classify",
	}

	TrackCost(cost, metadata)

	// Get cost breakdown
	breakdown := GetCostBreakdown(time.Now().Add(-1 * time.Hour))
	if breakdown == nil {
		t.Error("Expected cost breakdown to be returned")
	}

	// Check that it has some entries (may vary based on implementation)
	if len(breakdown) == 0 {
		t.Log("Cost breakdown is empty, costs may not be tracked properly")
	}
}

func TestExportCostReport(t *testing.T) {
	resetPricingTestState(t)

	// Add some costs first
	usage := &types.TokenUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
	}

	cost := CalculateCost(usage, "gpt-5-nano-2025-08-07", "openai")
	metadata := &types.ResultMetadata{
		RequestID:     "test-003",
		CorrelationID: "corr-003",
		Operation:     "generate",
	}

	TrackCost(cost, metadata)

	// Test exporting cost report
	tests := []struct {
		format  string
		wantErr bool
	}{
		{"json", false},
		{"csv", false},
		{"text", true}, // text format not supported
		{"invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			report, err := ExportCostReport(time.Now().Add(-1*time.Hour), tt.format)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExportCostReport() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && report == "" {
				t.Error("Expected non-empty report")
			}
			if tt.format == "json" && !tt.wantErr {
				var records []CostRecord
				if err := json.Unmarshal([]byte(report), &records); err != nil {
					t.Fatalf("expected valid JSON report, got error %v", err)
				}
				if len(records) == 0 {
					t.Fatal("expected JSON report to contain tracked records")
				}
			}
			if tt.format == "csv" && !tt.wantErr && !strings.HasPrefix(report, "Timestamp,RequestID,CorrelationID,") {
				t.Fatalf("expected CSV report to include correlation id column, got %q", report)
			}
		})
	}
}

func TestGetCostSummaryAndRequestCosts(t *testing.T) {
	resetPricingTestState(t)

	now := time.Now()
	records := []struct {
		requestID string
		model     string
		provider  string
		prompt    int
		output    int
	}{
		{"req-1", "gpt-5-mini-2025-08-07", "openai", 100, 50},
		{"req-2", "gpt-5-mini-2025-08-07", "openai", 200, 100},
	}

	for _, record := range records {
		usage := &types.TokenUsage{
			PromptTokens:     record.prompt,
			CompletionTokens: record.output,
			TotalTokens:      record.prompt + record.output,
		}
		cost := CalculateCost(usage, record.model, record.provider)
		TrackCost(cost, &types.ResultMetadata{
			RequestID:     record.requestID,
			CorrelationID: "corr-batch",
			Model:         record.model,
			Provider:      record.provider,
			EndTime:       now,
			TokenUsage:    usage,
		})
	}

	summary := GetCostSummary(now.Add(-time.Minute), map[string]string{"provider": "openai"})
	if summary.RequestCount != 2 {
		t.Fatalf("expected 2 requests, got %d", summary.RequestCount)
	}
	if summary.TotalTokens != 450 {
		t.Fatalf("expected 450 total tokens, got %d", summary.TotalTokens)
	}
	if summary.AverageTokensPerRequest != 225 {
		t.Fatalf("expected average tokens per request 225, got %v", summary.AverageTokensPerRequest)
	}
	if summary.AverageCostPerRequest <= 0 {
		t.Fatalf("expected positive average cost per request, got %v", summary.AverageCostPerRequest)
	}

	requests := GetRequestCosts(now.Add(-time.Minute), map[string]string{"provider": "openai"})
	if len(requests) != 2 {
		t.Fatalf("expected 2 request records, got %d", len(requests))
	}

	request, ok := GetRequestCost("req-2")
	if !ok {
		t.Fatal("expected req-2 to be found")
	}
	if request.CorrelationID != "corr-batch" {
		t.Fatalf("expected req-2 correlation id corr-batch, got %q", request.CorrelationID)
	}
	if request.TokenUsage.TotalTokens != 300 {
		t.Fatalf("expected req-2 total tokens 300, got %d", request.TokenUsage.TotalTokens)
	}

	filtered := GetRequestCosts(now.Add(-time.Minute), map[string]string{"correlation_id": "corr-batch"})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 correlation-filtered requests, got %d", len(filtered))
	}
}

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

// readCostReportCSV parses a CSV cost report into its header and rows.
func readCostReportCSV(t *testing.T, report string) (header []string, rows [][]string) {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(report)).ReadAll()
	if err != nil {
		t.Fatalf("the report is not valid CSV: %v\n%s", err, report)
	}
	if len(records) == 0 {
		t.Fatal("the report is empty")
	}
	return records[0], records[1:]
}

// The default model costs about a hundred-thousandth of a dollar for a small
// call, which a four-place format wrote as 0.0000: a column of zeros that sums
// to nothing and does not say whether the calls were free or unpriced.
func TestTheCSVReportKeepsTheCostOfASmallCall(t *testing.T) {
	resetAll(t)

	cost := CalculateCost(&types.TokenUsage{PromptTokens: 100, TotalTokens: 100}, "gpt-6-luna", "openai")
	if cost == nil || !cost.Priced || cost.TotalCost <= 0 || cost.TotalCost >= 0.0001 {
		t.Fatalf("the fixture is wrong: %+v", cost)
	}
	TrackCost(cost, &types.ResultMetadata{RequestID: "small", Model: "gpt-6-luna", Provider: "openai", EndTime: time.Now(),
		TokenUsage: &types.TokenUsage{PromptTokens: 100, TotalTokens: 100}})

	report, err := ExportCostReport(time.Time{}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	header, rows := readCostReportCSV(t, report)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	column := func(name string) string {
		for i, h := range header {
			if h == name {
				return rows[0][i]
			}
		}
		t.Fatalf("no %q column in %v", name, header)
		return ""
	}
	got, err := strconv.ParseFloat(column("Cost"), 64)
	if err != nil || got != cost.TotalCost {
		t.Errorf("Cost = %q, want %v exactly", column("Cost"), cost.TotalCost)
	}
	if column("Priced") != "true" || column("PricingQuality") != string(types.PricingExact) {
		t.Errorf("Priced = %q, PricingQuality = %q", column("Priced"), column("PricingQuality"))
	}
}

// An unpriced call is marked rather than written as a cost of zero.
func TestTheCSVReportMarksAnUnpricedCall(t *testing.T) {
	resetAll(t)
	trackUnpriced(t, "unpriced-1")
	track(t, "priced-1", 0.5)

	report, err := ExportCostReport(time.Time{}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	header, rows := readCostReportCSV(t, report)
	index := map[string]int{}
	for i, h := range header {
		index[h] = i
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	unpriced, priced := rows[0], rows[1]
	if unpriced[index["Cost"]] != "" || unpriced[index["Priced"]] != "false" || unpriced[index["PricingQuality"]] != string(types.PricingUnknown) {
		t.Errorf("unpriced row = %v: want an empty Cost, Priced false and quality %q", unpriced, types.PricingUnknown)
	}
	if priced[index["Cost"]] != "0.5" || priced[index["Priced"]] != "true" {
		t.Errorf("priced row = %v: want Cost 0.5 and Priced true", priced)
	}
}

// Text fields that hold a delimiter, a quote or a line break stay in their
// column, and one a spreadsheet would read as a formula is written as text.
func TestTheCSVReportEscapesTextFields(t *testing.T) {
	resetAll(t)

	cases := []struct {
		name      string
		requestID string
		want      string
	}{
		{"plain", "req-1", "req-1"},
		{"comma", "a,b", "a,b"},
		{"quote", `say "hi"`, `say "hi"`},
		{"newline", "line1\nline2", "line1\nline2"},
		{"carriage return", "line1\rline2", "line1\rline2"},
		{"equals formula", "=SUM(A1:A9)", "'=SUM(A1:A9)"},
		{"plus formula", "+1+1", "'+1+1"},
		{"minus formula", "-2", "'-2"},
		{"at formula", "@cmd", "'@cmd"},
		{"tab prefix", "\tx", "'\tx"},
		{"carriage return prefix", "\rx", "'\rx"},
		{"leading space is not a formula", " =1", " =1"},
		{"comma and formula", "=a,b", "'=a,b"},
		{"empty", "", ""},
	}
	for i, tc := range cases {
		TrackCost(&types.CostInfo{TotalCost: 0.1, Priced: true, Quality: types.PricingExact}, &types.ResultMetadata{
			RequestID: tc.requestID, CorrelationID: fmt.Sprintf("c%d", i), Operation: "extract", EndTime: time.Now(),
		})
	}

	report, err := ExportCostReport(time.Time{}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	header, rows := readCostReportCSV(t, report)
	if len(rows) != len(cases) {
		t.Fatalf("%d rows, want %d: a field shifted the row boundaries", len(rows), len(cases))
	}
	for i, tc := range cases {
		if len(rows[i]) != len(header) {
			t.Errorf("%s: %d columns, want %d", tc.name, len(rows[i]), len(header))
			continue
		}
		if rows[i][1] != tc.want {
			t.Errorf("%s: RequestID cell = %q, want %q", tc.name, rows[i][1], tc.want)
		}
		if rows[i][2] != fmt.Sprintf("c%d", i) {
			t.Errorf("%s: the next column moved: %q", tc.name, rows[i][2])
		}
	}
}

// Every column that carries caller text is guarded, not only the request id:
// the correlation id, operation, model and provider reach the report from
// request metadata the caller controls.
func TestEveryTextColumnOfTheCSVReportIsGuardedAgainstFormulas(t *testing.T) {
	columns := []struct {
		name string
		set  func(m *types.ResultMetadata, text string)
	}{
		{"RequestID", func(m *types.ResultMetadata, s string) { m.RequestID = s }},
		{"CorrelationID", func(m *types.ResultMetadata, s string) { m.CorrelationID = s }},
		{"Operation", func(m *types.ResultMetadata, s string) { m.Operation = s }},
		{"Model", func(m *types.ResultMetadata, s string) { m.Model = s }},
		{"Provider", func(m *types.ResultMetadata, s string) { m.Provider = s }},
	}
	leads := map[string]string{"equals": "=", "plus": "+", "minus": "-", "at": "@", "tab": "\t", "carriage return": "\r"}
	for _, column := range columns {
		for leadName, lead := range leads {
			t.Run(column.name+" starting with "+leadName, func(t *testing.T) {
				resetAll(t)
				metadata := &types.ResultMetadata{RequestID: "r", EndTime: time.Now()}
				column.set(metadata, lead+"cmd|' /C calc'!A0")
				TrackCost(&types.CostInfo{TotalCost: 0.1, Currency: "USD", Priced: true}, metadata)

				report, err := ExportCostReport(time.Time{}, "csv")
				if err != nil {
					t.Fatal(err)
				}
				header, rows := readCostReportCSV(t, report)
				if len(rows) != 1 {
					t.Fatalf("%d rows, want 1\n%s", len(rows), report)
				}
				index := -1
				for i, name := range header {
					if name == column.name {
						index = i
					}
				}
				if index < 0 {
					t.Fatalf("the report has no %s column: %v", column.name, header)
				}
				if got := rows[0][index]; !strings.HasPrefix(got, "'"+lead) {
					t.Fatalf("%s = %q, want an apostrophe before the leading %q", column.name, got, lead)
				}
			})
		}
	}
}
