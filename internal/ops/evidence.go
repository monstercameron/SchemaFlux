package ops

import (
	"fmt"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-002, wiring half. internal/types/evidence.go defines the shape of a
// claim and the source it points into; this is where a batch of claims is
// actually checked against an operation's declared policy and turned into
// the one failure mode that means "a claim had nothing supporting it":
// types.KindEvidenceViolation.

// EvidenceCarrier is implemented by a decoded Out that can report the claims
// the model made about its own fields' provenance. RunOp (op.go) type-asserts
// a candidate against this once Decode and Invariants have both passed, and
// only when the operation's contract says evidence is required -- an Out
// that never implements it is unaffected by EvidenceRequired being false,
// and fails loudly (KindEvidenceViolation, not a silent no-op) when it is
// true and the type cannot say what it based its answer on.
type EvidenceCarrier interface {
	EvidenceClaims() []types.ClaimProvenance
}

// EvidenceSourced is implemented by an In that can supply the sources a
// claim's evidence is checked against. An In that does not implement it
// contributes no sources, which StrictEvidence treats as every reference
// failing to resolve -- again a loud failure, not a pass by omission.
type EvidenceSourced interface {
	EvidenceSources() map[string]types.EvidenceSource
}

// StrictEvidence checks a batch of claims against an operation's evidence
// policy, and returns a *types.OperationError carrying
// types.KindEvidenceViolation naming how many claims failed -- never which
// ones or what they said, because a claim's FieldPath and Method can carry
// enough of a caller's own vocabulary (field names chosen by their schema)
// that AGENTS.md's payload rule applies the same way it does to a value.
//
// The four policies (types.EvidencePolicy) differ only in which claims are
// required to carry a valid reference:
//
//   - EvidenceNone: nothing is checked. Always nil.
//   - EvidenceMaterialFields: only claims whose FieldPath is in
//     materialFields must resolve; a field never claimed at all is itself a
//     failure -- a material field with no claim is indistinguishable from a
//     material field the model fabricated without saying so.
//   - EvidenceAllModelDerived: every claim not marked Inferred must resolve.
//     An Inferred claim is exempt by construction.
//   - EvidenceNoInference: every claim must resolve, and none may be marked
//     Inferred -- under this policy, an inferred field is not permitted to
//     exist at all, so a model that says it inferred one has already
//     violated the contract.
func StrictEvidence(op string, policy types.EvidencePolicy, materialFields []string, claims []types.ClaimProvenance, sources map[string]types.EvidenceSource) error {
	if policy == types.EvidenceNone {
		return nil
	}

	byField := make(map[string]types.ClaimProvenance, len(claims))
	for _, claim := range claims {
		byField[claim.FieldPath] = claim
	}

	var unresolved, missing, disallowedInference int

	checkClaim := func(claim types.ClaimProvenance, noInferenceAllowed bool) {
		if noInferenceAllowed && claim.Inferred {
			disallowedInference++
			return
		}
		if claim.Inferred {
			// Exempt: the model itself says this was not read from a source.
			return
		}
		if len(claim.Evidence) == 0 {
			unresolved++
			return
		}
		for _, ref := range claim.Evidence {
			if err := types.ValidateEvidenceRef(ref, sources); err != nil {
				unresolved++
				return
			}
		}
	}

	switch policy {
	case types.EvidenceMaterialFields:
		for _, field := range materialFields {
			claim, found := byField[field]
			if !found {
				missing++
				continue
			}
			checkClaim(claim, false)
		}
	case types.EvidenceAllModelDerived:
		for _, claim := range claims {
			checkClaim(claim, false)
		}
	case types.EvidenceNoInference:
		for _, claim := range claims {
			checkClaim(claim, true)
		}
	}

	if unresolved == 0 && missing == 0 && disallowedInference == 0 {
		return nil
	}

	var reason string
	switch {
	case missing > 0 && unresolved > 0:
		reason = fmt.Sprintf("%d material field(s) had no claim at all and %d claim(s) had no valid supporting reference", missing, unresolved)
	case missing > 0:
		reason = fmt.Sprintf("%d material field(s) had no claim at all", missing)
	case disallowedInference > 0 && unresolved > 0:
		reason = fmt.Sprintf("%d claim(s) were marked inferred, which this operation's evidence policy does not permit, and %d claim(s) had no valid supporting reference", disallowedInference, unresolved)
	case disallowedInference > 0:
		reason = fmt.Sprintf("%d claim(s) were marked inferred, which this operation's evidence policy does not permit", disallowedInference)
	default:
		reason = fmt.Sprintf("%d claim(s) had no valid supporting reference", unresolved)
	}

	return &types.OperationError{
		Kind:    types.KindEvidenceViolation,
		Op:      op,
		Message: fmt.Sprintf("evidence policy %q was not satisfied: %s", policy, reason),
	}
}
