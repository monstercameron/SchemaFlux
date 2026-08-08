package telemetry

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// OB-002. The catalog's own rule (docs/engineering/plans/to-production.md
// §15.1): "High-cardinality details belong in logs or the envelope, not
// metric labels." A request ID in a label multiplies the series count by
// traffic -- these tests walk every tag key and value RecordBatchMetrics and
// RecordPlanMetrics actually emit, rather than eyeballing the source, and
// fail if a forbidden identifier shape shows up anywhere.

func enableMetrics(t *testing.T) {
	t.Helper()
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(true)
}

// forbiddenLabelKeys are the identifiers that must never appear as a tag
// key: a request ID, a correlation ID, a schema hash, or any other per-call
// identifier that would multiply a metric's series count by traffic instead
// of staying a fixed, small vocabulary.
var forbiddenLabelKeys = []string{
	"request_id", "requestid", "requestID",
	"correlation_id", "correlationid", "correlationID",
	"schema_hash", "schemahash", "schema_id", "schemaid",
	"trace_id", "traceid", "span_id", "spanid",
	"item_id", "itemid", "batch_id", "batchid",
}

// walkLabels asserts every tag key across snapshots is not one of
// forbiddenLabelKeys, and that no tag value looks like a per-call
// identifier: this library's own request/correlation ids are "req_" or
// "corr_" prefixed hex (llm_helper.go's requestID generation) and its batch
// ids are "i-NNNNNN" (batchprotocol.go's itemID) -- neither shape should
// ever appear as a label value emitted by this file.
func walkLabels(t *testing.T, snapshots []MetricSnapshot) {
	t.Helper()
	if len(snapshots) == 0 {
		t.Fatal("no metrics were recorded; this test cannot prove anything about labels that were never emitted")
	}
	for _, s := range snapshots {
		for key, value := range s.Tags {
			lower := strings.ToLower(key)
			for _, forbidden := range forbiddenLabelKeys {
				if lower == strings.ToLower(forbidden) {
					t.Errorf("metric %s carries forbidden high-cardinality label key %q", s.Name, key)
				}
			}
			if strings.HasPrefix(value, "req_") || strings.HasPrefix(value, "corr_") {
				t.Errorf("metric %s tag %s=%q looks like a request/correlation id, not a label value", s.Name, key, value)
			}
			if strings.HasPrefix(value, "i-") && len(value) == 8 {
				// batchprotocol.go's itemID format: "i-" + 6 digits.
				t.Errorf("metric %s tag %s=%q looks like a batch-protocol item id, not a label value", s.Name, key, value)
			}
		}
	}
}

func TestRecordBatchMetricsEmitsNoHighCardinalityLabels(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{
		Total: 10, Succeeded: 8, Failed: 1, Cancelled: 1,
		ValidItemRatio: 0.8, TotalRepairs: 2, AtomicFallbacks: 1,
		CostPriced: true, Currency: "USD", AcceptedCost: 0.05, FailedCost: 0.01,
	}
	RecordBatchMetrics("classify/v2", "mdsp_recover", m)

	walkLabels(t, SnapshotMetrics())
}

func TestRecordBatchMetricsUnpricedEmitsNoCostSeries(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{Total: 3, Succeeded: 3, ValidItemRatio: 1}
	RecordBatchMetrics("classify/v2", "atomic_partial", m)

	for _, s := range SnapshotMetrics() {
		if s.Name == "schemaflux_cost_total" {
			t.Errorf("an unpriced batch must not emit schemaflux_cost_total (PR-002: unpriced is not free), got snapshot %+v", s)
		}
	}
}

func TestRecordBatchMetricsItemsTotalCoversEveryStatus(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{Total: 6, Succeeded: 3, Failed: 2, Cancelled: 1}
	RecordBatchMetrics("extract/v1", "mdsp_recover", m)

	want := map[string]float64{"succeeded": 3, "failed": 2, "cancelled": 1}
	for status, expect := range want {
		snap, ok := GetMetricSnapshot("schemaflux_items_total", map[string]string{
			"operation": "extract/v1", "shape": "mdsp_recover", "status": status,
		})
		if !ok {
			t.Fatalf("no schemaflux_items_total snapshot for status %q", status)
		}
		if snap.LastValue != expect {
			t.Errorf("status %q: LastValue = %v, want %v", status, snap.LastValue, expect)
		}
	}
}

func TestRecordBatchMetricsComplianceRatioMatchesInput(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{Total: 4, Succeeded: 3, ValidItemRatio: 0.75}
	RecordBatchMetrics("summarize/v1", "mdsp_recover", m)

	snap, ok := GetMetricSnapshot("schemaflux_batch_compliance_ratio", map[string]string{
		"operation": "summarize/v1", "shape": "mdsp_recover",
	})
	if !ok {
		t.Fatal("no schemaflux_batch_compliance_ratio snapshot recorded")
	}
	if snap.LastValue != 0.75 {
		t.Errorf("LastValue = %v, want 0.75", snap.LastValue)
	}
}

func TestRecordBatchMetricsOmissionsTotalMatchesInput(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{Total: 5, Succeeded: 4, Failed: 1, Omissions: 2, AtomicFallbacks: 1}
	RecordBatchMetrics("extract/v1", "mdsp_recover", m)

	snap, ok := GetMetricSnapshot("schemaflux_omissions_total", map[string]string{
		"operation": "extract/v1", "shape": "mdsp_recover",
	})
	if !ok {
		t.Fatal("no schemaflux_omissions_total snapshot recorded")
	}
	if snap.LastValue != 2 {
		t.Errorf("LastValue = %v, want 2", snap.LastValue)
	}
}

