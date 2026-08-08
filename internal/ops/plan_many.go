package ops

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-007: a plural API, with batching, ordering, and a call ceiling as
// stated semantics rather than a caller's own loop reinterpreting a
// singular function.
//
// RunOpMany is the generic engine a plural API (a prospective ExtractMany,
// ClassifyMany) wires an Op[In, Out] onto. It is not those functions itself
// -- schemaflux.go is off limits to this task, and neither of Extract's or
// Classify's current Op descriptors (opextract.go) render a per-item prompt
// from In, which a genuine batch of independent inputs needs BuildPrompt to
// do. Wiring ExtractMany/ClassifyMany at the root is future work; this is
// the part of it that belongs in internal/ops.
//
// What it does deliver: Preflight decides the shape, the shape decides how
// items are dispatched, dispatch runs through MapReduce's existing bounded
// worker pool (CF-009) rather than a hand-rolled goroutine loop, and the
// result order always matches the input order regardless of which chunk or
// item finished first.
//
// What it does not deliver: PL-008's partial-success policies. A single
// item's or chunk's failure fails the whole call, the same way MapReduce's
// default (ContinueOnError: false) already behaves everywhere else in this
// package. A caller that needs "get me the 498 that worked" is asking for
// PL-008, which RunOpManyPartial (partial.go) delivers as a separate,
// additional entry point rather than a change to this function's contract:
// RunOpMany's callers today rely on "any failure fails the call", and
// changing that out from under them silently would be exactly the kind of
// drift AGENTS.md warns about. RunOpManyPartial always runs ShapeAtomic
// (one call per item); it does not give MDSP-chunk failures the same
// per-item accounting -- that is PL-009's progressive recovery cascade and
// is not built here either.
func RunOpMany[In, Out any](ctx context.Context, op Op[In, Out], items []In, req types.PlanRequest) ([]types.Result[Out], types.Plan, error) {
	plan, err := Preflight(op, items, req)
	if err != nil {
		return nil, plan, err
	}
	if !plan.Eligible {
		return nil, plan, fmt.Errorf("op %s: plan is not eligible: %s", op.ID, plan.IneligibleReason)
	}
	if len(items) == 0 {
		return nil, plan, nil
	}

	switch plan.Shape {
	case types.ShapeAtomic:
		return runManyAtomic(ctx, op, items, req.Options, plan)

	case types.ShapeMDSP, types.ShapeGlobal:
		return runManyBatched(ctx, op, items, req.Options, plan)

	default:
		return nil, plan, fmt.Errorf("op %s: shape %s has no execution path in this delivery", op.ID, plan.Shape)
	}
}

// runManyAtomic runs one RunOpResult per item, bounded to DefaultConcurrency
// in flight, via MapReduce with ChunkSize 1 -- one goroutine pool, reused,
// rather than a second one this function starts by hand.
func runManyAtomic[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, plan types.Plan) ([]types.Result[Out], types.Plan, error) {
	results, _, err := MapReduce(ctx, items,
		MapReduceOptions{ChunkSize: 1, Concurrency: DefaultConcurrency},
		func(ctx context.Context, chunk []In) (types.Result[Out], error) {
			return RunOpResult(ctx, op, chunk[0], opt)
		})
	if err != nil {
		return nil, plan, err
	}
	return results, plan, nil
}

// runManyBatched runs RunOpBatch once per planned chunk, using the chunk
// sizes Preflight already computed rather than recomputing them -- an
// execution that used a different chunk size than the plan it claims to be
// running is exactly the drift PL-001 exists to make impossible to miss,
// since plan.Chunks is what a caller compares the run against afterward.
//
// Oversized items (types.Plan.OversizedItems) run atomically, one call
// each, alongside the chunked calls.
func runManyBatched[In, Out any](ctx context.Context, op Op[In, Out], items []In, opt types.OpOptions, plan types.Plan) ([]types.Result[Out], types.Plan, error) {
	results := make([]types.Result[Out], len(items))

	oversizedIndex := make(map[int]bool, len(plan.OversizedItems))
	for _, o := range plan.OversizedItems {
		oversizedIndex[o.Index] = true
	}

	// Chunked items, in the order Preflight assigned them: every index not
	// in oversizedIndex, grouped by plan.Chunks' sizes.
	var chunkedIndexes []int
	for i := range items {
		if !oversizedIndex[i] {
			chunkedIndexes = append(chunkedIndexes, i)
		}
	}

	offset := 0
	for _, chunk := range plan.Chunks {
		if chunk.ItemCount <= 0 {
			continue
		}
		end := offset + chunk.ItemCount
		if end > len(chunkedIndexes) {
			return nil, plan, fmt.Errorf("op %s: plan's chunk boundaries (%d items) do not match the %d non-oversized items to run",
				op.ID, offset+chunk.ItemCount, len(chunkedIndexes))
		}
		indexes := chunkedIndexes[offset:end]

		chunkItems := make([]In, len(indexes))
		for j, idx := range indexes {
			chunkItems[j] = items[idx]
		}

		result, err := RunOpBatchResult(ctx, op, chunkItems, opt)
		if err != nil {
			return nil, plan, err
		}
		if len(result.Value) != len(indexes) {
			return nil, plan, fmt.Errorf("op %s: batch returned %d outputs for %d items", op.ID, len(result.Value), len(indexes))
		}
		for j, idx := range indexes {
			results[idx] = types.Result[Out]{Value: result.Value[j], Meta: result.Meta}
		}
		offset = end
	}

	for _, o := range plan.OversizedItems {
		res, err := RunOpResult(ctx, op, items[o.Index], opt)
		if err != nil {
			return nil, plan, err
		}
		results[o.Index] = res
	}

	return results, plan, nil
}
