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

// Which statuses mean "come back later" rather than "this request was wrong".
func TestIsRateLimited(t *testing.T) {
	retryable := []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}
	for _, status := range retryable {
		if !isRateLimited(status) {
			t.Errorf("status %d should be treated as a rate limit", status)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 404, 422, 500, 502, 504} {
		if isRateLimited(status) {
			t.Errorf("status %d must not be treated as a rate limit", status)
		}
	}
}

// RetryAfterFrom has to see through the wrapping the call stack adds, or the
// wait is discovered by nobody.
func TestRetryAfterFromUnwraps(t *testing.T) {
	base := &RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: 53 * time.Second, Body: "quota"}

	cases := []struct {
		name string
		err  error
		want time.Duration
		is   bool
	}{
		{"direct", base, 53 * time.Second, true},
		{"wrapped_once", fmt.Errorf("extraction failed: %w", base), 53 * time.Second, true},
		{"wrapped_twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", base)), 53 * time.Second, true},
		{"no_wait_stated", &RateLimitError{Provider: "x", StatusCode: 429}, 0, true},
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
		Provider: "cerebras", StatusCode: 429, RetryAfter: 53 * time.Second,
		Body: `{"message":"Requests per minute limit exceeded"}`,
	}
	message := withWait.Error()
	for _, want := range []string{"cerebras", "429", "53s", "per minute limit exceeded"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q: %s", want, message)
		}
	}

	withoutWait := &RateLimitError{Provider: "cerebras", StatusCode: 429, Body: "slow down"}
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
	if !strings.Contains(err.Error(), "Rate limit reached") {
		t.Errorf("the reason was lost: %v", err)
	}
}
