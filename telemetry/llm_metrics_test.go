package telemetry

import (
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RecordLLMMetrics tags every series with provider/model/mode/intelligence --
// a bounded, declared vocabulary, per RecordBatchMetrics's own §15.1 rule
// (opsmetrics.go). These tests exercise it directly since nothing else in
// this package's test suite calls it.

// A nil metadata pointer must be a safe no-op: RecordLLMMetrics is called
// from a completion path that may not always have metadata to report.
func TestRecordLLMMetricsNilMetadataIsSafe(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordLLMMetrics(nil) // must not panic

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected nil metadata to record nothing, got %d snapshots", len(snapshots))
	}
}

// The full path: requests, duration, every non-zero token bucket, and every
// cost bucket, each carrying the same fixed provider/model/mode/intelligence
// tag set.
func TestRecordLLMMetricsRecordsRequestsTokensAndCost(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        "gpt-5.6-luna",
		Mode:         types.Strict,
		Intelligence: types.Smart,
		Duration:     250 * time.Millisecond,
		TokenUsage: &types.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			InputTokens:      90,
			OutputTokens:     45,
			CachedTokens:     10,
			ReasoningTokens:  5,
		},
		CostInfo: &types.CostInfo{
			TotalCost:      0.03,
			PromptCost:     0.02,
			CompletionCost: 0.01,
			CachedCost:     0.001,
			ReasoningCost:  0.002,
		},
	}

	RecordLLMMetrics(metadata)

	tags := map[string]string{
		"provider": "openai", "model": "gpt-5.6-luna", "mode": "strict", "intelligence": "smart",
	}

	wantCounters := map[string]float64{
		"llm_requests":            1,
		"llm_request_duration_ms": 250,
		"llm_tokens_prompt":       100,
		"llm_tokens_completion":   50,
		"llm_tokens_total":        150,
		"llm_tokens_input":        90,
		"llm_tokens_output":       45,
		"llm_tokens_cached":       10,
		"llm_tokens_reasoning":    5,
		"llm_cost_total_usd":      0.03,
		"llm_cost_prompt_usd":     0.02,
		"llm_cost_completion_usd": 0.01,
		"llm_cost_cached_usd":     0.001,
		"llm_cost_reasoning_usd":  0.002,
	}
	for name, want := range wantCounters {
		snapshot, ok := GetMetricSnapshot(name, tags)
		if !ok {
			t.Errorf("metric %s was not recorded", name)
			continue
		}
		if snapshot.LastValue != want {
			t.Errorf("metric %s LastValue = %v, want %v", name, snapshot.LastValue, want)
		}
	}
}

// Zero-valued optional buckets (no cache/reasoning tokens or cost) must not
// emit a series at all -- RecordLLMMetrics only records what actually
// happened, not a full fixed schema padded with zeros.
func TestRecordLLMMetricsOmitsZeroOptionalBuckets(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        "gpt-5-mini",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
		TokenUsage: &types.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
			// InputTokens/OutputTokens/CachedTokens/ReasoningTokens left zero.
		},
		// CostInfo left nil entirely.
	}

	RecordLLMMetrics(metadata)

	forbidden := []string{
		"llm_tokens_input", "llm_tokens_output", "llm_tokens_cached", "llm_tokens_reasoning",
		"llm_cost_total_usd", "llm_cost_prompt_usd", "llm_cost_completion_usd",
		"llm_cost_cached_usd", "llm_cost_reasoning_usd",
	}
	recorded := map[string]bool{}
	for _, s := range SnapshotMetrics() {
		recorded[s.Name] = true
	}
	for _, name := range forbidden {
		if recorded[name] {
			t.Errorf("metric %s should not be emitted when its source value is zero/absent", name)
		}
	}
}

// The privacy bar this task names explicitly, applied here: RecordLLMMetrics
// builds its tag map from exactly provider/model/mode/intelligence -- a
// fixed, small, declared vocabulary (opsmetrics.go's §15.1 rule). A caller's
// payload never has a route into a tag key at all, because nothing in
// ResultMetadata's request/response body fields (there are none on this
// type) is ever read here. This walks the emitted tag set and asserts it is
// exactly that closed set, on every series RecordLLMMetrics produced,
// proving the vocabulary stayed closed rather than just reading the source
// once and trusting it never grows a field.
func TestRecordLLMMetricsTagVocabularyIsClosed(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	// A marker planted in the one string field that becomes a tag value at
	// all (Model) still must not smuggle in a NEW tag *key* -- the closed
	// vocabulary is a property of the key set, independent of what any one
	// value contains.
	const marker = "MARKER-should-stay-a-value-never-a-key-9f2c"

	metadata := &types.ResultMetadata{
		Provider:     "openai",
		Model:        marker,
		Mode:         types.Strict,
		Intelligence: types.Smart,
		TokenUsage:   &types.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		CostInfo:     &types.CostInfo{TotalCost: 1, PromptCost: 1, CompletionCost: 0},
	}
	RecordLLMMetrics(metadata)

	allowedKeys := map[string]bool{"provider": true, "model": true, "mode": true, "intelligence": true}
	snapshots := SnapshotMetrics()
	if len(snapshots) == 0 {
		t.Fatal("expected RecordLLMMetrics to emit at least one series")
	}
	for _, snapshot := range snapshots {
		if strings.Contains(snapshot.Name, marker) {
			t.Fatalf("metric name %q contains the payload marker", snapshot.Name)
		}
		for key := range snapshot.Tags {
			if !allowedKeys[key] {
				t.Fatalf("metric %s carries an undeclared tag key %q outside {provider,model,mode,intelligence}", snapshot.Name, key)
			}
		}
	}
}
