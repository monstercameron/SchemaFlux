package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// TC-002. Evidence is a claim the model made about its own answer unless
// something verified it.
//
// Op.Semantics.EvidenceRequired and Op.Contract.EvidenceRequired
// (internal/ops/op.go) were declared and never read: a caller could ask an
// operation to require evidence and the answer came back the same whether it
// did or not. This file is the vocabulary that fills those slots.
// internal/ops/evidence.go is where a claim is actually checked against a
// source; this file is the shape of a claim and the source it points into,
// kept in types because Meta (result.go) carries it and Meta cannot import
// the ops package.
//
// A cited span is not a proof the citation is true -- the runtime validates
// that a reference is in bounds and matches the source it claims to come
// from, and it does not, and cannot, decide whether the cited text actually
// entails the claim. That judgment call is exactly the thing this library
// does not get to make silently; conflating "the model pointed here" with
// "here is where the answer really came from" would be D-09 again, one
// field over.

// EvidenceRef locates the span a claim is anchored to, in the source the
// claim was drawn from -- never the claimed value itself. Storing the
// quoted text here would put the caller's payload into every Meta this
// library returns, which every caller logs; StartByte/EndByte/JSONPointer
// and SourceDigest describe where the value lives without reproducing it.
type EvidenceRef struct {
	// SourceID names which source this reference points into, for a claim
	// drawn from one of several inputs -- a multi-document extraction, or a
	// pipeline stage whose source is an earlier stage's output. Required:
	// an evidence reference with no source is not checkable.
	SourceID string

	// JSONPointer is an RFC 6901 pointer into the source when the source is
	// structured (JSON), addressing a field rather than a byte range. Empty
	// when the source is treated as plain text and StartByte/EndByte are
	// used instead. A reference may carry either or both; ValidateEvidenceRef
	// checks whichever is set.
	JSONPointer string

	// StartByte and EndByte are a byte range into the source text,
	// half-open: [StartByte, EndByte). Both zero with an empty JSONPointer
	// means the reference carries no locator at all, which
	// ValidateEvidenceRef rejects -- a claim that names a source but not a
	// place in it is not a reference, it is an assertion wearing one.
	StartByte int
	EndByte   int

	// SourceDigest is the digest (SHA-256, hex-encoded; see DigestSource) of
	// the exact source text this reference was computed against. A
	// pipeline's source can change between the moment a span was cited and
	// the moment it is checked -- a later stage re-running on updated
	// input, a cache serving a stale document -- and a byte range that used
	// to be correct silently points at the wrong text once that happens. A
	// bounds check alone cannot tell a stale-but-in-range offset from a
	// correct one; the digest is what does.
	SourceDigest string
}

// IsZero reports whether a reference carries nothing -- no source, no
// locator. NewOp callers use this to skip mandatory-evidence enforcement on
// an intentionally empty claim only when the claim itself is also marked
// Inferred; StrictEvidence is where that distinction is actually enforced.
func (r EvidenceRef) IsZero() bool {
	return r.SourceID == "" && r.JSONPointer == "" && r.StartByte == 0 && r.EndByte == 0 && r.SourceDigest == ""
}

// EvidenceSource is a source document keyed by SourceID, kept apart from
// EvidenceRef so a caller supplies the actual text once (to build sources)
// and every reference into it only carries the digest, never the text.
type EvidenceSource struct {
	ID   string
	Text string
}

