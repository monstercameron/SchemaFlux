package ops

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-011: global and hierarchical operation algebras.
//
// AGENTS.md's rule is explicit: "Sort returns a permutation. Cluster returns
// a partition. The shared checks live in internal/ops/invariants.go. Use
// them; do not write a fourth membership test." invariants.go already has
// SameMultiset (a permutation) and CoversExactlyOnce (a partition). This file
// is the thin adapter layer that turns those two checks into
// BatchAlgebra.GlobalValidate functions an operation wires in, plus the
// execution path (RunOpGlobal) that runs an aggregate operation's whole-set
// contract correctly instead of the silent gap PL-011's own text names:
// runManyBatched (plan_many.go) concatenates every chunk's independently
// produced, chunk-local results with no check that the assembled whole is
// still a valid permutation or partition of the ORIGINAL set. For Sort and
// Cluster that gap is not a missing nicety -- a chunk that reorders its own
// twenty items is, by construction, never a permutation of the other four
// hundred and eighty it never saw, and nothing before this file checked that.
//
// What this delivers: PermutationValidate and PartitionValidate (reusing
// SameMultiset/CoversExactlyOnce rather than a third and fourth
// implementation of the same two checks), and RunOpGlobal, which runs the
// happy path -- a plan whose whole input fits in one MDSP call, where
// RunOpBatch's existing GlobalValidate (batchprotocol.go) already validates
// the complete set because the chunk IS the complete set -- and REFUSES
// outright, before spending a single call, when the plan needs more than one
// chunk.
//
// What this does not deliver: a cross-chunk merge for any of PL-011's named
// algebras (rank-in-chunks-then-reglobal for ranking, hash/embedding
// candidate generation for deduplication, chunk-summary-then-synthesize for
// global synthesis). TODOS.md's own PL-011 text says why: "no generic
// chunker can derive them ... written per operation". Building one
// correctly is a per-operation design question this delivery does not have
// an operation to design it against yet -- no Sort or Cluster Op exists in
// this codebase today (recover.go's PL-009 doc comment calls them "a future
// Sort/Cluster"). Refusing the multi-chunk case is the honest alternative to
// guessing at a merge and shipping something that looks like it works on a
// small fixture and silently corrupts a real 5,000-item run.

// PermutationValidate returns a BatchAlgebra.GlobalValidate for an aggregate
// operation whose contract is "the output is every input, reordered" --
// Sort's contract (types.AlgebraPermutation). It is SameMultiset
// (invariants.go), bound to the GlobalValidate signature: items and outputs
// share a type because a permutation reorders values, it never transforms
// them into something else.
func PermutationValidate[T any]() func(items, outputs []T) error {
	return func(items, outputs []T) error {
		return SameMultiset(items, outputs)
	}
}

// PartitionValidate returns a BatchAlgebra.GlobalValidate for an aggregate
// operation whose contract is "every input belongs to exactly one group" --
// Cluster's contract (types.AlgebraPartition). Each Out in outputs
// represents one group; indicesOf reads which input indices that group
// claims, exactly the shape CoversExactlyOnce (invariants.go) already checks
// -- every index used once, none out of range, none repeated. This function
// never invents a grouping of its own; it only reads the one the operation's
// own Out type reports.
func PartitionValidate[In, Out any](indicesOf func(Out) []int) func(items []In, outputs []Out) error {
	return func(items []In, outputs []Out) error {
		groups := make([][]int, len(outputs))
		for i, out := range outputs {
			groups[i] = indicesOf(out)
		}
		return CoversExactlyOnce(len(items), groups)
	}
}

// RunOpGlobal runs a BatchAggregate operation over items, honoring its
// whole-set contract rather than the chunk-local one runManyBatched
// (plan_many.go) applies to every shape including ShapeGlobal today.
//
// It returns Preflight's plan alongside the result even on refusal, so a
// caller can see exactly why (plan.Chunks' length is the number the error
// message already names) without a second call.
//
// The refusal is unconditional, not a best-effort attempt: an aggregate
// answer that fails PL-011's whole-set check is refused rather than
// partially applied, and a plan that would require more than one chunk is
// refused before any provider call is made at all -- there is no partial
// result to return in either case, only the plan and the error.
func RunOpGlobal[In, Out any](ctx context.Context, op Op[In, Out], items []In, req types.PlanRequest) (types.Result[[]Out], types.Plan, error) {
	if op.Batch.Class != types.BatchAggregate {
		return types.Result[[]Out]{}, types.Plan{}, fmt.Errorf(
			"op %s: batch class %s is not BatchAggregate; RunOpGlobal only runs aggregate algebras (Sort, Cluster, and similar whole-set contracts)",
			op.ID, op.Batch.Class)
	}

	plan, err := Preflight(op, items, req)
	if err != nil {
		return types.Result[[]Out]{}, plan, err
	}
	if !plan.Eligible {
		return types.Result[[]Out]{}, plan, fmt.Errorf("op %s: plan is not eligible: %s", op.ID, plan.IneligibleReason)
	}
	if len(items) == 0 {
		return types.Result[[]Out]{}, plan, nil
	}
	if plan.Shape != types.ShapeGlobal && plan.Shape != types.ShapeMDSP {
		return types.Result[[]Out]{}, plan, fmt.Errorf(
			"op %s: shape %s has no global execution path; RunOpGlobal requires a plan that chose ShapeGlobal or ShapeMDSP",
			op.ID, plan.Shape)
	}

	// The refusal PL-011's verify line asks for: a set that does not fit one
	// MDSP call has no cross-chunk merge in this delivery, so it is refused
	// here -- before a call is made -- rather than run through
	// runManyBatched's per-chunk concatenation, which would silently accept
	// an assembled "permutation" or "partition" that was never checked as a
	// whole.
	if len(plan.Chunks) > 1 || len(plan.OversizedItems) > 0 {
		return types.Result[[]Out]{}, plan, fmt.Errorf(
			"op %s: %d item(s) need %d chunk(s) and %d oversized call(s) under this plan's budget; "+
				"PL-011 has no cross-chunk merge for aggregate algebras in this delivery, so this run is "+
				"refused rather than assembling chunk-local answers that were never validated as a whole "+
				"-- lower the item count, raise the budget, or force a smaller ShapeAtomic run instead",
			op.ID, len(items), len(plan.Chunks), len(plan.OversizedItems))
	}
	if len(plan.Chunks) == 1 && plan.Chunks[0].ItemCount != len(items) {
		// Defensive: packChunks (plan.go) is trusted to produce a chunk that
		// covers every non-oversized item when there are no oversized items
		// and only one chunk, but this is the one place a silent mismatch
		// here would turn into a silently wrong global answer, so it is
		// checked rather than assumed.
		return types.Result[[]Out]{}, plan, fmt.Errorf(
			"op %s: single chunk covers %d of %d items; refusing rather than running a global algebra over a partial set",
			op.ID, plan.Chunks[0].ItemCount, len(items))
	}

	result, err := RunOpBatchResult(ctx, op, items, req.Options)
	return result, plan, err
}
