package mw

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// redactCapturingProvider records the exact request it received, so a test
// can assert on what actually reached "the wire" rather than trusting the
// middleware's own bookkeeping.
type redactCapturingProvider struct {
	got llm.CompletionRequest
}

func (p *redactCapturingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.got = req
	return llm.CompletionResponse{Content: "ok"}, nil
}
func (p *redactCapturingProvider) Name() string                                   { return "capture" }
func (p *redactCapturingProvider) EstimateCost(req llm.CompletionRequest) float64 { return 0 }
func (p *redactCapturingProvider) RetryPolicy() (int, time.Duration)              { return 0, 0 }

func TestRedactEgressScrubsOpenAIKeyFromUserPrompt(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		UserPrompt: "Here is my key: " + key + " please use it.",
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if strings.Contains(capture.got.UserPrompt, key) {
		t.Fatalf("the key reached the provider unredacted: %q", capture.got.UserPrompt)
	}
	if !strings.Contains(capture.got.UserPrompt, "[redacted:openai-key]") {
		t.Fatalf("marker missing from redacted prompt: %q", capture.got.UserPrompt)
	}
}

func TestRedactEgressScrubsAnthropicKeyFromSystemPrompt(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	key := "sk-ant-api03-Xy7ZqW2mNb8Vc4Lk1Jh6Gf3Ds9Pa5Rt0" // secret-scan: allow
	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		SystemPrompt: "credential=" + key,
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if strings.Contains(capture.got.SystemPrompt, key) {
		t.Fatalf("the key reached the provider unredacted: %q", capture.got.SystemPrompt)
	}
	// The label says which rule fired. The OpenAI pattern also matches this key
	// and used to come first, so the rule that names it never ran.
	if capture.got.SystemPrompt != "credential=[redacted:anthropic-key]" {
		t.Fatalf("SystemPrompt = %q, want the anthropic label", capture.got.SystemPrompt)
	}
}

// A private key is redacted from its BEGIN line to its END line: the body is
// the secret, and a match on the header alone sent it on.
func TestRedactEgressScrubsAWholePrivateKeyBlock(t *testing.T) {
	const body = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\nVxTq9kQp2n0rYtb7FzXw3sLmN5dAeCj8uHgIo1RkP4yZaBvQ6lE="
	block := func(kind string) string {
		return "-----BEGIN " + kind + "-----\n" + body + "\n-----END " + kind + "-----" // secret-scan: allow
	}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"rsa", block("RSA PRIVATE KEY"), "[redacted:private-key-block]"},
		{"pkcs8", block("PRIVATE KEY"), "[redacted:private-key-block]"},
		{"ec", block("EC PRIVATE KEY"), "[redacted:private-key-block]"},
		{"openssh", block("OPENSSH PRIVATE KEY"), "[redacted:private-key-block]"},
		{"encrypted", block("ENCRYPTED PRIVATE KEY"), "[redacted:private-key-block]"},
		{"dsa", block("DSA PRIVATE KEY"), "[redacted:private-key-block]"},
		{"pgp", block("PGP PRIVATE KEY BLOCK"), "[redacted:private-key-block]"},
		{"text around it", "keep this " + block("RSA PRIVATE KEY") + " and this", "keep this [redacted:private-key-block] and this"},
		{"two blocks", block("RSA PRIVATE KEY") + "\nbetween\n" + block("EC PRIVATE KEY"), "[redacted:private-key-block]\nbetween\n[redacted:private-key-block]"},
		{"windows line endings", "-----BEGIN RSA PRIVATE KEY-----\r\n" + body + "\r\n-----END RSA PRIVATE KEY-----", "[redacted:private-key-block]"},                // secret-scan: allow
		{"a block with no end line is redacted to the end of the text", "before\n-----BEGIN RSA PRIVATE KEY-----\n" + body, "before\n[redacted:private-key-block]"}, // secret-scan: allow
		{"a public key is not a private key", "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----", "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &redactCapturingProvider{}
			wrapped := RedactEgress()(capture)
			if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: tc.input}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got := capture.got.UserPrompt; got != tc.want {
				t.Fatalf("UserPrompt = %q, want %q", got, tc.want)
			}
			if strings.Contains(capture.got.UserPrompt, "MIIEvQ") || strings.Contains(capture.got.UserPrompt, "VxTq9k") {
				t.Fatalf("key material reached the provider: %q", capture.got.UserPrompt)
			}
		})
	}
}

// The free text of a request is not only its two prompts: earlier messages and
// tool descriptions leave the process too. What the caller passed in is not
// edited.
func TestRedactEgressScrubsMessagesAndToolDescriptions(t *testing.T) {
	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	messages := []llm.Message{
		{Role: "user", Content: "my key is " + key},
		{Role: "tool", Content: "mail ops@example.com", ToolCallID: "c1"},
		{Role: "assistant", Content: "nothing secret here"},
	}
	tools := []llm.Tool{
		{Name: "lookup", Description: "uses " + key, Parameters: map[string]any{"type": "object"}},
		{Name: "plain", Description: "no secret"},
	}
	req := llm.CompletionRequest{UserPrompt: "u", Messages: messages, Tools: tools}
	if _, err := wrapped.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	sent := capture.got
	if sent.Messages[0].Content != "my key is [redacted:openai-key]" || sent.Messages[1].Content != "mail [redacted:email]" || sent.Messages[2].Content != "nothing secret here" {
		t.Fatalf("messages sent = %+v", sent.Messages)
	}
	if sent.Messages[1].Role != "tool" || sent.Messages[1].ToolCallID != "c1" {
		t.Fatalf("a message's identifiers were changed: %+v", sent.Messages[1])
	}
	if sent.Tools[0].Description != "uses [redacted:openai-key]" || sent.Tools[1].Description != "no secret" || sent.Tools[0].Name != "lookup" {
		t.Fatalf("tools sent = %+v", sent.Tools)
	}
	if sent.Tools[0].Parameters["type"] != "object" {
		t.Fatalf("a tool's parameters were lost: %+v", sent.Tools[0].Parameters)
	}
	// The caller's own slices still hold what the caller wrote.
	if !strings.Contains(messages[0].Content, key) || !strings.Contains(tools[0].Description, key) {
		t.Fatalf("the caller's request was edited in place: %+v %+v", messages, tools)
	}
}

