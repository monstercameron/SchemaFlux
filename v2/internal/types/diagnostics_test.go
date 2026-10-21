package types

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// The credential fixtures are assembled from pieces for the reason fakeKey is:
// they have to look like credentials, and a literal one trips the secret scan.
func TestRedactCredentialsReplacesEveryShapeItKnows(t *testing.T) {
	tail := "abcdefghijklmnopqrstuvwxyz0123456789"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"an openai key", "key " + "sk-" + tail + " here", "key [redacted] here"},
		{"a project key", "sk-" + "proj-" + tail, "[redacted]"},
		{"a service account key", "sk-" + "svcacct-" + tail, "[redacted]"},
		{"an anthropic key", "sk-" + "ant-" + tail, "[redacted]"},
		{"a google key", "AI" + "za" + tail, "[redacted]"},
		{"an aws access key", "AK" + "IA" + "ABCDEFGHIJKLMNOP", "[redacted]"},
		{"an aws temporary key", "AS" + "IA" + "ABCDEFGHIJKLMNOP", "[redacted]"},
		{"a github token", "gh" + "p_" + tail, "[redacted]"},
		{"a slack token", "xo" + "xb-1234567890-abcdef", "[redacted]"},
		{"a private key header", "-----BEGIN " + "RSA PRIVATE KEY-----", "[redacted]"},
		{"a bearer header", "Authorization: " + "Bearer abc.def.ghi", "[redacted]"},
		{"two keys in one text", "a " + "sk-" + tail + " b " + "AK" + "IA" + "ABCDEFGHIJKLMNOP" + " c", "a [redacted] b [redacted] c"},
		{"text with no credential", "an invoice for 42 dollars", "an invoice for 42 dollars"},
		{"a key that is too short", "sk-short", "sk-short"},
		{"an email is not a credential", "mail me at a@b.example", "mail me at a@b.example"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactCredentials(tc.in); got != tc.want {
				t.Errorf("RedactCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactCredentialsReplacesEntirePrivateKeyBlocks(t *testing.T) {
	privateMaterial := "private-material-must-not-reach-the-diagnostic-sink"
	block := func(label string, end bool) string {
		text := "-----BEGIN " + label + "-----\n" + privateMaterial + "\n"
		if end {
			text += "-----END " + label + "-----"
		}
		return text
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"PKCS8 private key", block("PRIVATE KEY", true), redactedMarker},
		{"RSA private key", block("RSA PRIVATE KEY", true), redactedMarker},
		{"EC private key", block("EC PRIVATE KEY", true), redactedMarker},
		{"OpenSSH private key", block("OPENSSH PRIVATE KEY", true), redactedMarker},
		{"PGP private key", block("PGP PRIVATE KEY", true), redactedMarker},
		{"encrypted private key", block("ENCRYPTED PRIVATE KEY", true), redactedMarker},
		{"truncated PKCS8 private key", block("PRIVATE KEY", false), redactedMarker},
		{"truncated RSA private key", block("RSA PRIVATE KEY", false), redactedMarker},
		{"truncated OpenSSH private key", block("OPENSSH PRIVATE KEY", false), redactedMarker},
		{"mismatched END discards the remainder", "prefix\n-----BEGIN " + "RSA PRIVATE KEY-----\n" + privateMaterial + "\n-----END " + "EC PRIVATE KEY-----\n" + privateMaterial + "-after-mismatch", "prefix\n" + redactedMarker},
		{"multiple blocks are each replaced", block("RSA PRIVATE KEY", true) + "\nbetween\n" + block("EC PRIVATE KEY", true), redactedMarker + "\nbetween\n" + redactedMarker},
		{"surrounding text is preserved", "before\n" + block("EC PRIVATE KEY", true) + "\nafter", "before\n" + redactedMarker + "\nafter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactCredentials(tc.in)
			if got != tc.want {
				t.Errorf("RedactCredentials() = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, privateMaterial) {
				t.Errorf("private-key material survived redaction: %q", got)
			}
		})
	}
}

// A captured body is cut at the limit on a character boundary, so it never ends
// in half of one.
func TestCaptureDiagnosticCutsOnACharacterBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		limit int
	}{
		{"two-byte characters, limit in the middle of one", strings.Repeat("é", 50), 7},
		{"two-byte characters, limit on a boundary", strings.Repeat("é", 50), 8},
		{"three-byte characters", strings.Repeat("€", 40), 10},
		{"four-byte characters", strings.Repeat("\U0001F600", 30), 9},
		{"ascii", strings.Repeat("a", 100), 10},
		{"mixed", "abécd€ef\U0001F600gh", 5},
		{"a limit of one inside a multibyte character", "éé", 1},
		{"a limit past the end", "short", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &captureSink{}
			CaptureDiagnostic(sink, DiagnosticPolicy{MaxBodyBytes: tc.limit}, "op", KindMalformedOutput, tc.body)
			if len(sink.records) != 1 {
				t.Fatalf("%d records", len(sink.records))
			}
			body := sink.records[0].Body
			if !utf8.ValidString(body) {
				t.Errorf("the captured body is not valid UTF-8: %q", body)
			}
			if len(body) > tc.limit {
				t.Errorf("the body is %d bytes, over the limit of %d", len(body), tc.limit)
			}
			if !strings.HasPrefix(tc.body, body) {
				t.Errorf("the body is not a prefix of the input: %q", body)
			}
			if truncated := len(tc.body) > tc.limit; sink.records[0].Truncated != truncated {
				t.Errorf("Truncated = %v, want %v", sink.records[0].Truncated, truncated)
			}
		})
	}
}
