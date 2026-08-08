package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// PL-001/PL-002: the plan is a separate, inspectable thing from execution.
//
// Everything in this file is data -- structs and pure functions over them,
// no context, no provider, no I/O -- for the same reason Op (internal/ops/
// op.go) is: a plan a caller can serialize, diff, and reason about has to be
// something that cannot accidentally spend money by existing. Preflight
// (internal/ops/plan.go) is the one function that builds a Plan; nothing in
// this file calls it or anything that could call a model.

// ExecutionShape is the fan-out shape a plan chooses for running an
// operation over one or more inputs. PL-002.
type ExecutionShape uint8

const (
	// ShapeUnspecified means the planner has not chosen yet -- the zero
	// value of a Plan under construction, and the value a PlanRequest's
	// Force field carries when the caller has not forced anything.
	ShapeUnspecified ExecutionShape = iota

	// ShapeAtomic: one call per input. Always legal, for every operation --
	// it is what RunOp already does one item at a time.
	ShapeAtomic

	// ShapeMDSP: many data, single prompt. Many inputs are tagged with
	// invocation-local ids (PL-003), encoded into one request, and the
	// response is resolved back to the caller's inputs by id.
	ShapeMDSP

	// ShapeSDMP: single datum, many prompts. A staged pipeline over one
	// input, passing structured intermediates between stages instead of
	// resending the source (PL-010). No operation in this delivery declares
	// a staged form; forcing this shape is refused at plan time and named as
	// such, not silently downgraded to atomic.
	ShapeSDMP

	// ShapeGlobal: an aggregate batch algebra whose validation spans the
	// whole set and, when the set does not fit one MDSP call, chunks with a
	// merge step that understands the global contract (PL-011) rather than
	// concatenating chunk results as if they were independent.
	ShapeGlobal
)

// String renders the shape for logs, plans, and envelopes.
func (s ExecutionShape) String() string {
	switch s {
	case ShapeAtomic:
		return "atomic"
	case ShapeMDSP:
		return "mdsp"
	case ShapeSDMP:
		return "sdmp"
	case ShapeGlobal:
		return "global"
	default:
		return "unspecified"
	}
}

// PlanRequest is what a caller hands Preflight: the resolved per-call
// options, an optional forced shape, and an optional budget ceiling.
//
// Force being legitimate in both directions -- forcing Atomic for risk,
// forcing MDSP for cost -- is a PL-002 requirement, so this is not a hint:
// Preflight either honours it and records that it did, or refuses the plan
// and names why, and never silently substitutes its own choice instead.
type PlanRequest struct {
	Options OpOptions

	// Force is the caller's forced shape. ShapeUnspecified means the caller
	// stated no opinion and the planner chooses.
	Force ExecutionShape

	// ForceReason is why the caller forced Force, recorded on the plan
	// verbatim so a later reader (a log, a review packet) does not have to
	// guess whether it was a risk decision or a cost one. Ignored when Force
	// is ShapeUnspecified.
	ForceReason string

	// Budget is the maximum the caller is willing to spend on this logical
	// request, in the pricing currency (USD today -- CostInfo.Currency).
	// Zero means unbounded. It bounds chunk packing (PL-004's per-call-cost
	// bound) and is carried onto the plan so a caller can see whether the
	// estimate fit inside it.
	Budget float64

	// MaxItemsPerCall caps how many items one MDSP chunk may hold,
	// independent of any token or byte bound. Zero means the operation's own
	// default applies.
	MaxItemsPerCall int
}

// PolicySnapshot is the sensitive-content-free half of an OpOptions: the
// settings that change what Preflight decides, with the caller's own text
// stripped out.
//
// OpOptions.Steering is natural-language guidance the caller wrote -- it can
// contain anything a prompt can, which is exactly what AGENTS.md's "never
// log or embed the caller's payload" rule is about. A Plan is meant to be
// inspected and stored, so it carries whether steering was set, never what
// it said. The same reasoning drops OpOptions.Context (not data, but not
// serializable either) and RequestID/CorrelationID (identifiers for this
// call, not inputs to what the plan decides).
type PolicySnapshot struct {
	Mode            Mode
	Intelligence    Speed
	MaxOutputTokens int
	Threshold       float64
	HasSteering     bool
	ResponseFormat  string
}