func TestRecordBatchMetricsOmissionsTotalEmitsZeroForARunThatNeverBatched(t *testing.T) {
	// RunOpManyPartial's own runs report Omissions == 0 honestly (see
	// types.BatchResult.Metrics' doc comment) -- this must still emit a
	// zero-valued series, not skip it, so a caller graphing omissions over
	// time sees an unbroken line rather than a gap that looks like missing
	// data.
	enableMetrics(t)

	RecordBatchMetrics("classify/v2", "atomic_partial", types.BatchMetrics{Total: 3, Succeeded: 3})

	snap, ok := GetMetricSnapshot("schemaflux_omissions_total", map[string]string{
		"operation": "classify/v2", "shape": "atomic_partial",
	})
	if !ok {
		t.Fatal("no schemaflux_omissions_total snapshot recorded for a zero-omission run")
	}
	if snap.LastValue != 0 {
		t.Errorf("LastValue = %v, want 0", snap.LastValue)
	}
}

func TestRecordBatchMetricsCostPerAcceptedItemEmitsTheGuardedNumber(t *testing.T) {
	enableMetrics(t)

	m := types.BatchMetrics{
		Total: 4, Succeeded: 2, Failed: 2,
		CostPriced: true, Currency: "USD", CostQuality: types.PricingExact,
		AcceptedCost: 2.0, FailedCost: 2.0, CostPerAcceptedItem: 1.0,
	}
	RecordBatchMetrics("summarize/v1", "mdsp_recover", m)

	snap, ok := GetMetricSnapshot("schemaflux_cost_per_accepted_item", map[string]string{
		"operation": "summarize/v1", "shape": "mdsp_recover", "currency": "USD",
	})
	if !ok {
		t.Fatal("no schemaflux_cost_per_accepted_item snapshot recorded")
	}
	if snap.LastValue != 1.0 {
		t.Errorf("LastValue = %v, want 1.0 -- cost per ACCEPTED item, not per item sent", snap.LastValue)
	}
}

func TestRecordBatchMetricsCostPerAcceptedItemUnknownEmitsNothing(t *testing.T) {
	// PL-013's own trap: a batch can be priced overall (CostPriced true, a
	// failed item's call was still billed) while CostQuality stays
	// PricingUnknown because nothing succeeded to price per-item. That must
	// not produce a confident 0.0 series.
	enableMetrics(t)

	m := types.BatchMetrics{
		Total: 2, Failed: 2,
		CostPriced: true, Currency: "USD", CostQuality: types.PricingUnknown,
		FailedCost: 1.0,
	}
	RecordBatchMetrics("extract/v1", "atomic_partial", m)

	for _, s := range SnapshotMetrics() {
		if s.Name == "schemaflux_cost_per_accepted_item" {
			t.Errorf("a batch with zero priced accepted items must not emit schemaflux_cost_per_accepted_item, got snapshot %+v", s)
		}
	}
}

func TestRecordBatchMetricsDisabledWhenMetricsAreOff(t *testing.T) {
	ResetMetrics()
	t.Cleanup(ResetMetrics)
	t.Setenv("SCHEMAFLUX_METRICS", "")
	original := config.IsMetricsEnabled()
	t.Cleanup(func() { config.SetMetricsEnabled(original) })
	config.SetMetricsEnabled(false)

	RecordBatchMetrics("classify/v2", "mdsp_recover", types.BatchMetrics{Total: 5, Succeeded: 5})

	if len(SnapshotMetrics()) != 0 {
		t.Error("RecordMetric/RecordMetricValue must no-op when metrics are disabled")
	}
}

func TestRecordPlanMetricsEmitsNoHighCardinalityLabels(t *testing.T) {
	enableMetrics(t)

	plan := types.Plan{
		Operation: types.OperationID{Name: "classify", Version: "v2"},
		Eligible:  true,
		Shape:     types.ShapeMDSP,
		Chunks: []types.ChunkPlan{
			{ItemCount: 20}, {ItemCount: 20}, {ItemCount: 15},
		},
	}
	RecordPlanMetrics("classify/v2", plan)

	walkLabels(t, SnapshotMetrics())
}

func TestRecordPlanMetricsBatchSizeOneObservationPerChunk(t *testing.T) {
	enableMetrics(t)

	plan := types.Plan{
		Operation: types.OperationID{Name: "sort", Version: "v1"},
		Eligible:  true,
		Shape:     types.ShapeGlobal,
		Chunks:    []types.ChunkPlan{{ItemCount: 30}, {ItemCount: 10}},
	}
	RecordPlanMetrics("sort/v1", plan)

	snap, ok := GetMetricSnapshot("schemaflux_batch_size", map[string]string{"operation": "sort/v1", "shape": "global"})
	if !ok {
		t.Fatal("no schemaflux_batch_size snapshot recorded")
	}
	if snap.Count != 2 {
		t.Errorf("expected one observation per chunk (2 chunks), got Count = %d", snap.Count)
	}
	if snap.Min != 10 || snap.Max != 30 {
		t.Errorf("Min/Max = %v/%v, want 10/30", snap.Min, snap.Max)
	}
}

func TestRecordPlanMetricsIneligiblePlanEmitsNothing(t *testing.T) {
	enableMetrics(t)

	plan := types.Plan{Operation: types.OperationID{Name: "broken", Version: "v1"}, Eligible: false}
	RecordPlanMetrics("broken/v1", plan)

	if len(SnapshotMetrics()) != 0 {
		t.Error("an ineligible plan ran nothing; it should not emit a fan-out metric implying it did")
	}
}
