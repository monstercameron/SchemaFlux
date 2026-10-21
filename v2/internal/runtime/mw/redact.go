package mw

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// RedactEgress scrubs a request's text before it is handed to the wrapped
// provider -- the last point in the process before the bytes go over the
// network to a third party. It redacts CompletionRequest.SystemPrompt and
// CompletionRequest.UserPrompt, the Content of every earlier Message (which for a
// tool's turn is the tool's result), the arguments of every tool call the model
// made in an earlier turn, the Description of every Tool, and every text of the
// typed Decision a request may carry (its state, its guidance, each question's
// text, each choice's options and their criteria): those are the free text of a
// request that leaves the process here. A decision provider reads the Decision
// and never the prompts, so a rule that covered the prompts alone would leave it
// all of the text in the clear. A tool's name and parameter schema, and a
// message's role and tool-call ID, are identifiers the caller chose and are sent
// as they are.
//
// The arguments of a call are JSON the model wrote against the caller's own
// schema, and they hold what the caller's text held. Each string value is
// redacted by the same rules and written back, and the rest is kept as it was:
// the keys, the numbers, the order, the white space. Arguments that are not JSON
// are redacted as the one piece of text they are, since sending what cannot be
// read would send it unscrubbed. The calls of a reply that comes back are the
// model's, answering the redacted conversation, and are returned as they are.
//
// A choice is answered with one of its options, and the answer comes back
// carrying the option as it was sent. Where a rule rewrote an option, the label
// of the answer is given back as the caller's own option, because the operation
// accepts only a label it offered. Two options that a rule rewrote into one text
// are sent apart by adding their positions, so that the answer can be told which
// was chosen. Nothing else in a response is touched.
// A response coming back has already left the provider on its side of the
// call, so redacting it after the fact would not change what the provider
// received, and this middleware is not the place that decides what a caller of
// the library gets to see (that is internal/ops's Redact operation, marked
// not-production-ready by F-021 pending its own fix).
//
// The request the caller passed is not modified: a redacted copy of each slice
// is made before a field is rewritten.
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
// silently load-bearing for production egress. Keeping the list local, with
// this comment as the record of why it is not shared, is the deliberate
// choice; another copy of the same handful of regexes is a smaller cost than
// either of those.
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

	// Everything a rule needs per request is settled here once: the replacement
	// text, fixed by the rule's name and the marker, and the anchored form of an
	// expression that starts on a literal.
	rules := make([]redactRule, len(patterns))
	for i, pattern := range patterns {
		rules[i] = newRedactRule(pattern, redactReplacement(cfg.marker, pattern.name))
	}

	return func(next Handler) Handler {
		return &redactingProvider{next: next, rules: rules, extra: cfg.fn}
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

// redactPattern is a rule as it is declared: a name and an expression, and
// for a built-in rule the literals a match starts on, which let the rule be
// skipped for a text that holds none of them (see redactRule).
type redactPattern struct {
	name    string
	pattern *regexp.Regexp

	// starts are the literals a match of pattern begins with, one of which has
	// to be in a text for the rule to match in it. Empty for a rule that
	// is not known to start on a literal, which is run over the whole text.
	starts []string

	// fold makes starts match whatever the case of their ASCII letters, as a
	// (?i) expression does.
	fold bool

	// group is the submatch the marker replaces: 0, the whole match, or a group
	// of the expression, to keep the words around a secret and replace the
	// secret alone.
	group int

	// around is a byte a match has to contain, and the rule is run on the
	// stretch of text around each one of it and nowhere else. Zero for none.
	around byte
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

// WithMarker overrides the default "[redacted:NAME]" replacement text. Every
// "%s" in the supplied marker is replaced by the name of the rule that fired,
// as a plain text substitution; a marker with no "%s" is used as a flat
// literal for every match, named or not. Nothing else in the marker is
// interpreted: a "%" or a "$" is written as it is.
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
// secrets already being gone. It is the caller's function, and a panic in it
// fails the request with types.KindInvariantViolation instead of sending text
// that may not have been scrubbed.
func WithFunc(fn func(string) string) RedactOption {
	return func(cfg *redactConfig) { cfg.fn = fn }
}

const defaultRedactMarker = "[redacted:%s]"

// redactReplacement is the text that stands in for a match of the rule called
// name under marker. A blank marker is the default one.
func redactReplacement(marker, name string) string {
	if marker == "" {
		marker = defaultRedactMarker
	}
	return strings.ReplaceAll(marker, "%s", name)
}

// builtinRedactPatterns are the shapes worth redacting on every request by
// default: they are unambiguous enough that a false positive is rare, and
// costly enough to leak that catching them by default is worth it. They do not
// include anything shaped like an ordinary number -- a credit-card-style regex
// has too high a false-positive rate against invoice totals, order IDs, and
// phone numbers to run unconditionally against arbitrary caller text; a caller
// who needs that coverage adds it with WithPattern, deliberately, for their
// own domain. A 40-character AWS secret access key and a "password:" label are
// left out for the same reason: neither has a shape that is not also the shape
// of a hash, an identifier or a sentence.
//
// The rules are not applied one after the other to the text the last one left.
// Every rule is looked for in the text as it came, and where two matches
// overlap the one that starts first wins, the longer of two that start together,
// and the earlier rule of two of one length: the Anthropic key is listed before
// the OpenAI key because it also satisfies the OpenAI pattern, and they match the
// same text. A private key is redacted from its BEGIN line to its END line, body
// included, or to the end of the text when there is no END line, since nothing
// says where a truncated key stops. A bearer token is redacted and the words
// around it are kept, so that a header inside JSON is still JSON.
//
// Each rule that starts on a literal says which (starts), and is looked for only
// where the literal is, so a text that holds none costs a scan for the literal
// and not a run of the expression over all of it.
var builtinRedactPatterns = []redactPattern{
	{name: "anthropic-key", pattern: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`), starts: []string{"sk-ant-"}},
	{name: "openai-key", pattern: regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}`), starts: []string{"sk-"}},
	{name: "google-api-key", pattern: regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{30,}`), starts: []string{"AIza"}},
	{name: "aws-access-key-id", pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), starts: []string{"AKIA", "ASIA"}},
	{name: "github-token", pattern: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), starts: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},
	{name: "github-fine-grained-token", pattern: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), starts: []string{"github_pat_"}},
	{name: "slack-token", pattern: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), starts: []string{"xox"}},
	{name: "stripe-key", pattern: regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{16,}`), starts: []string{"sk_live_", "rk_live_"}},
	{name: "jwt", pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), starts: []string{"eyJ"}},
	{name: "private-key-block", pattern: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----(?:.*?-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----|.*)`), starts: []string{"-----BEGIN "}},
	// The token of an authorization header, of any length, and the token of a
	// "Bearer" that stands alone, which has to be long enough not to be a word of
	// a sentence. A token does not start with a "[": what stands where it was is a
	// marker, and a marker is not a secret to redact again.
	{name: "bearer-header", pattern: regexp.MustCompile(`(?i)authorization"?\s*[:=]\s*"?bearer\s+([^\s"'\[][^\s"']*)`), starts: []string{"authorization"}, fold: true, group: 1},
	{name: "bearer-header", pattern: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{16,})`), starts: []string{"bearer"}, fold: true, group: 1},
	// Email addresses are the one PII shape common enough across ordinary
	// business text (invoices, tickets, notes) to be worth a default, and
	// precise enough that it rarely fires on anything else. The local part and the
	// domain are letters, marks and digits of any script, so an address with an
	// accented name or an internationalised domain is one. An address is looked
	// for around each "@" and nowhere else, which is why it has no starting
	// literal of its own.
	{name: "email", pattern: regexp.MustCompile(`[\p{L}\p{M}\p{N}._%+-]+@[\p{L}\p{M}\p{N}.-]+\.\p{L}{2,}`), around: '@'},
}

type redactingProvider struct {
	next  Handler
	rules []redactRule
	extra func(string) string
}

func (p *redactingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	redacted, options, err := p.redactRequest(ctx, req)
	if err != nil {
		return llm.CompletionResponse{}, err
	}
	resp, err := p.next.Complete(ctx, redacted)
	if err != nil || options == nil {
		return resp, err
	}
	resp.DecisionAnswers = options.restore(resp.DecisionAnswers)
	return resp, nil
}

// redactRequest returns req with its free text redacted, and the options of its
// decision that were rewritten, for the answer to be given back in the caller's
// words. A WithFunc function that panics is the caller's, and the text it was
// handed is not known to be scrubbed, so the request is refused: sending it on
// unredacted would be the failure this middleware exists to prevent. The
// panic's value can hold the text and is not logged.
func (p *redactingProvider) redactRequest(ctx context.Context, req llm.CompletionRequest) (out llm.CompletionRequest, options *redactedOptions, err error) {
	defer func() {
		if thrown := recover(); thrown != nil {
			logPanic(ctx, "A redaction function panicked", thrown)
			failure := types.Newf(types.KindInvariantViolation, "mw.RedactEgress", "a redaction function panicked")
			failure.Narrow = types.NarrowPanic
			out, options, err = llm.CompletionRequest{}, nil, failure
		}
	}()

	req.SystemPrompt = p.redact(req.SystemPrompt)
	req.UserPrompt = p.redact(req.UserPrompt)
	req.Decision, options = p.redactDecision(req.Decision)

	if len(req.Messages) > 0 {
		messages := make([]llm.Message, len(req.Messages))
		copy(messages, req.Messages)
		for i := range messages {
			messages[i].Content = p.redact(messages[i].Content)
			if len(messages[i].ToolCalls) > 0 {
				calls := make([]llm.ToolCall, len(messages[i].ToolCalls))
				copy(calls, messages[i].ToolCalls)
				for j := range calls {
					calls[j].Arguments = p.redactArguments(calls[j].Arguments)
				}
				messages[i].ToolCalls = calls
			}
		}
		req.Messages = messages
	}
	if len(req.Tools) > 0 {
		tools := make([]llm.Tool, len(req.Tools))
		copy(tools, req.Tools)
		for i := range tools {
			tools[i].Description = p.redact(tools[i].Description)
		}
		req.Tools = tools
	}
	return req, options, nil
}

// redactArguments redacts the string values of a tool call's arguments. JSON is
// walked as text and not decoded and written again, so what is not a string value
// is kept to the byte, and a value no rule changed is kept as it was written. A key
// is a string followed by a colon and is left alone: the keys are the schema's.
// Arguments that are not valid JSON are redacted whole as a single string.
func (p *redactingProvider) redactArguments(arguments json.RawMessage) json.RawMessage {
	if len(arguments) == 0 {
		return arguments
	}
	if !json.Valid(arguments) {
		redacted := p.redact(string(arguments))
		if redacted == string(arguments) {
			return arguments
		}
		return json.RawMessage(redacted)
	}
	// Most calls need no replacement. Keep their original bytes without making
	// a second copy, and copy each unchanged stretch only after a value changes.
	var out bytes.Buffer
	changed := false
	copyFrom := 0
	for i := 0; i < len(arguments); {
		if arguments[i] != '"' {
			i++
			continue
		}
		start := i
		end := i + 1
		for end < len(arguments) && arguments[end] != '"' {
			if arguments[end] == '\\' {
				end++
			}
			end++
		}
		literal := arguments[i : end+1]
		i = end + 1

		next := i
		for next < len(arguments) && (arguments[next] == ' ' || arguments[next] == '\t' || arguments[next] == '\n' || arguments[next] == '\r') {
			next++
		}
		var text string
		if (next < len(arguments) && arguments[next] == ':') || json.Unmarshal(literal, &text) != nil {
			continue
		}
		redacted := p.redact(text)
		if redacted == text {
			continue
		}
		if !changed {
			out.Grow(len(arguments))
			changed = true
		}
		out.Write(arguments[copyFrom:start])
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(redacted)
		out.Truncate(out.Len() - 1) // the newline Encode writes
		copyFrom = i
	}
	if !changed {
		return arguments
	}
	out.Write(arguments[copyFrom:])
	return json.RawMessage(out.Bytes())
}

// redact is text with what the rules match replaced, and then what the
// caller's function makes of it. Text no rule matches is returned as it came,
// with nothing allocated.
func (p *redactingProvider) redact(text string) string {
	if text == "" {
		return text
	}
	text = redactSpans(text, p.rules)
	if p.extra != nil {
		text = p.extra(text)
	}
	return text
}

// redactList redacts each string of a list. It returns the list itself when
// nothing in it changed, and a copy when something did; the caller's list is
// never written to. A nil list stays nil, which for a decision's criteria means
// "no descriptions" and is not the same as a list of blanks.
func (p *redactingProvider) redactList(list []string) (out []string, changed bool) {
	out = list
	for i, text := range list {
		redacted := p.redact(text)
		if redacted == text {
			continue
		}
		if !changed {
			out = append([]string(nil), list...)
			changed = true
		}
		out[i] = redacted
	}
	return out, changed
}

// redactDecision returns the decision with the text of every field redacted by
// the rules the prompts are: the state, the guidance, each question's text, each
// choice's options and each option's criteria. The typed decision says what the
// prompts say, and a decision provider reads it and not the prompts, so a
// middleware that rewrote only the prompts left the whole of what such a
// provider is sent in the clear.
//
// A decision with nothing to redact is returned as it is. Otherwise the result
// is a copy and the caller's decision, which the operation shares between calls,
// is not written to; a slice that did not change is shared, since nothing writes
// to the decision.
//
// An option the rules rewrote is what the provider chooses from, so the label
// it answers with is the rewritten text. The returned redactedOptions is what
// gives the caller its own option back.
func (p *redactingProvider) redactDecision(decision *llm.Decision) (*llm.Decision, *redactedOptions) {
	if decision == nil {
		return nil, nil
	}
	out := *decision
	out.State = p.redact(decision.State)
	out.Guidance = p.redact(decision.Guidance)
	changed := out.State != decision.State || out.Guidance != decision.Guidance

	var options *redactedOptions
	copied := false
	for i, question := range decision.Questions {
		sent := question
		sent.Ask = p.redact(question.Ask)
		var optionsChanged, criteriaChanged bool
		sent.Options, optionsChanged = p.redactList(question.Options)
		sent.Criteria, criteriaChanged = p.redactList(question.Criteria)
		if sent.Ask == question.Ask && !optionsChanged && !criteriaChanged {
			continue
		}
		if !copied {
			out.Questions = append([]llm.DecisionQuestion(nil), decision.Questions...)
			copied = true
		}
		changed = true
		if optionsChanged {
			sent.Options = distinctOptions(question.Options, sent.Options)
			if options == nil {
				options = &redactedOptions{}
			}
			options.add(i, question.Options, sent.Options)
		}
		out.Questions[i] = sent
	}
	if !changed {
		return decision, nil
	}
	return &out, options
}

// distinctOptions returns sent with any option that redaction made the same text
// as an earlier one told apart by its position. A decision provider answers with
// one of the texts it was sent, and two options that were one text could not be
// told from each other in the answer. Only the position is added, which is not
// the caller's data. Options that were the same text to begin with are left as
// one.
func distinctOptions(original, sent []string) []string {
	first := make(map[string]int, len(sent))
	out := sent
	copied := false
	for i, text := range sent {
		earlier, taken := first[text]
		if !taken {
			first[text] = i
			continue
		}
		if original[earlier] == original[i] {
			continue
		}
		candidate := text + " (option " + strconv.Itoa(i+1) + ")"
		for {
			if _, used := first[candidate]; !used {
				break
			}
			candidate += "'"
		}
		if !copied {
			out = append([]string(nil), sent...)
			copied = true
		}
		out[i] = candidate
		first[candidate] = i
	}
	return out
}

// redactedOptions remembers, for each question whose options were rewritten, the
// caller's options and the options that were sent in their place.
type redactedOptions struct {
	byQuestion map[int]rewrittenOptions
}

type rewrittenOptions struct {
	original, sent []string
}

func (r *redactedOptions) add(question int, original, sent []string) {
	if r.byQuestion == nil {
		r.byQuestion = make(map[int]rewrittenOptions)
	}
	r.byQuestion[question] = rewrittenOptions{original: original, sent: sent}
}

// restore gives each choice that was answered with an option as it was sent the
// caller's own option, by position among the options sent. The operation checks
// a label against the options its caller supplied, and a label of redacted text
// is one nobody offered. A label that is none of the options sent is left as it
// is, for that check to refuse. The slice the provider returned is not written
// to: a cache or a pair above may be holding it.
func (r *redactedOptions) restore(answers []llm.DecisionAnswer) []llm.DecisionAnswer {
	out := answers
	copied := false
	for i, answer := range answers {
		if answer.Kind != llm.DecisionChoice {
			continue
		}
		rewritten, ok := r.byQuestion[i]
		if !ok {
			continue
		}
		for n, sent := range rewritten.sent {
			if sent != answer.Label {
				continue
			}
			if rewritten.original[n] != answer.Label {
				if !copied {
					out = append([]llm.DecisionAnswer(nil), answers...)
					copied = true
				}
				out[i].Label = rewritten.original[n]
			}
			break
		}
	}
	return out
}

func (p *redactingProvider) Name() string { return p.next.Name() }

func (p *redactingProvider) EstimateCost(req llm.CompletionRequest) float64 {
	return p.next.EstimateCost(req)
}

func (p *redactingProvider) RetryPolicy() (int, time.Duration) {
	return p.next.RetryPolicy()
}