// PolicySnapshotFrom strips an OpOptions down to the fields that influence
// planning, discarding everything that is either sensitive or merely
// per-call identity.
func PolicySnapshotFrom(opt OpOptions) PolicySnapshot {
	return PolicySnapshot{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		MaxOutputTokens: opt.MaxOutputTokens,
		Threshold:       opt.Threshold,
		HasSteering:     opt.Steering != "",
		ResponseFormat:  opt.ResponseFormat,
	}
}

// Digest is a content hash of the policy snapshot, used both on Plan itself
// and by a caller comparing two plans' inputs without comparing every field.
func (p PolicySnapshot) Digest() string { return digestOf(p) }

// CapabilitySnapshot is what Preflight knew about the provider and model it
// planned against, at the moment it planned. A plan built against a stale
// snapshot is a CP-001 concern (ErrPlanStale); this delivery does not build
// the capability negotiation CP-001 describes, so the snapshot here is the
// minimal read Preflight can take without a provider call: the resolved
// provider and model names and the output-token ceiling their tier implies.
type CapabilitySnapshot struct {
	Provider        string
	Model           string
	MaxOutputTokens int
	ContextTokens   int
}

// Digest is a content hash of the capability snapshot.
func (c CapabilitySnapshot) Digest() string { return digestOf(c) }

// RejectedAlternative is a shape Preflight considered and did not choose,
// with why -- PL-014's "say what was chosen and what was rejected and why".
// A plan that names only its own choice cannot answer "why did this take 40
// calls instead of 1" or "why didn't this batch"; the rejected alternative
// is what a caller reviewing an adaptive decision actually needs.
type RejectedAlternative struct {
	Shape  ExecutionShape
	Reason string
}

// ChunkPlan is one chunk of a Plan's MDSP or global fan-out.
type ChunkPlan struct {
	// ItemCount is how many items this chunk holds.
	ItemCount int

	// BoundingRule names which of PL-004's bounds decided this chunk's size:
	// "item_count", "input_tokens", "output_reserve", "context_limit",
	// "per_call_cost", or "payload_bytes". Empty means every remaining item
	// fit without hitting a bound.
	BoundingRule string
}

// OversizedItem is an input that could not fit any chunk on its own -- PL-004's
// "never silently truncated" case. Index is its position in the caller's
// slice; Bound names what it broke.
type OversizedItem struct {
	Index int
	Bound string
}

// CostEstimate is a plan's honest read of what running it will cost.
//
// PR-002 established that an unpriced model reports unpriced, not $0.00:
// Priced false here means literally nothing is known about the rate, and
// EstimatedCost is meaningless in that state -- a caller must check Priced
// before reading EstimatedCost, the same discipline CostInfo.Priced already
// requires of a delivered result.
type CostEstimate struct {
	Priced                    bool
	EstimatedCost             float64
	Currency                  string
	PricingSource             string
	EstimatedPromptTokens     int
	EstimatedCompletionTokens int
}

