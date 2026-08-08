package types

import (
	"strings"
	"testing"
)

// TC-003. Direct, in-package tests for provenance.go, alongside the
// integration coverage in internal/ops/tc003_provenance_test.go that
// exercises the same functions through RunOpResult. Both are kept: this
// package's own test run is what attributes coverage to this file at all,
// since Go's default coverage profile does not credit a package for calls
// made from another package's tests.

func TestDigestValueHandlesNil(t *testing.T) {
	// nil must not panic and must still be a stable identity: the same
	// input (nothing) digests to the same output every time.
	a := DigestValue(nil)
	b := DigestValue(nil)
	if a != b || a == "" {
		t.Fatalf("DigestValue(nil) = %q / %q, want equal and non-empty", a, b)
	}
}

func TestDigestValueMatchesDigestSourceForStrings(t *testing.T) {
	// The string fast path and DigestSource must agree, because both are
	// documented as the same hash over the same bytes for a plain string
	// input.
	const text = "an ordinary string input"
	if got, want := DigestValue(text), DigestSource(text); got != want {
		t.Fatalf("DigestValue(string) = %q, want %q (DigestSource)", got, want)
	}
}

func TestDigestValueFallsBackForUnmarshalableInput(t *testing.T) {
	// A channel cannot be marshalled to JSON; DigestValue must still return
	// something deterministic rather than panicking or returning empty.
	ch := make(chan int)
	got := DigestValue(ch)
	if got == "" {
		t.Fatal("DigestValue() on an unmarshalable value returned empty")
	}
}

func TestProvenanceFullyTracedRequiresAllFour(t *testing.T) {
	full := Provenance{ResultID: "r", InputDigest: "i", OperationVersion: "o@v1", ResolvedModel: "m"}
	if !full.FullyTraced() {
		t.Fatal("FullyTraced() = false for a Provenance with every required field set")
	}

	// Each required field, removed one at a time, breaks it.
	missingResult := full
	missingResult.ResultID = ""
	if missingResult.FullyTraced() {
		t.Error("FullyTraced() = true with ResultID missing")
	}

	missingInput := full
	missingInput.InputDigest = ""
	if missingInput.FullyTraced() {
		t.Error("FullyTraced() = true with InputDigest missing")
	}

	missingOp := full
	missingOp.OperationVersion = ""
	if missingOp.FullyTraced() {
		t.Error("FullyTraced() = true with OperationVersion missing")
	}

	missingModel := full
	missingModel.ResolvedModel = ""
	if missingModel.FullyTraced() {
		t.Error("FullyTraced() = true with ResolvedModel missing")
	}
}

func TestNewResultIDLooksOpaqueNotSequential(t *testing.T) {
	a := NewResultID()
	b := NewResultID()
	if a == b {
		t.Fatal("two calls to NewResultID produced the same value")
	}
	// Hex-encoded, fixed length -- not a counter, not a timestamp string.
	if len(a) != 32 {
		t.Fatalf("NewResultID() length = %d, want 32 (16 bytes hex-encoded)", len(a))
	}
	if strings.ContainsAny(a, "ghijklmnopqrstuvwxyzGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("NewResultID() = %q, not valid hex", a)
	}
}
