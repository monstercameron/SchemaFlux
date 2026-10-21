package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A 429 was reported as a plain formatted error, so the `Retry-After: 53` that
// came with it was discarded and the retry loop fell back to a backoff capped
// at five seconds -- which cannot clear a per-minute window by construction.

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "53", 53 * time.Second},
		{"seconds_with_space", "  30  ", 30 * time.Second},
		{"fractional", "1.5", 1500 * time.Millisecond},
		{"zero_is_no_answer", "0", 0},
		{"negative_is_no_answer", "-5", 0},
		{"absent", "", 0},
		{"garbage", "soon", 0},
		{"http_date", now.Add(45 * time.Second).UTC().Format(http.TimeFormat), 45 * time.Second},
		{"http_date_in_the_past", now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0},
		{"clamped", "86400", MaxRetryAfter},
		{"clamped_date", now.Add(24 * time.Hour).UTC().Format(http.TimeFormat), MaxRetryAfter},
		// ParseFloat accepts these, and converting an out-of-range float to a
		// Duration gives a different answer on each architecture: a negative one on
		// amd64, which the clamp does not catch, and a saturated one on arm64.
		{"infinity_is_clamped", "Inf", MaxRetryAfter},
		{"plus_infinity_is_clamped", "+Inf", MaxRetryAfter},
		{"negative_infinity_is_no_answer", "-Inf", 0},
		{"nan_is_no_answer", "NaN", 0},
		{"huge_exponent_is_clamped", "1e30", MaxRetryAfter},
		{"just_past_the_duration_range_is_clamped", "9.3e9", MaxRetryAfter},
		{"hex_float_is_clamped", "0x1p62", MaxRetryAfter},
		{"tiny_positive_is_a_tiny_wait", "0.001", time.Millisecond},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.header, now)
			// HTTP dates carry second resolution, so allow a second of slack.
			if diff := got - tc.want; diff > time.Second || diff < -time.Second {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

// A server asking for a day must not park a caller's goroutine for a day.
func TestRetryAfterIsBounded(t *testing.T) {
	if got := parseRetryAfter("999999", time.Now()); got != MaxRetryAfter {
		t.Errorf("parseRetryAfter = %v, want it clamped to %v", got, MaxRetryAfter)
	}
	if MaxRetryAfter > 5*time.Minute {
		t.Errorf("MaxRetryAfter = %v; a ceiling that high is indistinguishable from hanging", MaxRetryAfter)
	}
}

// Which statuses carry a Retry-After worth reading: the two that mean "come back
// later" rather than "this request was wrong". What kind of failure each is, is
// decided by Classify.
func TestCarriesRetryAfter(t *testing.T) {
	retryable := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}
	for _, status := range retryable {
		if !carriesRetryAfter(status) {
			t.Errorf("status %d should carry a Retry-After", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 422, 500, 502, 504} {
		if carriesRetryAfter(status) {
			t.Errorf("status %d must not carry a Retry-After", status)
		}
	}
}

// The same status means the same thing from every provider. 429 is a rate limit
// and 503 is the provider being unavailable; both are retried, both carry the
// Retry-After the server sent, and neither carries the body.
func TestEveryProviderClassifiesThrottlingByStatus(t *testing.T) {
	type call func(t *testing.T, baseURL string) error
	providers := map[string]call{
		"openai": func(t *testing.T, baseURL string) error {
			p, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: baseURL, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
			return err
		},
		"openai stream": func(t *testing.T, baseURL string) error {
			p, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: baseURL, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range p.CompleteStream(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"}) {
				return err
			}
			return nil
		},
		"openai embeddings": func(t *testing.T, baseURL string) error {
			p, err := NewOpenAIProvider(ProviderConfig{APIKey: "k", BaseURL: baseURL, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}})
			return err
		},
		"chat completions": func(t *testing.T, baseURL string) error {
			p, err := NewOpenAICompatibleProvider("cerebras", ProviderConfig{APIKey: "k", BaseURL: baseURL, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			return err
		},
		"anthropic": func(t *testing.T, baseURL string) error {
			p := newTestAnthropicProvider(t, baseURL)
			_, err := p.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
			return err
		},
	}
	statuses := []struct {
		status int
		kind   types.ErrorKind
	}{
		{http.StatusTooManyRequests, types.KindRateLimited},
		{http.StatusServiceUnavailable, types.KindProviderUnavailable},
	}
	for name, run := range providers {
		for _, tc := range statuses {
			t.Run(fmt.Sprintf("%s_%d", name, tc.status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "7")
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
				}))
				defer server.Close()

				err := run(t, server.URL)
				if err == nil {
					t.Fatal("expected an error")
				}
				if got := Classify(err); got != tc.kind {
					t.Errorf("Classify = %v, want %v (%v)", got, tc.kind, err)
				}
				if !(&types.OperationError{Kind: Classify(err)}).Retryable() {
					t.Errorf("status %d is not retryable", tc.status)
				}
				if wait, ok := RetryAfterFrom(err); !ok || wait != 7*time.Second {
					t.Errorf("RetryAfterFrom = %v, %v; want 7s, true", wait, ok)
				}
			})
		}
	}
}

