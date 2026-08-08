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

// GetMetricSnapshot for a metric/tag combination nobody recorded must report
// "not found" rather than a zero-valued snapshot that reads as "this metric
// really was observed, and it was zero every time."
func TestGetMetricSnapshotUnknownMetricReportsNotFound(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)

	snapshot, ok := GetMetricSnapshot("no_such_metric_was_ever_recorded", nil)
	if ok {
		t.Fatalf("expected ok=false for an unrecorded metric, got snapshot %+v", snapshot)
	}
	if snapshot.Name != "" || snapshot.Count != 0 || len(snapshot.Tags) != 0 {
		t.Fatalf("expected the zero MetricSnapshot on a miss, got %+v", snapshot)
	}
}

// Average on a snapshot nothing has recorded into (Count == 0) must return 0
// rather than dividing by zero.
func TestMetricSnapshotAverageWithNoObservations(t *testing.T) {
	var snapshot MetricSnapshot
	if got := snapshot.Average(); got != 0 {
		t.Fatalf("Average() on an empty snapshot = %v, want 0", got)
	}
}

// RecordMetricValue with a blank (or whitespace-only) name must be silently
// skipped -- there is no metric to attach a series to.
func TestRecordMetricValueBlankNameIsSkipped(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetricValue("", 1, nil)
	RecordMetricValue("   ", 1, nil)

	if snapshots := SnapshotMetrics(); len(snapshots) != 0 {
		t.Fatalf("expected a blank metric name to record nothing, got %d snapshots", len(snapshots))
	}
}

// RegisterMetricSink(nil) must return a harmless no-op unregister rather than
// panicking later when the registry is walked with a nil sink inside it.
func TestRegisterMetricSinkNilIsHarmless(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	unregister := RegisterMetricSink(nil)
	if unregister == nil {
		t.Fatal("RegisterMetricSink(nil) returned a nil unregister func")
	}
	unregister() // must not panic

	// A real recording must still work: no nil sink was left behind to crash on.
	RecordMetric("after_nil_sink", 1, nil)
	if _, ok := GetMetricSnapshot("after_nil_sink", nil); !ok {
		t.Fatal("expected the metric to be recorded even after registering/unregistering a nil sink")
	}
}

// cloneTags(nil) and cloneTags({}) both return a non-nil empty map -- the
// snapshot's Tags field is never nil, so a caller can range over it
// unconditionally.
func TestCloneTagsNormalizesEmptyAndNil(t *testing.T) {
	if got := cloneTags(nil); got == nil || len(got) != 0 {
		t.Fatalf("cloneTags(nil) = %#v, want a non-nil empty map", got)
	}
	if got := cloneTags(map[string]string{}); got == nil || len(got) != 0 {
		t.Fatalf("cloneTags({}) = %#v, want a non-nil empty map", got)
	}

	original := map[string]string{"a": "1"}
	cloned := cloneTags(original)
	cloned["a"] = "mutated"
	if original["a"] != "1" {
		t.Fatal("cloneTags did not deep-copy; mutating the clone changed the source map")
	}
}

// canonicalTags(nil)/({}) must both be "" so an untagged metric and an
// explicitly-empty-tagged one collapse to the same aggregate key.
func TestCanonicalTagsEmptyIsBlank(t *testing.T) {
	if got := canonicalTags(nil); got != "" {
		t.Fatalf("canonicalTags(nil) = %q, want empty", got)
	}
	if got := canonicalTags(map[string]string{}); got != "" {
		t.Fatalf("canonicalTags({}) = %q, want empty", got)
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