// A WithFunc function that panics is the caller's, and the text it was given is
// not known to be scrubbed: the request is refused and never sent.
func TestRedactEgressRefusesARequestWhenAFuncPanics(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress(WithFunc(func(string) string { panic("redactor bug with " + "the text") }))(capture)

	resp, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: "private text"})
	if types.KindOf(err) != types.KindInvariantViolation {
		t.Fatalf("err = %v, want an invariant violation", err)
	}
	if types.NarrowingOf(err) != types.NarrowPanic {
		t.Fatalf("err narrows to %v, want a recovered panic", types.NarrowingOf(err))
	}
	if strings.Contains(err.Error(), "private text") || strings.Contains(err.Error(), "the text") {
		t.Fatalf("the error holds text: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("content %q came back with the failure", resp.Content)
	}
	if capture.got.UserPrompt != "" {
		t.Fatalf("the provider was sent %q", capture.got.UserPrompt)
	}
}

// A marker is plain text with the rule's name put in: nothing else in it is
// interpreted, whatever it contains.
func TestRedactEgressMarkersAreLiteral(t *testing.T) {
	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	cases := []struct{ marker, want string }{
		{"[x] 100%", "[x] 100%"},
		{"%s %s", "openai-key openai-key"},
		{"%s 100%", "openai-key 100%"},
		{"$1", "$1"},
		{"${0}%s", "${0}openai-key"},
		{"<%s>", "<openai-key>"},
	}
	for _, tc := range cases {
		t.Run(tc.marker, func(t *testing.T) {
			capture := &redactCapturingProvider{}
			wrapped := RedactEgress(WithMarker(tc.marker))(capture)
			if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: key}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if capture.got.UserPrompt != tc.want {
				t.Fatalf("UserPrompt = %q, want %q", capture.got.UserPrompt, tc.want)
			}
		})
	}
}

func TestRedactEgressScrubsEmailAddress(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		UserPrompt: "Contact jane.doe@example.com about the invoice.",
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if strings.Contains(capture.got.UserPrompt, "jane.doe@example.com") {
		t.Fatalf("email reached the provider unredacted: %q", capture.got.UserPrompt)
	}
	if !strings.Contains(capture.got.UserPrompt, "Contact") || !strings.Contains(capture.got.UserPrompt, "about the invoice.") {
		t.Fatalf("surrounding text was damaged: %q", capture.got.UserPrompt)
	}
}

// TestRedactEgressLeavesOrdinaryTextUntouched is the guard the task calls
// out by name: redaction that eats surrounding text makes the request
// useless. Ordinary prose with no secret shape in it must survive
// byte-for-byte.
func TestRedactEgressLeavesOrdinaryTextUntouched(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	prose := "Invoice #48213 for 3 units of SKU-9981, due on the 30th. " +
		"Customer reference: ORD-2026-000451. Total: 1,204.50 USD. " +
		"Please confirm receipt and let us know if the shipping address changed."
	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		SystemPrompt: "You are an invoicing assistant.",
		UserPrompt:   prose,
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if capture.got.UserPrompt != prose {
		t.Fatalf("ordinary text was altered:\n got:  %q\n want: %q", capture.got.UserPrompt, prose)
	}
	if capture.got.SystemPrompt != "You are an invoicing assistant." {
		t.Fatalf("system prompt was altered: %q", capture.got.SystemPrompt)
	}
}

func TestRedactEgressEmptyPromptsStayEmpty(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress()(capture)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if capture.got.SystemPrompt != "" || capture.got.UserPrompt != "" {
		t.Fatalf("empty prompts were not empty after redaction: system=%q user=%q",
			capture.got.SystemPrompt, capture.got.UserPrompt)
	}
}

func TestRedactEgressWithoutBuiltinsLeavesKnownKeyShapesAlone(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress(WithoutBuiltins())(capture)

	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: key}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if capture.got.UserPrompt != key {
		t.Fatalf("WithoutBuiltins() still redacted a built-in pattern: %q", capture.got.UserPrompt)
	}
}

func TestRedactEgressWithPatternAddsACallerRule(t *testing.T) {
	capture := &redactCapturingProvider{}
	ssn := regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	wrapped := RedactEgress(WithPattern("ssn", ssn))(capture)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		UserPrompt: "SSN on file: 123-45-6789 for this applicant.",
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if strings.Contains(capture.got.UserPrompt, "123-45-6789") {
		t.Fatalf("caller-supplied pattern did not redact: %q", capture.got.UserPrompt)
	}
	if !strings.Contains(capture.got.UserPrompt, "[redacted:ssn]") {
		t.Fatalf("marker missing for caller-supplied pattern: %q", capture.got.UserPrompt)
	}
}

func TestRedactEgressWithMarkerOverridesTheDefault(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress(WithMarker("<<gone>>"))(capture)

	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{
		UserPrompt: "key: sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345", // secret-scan: allow
	})
	if err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if !strings.Contains(capture.got.UserPrompt, "<<gone>>") {
		t.Fatalf("custom marker not applied: %q", capture.got.UserPrompt)
	}
	if strings.Contains(capture.got.UserPrompt, "[redacted") {
		t.Fatalf("default marker leaked through a custom marker: %q", capture.got.UserPrompt)
	}
}

func TestRedactEgressWithFuncRunsAfterPatterns(t *testing.T) {
	capture := &redactCapturingProvider{}
	upper := func(s string) string { return strings.ToUpper(s) }
	wrapped := RedactEgress(WithoutBuiltins(), WithFunc(upper))(capture)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: "hello"}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if capture.got.UserPrompt != "HELLO" {
		t.Fatalf("UserPrompt = %q, want %q", capture.got.UserPrompt, "HELLO")
	}
}

