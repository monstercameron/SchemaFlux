package telemetry

import (
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                     allocs/op       B/op
//	RecordLLMMetrics/off                 16       2048
//	RecordLLMMetrics/off/parallel        16       2048
//	RecordLLMMetrics/on                  88       6848
//	RecordLLMMetrics/on/parallel         88       6848
//	RecordMetric/off                      2        256

// What the metrics hook costs every call, with metrics off (the default) and on.
//
// CallLLM records the metrics of every completed request through
// RecordLLMMetrics. Metrics are off unless SCHEMAFLUX_METRICS=true, and a call
// that records nothing should cost close to nothing. Each of the eight or so
// RecordMetric calls it makes asks config.IsMetricsEnabled, which takes a lock
// and reads the environment, and the tag map is built before the first of them
// asks, so the "off" figure is the price of a feature nobody turned on.
//
// The SCHEMAFLUX_METRICS environment variable is cleared for the "off" runs, so
// a developer's own setting cannot make the baseline differ between machines.

func benchResultMetadata() *types.ResultMetadata {
	return &types.ResultMetadata{
		Model:        "gpt-5.4",
		Provider:     "openai",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
		Duration:     400 * time.Millisecond,
		TokenUsage:   &types.TokenUsage{PromptTokens: 1200, CompletionTokens: 240, TotalTokens: 1440},
		CostInfo:     &types.CostInfo{TotalCost: 0.0042, PromptCost: 0.003, CompletionCost: 0.0012, Priced: true},
	}
}

// benchMetrics sets whether metrics are on for one benchmark and puts the
// previous setting back.
func benchMetrics(b *testing.B, enabled bool) {
	b.Helper()
	configtest.Setenv(b, "SCHEMAFLUX_METRICS", "")
	previous := config.IsMetricsEnabled()
	config.SetMetricsEnabled(enabled)
	ResetMetrics()
	b.Cleanup(func() {
		config.SetMetricsEnabled(previous)
		ResetMetrics()
	})
}

func BenchmarkRecordLLMMetrics(b *testing.B) {
	metadata := benchResultMetadata()
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			benchMetrics(b, enabled)
			b.ReportAllocs()
			for b.Loop() {
				RecordLLMMetrics(metadata)
			}
		})
		b.Run(name+"/parallel", func(b *testing.B) {
			benchMetrics(b, enabled)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					RecordLLMMetrics(metadata)
				}
			})
		})
	}
}

// BenchmarkRecordMetric is one call to the recorder with metrics off: the unit
// RecordLLMMetrics repeats.
func BenchmarkRecordMetric(b *testing.B) {
	tags := map[string]string{"provider": "openai", "model": "gpt-5.4", "mode": "transform", "intelligence": "fast"}
	b.Run("off", func(b *testing.B) {
		benchMetrics(b, false)
		b.ReportAllocs()
		for b.Loop() {
			RecordMetric("llm_requests", 1, tags)
		}
	})
}
