package telemetry

import (
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
)

type testMetricSink struct {
	mu     sync.Mutex
	events []MetricEvent
}

func (s *testMetricSink) RecordMetric(event MetricEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *testMetricSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestRecordMetricAggregatesSnapshots(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetric("extract_duration", 120, map[string]string{"mode": "strict", "type": "Person"})
	RecordMetric("extract_duration", 80, map[string]string{"type": "Person", "mode": "strict"})

	snapshot, ok := GetMetricSnapshot("extract_duration", map[string]string{"mode": "strict", "type": "Person"})
	if !ok {
		t.Fatal("expected snapshot to exist")
	}

	if snapshot.Count != 2 {
		t.Fatalf("expected count 2, got %d", snapshot.Count)
	}
	if snapshot.Sum != 200 {
		t.Fatalf("expected sum 200, got %v", snapshot.Sum)
	}
	if snapshot.Min != 80 {
		t.Fatalf("expected min 80, got %v", snapshot.Min)
	}
	if snapshot.Max != 120 {
		t.Fatalf("expected max 120, got %v", snapshot.Max)
	}
	if snapshot.LastValue != 80 {
		t.Fatalf("expected last value 80, got %v", snapshot.LastValue)
	}
	if snapshot.Average() != 100 {
		t.Fatalf("expected average 100, got %v", snapshot.Average())
	}
	if snapshot.Tags["mode"] != "strict" || snapshot.Tags["type"] != "Person" {
		t.Fatalf("unexpected tags: %#v", snapshot.Tags)
	}
}

func TestRecordMetricValueSupportsDecimals(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetricValue("llm_cost_total_usd", 0.0125, map[string]string{"model": "gpt-5-mini"})
	RecordMetricValue("llm_cost_total_usd", 0.0375, map[string]string{"model": "gpt-5-mini"})

	snapshot, ok := GetMetricSnapshot("llm_cost_total_usd", map[string]string{"model": "gpt-5-mini"})
	if !ok {
		t.Fatal("expected cost snapshot to exist")
	}
	if snapshot.Sum != 0.05 {
		t.Fatalf("expected sum 0.05, got %v", snapshot.Sum)
	}
	if snapshot.Average() != 0.025 {
		t.Fatalf("expected average 0.025, got %v", snapshot.Average())
	}
}

func TestRecordMetricDisabledSkipsStorage(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(false)

	RecordMetric("generate_duration", 42, nil)

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected no snapshots, got %d", len(snapshots))
	}
}

func TestRegisterMetricSinkReceivesEvents(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	sink := &testMetricSink{}
	unregister := RegisterMetricSink(sink)
	t.Cleanup(unregister)

	RecordMetric("transform_duration", 33, map[string]string{"mode": "transform"})

	if sink.count() != 1 {
		t.Fatalf("expected sink to receive one event, got %d", sink.count())
	}

	unregister()
	RecordMetric("transform_duration", 44, map[string]string{"mode": "transform"})

	if sink.count() != 1 {
		t.Fatalf("expected sink count to remain 1 after unregister, got %d", sink.count())
	}
}