// TestRedactEgressThroughChainWithScriptedProvider is the composition case:
// RedactEgress wired through mw.Chain alongside Retry, so the seam is
// exercised, not just the bare middleware function -- and a retried call
// must still be redacted on every attempt, not just the first.
func TestRedactEgressThroughChainWithScriptedProvider(t *testing.T) {
	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	seen := []string{}
	var capture llm.Provider = completeFunc(func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		seen = append(seen, req.UserPrompt)
		if len(seen) == 1 {
			return llm.CompletionResponse{}, llm.NewAPIError("fake", "m", 503, "")
		}
		return llm.CompletionResponse{Content: "ok"}, nil
	})

	fc := newFakeClock(time.Unix(0, 0))
	retrier := Retry(WithMaxAttempts(2), WithBaseDelay(time.Millisecond))
	retried := retrier(capture)
	rp := retried.(*retryProvider)
	rp.sleep = fc.sleep

	wrapped := RedactEgress()(retried)

	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{UserPrompt: key}); err != nil {
		t.Fatalf("Complete returned an error: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("provider saw %d calls, want 2 (one failure, one retry)", len(seen))
	}
	for i, prompt := range seen {
		if strings.Contains(prompt, key) {
			t.Fatalf("attempt %d reached the provider unredacted: %q", i, prompt)
		}
	}
}

// A nil pattern passed to WithPattern is a no-op rather than a panic later
// when the pattern is used -- the caller made a mistake (e.g. a regexp that
// failed to compile and was passed anyway), and silently ignoring the rule
// they meant to add is safer than crashing the request path over it.
func TestWithPatternIgnoresANilRegexp(t *testing.T) {
	cfg := redactConfig{}
	WithPattern("broken", nil)(&cfg)
	if len(cfg.patterns) != 0 {
		t.Errorf("patterns = %+v, want none added for a nil regexp", cfg.patterns)
	}

	WithPattern("real", regexp.MustCompile(`x`))(&cfg)
	if len(cfg.patterns) != 1 {
		t.Errorf("patterns = %+v, want exactly one after a real pattern followed the nil one", cfg.patterns)
	}
}

func TestRedactReplacement(t *testing.T) {
	cases := []struct {
		name, marker, rule, want string
	}{
		{"the default marker names the rule", "", "email", "[redacted:email]"},
		{"a template", "[gone:%s]", "email", "[gone:email]"},
		{"a flat literal", "<<gone>>", "email", "<<gone>>"},
		{"every verb is replaced", "%s and %s", "ssn", "ssn and ssn"},
		{"a bare percent is written as it is", "[x] 100%", "ssn", "[x] 100%"},
		{"a percent after the verb is written as it is", "%s 100%", "ssn", "ssn 100%"},
		{"another verb is not interpreted", "%d %v %s", "ssn", "%d %v ssn"},
		{"a dollar sign is not a group reference", "$1 ${name} %s", "ssn", "$1 ${name} ssn"},
		{"an empty rule name", "[redacted:%s]", "", "[redacted:]"},
		{"a name holding a verb", "[%s]", "a%sb", "[a%sb]"},
		{"a marker that is only the verb", "%s", "email", "email"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactReplacement(tc.marker, tc.rule); got != tc.want {
				t.Fatalf("redactReplacement(%q, %q) = %q, want %q", tc.marker, tc.rule, got, tc.want)
			}
		})
	}
}

// The three secrets the decision tests plant. The email and the key are caught
// by the built-in rules; a phone number is not, and a deployment that wants it
// redacted adds a rule, as redactionTestPhoneRule does.
const (
	redactionTestEmail = "ada.lovelace@example.com"
	redactionTestPhone = "+1 (555) 010-4477"
	redactionTestKey   = "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
)

func redactionTestPhoneRule() RedactOption {
	return WithPattern("phone", regexp.MustCompile(`\+1 \(\d{3}\) \d{3}-\d{4}`))
}

// redactionTestDecision is a decision with a secret in every field that holds
// the caller's text.
func redactionTestDecision() *llm.Decision {
	return &llm.Decision{
		State:    `{"customer":"Ada","email":"` + redactionTestEmail + `","phone":"` + redactionTestPhone + `","note":"key ` + redactionTestKey + `"}`,
		Guidance: "Treat mail from " + redactionTestEmail + " as urgent, and call " + redactionTestPhone + ".",
		Questions: []llm.DecisionQuestion{
			{Kind: llm.DecisionYesNo, Ask: "Did " + redactionTestEmail + " write in about " + redactionTestKey + "?"},
			{
				Kind:     llm.DecisionChoice,
				Ask:      "Which contact does this concern?",
				Options:  []string{redactionTestPhone, redactionTestEmail, "someone else"},
				Criteria: []string{"the number " + redactionTestPhone, "key " + redactionTestKey, ""},
			},
			{Kind: llm.DecisionScale, Ask: "How urgent is " + redactionTestPhone + "?", Low: 1, High: 5},
		},
	}
}

// decisionText is every string a decision holds, joined, for a test to look
// for a secret in.
func decisionText(decision *llm.Decision) string {
	if decision == nil {
		return ""
	}
	parts := []string{decision.State, decision.Guidance}
	for _, question := range decision.Questions {
		parts = append(parts, question.Ask)
		parts = append(parts, question.Options...)
		parts = append(parts, question.Criteria...)
	}
	return strings.Join(parts, "\n")
}

func redactionTestRequest(decision *llm.Decision) llm.CompletionRequest {
	return llm.CompletionRequest{
		Model:        "gpt-6-luna",
		SystemPrompt: "You answer questions about a subject.",
		UserPrompt:   "Subject: " + redactionTestEmail,
		Decision:     decision,
	}
}

