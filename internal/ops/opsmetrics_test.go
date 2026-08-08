package ops

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// OB-002, wired end to end: Preflight, RunOpManyRecover, and
// RunOpManyPartial all call through to telemetry.RecordPlanMetrics /
// RecordBatchMetrics (plan.go, recover.go, partial.go). This is the
// integration half of telemetry/opsmetrics_test.go's unit tests: a caller
// who sets opt.RequestID and opt.CorrelationID (the two identifiers
// AGENTS.md and §15.1 both call out by name) must never see either one
// land in a metric label, walked here from the real call path rather than
// from a hand-built types.BatchMetrics value.

func withMetricsEnabled(t *testing.T) {
	t.Helper()
	telemetry.ResetMetrics()
	t.Cleanup(telemetry.ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)
}

func TestRunOpManyRecoverEmitsMetricsWithNoRequestOrCorrelationIDLabel(t *testing.T) {
	withMetricsEnabled(t)

	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	opt := types.OpOptions{RequestID: "req_should_never_be_a_label", CorrelationID: "corr_should_never_be_a_label"}
	items := recoverWidgets(5)

	_, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), opt, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover failed: %v", err)
	}

	assertNoIdentifierLabels(t, "req_should_never_be_a_label", "corr_should_never_be_a_label")
}

func TestRunOpManyPartialEmitsMetricsWithNoRequestOrCorrelationIDLabel(t *testing.T) {
	withMetricsEnabled(t)

	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	opt := types.OpOptions{RequestID: "req_partial_marker", CorrelationID: "corr_partial_marker"}
	items := recoverWidgets(3)

	_, err := RunOpManyPartial(ctx, recoverWidgetOp(), items, opt, PartialConfig{Policy: types.PolicyCollectFailures})
	if err != nil {
		t.Fatalf("RunOpManyPartial failed: %v", err)
	}

	assertNoIdentifierLabels(t, "req_partial_marker", "corr_partial_marker")
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
