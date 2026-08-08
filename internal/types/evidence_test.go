package types

import (
	"strings"
	"testing"
)

// TC-002. DigestSource and ValidateEvidenceRef are the runtime half of the
// evidence contract: given a claimed reference and the source it names, do
// the two actually agree. These tests exercise that check directly, without
// any operation or provider involved.

func TestDigestSourceIsStableAndContentAddressed(t *testing.T) {
	a := DigestSource("the quick brown fox")
	b := DigestSource("the quick brown fox")
	c := DigestSource("the quick brown fox.")

	if a != b {
		t.Fatalf("DigestSource is not stable across calls: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("DigestSource did not change for different content")
	}
	if a == "" {
		t.Fatalf("DigestSource returned an empty digest")
	}
}

func TestValidateEvidenceRef(t *testing.T) {
	source := EvidenceSource{ID: "doc-1", Text: "Invoice total: $412.50, due June 1."}
	digest := DigestSource(source.Text)
	sources := map[string]EvidenceSource{"doc-1": source}

	cases := []struct {
		name    string
		ref     EvidenceRef
		wantErr bool
	}{
		{
			"valid byte range",
			EvidenceRef{SourceID: "doc-1", StartByte: 16, EndByte: 23, SourceDigest: digest},
			false,
		},
		{
			"valid JSON pointer, no byte range",
			EvidenceRef{SourceID: "doc-1", JSONPointer: "/total", SourceDigest: digest},
			false,
		},
		{
			"no source named",
			EvidenceRef{StartByte: 0, EndByte: 5, SourceDigest: digest},
			true,
		},
		{
			"source not supplied",
			EvidenceRef{SourceID: "doc-9", StartByte: 0, EndByte: 5, SourceDigest: digest},
			true,
		},
		{
			"no digest at all",
			EvidenceRef{SourceID: "doc-1", StartByte: 0, EndByte: 5},
			true,
		},
		{
			"wrong digest (stale or fabricated reference)",
			EvidenceRef{SourceID: "doc-1", StartByte: 0, EndByte: 5, SourceDigest: "deadbeef"},
			true,
		},
		{
			"no locator at all",
			EvidenceRef{SourceID: "doc-1", SourceDigest: digest},
			true,
		},
		{
			"end before start",
			EvidenceRef{SourceID: "doc-1", StartByte: 20, EndByte: 5, SourceDigest: digest},
			true,
		},
		{
			"negative start",
			EvidenceRef{SourceID: "doc-1", StartByte: -1, EndByte: 5, SourceDigest: digest},
			true,
		},
		{
			"end past the source's length",
			EvidenceRef{SourceID: "doc-1", StartByte: 0, EndByte: len(source.Text) + 500, SourceDigest: digest},
			true,
		},
		{
			"start equals end (empty but in-bounds range)",
			EvidenceRef{SourceID: "doc-1", StartByte: 5, EndByte: 5, SourceDigest: digest},
			false,
		},
		{
			"whole document",
			EvidenceRef{SourceID: "doc-1", StartByte: 0, EndByte: len(source.Text), SourceDigest: digest},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEvidenceRef(tc.ref, sources)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateEvidenceRef(%+v) = nil, want an error", tc.ref)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateEvidenceRef(%+v) = %v, want nil", tc.ref, err)
			}
		})
	}
}

// A source that changes between when a span was cited and when it is
// checked is exactly the case a bounds check alone cannot catch: the new,
// shorter text may still have something at the same offsets, in range and
// wrong.
func TestValidateEvidenceRefRejectsADriftedSource(t *testing.T) {
	original := EvidenceSource{ID: "doc-1", Text: "Total due: $500.00 by Friday."}
	digest := DigestSource(original.Text)

	ref := EvidenceRef{SourceID: "doc-1", StartByte: 12, EndByte: 19, SourceDigest: digest}

	updated := map[string]EvidenceSource{
		"doc-1": {ID: "doc-1", Text: "Total due: $999.99 by Friday."},
	}

	if err := ValidateEvidenceRef(ref, updated); err == nil {
		t.Fatal("a reference computed against the old text was accepted against the drifted one")
	}
}

// The digest travels with the reference; the source text never does. This is
// the check for "provenance carries no caller payload": feed a marker
// through as the source text and confirm nothing about EvidenceRef or the
// error ValidateEvidenceRef returns contains it.
func TestEvidenceRefNeverCarriesTheSourceText(t *testing.T) {
	const marker = "SCHEMAFLUX-PAYLOAD-MARKER-4f8c"
	source := EvidenceSource{ID: "doc-1", Text: "Confidential: " + marker + " must never leak."}
	digest := DigestSource(source.Text)
	sources := map[string]EvidenceSource{"doc-1": source}

	ref := EvidenceRef{SourceID: "doc-1", StartByte: 14, EndByte: 14 + len(marker), SourceDigest: digest}

	if strings.Contains(ref.SourceID, marker) || strings.Contains(ref.JSONPointer, marker) || strings.Contains(ref.SourceDigest, marker) {
		t.Fatalf("EvidenceRef carries the payload: %+v", ref)
	}

	// A wrong reference's error also names no payload -- only counts and
	// source IDs, the same restraint AGENTS.md requires of every error this
	// library returns.
	badRef := EvidenceRef{SourceID: "doc-1", StartByte: 0, EndByte: 3, SourceDigest: "wrong"}
	err := ValidateEvidenceRef(badRef, sources)
	if err == nil {
		t.Fatal("expected the wrong digest to be rejected")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("the validation error leaked the payload: %v", err)
	}
}

func TestEvidenceRefIsZero(t *testing.T) {
	if !(EvidenceRef{}).IsZero() {
		t.Error("the zero value should report IsZero")
	}
	if (EvidenceRef{SourceID: "doc-1"}).IsZero() {
		t.Error("a reference naming a source is not zero")
	}
	if (EvidenceRef{JSONPointer: "/x"}).IsZero() {
		t.Error("a reference naming a pointer is not zero")
	}
}

func TestEvidencePolicyString(t *testing.T) {
	cases := []struct {
		policy EvidencePolicy
		want   string
	}{
		{EvidenceNone, "none"},
		{EvidenceMaterialFields, "material fields only"},
		{EvidenceAllModelDerived, "all model-derived fields"},
		{EvidenceNoInference, "no inference"},
		{EvidencePolicy(99), "none"},
	}
	for _, tc := range cases {
		if got := tc.policy.String(); got != tc.want {
			t.Errorf("EvidencePolicy(%d).String() = %q, want %q", tc.policy, got, tc.want)
		}
	}
}
