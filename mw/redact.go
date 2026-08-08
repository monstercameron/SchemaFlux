package mw

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// RedactEgress scrubs a request's prompt text before it is handed to the
// wrapped provider -- the last point in the process before the bytes go over
// the network to a third party. It redacts CompletionRequest.SystemPrompt
// and CompletionRequest.UserPrompt only: those are what leaves the process
// here. A response coming back has already left the provider on its side of
// the call, so redacting it after the fact would not change what the
// provider received, and this middleware is not the place that decides what
// a caller of the library gets to see (that is internal/ops's Redact
// operation, marked not-production-ready by F-021 pending its own fix).
//
// # Why this is not schemafluxtest's credentialPatterns reused
//
// schemafluxtest/cassette.go already has a list of credential-shaped regexes,
// and scripts/secret_scan.py has a third copy in Python. All three exist for
// the same underlying worry -- a secret reaching somewhere it should not --
// but they guard three different moments and cannot share one definition:
//
//   - secret_scan.py runs at commit/CI time over every tracked file. It is
//     Python; nothing here can import it, and it is not scoped to a request.
//   - cassette.go's list runs at cassette-*write* time, guarding a file this
//     repository is about to commit. Its whole job is catching literal
//     vendor credential shapes before they touch disk, and it is right to be
//     narrow: a cassette fixture is code, and false positives there block a
//     commit.
//   - This list runs at *every egress call*, guarding a live network request
//     that carries a caller's arbitrary application data -- invoices,
//     tickets, notes -- not source code. Over-redacting here does not block
//     a commit, it silently corrupts a request in production, which is a
//     worse failure mode than a missed secret and the reason the built-ins
//     below stay deliberately narrow and are opt-out via WithoutBuiltins.
//
// Importing schemafluxtest from mw would also invert the dependency this
// package is supposed to have: schemafluxtest is test-support machinery that
// depends on the library, and a production middleware depending back on a
// test-support package for its redaction rules would make "go test" behavior
// silently load-bearing for production egress. Keeping the list local and
// small, with this comment as the record of why it is not shared, is the
// deliberate choice; a fourth copy of the same handful of regexes is a
// smaller cost than either of those.
//
// The caller supplies whatever else needs scrubbing for their domain --
// WithPattern for a labelled regex, WithFunc for anything a regex cannot
// express -- because this package cannot know what a caller's business data
// looks like, only what a leaked API credential looks like.
func RedactEgress(opts ...RedactOption) Middleware {
	cfg := redactConfig{builtins: true, marker: defaultRedactMarker}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	patterns := make([]redactPattern, 0, len(builtinRedactPatterns)+len(cfg.patterns))
	if cfg.builtins {
		patterns = append(patterns, builtinRedactPatterns...)
	}
	patterns = append(patterns, cfg.patterns...)

	return func(next Handler) Handler {
		return &redactingProvider{
			next:     next,
			patterns: patterns,
			marker:   cfg.marker,
			extra:    cfg.fn,
		}
	}
}

// RedactOption configures RedactEgress beyond its always-on built-ins.
type RedactOption func(*redactConfig)

type redactConfig struct {
	builtins bool
	marker   string
	patterns []redactPattern
	fn       func(string) string
}

type redactPattern struct {
	name    string
	pattern *regexp.Regexp
}

// WithoutBuiltins turns off the default patterns, for a caller who wants
// only their own rules -- e.g. a deployment that already redacts secrets at
// a proxy layer and would rather this middleware not double up.
func WithoutBuiltins() RedactOption {
	return func(cfg *redactConfig) { cfg.builtins = false }
}

// WithPattern adds a caller-supplied, named regex to the redaction set. The
// name appears in the marker that replaces a match (e.g. "[redacted:ssn]"),
// so a caller inspecting a redacted prompt can tell which rule fired without
// the original value ever having been logged anywhere.
func WithPattern(name string, pattern *regexp.Regexp) RedactOption {
	return func(cfg *redactConfig) {
		if pattern == nil {
			return
		}
		cfg.patterns = append(cfg.patterns, redactPattern{name: name, pattern: pattern})
	}
}

// WithMarker overrides the default "[redacted:NAME]" replacement text. It
// still has NAME substituted via %s if the supplied format contains one; a
// marker with no verb is used as a flat literal for every match, named or
// not.
func WithMarker(marker string) RedactOption {
	return func(cfg *redactConfig) {
		if marker != "" {
			cfg.marker = marker
		}
	}
}

// WithFunc adds an arbitrary redaction pass run after every regex pattern,
// for scrubbing that a regex cannot express -- a caller's own PII detector,
// a lookup against a list of known account numbers, anything stateful. It
// runs on the already-pattern-redacted text, so it can rely on regex-shaped
// secrets already being gone.
func WithFunc(fn func(string) string) RedactOption {
	return func(cfg *redactConfig) { cfg.fn = fn }
}

const defaultRedactMarker = "[redacted:%s]"

// builtinRedactPatterns are the shapes worth redacting on every request by
// default: they are unambiguous enough that a false positive is rare, and
// costly enough to leak that catching them by default is worth it. This is
// deliberately smaller than schemafluxtest's credentialPatterns (see the
// package doc comment above) and does not include anything shaped like an
// ordinary number -- a credit-card-style regex has too high a false-positive
// rate against invoice totals, order IDs, and phone numbers to run
// unconditionally against arbitrary caller text; a caller who needs that
// coverage adds it with WithPattern, deliberately, for their own domain.
var builtinRedactPatterns = []redactPattern{
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`)},
	{"aws-access-key-id", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"bearer-header", regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+\S+`)},
	// Email addresses are the one PII shape common enough across ordinary
	// business text (invoices, tickets, notes) to be worth a default, and
	// precise enough as a regex that it rarely fires on anything else.
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)},
}

type redactingProvider struct {
	next     Handler
	patterns []redactPattern
	marker   string
	extra    func(string) string
}

func (p *redactingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	req.SystemPrompt = p.redact(req.SystemPrompt)
	req.UserPrompt = p.redact(req.UserPrompt)
	return p.next.Complete(ctx, req)
}

func (p *redactingProvider) redact(text string) string {
	if text == "" {
		return text
	}
	for _, entry := range p.patterns {
		replacement := entry.name
		marker := p.marker
		if marker == "" {
			marker = defaultRedactMarker
		}
		var out string
		if containsVerb(marker) {
			out = fmt.Sprintf(marker, replacement)
		} else {
			out = marker
		}
		text = entry.pattern.ReplaceAllString(text, out)
	}
	if p.extra != nil {
		text = p.extra(text)
	}
	return text
}

// containsVerb reports whether s contains a %s verb, so WithMarker can
// accept either a template ("[gone:%s]") or a flat literal ("[gone]")
// without a separate option for each.
func containsVerb(s string) bool {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '%' && s[i+1] == 's' {
			return true
		}
	}
	return false
}

func (p *redactingProvider) Name() string { return p.next.Name() }

func (p *redactingProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *redactingProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