// RetryAfterFrom has to see through the wrapping the call stack adds, or the
// wait is discovered by nobody.
func TestRetryAfterFromUnwraps(t *testing.T) {
	base := &RateLimitError{APIError: &APIError{Provider: "cerebras", StatusCode: 429, Body: "quota"}, RetryAfter: 53 * time.Second}

	cases := []struct {
		name string
		err  error
		want time.Duration
		is   bool
	}{
		{"direct", base, 53 * time.Second, true},
		{"wrapped_once", fmt.Errorf("extraction failed: %w", base), 53 * time.Second, true},
		{"wrapped_twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", base)), 53 * time.Second, true},
		{"no_wait_stated", &RateLimitError{APIError: &APIError{Provider: "x", StatusCode: 429}}, 0, true},
		{"unrelated", errors.New("connection reset"), 0, false},
		{"nil", nil, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, rateLimited := RetryAfterFrom(tc.err)
			if rateLimited != tc.is {
				t.Errorf("rateLimited = %v, want %v", rateLimited, tc.is)
			}
			if wait != tc.want {
				t.Errorf("wait = %v, want %v", wait, tc.want)
			}
		})
	}
}

// The message still has to say what happened. The wait is extra information,
// not a replacement for the vendor's explanation.
func TestRateLimitErrorMessage(t *testing.T) {
	withWait := &RateLimitError{
		APIError: &APIError{
			Provider: "cerebras", StatusCode: 429,
			Body: `{"message":"Requests per minute limit exceeded"}`,
			// Extracted from the body by newAPIError in the real path; set
			// here because this test builds the value directly.
			Message: "Requests per minute limit exceeded",
		},
		RetryAfter: 53 * time.Second,
	}
	message := withWait.Error()
	for _, want := range []string{"cerebras", "429", "53s", "message withheld"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q: %s", want, message)
		}
	}
	if strings.Contains(message, "per minute limit exceeded") {
		t.Errorf("the vendor's message is in the error's text: %s", message)
	}
	if detail := withWait.Detail(); !strings.Contains(detail, "per minute limit exceeded") || !strings.Contains(detail, "retry after 53s") {
		t.Errorf("Detail() does not carry the vendor's message and the wait: %s", detail)
	}

	withoutWait := &RateLimitError{APIError: &APIError{Provider: "cerebras", StatusCode: 429, Body: "slow down"}}
	if strings.Contains(withoutWait.Error(), "retry after") {
		t.Errorf("a wait was reported that the server never gave: %s", withoutWait.Error())
	}
	if !strings.Contains(withoutWait.Error(), "429") {
		t.Errorf("the status is missing: %s", withoutWait.Error())
	}
}

// End to end through the transport: a throttled response must arrive at the
// caller as a typed error carrying the server's wait.
func TestCompatibleProviderSurfacesRateLimits(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		wantWait   time.Duration
	}{
		{"429_with_wait", http.StatusTooManyRequests, "53", 53 * time.Second},
		{"429_without_wait", http.StatusTooManyRequests, "", 0},
		{"503_with_wait", http.StatusServiceUnavailable, "10", 10 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"Requests per minute limit exceeded","code":"request_quota_exceeded"}`)
			}))
			defer server.Close()

			provider, _ := NewOpenAICompatibleProvider("cerebras", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})

			_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if err == nil {
				t.Fatal("a throttled response must be an error")
			}

			wait, rateLimited := RetryAfterFrom(err)
			if !rateLimited {
				t.Fatalf("the error is not typed as a rate limit: %T %v", err, err)
			}
			if wait != tc.wantWait {
				t.Errorf("wait = %v, want %v", wait, tc.wantWait)
			}
			if !strings.Contains(err.Error(), "quota") {
				t.Errorf("the vendor's explanation was lost: %v", err)
			}
		})
	}
}

// A non-throttling failure must stay an ordinary error, or the retry loop waits
// out a minute for a 400 it will never recover from.
func TestOrdinaryFailuresAreNotTypedAsRateLimits(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Deliberately present, to prove the status is what decides.
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"nope"}`)
			}))
			defer server.Close()

			provider, _ := NewOpenAICompatibleProvider("cerebras", ProviderConfig{
				APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
			})

			_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
			if err == nil {
				t.Fatal("expected an error")
			}
			if _, rateLimited := RetryAfterFrom(err); rateLimited {
				t.Errorf("status %d was typed as a rate limit; the retry loop would wait ten minutes for it", status)
			}
		})
	}
}

// The OpenAI Responses path gets the same treatment: it is the primary
// provider, and a 429 there is the one most likely to be hit under load.
func TestOpenAIProviderSurfacesRateLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Rate limit reached for gpt-5.6-luna"}}`)
	}))
	defer server.Close()

	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.6-luna", UserPrompt: "x"})
	if err == nil {
		t.Fatal("a 429 must be an error")
	}

	wait, rateLimited := RetryAfterFrom(err)
	if !rateLimited {
		t.Fatalf("the OpenAI path does not surface the wait: %T %v", err, err)
	}
	if wait != 20*time.Second {
		t.Errorf("wait = %v, want 20s", wait)
	}
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) || !strings.Contains(rateLimit.Message, "Rate limit reached") || !strings.Contains(rateLimit.Detail(), "Rate limit reached") {
		t.Errorf("the reason was lost: %v", err)
	}
	if strings.Contains(err.Error(), "Rate limit reached") {
		t.Errorf("the vendor's message is in the error's text: %v", err)
	}
}