// redactionTestRecorder answers a decision with the same yes and records what it
// was sent.
func redactionTestRecorder(name string) *scripted {
	return &scripted{name: name, model: name + "-model", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Provider: name, Model: name + "-model", Content: `{"q1":true}`, DecisionAnswers: answersTo(req.Decision, 0.9)}, nil
	}}
}

func TestRedactEgressRedactsEveryTextFieldOfADecision(t *testing.T) {
	recorder := redactionTestRecorder("decider")
	wrapped := RedactEgress(redactionTestPhoneRule())(recorder)

	if _, err := wrapped.Complete(context.Background(), redactionTestRequest(redactionTestDecision())); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := recorder.requests[0].Decision
	text := decisionText(got)
	for name, secret := range map[string]string{"email": redactionTestEmail, "phone": redactionTestPhone, "key": redactionTestKey} {
		if strings.Contains(text, secret) {
			t.Errorf("the %s reached the provider in the decision:\n%s", name, text)
		}
	}

	// Each field was redacted by the rules and not dropped: the marker stands where
	// the secret was, and the text around it is the caller's.
	checks := []struct {
		field, got, want string
	}{
		{"state", got.State, `"email":"[redacted:email]","phone":"[redacted:phone]","note":"key [redacted:openai-key]"`},
		{"guidance", got.Guidance, "Treat mail from [redacted:email] as urgent, and call [redacted:phone]."},
		{"yes or no", got.Questions[0].Ask, "Did [redacted:email] write in about [redacted:openai-key]?"},
		{"choice", got.Questions[1].Ask, "Which contact does this concern?"},
		{"option", got.Questions[1].Options[0], "[redacted:phone]"},
		{"option", got.Questions[1].Options[1], "[redacted:email]"},
		{"option", got.Questions[1].Options[2], "someone else"},
		{"criteria", got.Questions[1].Criteria[0], "the number [redacted:phone]"},
		{"criteria", got.Questions[1].Criteria[1], "key [redacted:openai-key]"},
		{"criteria", got.Questions[1].Criteria[2], ""},
		{"scale", got.Questions[2].Ask, "How urgent is [redacted:phone]?"},
	}
	for _, check := range checks {
		if check.got != check.want && !(check.field == "state" && strings.Contains(check.got, check.want)) {
			t.Errorf("the %s is %q, want %q", check.field, check.got, check.want)
		}
	}
	if recorder.requests[0].UserPrompt != "Subject: [redacted:email]" {
		t.Errorf("the prompt is %q: the decision's redaction must not replace the prompts'", recorder.requests[0].UserPrompt)
	}
}

func TestRedactEgressLeavesTheCallersDecisionAndTheShapeOfTheSentOneAlone(t *testing.T) {
	recorder := redactionTestRecorder("decider")
	original := redactionTestDecision()
	wrapped := RedactEgress(redactionTestPhoneRule())(recorder)

	if _, err := wrapped.Complete(context.Background(), redactionTestRequest(original)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !reflect.DeepEqual(original, redactionTestDecision()) {
		t.Errorf("the caller's decision was changed:\n%+v", original)
	}
	sent := recorder.requests[0].Decision
	if sent == original || &sent.Questions[0] == &original.Questions[0] ||
		&sent.Questions[1].Options[0] == &original.Questions[1].Options[0] ||
		&sent.Questions[1].Criteria[0] == &original.Questions[1].Criteria[0] {
		t.Error("the sent decision shares memory with the caller's")
	}
	for i, question := range sent.Questions {
		want := original.Questions[i]
		if question.Kind != want.Kind || question.Low != want.Low || question.High != want.High ||
			len(question.Options) != len(want.Options) || len(question.Criteria) != len(want.Criteria) {
			t.Errorf("question %d changed shape: %+v, was %+v", i+1, question, want)
		}
	}
}

func TestRedactEgressKeepsNilCriteriaNilAndPassesACleanDecisionOn(t *testing.T) {
	cases := []struct {
		name     string
		decision *llm.Decision
	}{
		{"a choice with no criteria", &llm.Decision{State: `"clean"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{"a", "b"}}}}},
		{"a yes or no", &llm.Decision{State: `"clean"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "Is it?"}}}},
		{"a scale", &llm.Decision{State: `"clean"`, Guidance: "Be strict.", Questions: []llm.DecisionQuestion{{Kind: llm.DecisionScale, Ask: "How much?", Low: 0, High: 10}}}},
		{"no questions", &llm.Decision{State: `"clean"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := redactionTestRecorder("decider")
			if _, err := RedactEgress()(recorder).Complete(context.Background(), redactionTestRequest(tc.decision)); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			sent := recorder.requests[0].Decision
			if sent != tc.decision {
				t.Errorf("a decision with nothing to redact was copied: it is the caller's own, and a copy is an allocation for nothing")
			}
			if len(tc.decision.Questions) > 0 && (sent.Questions[0].Criteria != nil) != (tc.decision.Questions[0].Criteria != nil) {
				t.Error("nil criteria did not stay nil")
			}
		})
	}

	t.Run("nil criteria stay nil when another field is redacted", func(t *testing.T) {
		recorder := redactionTestRecorder("decider")
		decision := &llm.Decision{State: `"` + redactionTestEmail + `"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{"a", "b"}}}}
		if _, err := RedactEgress()(recorder).Complete(context.Background(), redactionTestRequest(decision)); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if sent := recorder.requests[0].Decision; sent.Questions[0].Criteria != nil {
			t.Errorf("criteria %#v, want nil", sent.Questions[0].Criteria)
		}
	})
}

