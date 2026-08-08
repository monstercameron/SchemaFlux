package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-002. StrictEvidence is the enforcement half; internal/types/evidence_test.go
// covers ValidateEvidenceRef directly. These tests cover the four policies,
// the fabricated-field case the task's verify bullet names explicitly, and
// then the end-to-end path through RunOp so the wiring (op.go's
// EvidenceRequired/EvidencePolicy read, previously dead) is proven, not just
// the pure function underneath it.

func mustSource(id, text string) map[string]types.EvidenceSource {
	return map[string]types.EvidenceSource{
		id: {ID: id, Text: text},
	}
}

func validRef(id, text, span string) types.EvidenceRef {
	start := strings.Index(text, span)
	if start < 0 {
		panic("test setup: span not found in text")
	}
	return types.EvidenceRef{
		SourceID:     id,
		StartByte:    start,
		EndByte:      start + len(span),
		SourceDigest: types.DigestSource(text),
	}
}

func TestStrictEvidence(t *testing.T) {
	const doc = "The invoice total is $412.50, issued to Acme Corp."
	sources := mustSource("doc-1", doc)
	goodRef := validRef("doc-1", doc, "$412.50")

	cases := []struct {
		name           string
		policy         types.EvidencePolicy
		materialFields []string
		claims         []types.ClaimProvenance
		wantErr        bool
	}{
		{
			"EvidenceNone never checks anything",
			types.EvidenceNone, nil,
			[]types.ClaimProvenance{{FieldPath: "/total"}}, // no evidence, would fail any other policy
			false,
		},
		{
			"material field with valid evidence passes",
			types.EvidenceMaterialFields, []string{"/total"},
			[]types.ClaimProvenance{{FieldPath: "/total", Evidence: []types.EvidenceRef{goodRef}}},
			false,
		},
		{
			"material field with no claim at all fails",
			types.EvidenceMaterialFields, []string{"/total"},
			nil,
			true,
		},
		{
			"material field claimed but no evidence -- fabrication",
			types.EvidenceMaterialFields, []string{"/total"},
			[]types.ClaimProvenance{{FieldPath: "/total"}},
			true,
		},
		{
			"material field with an out-of-bounds reference fails",
			types.EvidenceMaterialFields, []string{"/total"},
			[]types.ClaimProvenance{{FieldPath: "/total", Evidence: []types.EvidenceRef{
				{SourceID: "doc-1", StartByte: 0, EndByte: 9999, SourceDigest: types.DigestSource(doc)},
			}}},
			true,
		},
		{
			"material field with a wrong-digest reference fails",
			types.EvidenceMaterialFields, []string{"/total"},
			[]types.ClaimProvenance{{FieldPath: "/total", Evidence: []types.EvidenceRef{
				{SourceID: "doc-1", StartByte: 0, EndByte: 5, SourceDigest: "not-the-real-digest"},
			}}},
			true,
		},
		{
			"non-material field is unconstrained under EvidenceMaterialFields",
			types.EvidenceMaterialFields, []string{"/total"},
			[]types.ClaimProvenance{
				{FieldPath: "/total", Evidence: []types.EvidenceRef{goodRef}},
				{FieldPath: "/notes"}, // no evidence, not material, fine
			},
			false,
		},
		{
			"AllModelDerived: unmarked claim with no evidence fails",
			types.EvidenceAllModelDerived, nil,
			[]types.ClaimProvenance{{FieldPath: "/total"}},
			true,
		},
		{
			"AllModelDerived: unmarked claim with valid evidence passes",
			types.EvidenceAllModelDerived, nil,
			[]types.ClaimProvenance{{FieldPath: "/total", Evidence: []types.EvidenceRef{goodRef}}},
			false,
		},
		{
			"AllModelDerived: a claim explicitly marked Inferred is exempt",
			types.EvidenceAllModelDerived, nil,
			[]types.ClaimProvenance{{FieldPath: "/summary", Inferred: true}},
			false,
		},
		{
			"NoInference: a claim marked Inferred is itself a violation",
			types.EvidenceNoInference, nil,
			[]types.ClaimProvenance{{FieldPath: "/summary", Inferred: true, Evidence: []types.EvidenceRef{goodRef}}},
			true,
		},
		{
			"NoInference: an unmarked claim still needs valid evidence",
			types.EvidenceNoInference, nil,
			[]types.ClaimProvenance{{FieldPath: "/total"}},
			true,
		},
		{
			"NoInference: an unmarked, evidenced claim passes",
			types.EvidenceNoInference, nil,
			[]types.ClaimProvenance{{FieldPath: "/total", Evidence: []types.EvidenceRef{goodRef}}},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := StrictEvidence("test-op", tc.policy, tc.materialFields, tc.claims, sources)
			if tc.wantErr && err == nil {
				t.Fatalf("StrictEvidence() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("StrictEvidence() = %v, want nil", err)
			}
			if tc.wantErr {
				if types.KindOf(err) != types.KindEvidenceViolation {
					t.Fatalf("KindOf(err) = %v, want KindEvidenceViolation", types.KindOf(err))
				}
			}
		})
	}
}

