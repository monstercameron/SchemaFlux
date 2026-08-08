package types

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeKey is assembled rather than written out. These tests prove a
// credential-shaped string gets redacted, so the fixture must look like a
// credential -- and a literal one trips both this repository's own secret scan
// and GitHub's push protection, neither of which can tell a fixture from the
// real thing, and neither of which should have to. The value under test is
// identical; only the spelling in the source changes.
var fakeKey = "sk-" + "abcdefghijklmnopqrstuvwxyz0123456789"

func TestDiagnosticRefString(t *testing.T) {
	zero := DiagnosticRef{}
	if got := zero.String(); got != "" {
		t.Errorf("String() on the zero ref = %q, want empty", got)
	}

	ref := DiagnosticRef{ID: "diag-1-1", Digest: "abcd"}
	got := ref.String()
	if !strings.Contains(got, "diag-1-1") || !strings.Contains(got, "abcd") {
		t.Errorf("String() = %q, missing the ID or digest", got)
	}
}

// captureSink is a minimal DiagnosticSink for exercising CaptureDiagnostic
// without any real backend.
type captureSink struct {
	records []DiagnosticRecord
}

func (s *captureSink) Capture(record DiagnosticRecord) {
	s.records = append(s.records, record)
}

// The off-by-default refusal: a nil sink captures nothing and hands back
// the zero DiagnosticRef, indistinguishable from "no diagnostic exists" --
// which is exactly what A-011's Revised line asks for.
func TestCaptureDiagnosticNilSinkCapturesNothing(t *testing.T) {
	ref := CaptureDiagnostic(nil, DiagnosticPolicy{}, "extract@v1", KindMalformedOutput, "some body text")
	if !ref.IsZero() {
		t.Errorf("CaptureDiagnostic with a nil sink returned a non-zero ref: %+v", ref)
	}
}

func TestCaptureDiagnosticStoresABoundedScrubbedDigestedRecord(t *testing.T) {
	sink := &captureSink{}
	body := "here is my key: " + fakeKey + " and nothing else"
	ref := CaptureDiagnostic(sink, DiagnosticPolicy{}, "extract@v1", KindMalformedOutput, body)

	if ref.IsZero() {
		t.Fatal("a configured sink still returned the zero ref")
	}
	if len(sink.records) != 1 {
		t.Fatalf("sink captured %d records, want 1", len(sink.records))
	}
	record := sink.records[0]

	if record.ID != ref.ID || record.Digest != ref.Digest {
		t.Errorf("record ID/Digest (%s/%s) does not match the returned ref (%s/%s)", record.ID, record.Digest, ref.ID, ref.Digest)
	}
	if strings.Contains(record.Body, fakeKey) {
		t.Errorf("the credential shape reached the sink unredacted: %q", record.Body)
	}
	if !strings.Contains(record.Body, redactedMarker) {
		t.Errorf("record.Body = %q, want the redacted marker in place of the credential", record.Body)
	}
	if record.Operation != "extract@v1" {
		t.Errorf("Operation = %q, want extract@v1", record.Operation)
	}
	if record.Kind != KindMalformedOutput {
		t.Errorf("Kind = %v, want KindMalformedOutput", record.Kind)
	}

	// Digest is the SHA-256 of the (already scrubbed) body actually stored.
	sum := sha256.Sum256([]byte(record.Body))
	if record.Digest != hex.EncodeToString(sum[:]) {
		t.Error("Digest does not match SHA-256 of the stored (scrubbed) body")
	}
}

func TestCaptureDiagnosticTruncatesToMaxBodyBytes(t *testing.T) {
	sink := &captureSink{}
	body := strings.Repeat("x", 100)
	CaptureDiagnostic(sink, DiagnosticPolicy{MaxBodyBytes: 10}, "op", KindMalformedOutput, body)

	record := sink.records[0]
	if len(record.Body) != 10 {
		t.Errorf("len(Body) = %d, want 10", len(record.Body))
	}
	if !record.Truncated {
		t.Error("Truncated = false despite the body exceeding MaxBodyBytes")
	}
}

func TestCaptureDiagnosticZeroMaxBodyBytesUsesDefault(t *testing.T) {
	sink := &captureSink{}
	body := strings.Repeat("y", DefaultDiagnosticMaxBodyBytes+50)
	CaptureDiagnostic(sink, DiagnosticPolicy{}, "op", KindMalformedOutput, body)

	record := sink.records[0]
	if len(record.Body) != DefaultDiagnosticMaxBodyBytes {
		t.Errorf("len(Body) = %d, want the default %d", len(record.Body), DefaultDiagnosticMaxBodyBytes)
	}
	if !record.Truncated {
		t.Error("Truncated = false despite exceeding the default bound")
	}
}

func TestCaptureDiagnosticAppliesExtraPatternsAfterBuiltins(t *testing.T) {
	sink := &captureSink{}
	extra := []*regexp.Regexp{regexp.MustCompile(`ACCT-\d{6}`)}
	body := "account ACCT-123456 and key " + fakeKey
	CaptureDiagnostic(sink, DiagnosticPolicy{ExtraPatterns: extra}, "op", KindMalformedOutput, body)

	record := sink.records[0]
	if strings.Contains(record.Body, "ACCT-123456") {
		t.Errorf("caller-supplied pattern did not redact: %q", record.Body)
	}
	if strings.Contains(record.Body, fakeKey) {
		t.Errorf("built-in pattern did not redact: %q", record.Body)
	}
}

// A nil entry in ExtraPatterns must not panic -- redactDiagnosticBody guards
// it explicitly.
func TestCaptureDiagnosticToleratesNilExtraPattern(t *testing.T) {
	sink := &captureSink{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CaptureDiagnostic panicked on a nil extra pattern: %v", r)
		}
	}()
	CaptureDiagnostic(sink, DiagnosticPolicy{ExtraPatterns: []*regexp.Regexp{nil}}, "op", KindMalformedOutput, "plain body")
}

func TestCaptureDiagnosticSetsExpiresAtOnlyWhenRetentionConfigured(t *testing.T) {
	sink := &captureSink{}
	CaptureDiagnostic(sink, DiagnosticPolicy{Retention: time.Hour}, "op", KindMalformedOutput, "body")
	withRetention := sink.records[0]
	if withRetention.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero despite a configured Retention")
	}
	if !withRetention.ExpiresAt.After(withRetention.CapturedAt) {
		t.Error("ExpiresAt is not after CapturedAt")
	}

	sink2 := &captureSink{}
	CaptureDiagnostic(sink2, DiagnosticPolicy{}, "op", KindMalformedOutput, "body")
	noRetention := sink2.records[0]
	if !noRetention.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is set despite no configured Retention -- this package holds nothing to expire")
	}
}

// nextDiagnosticID must be unique across many rapid calls within a process,
// which is the only uniqueness guarantee it makes.
func TestNextDiagnosticIDIsUniqueWithinAProcess(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := nextDiagnosticID()
		if seen[id] {
			t.Fatalf("nextDiagnosticID produced a duplicate: %s", id)
		}
		seen[id] = true
	}
}
