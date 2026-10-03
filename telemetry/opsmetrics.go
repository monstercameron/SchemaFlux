package telemetry

import "github.com/monstercameron/schemaflux/internal/types"

// RecordBatchMetrics emits item- and batch-level metrics from a finished plural
// run's summary: item outcomes by status, the share of valid items, repairs,
// atomic fallbacks, omissions, and cost, including cost per accepted item.
// Everything is read from types.BatchMetrics, the per-item accounting the batch
// engines already keep, rather than computed again here.
//
// operation is the Op's own ID string (types.OperationID.String(), a bounded,
// declared identifier, never a per-call request or correlation ID) and shape
// names how the run executed ("mdsp_recover", "atomic_partial", or a similar
// label the caller chooses).
//
// Every tag comes from a small fixed vocabulary: operation, shape, item status,
// and the pricing currency and quality. Request IDs, correlation IDs, batch item
// IDs and schema hashes are never tags. Each distinct tag set is a separate
// series, so those would multiply the series count by traffic; they belong in
// logs and in the result envelope.
//
// A run that was not priced emits nothing on schemaflux_cost_total, because a
// zero there would read as "this cost nothing". Cost per accepted item is also
// withheld when no priced item was accepted, since there is nothing to divide.
//
// Not emitted here: request-level counters and durations, provider attempts and
// duration, queue duration, the in-flight gauge and circuit state (the
// scheduler and resilience layers own those), token totals (BatchMetrics
// carries cost, not tokens), and budget and review counts (the policy layer's).
//
// The caller normally checks config.IsMetricsEnabled before calling, but
// RecordMetric checks it again, so a caller that forgets costs nothing.
func RecordBatchMetrics(operation, shape string, m types.BatchMetrics) {
	base := map[string]string{"operation": operation, "shape": shape}

	RecordMetric("schemaflux_items_total", int64(m.Succeeded), withTag(base, "status", "succeeded"))
	RecordMetric("schemaflux_items_total", int64(m.Failed), withTag(base, "status", "failed"))
	RecordMetric("schemaflux_items_total", int64(m.Cancelled), withTag(base, "status", "cancelled"))

	RecordMetricValue("schemaflux_batch_compliance_ratio", m.ValidItemRatio, base)
	RecordMetric("schemaflux_repairs_total", int64(m.TotalRepairs), base)
	RecordMetric("schemaflux_atomic_fallback_total", int64(m.AtomicFallbacks), base)
	RecordMetric("schemaflux_omissions_total", int64(m.Omissions), base)

	if !m.CostPriced {
		// An unpriced run emits nothing on schemaflux_cost_total rather than a
		// confident zero that would read as "this cost nothing" to anyone
		// graphing the series.
		return
	}
	costTags := withTag(base, "currency", m.Currency)
	if m.AcceptedCost != 0 || m.Succeeded > 0 {
		RecordMetricValue("schemaflux_cost_total", m.AcceptedCost, withTag(costTags, "quality", "accepted"))
	}
	if m.FailedCost != 0 || m.Failed > 0 {
		RecordMetricValue("schemaflux_cost_total", m.FailedCost, withTag(costTags, "quality", "failed"))
	}

	// schemaflux_cost_per_accepted_item is guarded a second time, past
	// m.CostPriced: a batch can be priced overall (a failed item's call was
	// still billed) while having zero succeeded, priced items to divide into
	// AcceptedCost. Metrics() leaves CostQuality at PricingUnknown for
	// exactly that case (batchresult.go) as well as the fully-unpriced one,
	// so checking it here is the one condition that covers both -- and that
	// zero must not become a series either way; it is unset, not a batch
	// that cost nothing per item.
	if m.CostQuality != types.PricingUnknown {
		RecordMetricValue("schemaflux_cost_per_accepted_item", m.CostPerAcceptedItem, costTags)
	}
}

// RecordPlanMetrics emits the fan-out metrics of a Preflight plan before any
// provider call is made: schemaflux_plan_nodes (the call ceiling the run's
// fan-out implies) and schemaflux_batch_size (one observation per planned
// chunk, so the aggregate's min, max and average describe the chunk sizes the
// packing produced, not just their total).
func RecordPlanMetrics(operation string, plan types.Plan) {
	if !plan.Eligible {
		return
	}
	tags := map[string]string{"operation": operation, "shape": plan.Shape.String()}

	RecordMetric("schemaflux_plan_nodes", int64(len(plan.Chunks)+len(plan.OversizedItems)), tags)
	for _, c := range plan.Chunks {
		RecordMetric("schemaflux_batch_size", int64(c.ItemCount), tags)
	}
}

// withTag returns a copy of base with one key/value added, so the caller's
// own map is never mutated out from under a concurrent caller reusing it.
func withTag(base map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[key] = value
	return out
}
