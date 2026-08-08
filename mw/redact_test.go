package mw

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
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

func TestRedactEgressDelegatesNameCostAndRetryPolicy(t *testing.T) {
	fake := &fakeProvider{name: "delegate-fake", maxRetries: 2, backoff: time.Second}
	wrapped := RedactEgress()(fake)

	if wrapped.Name() != "delegate-fake" {
		t.Fatalf("Name() = %q, want %q", wrapped.Name(), "delegate-fake")
	}
	if got := wrapped.EstimateCost(llm.CompletionRequest{}); got != 0.01 {
		t.Fatalf("EstimateCost() = %v, want 0.01", got)
	}
	retries, backoff := wrapped.RetryPolicy()
	if retries != 2 || backoff != time.Second {
		t.Fatalf("RetryPolicy() = (%d, %v), want (2, 1s)", retries, backoff)
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