// A chosen label is one of the options the CALLER supplied. When redaction
// rewrote an option on the way out, the provider chose the rewritten text, and
// the caller gets its own option back: the operation compares the label with the
// options it holds, and a label it never offered is a refused answer.
func TestRedactEgressGivesTheCallerBackItsOwnLabel(t *testing.T) {
	options := []string{redactionTestPhone, redactionTestEmail, "someone else", "key " + redactionTestKey}
	cases := []struct {
		name   string
		choose int
	}{
		{"an option that was a phone number", 0},
		{"an option that was an email", 1},
		{"an option nothing was redacted in", 2},
		{"an option that held a key", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider := &scripted{name: "decider", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
				sent := req.Decision.Questions[0].Options
				return llm.CompletionResponse{Provider: "decider", DecisionAnswers: []llm.DecisionAnswer{
					{Kind: llm.DecisionChoice, Label: sent[tc.choose], ModelConfidence: 0.8, ConfidenceReported: true},
				}}, nil
			}}
			decision := &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: options}}}
			resp, err := RedactEgress(redactionTestPhoneRule())(decider).Complete(context.Background(), redactionTestRequest(decision))
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			want := llm.DecisionAnswer{Kind: llm.DecisionChoice, Label: options[tc.choose], ModelConfidence: 0.8, ConfidenceReported: true}
			if len(resp.DecisionAnswers) != 1 || resp.DecisionAnswers[0] != want {
				t.Fatalf("the answer is %+v, want %+v", resp.DecisionAnswers, want)
			}
			// The answer reads back through the library's own reader: the label is
			// one of the options the caller supplied.
			if _, err := llm.RenderDecisionAnswers(decision, resp.DecisionAnswers); err != nil {
				t.Errorf("the answer is not an answer to the caller's question: %v", err)
			}
			sent := decider.requests[0].Decision.Questions[0].Options
			for _, option := range sent {
				for _, secret := range []string{redactionTestPhone, redactionTestEmail, redactionTestKey} {
					if strings.Contains(option, secret) {
						t.Errorf("option %q reached the provider", option)
					}
				}
			}
		})
	}
}

// Two options that differ only in what redaction removes would reach the provider
// as one text, and the label it chose could not be told from the other. They are
// sent apart, by position, and the answer is matched back by position.
func TestRedactEgressTellsApartOptionsThatRedactToTheSameText(t *testing.T) {
	cases := []struct {
		name    string
		options []string
		choose  int
	}{
		{"two emails, the first", []string{"ada@example.com", "grace@example.com", "plain"}, 0},
		{"two emails, the second", []string{"ada@example.com", "grace@example.com", "plain"}, 1},
		{"two emails and a plain one chosen", []string{"ada@example.com", "grace@example.com", "plain"}, 2},
		{"an option that is already the marker", []string{"[redacted:email]", "ada@example.com"}, 0},
		{"the marker's twin", []string{"[redacted:email]", "ada@example.com"}, 1},
		{"three of one kind", []string{"a@example.com", "b@example.com", "c@example.com"}, 2},
		{"a repeated option stays one text", []string{"same", "same", "other"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decider := &scripted{name: "decider", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
				sent := req.Decision.Questions[0].Options
				for i := range sent {
					for j := 0; j < i; j++ {
						if sent[i] == sent[j] && tc.options[i] != tc.options[j] {
							t.Errorf("options %d and %d reached the provider as the same text %q", j+1, i+1, sent[i])
						}
					}
				}
				return llm.CompletionResponse{Provider: "decider", DecisionAnswers: []llm.DecisionAnswer{{Kind: llm.DecisionChoice, Label: sent[tc.choose]}}}, nil
			}}
			decision := &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: tc.options}}}
			resp, err := RedactEgress()(decider).Complete(context.Background(), redactionTestRequest(decision))
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if got := resp.DecisionAnswers[0].Label; got != tc.options[tc.choose] {
				t.Errorf("the label is %q, want the caller's option %q", got, tc.options[tc.choose])
			}
			for _, option := range decider.requests[0].Decision.Questions[0].Options {
				if strings.Contains(option, "@example.com") {
					t.Errorf("option %q reached the provider unredacted", option)
				}
			}
		})
	}
}

func TestRedactEgressLeavesAnswersItHasNothingToRestoreInAlone(t *testing.T) {
	escalation := &llm.DecisionEscalation{Provider: "jev", Model: "jev-latest", Reason: llm.DecisionEscalatedBelowFloor, ModelConfidence: 0.4, ConfidenceReported: true}
	typed := []llm.DecisionAnswer{
		yes(0.9),
		{Kind: llm.DecisionChoice, Label: "[redacted:email]", ModelConfidence: 0.7, ConfidenceReported: true},
		{Kind: llm.DecisionScale, Level: 3},
	}
	decision := &llm.Decision{State: `"` + redactionTestEmail + `"`, Questions: []llm.DecisionQuestion{
		{Kind: llm.DecisionYesNo, Ask: "Is it?"},
		{Kind: llm.DecisionChoice, Ask: "Which?", Options: []string{redactionTestEmail, "other"}},
		{Kind: llm.DecisionScale, Ask: "How much?", Low: 1, High: 5},
	}}
	provider := &scripted{name: "decider", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Provider: "decider", Model: "m", DecisionAnswers: typed, DecisionEscalation: escalation, Content: "kept"}, nil
	}}
	snapshot := append([]llm.DecisionAnswer(nil), typed...)

	resp, err := RedactEgress()(provider).Complete(context.Background(), redactionTestRequest(decision))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.DecisionEscalation != escalation || resp.Content != "kept" || resp.Provider != "decider" || resp.Model != "m" {
		t.Errorf("the rest of the response changed: %+v", resp)
	}
	if resp.DecisionAnswers[0] != typed[0] || resp.DecisionAnswers[2] != typed[2] {
		t.Errorf("an answer with no label changed: %+v", resp.DecisionAnswers)
	}
	if resp.DecisionAnswers[1].Label != redactionTestEmail {
		t.Errorf("the label is %q, want the caller's option", resp.DecisionAnswers[1].Label)
	}
	// The provider's own slice is not written to: a cache or a pair above may hold it.
	if !reflect.DeepEqual(typed, snapshot) {
		t.Errorf("the provider's answers were changed in place: %+v", typed)
	}

	t.Run("a label that was never offered is left for the reader to refuse", func(t *testing.T) {
		stray := &scripted{name: "decider", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{DecisionAnswers: []llm.DecisionAnswer{yes(0.5), {Kind: llm.DecisionChoice, Label: "invented"}, {Kind: llm.DecisionScale, Level: 2}}}, nil
		}}
		resp, err := RedactEgress()(stray).Complete(context.Background(), redactionTestRequest(decision))
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if resp.DecisionAnswers[1].Label != "invented" {
			t.Errorf("the label is %q: a label nobody offered must not be turned into one", resp.DecisionAnswers[1].Label)
		}
		if _, err := llm.RenderDecisionAnswers(decision, resp.DecisionAnswers); err == nil {
			t.Error("the reader accepted a label nobody offered")
		}
	})
}

