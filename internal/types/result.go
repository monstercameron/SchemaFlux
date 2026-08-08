package types

import (
	"fmt"
	"time"
)

// The execution envelope.
//
// `(T, error)` is the right return for most calls and the wrong one for any
// call somebody has to explain later: it cannot say what the answer cost, how
// many attempts it took, which contract was actually delivered, or which of the
// things in it the model asserted rather than the library verified.
//
// Result[T] is the same execution with that record attached. Both forms run the
// identical path -- the envelope is built either way, and the plain form drops
// it -- because two return types that execute differently is how the two drift.
//
// The compartments are the point. A model's confidence is a claim; a membership
// check is a verified fact; a token count is a runtime observation. Mixing them
// into one bag of "metadata" is what let a model's self-reported score be read
// as a measurement, which is D-09 and half the review.

// ContractLevel is how strong a guarantee an answer carries.
//
// A caller reads DeliveredContract, not RequestedContract: asking for evidence
// and receiving structure is exactly the case worth noticing, and a library
// that only reports the request cannot tell you it happened.
type ContractLevel uint8

const (
	// ContractPromptOnly: the model was asked nicely. Nothing was enforced.
	ContractPromptOnly ContractLevel = iota

	// ContractJSONWellFormed: the answer parsed.
	ContractJSONWellFormed

	// ContractSchemaConstrained: the answer fits the declared type.
	ContractSchemaConstrained

	// ContractSchemaAndInvariantChecked: it fits, and the operation's own
	// deterministic rules hold -- a subset really is a subset.
	ContractSchemaAndInvariantChecked

	// ContractEvidenceChecked: material claims carry a reference into the
	// source, and the reference was validated.
	ContractEvidenceChecked

	// ContractFullyGoverned: evidence, provenance, capability, and data policy.
	ContractFullyGoverned
)

func (c ContractLevel) String() string {
	switch c {
	case ContractPromptOnly:
		return "prompt only"
	case ContractJSONWellFormed:
		return "json well-formed"
	case ContractSchemaConstrained:
		return "schema constrained"
	case ContractSchemaAndInvariantChecked:
		return "schema and invariants checked"
	case ContractEvidenceChecked:
		return "evidence checked"
	case ContractFullyGoverned:
		return "fully governed"
	default:
		return "unknown"
	}
}

// Check is one deterministic verification the runtime performed.
//
// These are facts, not opinions: the library ran the check and this is what
// happened. They live apart from anything the model said about its own work.
type Check struct {
	// Name identifies the check: "schema", "subset", "permutation",
	// "confidence floor", "numeric fidelity".
	Name string

	// Passed is the outcome.
	Passed bool

	// Detail explains a failure, with no payload in it.
	Detail string
}

// Meta is the record of how an answer was produced.
//
// Four compartments, kept apart on purpose:
//
//   - runtime facts: what the library and the provider observed
//   - contract: what was asked for and what was delivered
//   - checks: what the library verified deterministically
//   - model claims: what the model said about its own answer
type Meta struct {
	// --- Runtime facts.

	RequestID     string
	CorrelationID string
	Operation     string
	Provider      string
	Model         string

	// SchemaID is the contract identity: name, version, hash, dialect. A stored
	// result that cannot say which schema produced it cannot be reproduced.
	SchemaID string

	Usage TokenUsage
	Cost  CostInfo

	// CacheHitRatio is the share of this request's prompt tokens the provider
	// reported serving from its own prefix cache: CachedTokens over
	// PromptTokens, measured from what the provider said, never estimated.
	//
	// Zero means the provider reported no cached tokens -- which is also what
	// a provider that reports nothing at all produces, so a persistent zero on
	// repeated identical prefixes is the signal that caching is silently doing
	// nothing rather than proof that it is. That is the case CA-006 asks to be
	// diagnosed rather than left to be discovered on a bill.
	CacheHitRatio float64

	// Attempts is every provider call this answer took, including retries and
	// repairs. Repairs counts the subset that were repairs -- a distinction
	// that matters because they are different failures with different fixes.
	Attempts int
	Repairs  int

	// Strategy names the path taken when an operation has more than one, such
	// as a Sort that fell back to per-item scoring.
	Strategy string

	Elapsed time.Duration

	// --- Contract.

	RequestedContract ContractLevel
	DeliveredContract ContractLevel

	// --- Checks.

	Checks []Check

	// --- Model drift (TC-005, partial).
	//
	// RequestedTier is the caller's Speed at the time of the call --
	// TierUnset when they expressed no opinion. Speed.String() already
	// documents Smart/Fast/Quick as floating rather than a pin, so this
	// field's honesty is inherited from an existing type, not invented
	// here: it records what was asked for, not a guarantee about what
	// answered.
	//
	RequestedTier Speed

	// RequestedModel is the model this call asked for: the caller's
	// OpOptions.Model pin when they set one, otherwise whatever the tier
	// mapping resolved to before the request went out.
	//
	// Model (below) is what the provider said answered. The two are separate
	// fields because they are separate facts, and a single "model" field
	// silently reports the second while every reader assumes the first --
	// which is the whole of the drift problem. A provider that substitutes,
	// or that reports a dated revision of what was asked for, is visible only
	// as a difference between these two.
	RequestedModel string

	// ModelDrifted reports that the provider answered with a model other than
	// the one requested. It is derived, not claimed: RequestedModel !=
	// Model, with both non-empty. When either is empty the answer is
	// unknown rather than false, and this stays false while
	// ModelDriftUnknown says why -- an unobserved substitution must not read
	// as an observed absence of one.
	ModelDrifted bool

	// ModelDriftUnknown reports that drift could not be determined, because
	// the request or the response did not name a model. A provider that
	// echoes nothing back leaves this true; treating that silence as "no
	// drift" would be the fail-open this envelope exists to prevent.
	ModelDriftUnknown bool

	// --- Model claims.

	// ModelConfidence is the model's own score for its own answer, produced by
	// the same process that produced the answer being scored. It is not
	// calibrated, not comparable across models or prompts, and not a
	// measurement. It lives here rather than beside the checks so that reading
	// it as one takes deliberate effort.
	ModelConfidence float64

	// ModelClaims holds anything else the model asserted about its work --
	// which fields it inferred, what it says it left out.
	ModelClaims map[string]any

	// --- Provenance (TC-003).

	// Provenance is the lineage record: result and parent IDs, input and
	// schema digests, operation and prompt versions, the resolved model,
	// normalizers applied, the recovery path, cache usage, and library and
	// adapter versions. See provenance.go's doc comment for the full
	// account of what each field is and is not.
	//
	// Populated by ops.RunOpResult (internal/ops/op.go) for operations that
	// run through the Op[In, Out] execution path; the zero value elsewhere.
	Provenance Provenance
}

