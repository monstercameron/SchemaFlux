package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
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
	// scrubbed of the credential shapes diagnosticPatterns recognises, including
	// complete private-key blocks and truncated blocks without an END marker. It is
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
// before it reaches a sink, and from content a logger is asked to redact. They
// are the one list in this package; RedactCredentials exports it, so a layer
// that needs the same scrubbing calls it and does not keep a copy that drifts.
var diagnosticPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`),
}

var (
	privateKeyBegin = regexp.MustCompile(`-----BEGIN ([A-Z ]*PRIVATE KEY(?: BLOCK)?)-----`)
	privateKeyEnd   = regexp.MustCompile(`-----END ([A-Z ]*PRIVATE KEY(?: BLOCK)?)-----`)
)

const redactedMarker = "[redacted]"

// RedactCredentials replaces every credential shape this package recognises in
// text with a fixed marker: provider API keys, cloud access keys, tokens, full
// private-key blocks and bearer authorization values. An unterminated private
// key block is replaced through the end of the text. It is not a general content
// filter: text that matches none of the shapes comes back unchanged.
func RedactCredentials(text string) string {
	text = redactPrivateKeyBlocks(text)
	for _, pattern := range diagnosticPatterns {
		text = pattern.ReplaceAllString(text, redactedMarker)
	}
	return text
}

// redactPrivateKeyBlocks replaces each complete PEM block only when its END
// label matches its BEGIN label. A mismatched or missing END leaves the block's
// boundary unknowable, so the remainder is discarded rather than risking that
// key material after a malformed marker reaches a diagnostic sink.
func redactPrivateKeyBlocks(text string) string {
	var out strings.Builder
	cursor := 0
	changed := false
	for cursor < len(text) {
		begin := privateKeyBegin.FindStringSubmatchIndex(text[cursor:])
		if begin == nil {
			break
		}
		start := cursor + begin[0]
		bodyStart := cursor + begin[1]
		label := text[cursor+begin[2] : cursor+begin[3]]
		end := privateKeyEnd.FindStringSubmatchIndex(text[bodyStart:])

		out.WriteString(text[cursor:start])
		out.WriteString(redactedMarker)
		changed = true
		if end == nil {
			return out.String()
		}
		endAfter := bodyStart + end[1]
		endLabel := text[bodyStart+end[2] : bodyStart+end[3]]
		if endLabel != label {
			return out.String()
		}
		cursor = endAfter
	}
	if !changed {
		return text
	}
	out.WriteString(text[cursor:])
	return out.String()
}

// redactDiagnosticBody scrubs the built-in credential shapes plus any
// caller-supplied patterns, in that order, so a caller's own pattern can
// still catch whatever the built-ins already turned into a marker.
func redactDiagnosticBody(body string, extra []*regexp.Regexp) string {
	out := RedactCredentials(body)
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
		// The limit is in bytes, and the cut backs up to the start of a character,
		// so Body never ends in half of one: a store or a JSON encoder would
		// replace or refuse the broken sequence.
		for limit > 0 && !utf8.RuneStart(scrubbed[limit]) {
			limit--
		}
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