// DigestSource computes the digest ValidateEvidenceRef checks a reference's
// SourceDigest against. It is content-addressed rather than versioned by a
// caller-supplied label, because a label can be wrong in a way a hash cannot.
func DigestSource(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// EvidencePolicy is how strictly an operation's output must anchor its
// claims to a source. It is data an operation declares
// (Op.Contract.EvidencePolicy, internal/ops/op.go), not enforcement by
// itself -- internal/ops/evidence.go's StrictEvidence is what reads it.
type EvidencePolicy uint8

const (
	// EvidenceNone: nothing about provenance is checked. The default, and
	// the only sound default -- an operation that has not opted in gets
	// exactly the behaviour it had before this existed.
	EvidenceNone EvidencePolicy = iota

	// EvidenceMaterialFields: only the fields an operation names as
	// material (Op.Contract.MaterialFields) must carry evidence. Everything
	// else is unconstrained -- a summary's word count does not need a span,
	// its dollar total does.
	EvidenceMaterialFields

	// EvidenceAllModelDerived: every claim the model did not itself mark
	// Inferred must carry at least one valid EvidenceRef. A field the model
	// marks Inferred is exempt by construction -- that is what Inferred
	// means -- but an un-marked claim with nothing behind it is exactly the
	// fabrication this mode exists to catch.
	EvidenceAllModelDerived

	// EvidenceNoInference: the strictest mode. No claim may be marked
	// Inferred at all, and every claim must carry a valid reference. A
	// field the runtime cannot support with a checked span stays absent
	// rather than being guessed -- TC-002's "NoInference" requirement,
	// enforced the same way as the other three: a claim is checked, not a
	// decoded field silently defaulted.
	EvidenceNoInference
)

func (p EvidencePolicy) String() string {
	switch p {
	case EvidenceMaterialFields:
		return "material fields only"
	case EvidenceAllModelDerived:
		return "all model-derived fields"
	case EvidenceNoInference:
		return "no inference"
	default:
		return "none"
	}
}

// ClaimProvenance is what the model asserted about one field of its own
// answer: which source spans it says support the value, and whether it is
// telling you the value was inferred rather than read. This is the model's
// account of itself -- Evidence being present does not mean the runtime
// verified it entails the claim, only that a reference exists to check. Read
// StrictEvidence's doc comment (internal/ops/evidence.go) for what "checked"
// actually means here.
type ClaimProvenance struct {
	// FieldPath identifies the claim's place in the output -- a JSON
	// pointer or dotted path into the decoded value, e.g. "/total" or
	// "invoice.total". It is the output's address, not the source's;
	// EvidenceRef.JSONPointer is the source-side address.
	FieldPath string

	// Evidence is every span the model cited for this field. Empty means
	// the model cited nothing, which is only acceptable when Inferred is
	// true -- StrictEvidence is where that rule is applied.
	Evidence []EvidenceRef

	// Inferred is the model's own claim that this field was not read from
	// a source but produced -- calculated, defaulted, or guessed. It is as
	// much a model claim as ModelConfidence (result.go): the runtime does
	// not verify that a field really was inferred, only that the model
	// said so, and enforcement (StrictEvidence) treats an unmarked claim
	// with no evidence as a stricter failure than a marked one.
	Inferred bool

	// Method is the model's own description of how the value was produced
	// -- "quoted", "computed from /lineItems", "summarized across three
	// spans". Free text, and, like Inferred, a claim rather than a
	// verified fact.
	Method string
}

// ValidateEvidenceRef checks one reference against the source it claims to
// point into: the source exists, the digest matches the source's current
// text (so a stale or wrong reference is rejected rather than trusted
// because it happens to be in range), and the locator -- byte range or JSON
// pointer -- is present and, for a byte range, actually within the text.
//
// It does not, and cannot, check that the referenced span supports the
// claim. That is a judgment call this library does not make; see the
// package doc comment above.
func ValidateEvidenceRef(ref EvidenceRef, sources map[string]EvidenceSource) error {
	if ref.SourceID == "" {
		return fmt.Errorf("evidence reference names no source")
	}
	source, found := sources[ref.SourceID]
	if !found {
		return fmt.Errorf("evidence reference names source %q, which was not supplied", ref.SourceID)
	}

	digest := DigestSource(source.Text)
	if ref.SourceDigest == "" {
		return fmt.Errorf("evidence reference for source %q carries no digest to check", ref.SourceID)
	}
	if ref.SourceDigest != digest {
		return fmt.Errorf("evidence reference for source %q does not match the source's current content (the source changed, or the reference is wrong)", ref.SourceID)
	}

	hasRange := ref.StartByte != 0 || ref.EndByte != 0
	hasPointer := ref.JSONPointer != ""
	if !hasRange && !hasPointer {
		return fmt.Errorf("evidence reference for source %q has no byte range and no JSON pointer -- nothing to locate", ref.SourceID)
	}

	if hasRange {
		length := len(source.Text)
		if ref.StartByte < 0 || ref.EndByte < ref.StartByte || ref.EndByte > length {
			return fmt.Errorf("evidence reference for source %q is out of bounds (%d bytes, source is %d bytes)",
				ref.SourceID, ref.EndByte-ref.StartByte, length)
		}
	}

	return nil
}
