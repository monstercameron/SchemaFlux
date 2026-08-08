package types

// AlgebraKind is the finer batch-algebra taxonomy PL-006 asks for, layered on
// top of BatchClass (op.go). BatchClass says how much validation a batch
// carries -- none, per-item, or whole-set; AlgebraKind says what shape that
// validation takes, which is what a generic map/reduce needs to know before
// it is allowed to run a declared algebra rather than invent one.
//
// This lives beside BatchClass rather than replacing it: BatchClass is
// already load-bearing (NewOp's build check reads it, RunOp's callers switch
// on it) and changing its meaning out from under existing callers is not
// this task's to make. AlgebraKind is the additional declaration PL-006
// closes ARC-16 with -- see BatchAlgebra.Kind in internal/ops/op.go.
type AlgebraKind uint8

const (
	// AlgebraUnspecified means nobody has said. A stable operation whose
	// BatchClass requires a batch form (Itemwise or Aggregate) may not ship
	// in this state -- see NewOp in internal/ops/op.go.
	AlgebraUnspecified AlgebraKind = iota

	// AlgebraIndependent: each output depends only on its own input. Batching
	// is purely a transport optimization -- ExtractMany, ClassifyMany.
	AlgebraIndependent

	// AlgebraSubset: the output is a subset of the input, chosen by a
	// relational rule -- Filter's contract.
	AlgebraSubset

	// AlgebraPermutation: the output is every input, reordered -- Sort's
	// contract.
	AlgebraPermutation

	// AlgebraPartition: the outputs partition the input into disjoint groups
	// -- Cluster's contract. CoversExactlyOnce (invariants.go) is the check.
	AlgebraPartition

	// AlgebraGraph: the outputs describe relationships between input pairs or
	// members -- deduplication's candidate-pair judgments.
	AlgebraGraph

	// AlgebraHierarchical: the outputs compose in stages, each consuming the
	// previous stage's output alongside a share of the input -- global
	// synthesis over chunk summaries.
	AlgebraHierarchical

	// AlgebraSequential: outputs depend on the outputs before them in a fixed
	// order -- a running total, a state machine over the input.
	AlgebraSequential
)

// String renders the kind for logs and plan explanations.
func (k AlgebraKind) String() string {
	switch k {
	case AlgebraIndependent:
		return "independent"
	case AlgebraSubset:
		return "subset"
	case AlgebraPermutation:
		return "permutation"
	case AlgebraPartition:
		return "partition"
	case AlgebraGraph:
		return "graph"
	case AlgebraHierarchical:
		return "hierarchical"
	case AlgebraSequential:
		return "sequential"
	default:
		return "unspecified"
	}
}

// RequiresExactCoverage reports whether the kind's batch protocol demands
// every offered id answered exactly once -- true for every kind PL-003's
// shared id protocol can validate generically (Independent, Permutation, and
// Partition's per-item pass before CoversExactlyOnce runs on the groups), and
// false for Subset, whose contract explicitly permits fewer ids back than
// were offered.
func (k AlgebraKind) RequiresExactCoverage() bool {
	switch k {
	case AlgebraSubset:
		return false
	default:
		return true
	}
}
