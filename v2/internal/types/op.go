package types

// The operation descriptor's flat, data-only vocabulary.
//
// A-001's Revised line widens the six-field Op the task body described into a
// public shape with a version, a semantics block, an output contract, a batch
// algebra, and a default policy. The pieces that are pure enumerations and
// identity -- the things a planner or a middleware would switch on without
// needing to call anything -- live here, in types, so they carry no import of
// context, of a provider, or of the ops package's execution machinery. The
// pieces that hold functions over a specific In/Out pair (OutputContract,
// BatchAlgebra) are generic and stay in internal/ops/op.go, next to the Op
// type that embeds them.

// OperationID identifies an operation and the behavioural version it is on.
//
// Version exists because a prompt edit is a behaviour change with no Go API
// change to show for it -- Gap-13, the reason golden_prompts_test.go exists.
// PS-007 needs something to hang a version on; this is it. It is deliberately
// not the same identity as a SchemaDescriptor (schemaid.go): the schema is the
// shape of the answer, the OperationID is the version of the question, and a
// change to either without the other is a real, distinguishable case -- the
// prompt wording can change while the target type does not, and the target
// type can change while the prompt template does not. Collapsing them into one
// identity would make one of those two changes invisible.
type OperationID struct {
	// Name is the operation's stable name: "extract", "classify", "summarize".
	Name string

	// Version is bumped when the operation's contract changes in a way that
	// is not visible in the schema hash or the rendered prompt bytes -- the
	// repair policy, the strict/transform decode path, which errors are
	// retried. extractPromptVersion (internal/ops/core.go) is the existing
	// instance of this idea; OperationID.Version is where it belongs now.
	Version string
}

// String renders the identity compactly, for logs, cache keys, and
// provenance -- the same shape SchemaDescriptor.String() already uses.
func (id OperationID) String() string {
	if id.Name == "" {
		return "unnamed@" + id.Version
	}
	if id.Version == "" {
		return id.Name + "@unversioned"
	}
	return id.Name + "@" + id.Version
}

// Category classifies what kind of thing an operation asks a model to do.
// A planner deciding whether an operation can be fused or reordered needs
// this without inspecting the operation's prompt template.
type Category uint8

const (
	CategoryUnspecified Category = iota
	CategoryExtraction
	CategoryTransformation
	CategoryGeneration
	CategorySelection
	CategoryJudgment
)

func (c Category) String() string {
	switch c {
	case CategoryExtraction:
		return "extraction"
	case CategoryTransformation:
		return "transformation"
	case CategoryGeneration:
		return "generation"
	case CategorySelection:
		return "selection"
	case CategoryJudgment:
		return "judgment"
	default:
		return "unspecified"
	}
}

// StabilityTier says how strongly an operation's contract is meant to hold,
// which is what the batch-class build check (op.go's NewOp) reads: a stable
// operation with no declared batch class fails construction, an experimental
// one does not, because an experimental operation's shape is still moving.
type StabilityTier uint8

const (
	StabilityUnspecified StabilityTier = iota
	StabilityExperimental
	StabilityStable
)

func (s StabilityTier) String() string {
	switch s {
	case StabilityExperimental:
		return "experimental"
	case StabilityStable:
		return "stable"
	default:
		return "unspecified"
	}
}

// Semantics describes what an operation is allowed to do to its input,
// independent of how any particular call implements it. This is the block a
// caller-facing planner reads to decide, for instance, whether an operation's
// output order may be relied on, or whether it may invent facts not present
// in the input.
type Semantics struct {
	Category Category

	// InferencePermitted reports whether the operation is allowed to produce
	// values not literally present in its input -- true for Extract (a
	// missing field may be inferred in Transform mode), false for an
	// operation whose contract is closed-world, like a redaction.
	InferencePermitted bool

	// EvidenceRequired reports whether a material claim in the output must
	// carry a reference into the input that the runtime can check. It is
	// data here, not enforcement -- ContractEvidenceChecked (internal/types
	// /result.go) is where enforcement would report against it.
	EvidenceRequired bool

	// PreservesIdentity reports whether output items correspond one-to-one
	// with input items by value -- the contract Choose, Filter, and Sort
	// have, and the reason invariants.go's SameMultiset/SubsetOf exist.
	PreservesIdentity bool

	// PreservesOrder reports whether the operation must return items in the
	// order it received them. A Sort declares this false by construction; a
	// Filter declares it true.
	PreservesOrder bool

	Stability StabilityTier
}

// BatchClass says how an operation composes across many inputs. It is the
// field the Revised line's build-time check is about: a stable operation
// with BatchUnspecified fails NewOp.
type BatchClass uint8

const (
	// BatchUnspecified means nobody has said. A stable operation may not
	// ship in this state; see op.go's NewOp.
	BatchUnspecified BatchClass = iota

	// BatchNone: the operation has no batch form. Each input is its own
	// call, and nothing merges them.
	BatchNone

	// BatchItemwise: many inputs may be encoded into one request and decoded
	// back into as many outputs, but each output stands on its own -- no
	// validation spans the batch.
	BatchItemwise

	// BatchAggregate: the batch has a validation that only makes sense
	// across the whole set -- CoversExactlyOnce for a clustering, or
	// SameMultiset for a sort. GlobalValidate on BatchAlgebra is where that
	// check lives.
	BatchAggregate
)

func (c BatchClass) String() string {
	switch c {
	case BatchNone:
		return "none"
	case BatchItemwise:
		return "itemwise"
	case BatchAggregate:
		return "aggregate"
	default:
		return "unspecified"
	}
}

// DefaultPolicy is the operation's default execution policy, applied when a
// caller states no opinion. It reuses Mode and Speed (types.go, A-005) rather
// than inventing a parallel pair, so "no opinion" already means what
// ModeUnset/TierUnset mean everywhere else in this library.
type DefaultPolicy struct {
	Mode  Mode
	Speed Speed

	// RepairAttempts is how many times the operation may show the model its
	// own mistake and ask again, absent a caller override. Extract's
	// existing default (ops.DefaultRepairAttempts) is what this carries once
	// ported.
	RepairAttempts int
}
