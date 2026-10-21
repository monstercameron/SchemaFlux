package ops

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// OB-002, wired end to end: Preflight calls through to
// telemetry.RecordPlanMetrics (plan.go). This is the integration half of
// telemetry/opsmetrics_test.go's unit tests: a caller who sets
// opt.RequestID and opt.CorrelationID (the two identifiers AGENTS.md and
// §15.1 both call out by name) must never see either one land in a metric
// label, walked here from the real call path rather than from a hand-built
// types.BatchMetrics value.

func withMetricsEnabled(t *testing.T) {
	t.Helper()
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)
}

func TestPreflightEmitsPlanMetricsWithNoRequestOrCorrelationIDLabel(t *testing.T) {
	withMetricsEnabled(t)

	opt := types.OpOptions{RequestID: "req_preflight_marker", CorrelationID: "corr_preflight_marker"}
	items := widgets(5)

	if _, err := Preflight(widgetOp(), items, types.PlanRequest{Options: opt}); err != nil {
		t.Fatalf("Preflight failed: %v", err)
	}

	assertNoIdentifierLabels(t, "req_preflight_marker", "corr_preflight_marker")
}

// assertNoIdentifierLabels walks every metric snapshot recorded during the
// test and fails if any tag key or value equals or contains one of the
// caller-supplied identifiers -- the walk PL-011/OB-002's verify line asks
// for, over the real snapshots this package's own call sites produced,
// not a hand-built fixture.
func assertNoIdentifierLabels(t *testing.T, forbidden ...string) {
	t.Helper()
	snapshots := telemetry.SnapshotMetrics()
	if len(snapshots) == 0 {
		t.Fatal("no metrics were recorded; this test cannot prove anything about labels that were never emitted")
	}
	for _, s := range snapshots {
		for key, value := range s.Tags {
			for _, f := range forbidden {
				if key == f || value == f || strings.Contains(value, f) {
					t.Errorf("metric %s carries the caller's identifier in a label: %s=%q", s.Name, key, value)
				}
			}
		}
	}
}

// metricNames is a metric sink that keeps the name of every metric it is given.
type metricNames struct {
	mu    sync.Mutex
	names map[string]int
}

func (m *metricNames) RecordMetric(event telemetry.MetricEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.names == nil {
		m.names = map[string]int{}
	}
	m.names[event.Name]++
}

// A host that registers a metric sink and never sets SCHEMAFLUX_METRICS is
// given the duration of each call of Extract, Transform and Generate, as it is
// given every other metric: the switch is on when a sink is registered
// (telemetry.MetricsEnabled), and the three sites used to ask the switch alone.
func TestASinkRegisteredWithTheSwitchOffIsGivenTheDurationOfEachCall(t *testing.T) {
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	configtest.Setenv(t, "SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(false)
	if telemetry.MetricsEnabled() {
		t.Fatal("the switch is on before a sink is registered")
	}

	var sink metricNames
	t.Cleanup(telemetry.RegisterMetricSink(&sink))
	if config.IsMetricsEnabled() || !telemetry.MetricsEnabled() {
		t.Fatalf("the switch is %v and the gate is %v, want off and on", config.IsMetricsEnabled(), telemetry.MetricsEnabled())
	}

	type invoice struct {
		Number string `json:"number"`
	}
	ctx := WithProvider(context.Background(), &blockingProvider{name: "local"})
	extractOptions := NewExtractOptions()
	extractOptions.CommonOptions.Context = ctx
	if _, err := Extract[invoice]("Invoice INV-1 from Acme", extractOptions); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	transformOptions := NewTransformOptions()
	transformOptions.CommonOptions.Context = ctx
	if _, err := Transform[invoice, invoice](invoice{Number: "1"}, transformOptions); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	generateOptions := NewGenerateOptions()
	generateOptions.CommonOptions.Context = ctx
	if _, err := Generate[invoice]("Write an invoice.", generateOptions); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, name := range []string{"extract_duration", "transform_duration", "generate_duration"} {
		if sink.names[name] != 1 {
			t.Errorf("the sink was given %d %s metrics, want 1 (it was given %v)", sink.names[name], name, sink.names)
		}
	}
}
