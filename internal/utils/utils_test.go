package utils

import (
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/telemetry"
)

func TestRecordMetricDelegatesToTelemetry(t *testing.T) {
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")

	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)

	RecordMetric("internal_metric", 7, map[string]string{"source": "utils"})

	snapshot, ok := telemetry.GetMetricSnapshot("internal_metric", map[string]string{"source": "utils"})
	if !ok {
		t.Fatal("expected metric snapshot to be recorded")
	}
	if snapshot.Count != 1 || snapshot.Sum != 7 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}
