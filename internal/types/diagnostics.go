package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

// The second channel A-011's Revised line asks for.
//
// "No payload in errors" is correct and, by itself, leaves debugging with
// nothing: a caller who cannot reproduce a malformed answer cannot fix
// whatever produced it. The answer is not to relax the rule on errors -- it
// is to give a caller an explicit, opt-in place to send the content instead,
// with its own bounds and its own retention, so an ordinary error can stay
// payload-free and still point at something.
//
// A DiagnosticSink is that place. It does not exist by default: RunOp (in
// internal/ops) looks for one on the context and, finding none, captures
// nothing and returns the zero DiagnosticRef -- the "off by default" half of
// the Revised line. A caller that wants the content wires a sink in and gets
// a reference back on every error that would otherwise have discarded it.

// DiagnosticRecord is what a configured sink receives.
//
// Body has already been bounded and scrubbed by CaptureDiagnostic before a
// sink ever sees it -- the sink is not trusted to redact, because a sink is
// often "write this to disk or ship it to an observability backend", and a
// credential that reaches that point has already left the process the same
// way an unredacted error would have.
type DiagnosticRecord struct {
	// ID is unique per capture, so a caller storing many records can look one
	// up by the reference an error carries.
	ID string

	// Digest is the SHA-256 of Body, hex-encoded. It lets a caller confirm a
	// stored record matches the reference in an error without trusting the ID
	// alone, and it lets two errors that captured the same content be
	// recognised as duplicates without comparing the content itself.
	Digest string

	// Operation names what was running, e.g. "extract@v1" (OperationID's own
	// rendering), so a sink holding records from many operations can filter.
	Operation string

	// Kind is the classified failure the capture was taken for.
	Kind ErrorKind

	// Body is the captured content: bounded to the configured limit and
	// scrubbed of the credential shapes diagnosticPatterns recognises. It is
	// still the caller's application data beyond that -- the sink is the
	// place that opted in to holding it, which is why capture is off by
	// default and every field above exists to make the record traceable
	// rather than anonymous.
	Body string

	// Truncated reports whether Body was cut to fit MaxBodyBytes.
	Truncated bool

	// CapturedAt is when the record was built.
	CapturedAt time.Time

	// ExpiresAt is when the record's retention window (DiagnosticPolicy.
	// Retention) says it may be discarded. It is the zero Time when no
	// retention was configured, which leaves the decision to the sink -- this
	// package has no storage of its own to expire anything from.
	ExpiresAt time.Time
}

// DiagnosticSink is the caller-supplied capture point.
//
// Capture is called synchronously from the failing call path (withRepair, by
// way of internal/ops's captureDiagnostic), so an implementation that talks
// to a slow backend should hand the record to a queue rather than block the
// operation on it -- this package makes no guarantee about how long Capture
// is allowed to take, and does not impose one, because that trade-off
// belongs to the caller who chose to configure a sink at all.
type DiagnosticSink interface {
	Capture(record DiagnosticRecord)
}

// DiagnosticRef is what an ordinary error carries instead of the payload: an
// ID and a content digest into whatever a sink stored, or the zero value when
// no sink was configured. Printing it is safe -- neither field is the
// content itself.
type DiagnosticRef struct {
	ID     string
	Digest string
}

// IsZero reports whether the reference points at nothing, which is the case
// whenever no sink was configured for the call that failed.
func (r DiagnosticRef) IsZero() bool {
	return r.ID == ""
}

// String renders the reference for inclusion in an error message. It carries
// no content, only where the content -- if a sink captured it -- can be
// found.
func (r DiagnosticRef) String() string {
	if r.IsZero() {
		return ""
	}
	return fmt.Sprintf("diagnostic %s, digest %s", r.ID, r.Digest)
}

// DiagnosticPolicy bounds and redacts what CaptureDiagnostic hands to a sink.
type DiagnosticPolicy struct {
	// MaxBodyBytes caps the captured body. Zero uses
	// DefaultDiagnosticMaxBodyBytes; "bounded" is not optional the way the
	// sink itself is.
	MaxBodyBytes int

	// Retention is how long a captured record's content is expected to stay
	// available. It is recorded on the record as ExpiresAt for the sink to
	// honour; this package holds nothing itself to expire.
	Retention time.Duration

	// ExtraPatterns are caller-domain redactions run in addition to the
	// built-in credential shapes -- an account number format, an internal
	// token shape, anything this package cannot know about generically.
	ExtraPatterns []*regexp.Regexp
}