// Degraded reports whether the answer carries a weaker guarantee than was
// asked for. A caller who checks nothing else should check this.
func (m Meta) Degraded() bool {
	return m.DeliveredContract < m.RequestedContract
}

// Failed returns the checks that did not pass.
func (m Meta) Failed() []Check {
	var failed []Check
	for _, check := range m.Checks {
		if !check.Passed {
			failed = append(failed, check)
		}
	}
	return failed
}

// String is a one-line summary for a log.
func (m Meta) String() string {
	summary := fmt.Sprintf("%s via %s/%s: %d attempt(s)", m.Operation, m.Provider, m.Model, m.Attempts)
	if m.Repairs > 0 {
		summary += fmt.Sprintf(" (%d repair(s))", m.Repairs)
	}
	summary += fmt.Sprintf(", %s, %d tokens", m.Elapsed.Round(time.Millisecond), m.Usage.TotalTokens)
	if m.Cost.Priced {
		summary += fmt.Sprintf(", %s %.6f", m.Cost.Currency, m.Cost.TotalCost)
	} else {
		summary += ", cost unknown"
	}
	summary += ", delivered " + m.DeliveredContract.String()
	if m.Degraded() {
		summary += " (asked for " + m.RequestedContract.String() + ")"
	}
	return summary
}

// Result pairs a value with the record of how it was produced.
type Result[T any] struct {
	Value T
	Meta  Meta
}

// MetaFrom builds the runtime-facts half of an envelope from the metadata the
// call path already collects.
//
// It exists so the envelope is assembled in one place: an operation that builds
// its own is an operation that will report a different thing from every other.
func MetaFrom(metadata *ResultMetadata) Meta {
	if metadata == nil {
		return Meta{}
	}

	meta := Meta{
		RequestID:      metadata.RequestID,
		CorrelationID:  metadata.CorrelationID,
		Operation:      metadata.Operation,
		Provider:       metadata.Provider,
		Model:          metadata.Model,
		RequestedModel: metadata.RequestedModel,
		Attempts:       metadata.RetryCount + 1,
		Elapsed:        metadata.Duration,
	}

	// TC-005's drift, derived here rather than claimed anywhere: a difference
	// between what was asked for and what answered. Either side missing makes
	// the answer unknown, not "no drift" -- a provider that echoes nothing back
	// has told us nothing, and recording that silence as agreement is the
	// fail-open this compartment exists to prevent.
	switch {
	case meta.RequestedModel == "" || metadata.ObservedModel == "":
		meta.ModelDriftUnknown = true
	case meta.RequestedModel != metadata.ObservedModel:
		meta.ModelDrifted = true
	}

	if metadata.TokenUsage != nil {
		meta.Usage = *metadata.TokenUsage
	}
	if metadata.CostInfo != nil {
		meta.Cost = *metadata.CostInfo
	}
	if schemaID, ok := metadata.Custom["schema_id"].(string); ok {
		meta.SchemaID = schemaID
	}

	return meta
}