// A chain that redacts in front of the pair sends neither half what it must not.
func TestRedactEgressInFrontOfTheDecisionsPairRedactsForBothHalves(t *testing.T) {
	secrets := []string{redactionTestEmail, redactionTestPhone, redactionTestKey}
	requestText := func(req llm.CompletionRequest) string {
		return req.SystemPrompt + "\n" + req.UserPrompt + "\n" + decisionText(req.Decision)
	}
	request := func() llm.CompletionRequest {
		req := redactionTestRequest(redactionTestDecision())
		req.UserPrompt = "Subject: " + redactionTestEmail + " " + redactionTestPhone + " " + redactionTestKey
		return req
	}
	assertClean := func(t *testing.T, sinks ...*scripted) {
		t.Helper()
		for _, sink := range sinks {
			if len(sink.requests) == 0 {
				t.Fatalf("the %s was sent nothing", sink.name)
			}
			for _, secret := range secrets {
				if strings.Contains(requestText(sink.requests[0]), secret) {
					t.Errorf("%q reached the %s", secret, sink.name)
				}
			}
		}
	}

	t.Run("the decider alone answers", func(t *testing.T) {
		generator, decider := generatorAnswering(generatorJSON), redactionTestRecorder("jev")
		pair := RedactEgress(redactionTestPhoneRule())(Decisions(generator, decider, DecisionOptions{}))
		if _, err := pair.Complete(context.Background(), request()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if generator.calls() != 0 || decider.calls() != 1 {
			t.Fatalf("%d calls to the generator and %d to the decider", generator.calls(), decider.calls())
		}
		assertClean(t, decider)
	})

	t.Run("a fallback to the generator sends it the same redacted request", func(t *testing.T) {
		generator := generatorAnswering(generatorJSON)
		decider := deciderAnswering(yes(0.3))
		pair := RedactEgress(redactionTestPhoneRule())(Decisions(generator, decider, DecisionOptions{Floor: 0.9}))
		resp, err := pair.Complete(context.Background(), request())
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if generator.calls() != 1 || decider.calls() != 1 || resp.DecisionEscalation == nil {
			t.Fatalf("%d calls to the generator and %d to the decider, escalation %+v", generator.calls(), decider.calls(), resp.DecisionEscalation)
		}
		assertClean(t, generator, decider)
		if !reflect.DeepEqual(generator.requests[0], decider.requests[0]) {
			t.Error("the generator was not sent the request the decider was sent")
		}
	})

	t.Run("a decider that fails in transit falls to a generator that is sent nothing unredacted", func(t *testing.T) {
		generator := generatorAnswering(generatorJSON)
		decider := deciderFailing(llm.NewAPIError("jev", "jev-latest", 503, ""))
		pair := RedactEgress(redactionTestPhoneRule())(Decisions(generator, decider, DecisionOptions{}))
		if _, err := pair.Complete(context.Background(), request()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		assertClean(t, generator, decider)
	})

	t.Run("a decider that refuses the decision as one it cannot take falls to a generator sent nothing unredacted", func(t *testing.T) {
		generator := generatorAnswering(generatorJSON)
		decider := deciderFailing(&types.OperationError{Kind: types.KindUnsupportedCapability, Op: "llm.decide"})
		pair := RedactEgress(redactionTestPhoneRule())(Decisions(generator, decider, DecisionOptions{}))
		if _, err := pair.Complete(context.Background(), request()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		assertClean(t, generator, decider)
	})

	t.Run("the label the pair's decider chose is the caller's", func(t *testing.T) {
		options := []string{redactionTestEmail, "other"}
		decision := &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: options}}}
		decider := &scripted{name: "jev", model: "jev-latest", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: []llm.DecisionAnswer{
				{Kind: llm.DecisionChoice, Label: req.Decision.Questions[0].Options[0], ModelConfidence: 0.99, ConfidenceReported: true},
			}}, nil
		}}
		pair := RedactEgress()(Decisions(generatorAnswering(generatorJSON), decider, DecisionOptions{Floor: 0.5}))
		resp, err := pair.Complete(context.Background(), redactionTestRequest(decision))
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if resp.DecisionAnswers[0].Label != redactionTestEmail {
			t.Errorf("the label is %q, want the caller's own option", resp.DecisionAnswers[0].Label)
		}
	})
}

