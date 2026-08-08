package telemetry

import "github.com/monstercameron/schemaflux/internal/types"

// OB-002: the metrics catalog per docs/engineering/plans/to-production.md
// §15.2, for the slice PL-013 unblocked -- item outcomes, batch size, batch
// compliance ratio, repairs, atomic fallbacks, omissions, and cost (including
// cost per accepted item) -- all read from types.BatchMetrics
// (internal/types/batchresult.go), the same per-item accounting PL-008/
// PL-009 already keep, rather than a second accounting pass computed here.
//
// §15.1's rule is the one this file exists to honor: "Item IDs and request
// IDs may be attributes only when cardinality and privacy policies permit.
// High-cardinality details belong in logs or the envelope, not metric
// labels." Every tag map built here is a fixed, small vocabulary --
// operation name (a bounded, declared set, the same kind of value
// RecordLLMMetrics already tags with "model"), shape, item status, pricing
// currency and quality. None of them is a request ID, a correlation ID, a
// batch id (batchprotocol.go's "i-000007" strings), or a schema hash --
// those live in the envelope (types.Meta, types.ItemResult) and in logs,
// never in a tag map RecordMetric turns into a metric series.
//
// What this delivers, against §15.2's table: schemaflux_items_total,
// schemaflux_batch_size, schemaflux_batch_compliance_ratio,
// schemaflux_repairs_total, schemaflux_atomic_fallback_total,
// schemaflux_omissions_total, schemaflux_cost_total, and
// schemaflux_cost_per_accepted_item, plus schemaflux_plan_nodes from a
// Preflight plan before any call is made.
//
// schemaflux_omissions_total and schemaflux_cost_per_accepted_item are
// PL-013's own two named numbers that were missing from this file even after
// the rest of the catalog above shipped: omissions is right there on
// types.BatchMetrics (an item no first MDSP pass resolved) and was simply
// never wired to a series, and cost-per-accepted-item -- PL-013's "the number
// that actually says whether a batch was worth running" -- was only ever
// reconstructable by a caller dividing two other series by hand. Both follow
// the same unpriced rule as schemaflux_cost_total below: an unpriced or
// all-failed run emits no schemaflux_cost_per_accepted_item series, never a
// confident zero.
//
// What this does not: schemaflux_requests_total/_duration_seconds (a
// logical-request-level counter/histogram this package cannot emit from
// inside internal/ops's batch engines without also owning the call sites
// this task's file list excludes -- core.go's Extract/Transform/Generate
// already emit their own *_duration metrics, see llm_metrics.go's doc
// comment, and unifying them is a separate task); provider attempts/
// duration, queue duration, in-flight gauge, and circuit state (owned by
// the scheduler and resilience layers, internal/ops/scheduler.go and
// resilience.go, both off limits to this task); tokens_total (BatchMetrics
// carries cost, not a token count -- adding one is PL-013's own remaining
// list, not this task's); and budget_exceeded_total/review_required_total
// (policy.go's territory, also off limits). Each of those is a real gap in
// the §15.2 catalog, not an oversight -- see this file's own doc comment for
// which package would have to own it.

// RecordBatchMetrics emits OB-002's item- and batch-level metrics from a
// finished plural run's PL-013 summary. operation is the Op's own ID string
// (types.OperationID.String(), a bounded, declared identifier -- never a
// per-call request or correlation ID) and shape names how the run executed
// ("mdsp_recover", "atomic_partial", or similar caller-chosen label).
//
// Gated the same way every other call site in this package is
// (config.IsMetricsEnabled(), checked by the caller before this is called,
// the same convention core.go's *_duration metrics already follow) --
// RecordMetric itself re-checks it too, so a caller that forgets still costs
// nothing.
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
		// PR-002's rule, carried into metrics: an unpriced run emits nothing
		// on schemaflux_cost_total rather than a confident zero that would
		// read as "this cost nothing" to anyone graphing the series.
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

// RecordPlanMetrics emits OB-002's fan-out metrics from a Preflight plan,
// before any provider call is made -- schemaflux_plan_nodes (the call
// ceiling a run's fan-out implies) and schemaflux_batch_size (one
// observation per planned chunk, so the aggregate registry's min/max/average
// describe the real chunk-size distribution PL-004's packing produced, not
// just its total).
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
