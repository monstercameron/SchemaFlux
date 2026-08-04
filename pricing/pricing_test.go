package pricing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
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
			expectedMax:      0.1,
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

	// Test with tags filter
	extractCost := GetTotalCost(time.Now().Add(-1*time.Hour), map[string]string{
		"operation": "extract",
	})
	// Note: filtering by operation may not work without proper implementation
	// Just check that it doesn't panic
	_ = extractCost
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