func TestRedactEgressRefusesADecisionWhenAFuncPanicsInIt(t *testing.T) {
	// Panics only on the decision's text, so the prompts pass and the refusal is
	// the decision's.
	capture := &scripted{name: "decider", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{Content: "ok"}, nil
	}}
	wrapped := RedactEgress(WithFunc(func(text string) string {
		if strings.Contains(text, "decision-secret") {
			panic("redactor bug with " + text)
		}
		return text
	}))(capture)
	req := llm.CompletionRequest{UserPrompt: "fine", Decision: &llm.Decision{State: `"decision-secret"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionYesNo, Ask: "Is it?"}}}}

	resp, err := wrapped.Complete(context.Background(), req)
	if types.KindOf(err) != types.KindInvariantViolation || types.NarrowingOf(err) != types.NarrowPanic {
		t.Fatalf("err = %v (kind %v, narrow %v), want a recovered panic", err, types.KindOf(err), types.NarrowingOf(err))
	}
	if strings.Contains(err.Error(), "decision-secret") {
		t.Errorf("the error holds the text: %v", err)
	}
	if capture.calls() != 0 || resp.Content != "" {
		t.Errorf("the provider was called %d times, content %q", capture.calls(), resp.Content)
	}
}

// A cache either side of the redactor keeps a typed answer, and a hit carries the
// caller's own label: inside, the entry holds the label as it was sent and the
// redactor restores it on every call; outside, the entry holds the restored one.
func TestRedactEgressAndCacheInEitherOrderKeepTheCallersLabel(t *testing.T) {
	orders := map[string]func(llm.Provider) llm.Provider{
		"the cache inside":  func(p llm.Provider) llm.Provider { return Chain(p, RedactEgress(), Cache()) },
		"the cache outside": func(p llm.Provider) llm.Provider { return Chain(p, Cache(), RedactEgress()) },
	}
	for name, build := range orders {
		t.Run(name, func(t *testing.T) {
			decider := &scripted{name: "jev", answer: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
				return llm.CompletionResponse{Provider: "jev", Model: "jev-1.13.0", DecisionAnswers: []llm.DecisionAnswer{
					{Kind: llm.DecisionChoice, Label: req.Decision.Questions[0].Options[1], ModelConfidence: 0.9, ConfidenceReported: true},
				}}, nil
			}}
			options := []string{"plain", redactionTestEmail}
			decision := &llm.Decision{State: `"s"`, Questions: []llm.DecisionQuestion{{Kind: llm.DecisionChoice, Ask: "Which?", Options: options}}}
			chain := build(decider)

			for call := 1; call <= 3; call++ {
				resp, err := chain.Complete(context.Background(), redactionTestRequest(decision))
				if err != nil {
					t.Fatalf("call %d: %v", call, err)
				}
				if got := resp.DecisionAnswers[0].Label; got != redactionTestEmail {
					t.Errorf("call %d: the label is %q, want the caller's option", call, got)
				}
			}
			if decider.calls() != 1 {
				t.Errorf("the provider was called %d times, want 1: the answer was kept", decider.calls())
			}
			if strings.Contains(decisionText(decider.requests[0].Decision), redactionTestEmail) {
				t.Error("the email reached the provider")
			}
		})
	}
}

// An earlier tool call of the model's carries caller data too: the arguments of
// a call to a lookup tool are the email address or the key the caller's own text
// held, and they go back to the provider with the conversation. Each string value
// of the arguments is redacted by the rules the prompts are, and the shape of the
// object is kept.
func TestRedactEgressRedactsTheArgumentsOfEarlierToolCalls(t *testing.T) {
	key := "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345" // secret-scan: allow
	const email = "ada@example.com"

	cases := []struct {
		name      string
		arguments string
		want      string // "" when the arguments are to be sent as they are
	}{
		{"a value", `{"to":"` + email + `"}`, `{"to":"[redacted:email]"}`},
		{"a value among others", `{"n":3,"to":"` + email + `","ok":true,"none":null}`, `{"n":3,"to":"[redacted:email]","ok":true,"none":null}`},
		{"a nested object", `{"a":{"b":{"c":"` + email + `"}}}`, `{"a":{"b":{"c":"[redacted:email]"}}}`},
		{"an array of values", `{"to":["x","` + email + `",7]}`, `{"to":["x","[redacted:email]",7]}`},
		{"an array of objects", `{"rows":[{"k":"` + key + `"},{"k":"fine"}]}`, `{"rows":[{"k":"[redacted:openai-key]"},{"k":"fine"}]}`},
		{"two secrets in one value", `{"note":"` + email + ` and ` + key + `"}`, `{"note":"[redacted:email] and [redacted:openai-key]"}`},
		{"a key is not a value", `{"` + email + `":"x"}`, ""},
		{"a key that is a secret and its value", `{"` + email + `":"` + email + `"}`, `{"` + email + `":"[redacted:email]"}`},
		{"white space is kept", "{ \"to\" : \"" + email + "\" ,\n \"n\" : 1 }", "{ \"to\" : \"[redacted:email]\" ,\n \"n\" : 1 }"},
		{"key order is kept", `{"z":"` + email + `","a":"` + email + `"}`, `{"z":"[redacted:email]","a":"[redacted:email]"}`},
		{"a number is not text", `{"phone":5550104477}`, ""},
		{"nothing to redact", `{"city":"Oslo","days":3}`, ""},
		{"an empty object", `{}`, ""},
		{"escapes are kept as the text they stand for", `{"note":"say \"hi\" to ` + email + `\nthanks"}`, `{"note":"say \"hi\" to [redacted:email]\nthanks"}`},
		{"a multi-byte character", `{"note":"caf` + "é" + ` ` + email + `"}`, `{"note":"caf` + "é" + ` [redacted:email]"}`},
		{"markup is not escaped", `{"note":"<b>` + email + `</b> & more"}`, `{"note":"<b>[redacted:email]</b> & more"}`},
		{"arguments that are not JSON are redacted as text", `to=` + email + ` {`, `to=[redacted:email] {`},
		{"arguments that are cut off", `{"to":"` + email, `{"to":"[redacted:email]`},
		{"arguments that are a bare string", `"` + email + `"`, `"[redacted:email]"`},
		{"empty arguments", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &redactCapturingProvider{}
			arguments := json.RawMessage(tc.arguments)
			original := append(json.RawMessage(nil), arguments...)
			messages := []llm.Message{
				{Role: "user", Content: "mail " + email},
				{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "send_mail", Arguments: arguments}}},
				{Role: "tool", Content: "sent to " + email, ToolCallID: "call_1"},
			}
			if _, err := RedactEgress()(capture).Complete(context.Background(), llm.CompletionRequest{Model: "m", Messages: messages}); err != nil {
				t.Fatalf("Complete: %v", err)
			}

			sent := capture.got.Messages
			want := tc.want
			if want == "" {
				want = tc.arguments
			}
			if got := string(sent[1].ToolCalls[0].Arguments); got != want {
				t.Errorf("the arguments sent\n got %s\nwant %s", got, want)
			}
			if tc.want != "" && tc.arguments != "" && json.Valid([]byte(tc.arguments)) && !json.Valid(sent[1].ToolCalls[0].Arguments) {
				t.Errorf("the arguments sent are no longer JSON: %s", sent[1].ToolCalls[0].Arguments)
			}
			if call := sent[1].ToolCalls[0]; call.ID != "call_1" || call.Name != "send_mail" {
				t.Errorf("a call's identifiers were changed: %+v", call)
			}
			// What the same rules do to the turns around it is as before.
			if sent[0].Content != "mail [redacted:email]" || sent[2].Content != "sent to [redacted:email]" || sent[2].ToolCallID != "call_1" {
				t.Errorf("the turns around the call were not redacted as before: %+v", sent)
			}
			// The caller's own request is not edited.
			if string(messages[1].ToolCalls[0].Arguments) != string(original) || !strings.Contains(messages[0].Content, email) {
				t.Errorf("the caller's messages were edited in place: %+v", messages)
			}
		})
	}
}

// The rules the caller added apply to the arguments of a call as they do to a
// prompt, and so does the marker, and a function.
func TestRedactEgressAppliesTheCallersOwnRulesToToolCallArguments(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress(redactionTestPhoneRule(), WithMarker("<%s>"), WithFunc(strings.ToUpper))(capture)
	messages := []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "dial", Arguments: json.RawMessage(`{"number":"call ` + redactionTestPhone + ` now"}`)}}}}
	if _, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if got, want := string(capture.got.Messages[0].ToolCalls[0].Arguments), `{"number":"CALL <PHONE> NOW"}`; got != want {
		t.Errorf("arguments = %s, want %s", got, want)
	}
}

// A WithFunc function that panics on an argument is the caller's, and the text it
// was handed is not known to be scrubbed: the request is refused, as it is for a
// prompt.
func TestRedactEgressRefusesARequestWhenAFuncPanicsOnToolCallArguments(t *testing.T) {
	capture := &redactCapturingProvider{}
	wrapped := RedactEgress(WithFunc(func(text string) string {
		if strings.Contains(text, "boom") {
			panic("the function failed on " + text)
		}
		return text
	}))(capture)
	_, err := wrapped.Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "t", Arguments: json.RawMessage(`{"a":"boom"}`)}}},
	}})
	if err == nil || types.KindOf(err) != types.KindInvariantViolation {
		t.Fatalf("err = %v, want an invariant violation", err)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Errorf("the error carries the text: %v", err)
	}
	if capture.got.Messages != nil {
		t.Error("the request was sent")
	}
}

// What comes back is the model's, answering the redacted conversation, and the
// caller needs its real structure: the calls of a reply are not redacted or
// changed.
func TestRedactEgressLeavesTheCallsOfAReplyAlone(t *testing.T) {
	const email = "ada@example.com"
	calls := []llm.ToolCall{{ID: "call_9", Name: "send_mail", Arguments: json.RawMessage(`{"to":"` + email + `"}`)}}
	provider := &scripted{name: "p", answer: func(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
		return llm.CompletionResponse{ToolCalls: calls, FinishReason: "tool_calls"}, nil
	}}
	resp, err := RedactEgress()(provider).Complete(context.Background(), llm.CompletionRequest{Messages: []llm.Message{{Role: "user", Content: email}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resp.ToolCalls, calls) || resp.Content != "" {
		t.Errorf("the reply came back changed: %+v", resp)
	}
}

// A request with no tool call in its history is passed on as it was: the same
// messages, with nothing added to them.
func TestRedactEgressPassesMessagesWithNoToolCallsOnWithNoneAdded(t *testing.T) {
	capture := &redactCapturingProvider{}
	messages := []llm.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}, {Role: "tool", Content: "r", ToolCallID: "c1"}}
	if _, err := RedactEgress()(capture).Complete(context.Background(), llm.CompletionRequest{Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capture.got.Messages, messages) {
		t.Errorf("the messages changed: %+v", capture.got.Messages)
	}
	for _, message := range capture.got.Messages {
		if message.ToolCalls != nil {
			t.Errorf("a message gained tool calls: %+v", message)
		}
	}
}

// redactBenchText is clean business prose of about size bytes: nothing in it a
// built-in rule matches, which is the common request and the case where a
// redactor that scans the whole text once for every rule pays for nothing.
func redactBenchText(size int) string {
	const paragraph = "Invoice #48213 for 3 units of SKU-9981 is due on the 30th. Customer reference ORD-2026-000451, " +
		"total 1,204.50 USD. Please confirm receipt and let us know if the shipping address changed; the warehouse " +
		"closes at five and the carrier collects on Tuesday and Friday. "
	var text strings.Builder
	for text.Len() < size {
		text.WriteString(paragraph)
	}
	return text.String()[:size]
}

// BenchmarkRedact measures the scrubbing of one text with the built-in rules:
// clean text of two sizes, and the same text with one email address and one key
// in it. ns/KB is the figure to compare across sizes.
func BenchmarkRedact(b *testing.B) {
	provider := RedactEgress()(nil).(*redactingProvider)
	for _, size := range []int{3_500, 110_000} {
		clean := redactBenchText(size)
		dirty := clean[:size/2] + " write to jane.doe@example.com or use sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz9012345 " + clean[size/2:] // secret-scan: allow
		for _, tc := range []struct{ name, text string }{{"clean", clean}, {"email and key", dirty}} {
			b.Run(fmt.Sprintf("%s/%dB", tc.name, size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(tc.text)))
				for b.Loop() {
					provider.redact(tc.text)
				}
			})
		}
	}
}