// The error StrictEvidence returns never names a field path or a method --
// both can carry a caller's own schema vocabulary -- only counts, matching
// the restraint invariants.go's SubsetOf/SameMultiset already apply.
func TestStrictEvidenceErrorCarriesNoFieldNames(t *testing.T) {
	const secretFieldName = "socialSecurityNumber"
	claims := []types.ClaimProvenance{{FieldPath: "/" + secretFieldName}}
	err := StrictEvidence("test-op", types.EvidenceAllModelDerived, nil, claims, nil)
	if err == nil {
		t.Fatal("expected a violation")
	}
	if strings.Contains(err.Error(), secretFieldName) {
		t.Fatalf("the error names the field path: %v", err)
	}
}

// --- Wiring: op.go reads Contract.EvidenceRequired/EvidencePolicy, which
// were previously declared and never read.

type evidenceClaimant struct {
	Total  string
	claims []types.ClaimProvenance
}

func (e evidenceClaimant) EvidenceClaims() []types.ClaimProvenance { return e.claims }

// evidenceInput carries the sources RunOp checks a decoded evidenceClaimant
// against, via the EvidenceSourced seam op.go's RunOp reads from In.
type evidenceInput struct {
	sources map[string]types.EvidenceSource
}

func (e evidenceInput) EvidenceSources() map[string]types.EvidenceSource { return e.sources }