// Plan is Preflight's output: what would run, without running it.
//
// It is immutable once built -- nothing in this package or internal/ops
// mutates a Plan after Preflight returns one -- and it is the whole of
// PL-001's contract: given the same operation identity, the same item count
// and shapes, the same PolicySnapshot, the same CapabilitySnapshot, and the
// same EstimatorVersion, two calls to Preflight produce byte-identical
// Serialize output. Nothing here is a timestamp, a random id, or anything
// else that would make that false.
type Plan struct {
	Operation OperationID

	// InputShape describes the input without reproducing it -- DescribeValue
	// run over the caller's slice, so a plan says "[]string of 240" and
	// never the 240 strings.
	InputShape string
	ItemCount  int

	// Eligible reports whether this operation can run at all against this
	// input under this policy. IneligibleReason explains a false value --
	// always the case a forced shape is illegal for the operation, or the
	// operation itself is malformed (no BuildPrompt, no Decode).
	Eligible         bool
	IneligibleReason string

	// Shape is what will run: the caller's forced choice, or the planner's.
	ShapeForced bool
	Shape       ExecutionShape
	ShapeReason string

	// Alternatives lists every other shape Preflight considered for this
	// operation and input, and why each was not chosen -- PL-014. Empty
	// only when there was nothing else to consider (an ineligible plan
	// never reaches shape selection).
	Alternatives []RejectedAlternative

	Chunks         []ChunkPlan
	OversizedItems []OversizedItem

	// MaxCalls is the ceiling on provider calls this plan's execution will
	// make: len(Chunks) plus len(OversizedItems) routed atomically for MDSP
	// and global shapes, or ItemCount for atomic. A run that honours the
	// plan never exceeds it.
	MaxCalls int

	EstimatedCost CostEstimate

	Policy           PolicySnapshot
	Capability       CapabilitySnapshot
	EstimatorVersion string

	// PolicyDigest and CapabilityDigest are content hashes of Policy and
	// Capability, carried alongside the snapshots themselves so a caller
	// comparing two plans -- or a cache keyed on "would this plan differ" --
	// can compare a fixed-width string instead of the whole struct.
	PolicyDigest     string
	CapabilityDigest string
}

// canonicalJSON marshals v with json.Marshal, which is deterministic for any
// value containing no maps: struct fields serialize in declaration order,
// slices in slice order. Every type in this file follows that rule on
// purpose -- see the "no maps" note on Plan's doc comment -- so this
// function, not a hand-rolled encoder, is what PL-001's byte-identical
// requirement rests on.
func canonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

func digestOf(v any) string {
	encoded, err := canonicalJSON(v)
	if err != nil {
		// Every type this is called with is plain data with no channels,
		// functions, or cyclic pointers, so this path is unreachable in
		// practice; it degrades to a fixed marker rather than panicking, so
		// a future field addition that breaks this assumption fails loudly
		// in a test rather than crashing a caller's planning path.
		return "undigestable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Digest is a content hash of the whole plan -- two plans with the same
// Digest made the identical decision, including the estimate.
func (p Plan) Digest() string {
	return digestOf(p)
}

// Serialize renders the plan as canonical JSON. It carries no caller value:
// every field is a shape, a count, an identifier, or a number this package
// computed -- never an item from the slice Preflight was given.
func (p Plan) Serialize() ([]byte, error) {
	return canonicalJSON(p)
}

// Explain renders a human-readable summary of the plan -- PL-014's
// pre-execution explanation, in the minimal form this delivery builds: mode,
// chunking, call ceiling, and cost range. The full explainer (parallelism,
// recovery ladder, data policy) is PL-014 itself and is not built here.
func (p Plan) Explain() string {
	if !p.Eligible {
		return fmt.Sprintf("%s: not eligible -- %s", p.Operation, p.IneligibleReason)
	}

	summary := fmt.Sprintf("%s: %d item(s), shape %s", p.Operation, p.ItemCount, p.Shape)
	if p.ShapeForced {
		summary += fmt.Sprintf(" (forced: %s)", p.ShapeReason)
	} else {
		summary += fmt.Sprintf(" (%s)", p.ShapeReason)
	}
	for _, alt := range p.Alternatives {
		summary += fmt.Sprintf("\nRejected: %s -- %s", alt.Shape, alt.Reason)
	}
	if len(p.Chunks) > 0 {
		summary += fmt.Sprintf(", %d chunk(s)", len(p.Chunks))
	}
	if len(p.OversizedItems) > 0 {
		summary += fmt.Sprintf(", %d item(s) routed atomically (oversized)", len(p.OversizedItems))
	}
	summary += fmt.Sprintf(", at most %d call(s)", p.MaxCalls)
	if p.EstimatedCost.Priced {
		summary += fmt.Sprintf(", estimated %s %.6f", p.EstimatedCost.Currency, p.EstimatedCost.EstimatedCost)
	} else {
		summary += ", cost unpriced"
	}
	return summary
}