// DefaultDiagnosticMaxBodyBytes is the bound applied when a policy leaves
// MaxBodyBytes at zero. It is generous enough to show a malformed JSON body
// or a rejected paragraph in full, and small enough that a sink storing many
// of these does not become an unbounded log of caller data by accident.
const DefaultDiagnosticMaxBodyBytes = 4096

var diagnosticSeq uint64

// nextDiagnosticID returns a value unique within the process. It does not
// need to be unique across processes or restarts -- a sink is free to
// namespace it further -- only unique enough that two records captured in
// the same run are distinguishable.
func nextDiagnosticID() string {
	n := atomic.AddUint64(&diagnosticSeq, 1)
	return fmt.Sprintf("diag-%d-%d", time.Now().UnixNano(), n)
}

// diagnosticPatterns are the credential shapes scrubbed from a captured body
// before it reaches a sink.
//
// This is a third copy of a list mw/redact.go and schemafluxtest/cassette.go
// already have, and mw/redact.go's own comment explains why those two do not
// share one: they guard different moments (a live egress request; a fixture
// about to be committed) with different failure costs for a false positive.
// This is a fourth moment with its own cost profile again -- a body a caller
// has explicitly opted into storing outside the error, for as long as
// DiagnosticPolicy.Retention says, in whatever backend the sink writes to.
// That is closer to cassette.go's "about to persist somewhere durable" than
// to mw's "about to leave the process on every call", but it is still a
// different write path with a different caller and a different lifetime, and
// internal/types cannot import mw (mw imports internal/types; the reverse
// would be a cycle) or schemafluxtest (test-support code the production path
// must not depend on) to reuse either list. Duplicating a handful of regexes
// a fourth time costs less than any of the alternatives: sharing across a
// layering boundary that cannot support it, or leaving a caller's captured
// diagnostic body unscrubbed because the "right" list lived somewhere this
// package could not reach.
var diagnosticPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`),
}

const redactedMarker = "[redacted]"

// redactDiagnosticBody scrubs the built-in credential shapes plus any
// caller-supplied patterns, in that order, so a caller's own pattern can
// still catch whatever the built-ins already turned into a marker.
func redactDiagnosticBody(body string, extra []*regexp.Regexp) string {
	out := body
	for _, pattern := range diagnosticPatterns {
		out = pattern.ReplaceAllString(out, redactedMarker)
	}
	for _, pattern := range extra {
		if pattern != nil {
			out = pattern.ReplaceAllString(out, redactedMarker)
		}
	}
	return out
}

// CaptureDiagnostic bounds, redacts, and digests body, hands the result to
// sink, and returns the reference an ordinary error carries in its place.
//
// A nil sink is the off-by-default case: nothing is redacted, nothing is
// digested, nothing is captured, and the zero DiagnosticRef comes back --
// which is indistinguishable from "no diagnostic exists for this error"
// because that is exactly what it means.
func CaptureDiagnostic(sink DiagnosticSink, policy DiagnosticPolicy, operation string, kind ErrorKind, body string) DiagnosticRef {
	if sink == nil {
		return DiagnosticRef{}
	}

	limit := policy.MaxBodyBytes
	if limit <= 0 {
		limit = DefaultDiagnosticMaxBodyBytes
	}

	scrubbed := redactDiagnosticBody(body, policy.ExtraPatterns)

	truncated := false
	if len(scrubbed) > limit {
		// Truncating by byte, not rune, could split a multi-byte character at
		// the boundary. That is acceptable here -- Body is diagnostic text
		// for a human or a store, not data this package parses back -- and
		// matching mw/redact.go's own byte-oriented regex work keeps the two
		// scrubbing passes consistent about what "the text" means.
		scrubbed = scrubbed[:limit]
		truncated = true
	}

	digest := sha256.Sum256([]byte(scrubbed))
	ref := DiagnosticRef{
		ID:     nextDiagnosticID(),
		Digest: hex.EncodeToString(digest[:]),
	}

	record := DiagnosticRecord{
		ID:         ref.ID,
		Digest:     ref.Digest,
		Operation:  operation,
		Kind:       kind,
		Body:       scrubbed,
		Truncated:  truncated,
		CapturedAt: time.Now(),
	}
	if policy.Retention > 0 {
		record.ExpiresAt = record.CapturedAt.Add(policy.Retention)
	}

	sink.Capture(record)
	return ref
}