func evidenceOp(policy types.EvidencePolicy, decode func(body string) (evidenceClaimant, error)) Op[evidenceInput, evidenceClaimant] {
	return Op[evidenceInput, evidenceClaimant]{
		ID:        types.OperationID{Name: "evidenceTest", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[evidenceClaimant]{
			SchemaName:       "evidenceClaimant",
			Decode:           decode,
			EvidenceRequired: true,
			EvidencePolicy:   policy,
		},
		Batch: BatchAlgebra[evidenceInput, evidenceClaimant]{Class: types.BatchNone},
		BuildPrompt: func(input evidenceInput, _ types.OpOptions) (string, string) {
			return "extract the total, with evidence", "doc"
		},
	}
}

// An operation that requires evidence and gets none fails with
// KindEvidenceViolation rather than returning the answer -- the task's
// first verify bullet, proven through RunOp rather than through
// StrictEvidence directly.
func TestRunOpRequiringEvidenceFailsWithoutIt(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "$412.50", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := evidenceOp(types.EvidenceAllModelDerived, func(body string) (evidenceClaimant, error) {
		// The model answered, but claims no evidence for it at all --
		// exactly the fabrication StrictEvidence exists to catch.
		return evidenceClaimant{Total: body, claims: []types.ClaimProvenance{{FieldPath: "/total"}}}, nil
	})

	input := evidenceInput{sources: mustSource("doc-1", "Invoice total: $412.50 due June 1.")}

	_, _, err := RunOp(context.Background(), op, input, types.OpOptions{})
	if err == nil {
		t.Fatal("RunOp succeeded despite the operation requiring evidence the answer never supplied")
	}
	if types.KindOf(err) != types.KindEvidenceViolation {
		t.Fatalf("KindOf(err) = %v, want KindEvidenceViolation", types.KindOf(err))
	}
}

// A model's cited evidence is reachable and labelled as the model's claim,
// not as something the library verified -- proven by StrictEvidence
// accepting exactly the reference the model cited, resolved against the
// real source, with the claim itself still carrying Inferred=false (an
// assertion) rather than any field claiming to be a verified fact.
func TestRunOpAcceptsAValidCitedSpan(t *testing.T) {
	const doc = "Invoice total: $412.50 due June 1."
	digest := types.DigestSource(doc)

	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "$412.50", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := evidenceOp(types.EvidenceAllModelDerived, func(body string) (evidenceClaimant, error) {
		start := strings.Index(doc, body)
		claim := types.ClaimProvenance{
			FieldPath: "/total",
			Evidence: []types.EvidenceRef{{
				SourceID: "doc-1", StartByte: start, EndByte: start + len(body), SourceDigest: digest,
			}},
			Inferred: false,
			Method:   "quoted",
		}
		return evidenceClaimant{Total: body, claims: []types.ClaimProvenance{claim}}, nil
	})

	input := evidenceInput{sources: mustSource("doc-1", doc)}

	value, _, err := RunOp(context.Background(), op, input, types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOp: %v", err)
	}
	if value.Total != "$412.50" {
		t.Fatalf("value = %+v", value)
	}
	claim := value.EvidenceClaims()[0]
	if claim.Inferred {
		t.Error("the claim should not be marked inferred: it was quoted")
	}
	if len(claim.Evidence) != 1 {
		t.Fatalf("Evidence = %v, want the one cited reference to remain reachable", claim.Evidence)
	}
	// The claim is the model's own account -- checking it is not the same
	// as the library asserting the citation entails the value. Confirm that
	// distinction has a place to live: the claim carries Method ("quoted"),
	// which is exactly the kind of field a *measurement* would never need.
	if claim.Method != "quoted" {
		t.Fatalf("Method = %q, want the model's own claim to survive intact", claim.Method)
	}
}

// An Out type that requires evidence but implements no way to report it
// fails loudly rather than silently skipping enforcement -- EvidenceRequired
// meaning something concrete even for a type that was never taught to carry
// claims.
func TestRunOpRequiringEvidenceFailsWhenOutCannotCarryIt(t *testing.T) {
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "plain string, no evidence carrier", nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := Op[string, string]{
		ID:        types.OperationID{Name: "noCarrier", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract: OutputContract[string]{
			Decode:           func(body string) (string, error) { return body, nil },
			EvidenceRequired: true,
		},
		Batch:       BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) { return "sys", input },
	}

	_, _, err := RunOp(context.Background(), op, "x", types.OpOptions{})
	if err == nil {
		t.Fatal("expected a failure: string does not implement EvidenceCarrier")
	}
	if types.KindOf(err) != types.KindEvidenceViolation {
		t.Fatalf("KindOf(err) = %v, want KindEvidenceViolation", types.KindOf(err))
	}
}

// Provenance carries no caller payload: feed a marker string through as the
// input's source text and confirm it never appears in the returned error
// (the failure path) or in a validated reference (EvidenceRef never holds
// text -- internal/types/evidence_test.go proves that structurally; this
// confirms it end to end through RunOp).
func TestRunOpEvidenceFailureCarriesNoPayload(t *testing.T) {
	const marker = "SCHEMAFLUX-PAYLOAD-MARKER-91a2"

	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return marker, nil
	})
	t.Cleanup(func() { setLLMCaller(nil) })

	op := evidenceOp(types.EvidenceAllModelDerived, func(body string) (evidenceClaimant, error) {
		return evidenceClaimant{Total: body, claims: []types.ClaimProvenance{{FieldPath: "/total"}}}, nil
	})
	input := evidenceInput{sources: mustSource("doc-1", "Confidential document containing "+marker+".")}

	_, _, err := RunOp(context.Background(), op, input, types.OpOptions{})
	if err == nil {
		t.Fatal("expected an evidence violation")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("the error leaked the payload: %v", err)
	}
}
