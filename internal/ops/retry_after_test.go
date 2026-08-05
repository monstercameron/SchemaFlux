package ops

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// The retry loop computed a backoff that doubles from 500ms and stops at five
// seconds. Providers that rate-limit per minute answer with the exact wait they
// want -- Cerebras' free tier says `Retry-After: 53` -- so every attempt landed
// inside the same closed window and the retry budget bought nothing but
// latency. These pin the fix: when the server states a wait, that wait wins.

func TestNextRetryDelayPrefersTheServersWait(t *testing.T) {
	base := 500 * time.Millisecond

	cases := []struct {
		name    string
		err     error
		attempt int
		want    time.Duration
	}{
		{
			"rate_limit_beats_the_backoff",
			&llm.RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: 53 * time.Second},
			1, 53 * time.Second,
		},
		{
			"rate_limit_beats_the_backoff_on_later_attempts_too",
			&llm.RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: 53 * time.Second},
			4, 53 * time.Second,
		},
		{
			"wrapped_rate_limit_is_still_honoured",
			fmt.Errorf("extraction failed: %w",
				&llm.RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: 30 * time.Second}),
			2, 30 * time.Second,
		},
		{
			// A rate limit with no stated wait leaves the backoff in charge
			// rather than substituting a wait of zero and hammering the server.
			"rate_limit_without_a_wait_falls_back_to_the_backoff",
			&llm.RateLimitError{Provider: "cerebras", StatusCode: 429},
			1, base,
		},
		{
			"ordinary_failure_uses_the_backoff",
			errors.New("connection reset"),
			1, base,
		},
		{
			"ordinary_failure_still_doubles",
			errors.New("connection reset"),
			3, 2 * time.Second,
		},
		{
			"ordinary_failure_is_still_capped",
			errors.New("connection reset"),
			10, 5 * time.Second,
		},
		{
			"a_shorter_wait_than_the_backoff_is_still_the_servers_call",
			&llm.RateLimitError{Provider: "openai", StatusCode: 429, RetryAfter: 100 * time.Millisecond},
			5, 100 * time.Millisecond,
		},
		{
			"nil_error_uses_the_backoff",
			nil,
			1, base,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextRetryDelay(tc.err, base, tc.attempt); got != tc.want {
				t.Errorf("nextRetryDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

// The computed backoff can never clear a per-minute window. This is the whole
// reason the server's number has to win, stated as a test so a later change to
// the cap cannot quietly reintroduce the bug.
func TestTheComputedBackoffCannotClearAPerMinuteWindow(t *testing.T) {
	base := 500 * time.Millisecond

	var longest time.Duration
	for attempt := 1; attempt <= 20; attempt++ {
		if delay := retryDelay(base, attempt); delay > longest {
			longest = delay
		}
	}

	if longest >= time.Minute {
		t.Skip("the backoff now exceeds a minute; the server's wait is no longer strictly necessary")
	}

	rateLimited := &llm.RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: 53 * time.Second}
	if got := nextRetryDelay(rateLimited, base, 1); got <= longest {
		t.Errorf("nextRetryDelay = %v, which is within the backoff's %v ceiling -- "+
			"the retry would land inside the same closed window", got, longest)
	}
}

// A rate limit is retryable by type, not by how its message happens to read.
func TestRateLimitsAreRetryableByType(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"typed_rate_limit",
			&llm.RateLimitError{Provider: "cerebras", StatusCode: 429, RetryAfter: time.Second, Body: "quota"},
			true,
		},
		{
			"typed_rate_limit_wrapped",
			fmt.Errorf("extraction failed: %w", &llm.RateLimitError{Provider: "cerebras", StatusCode: 429}),
			true,
		},
		{
			// The body is matched against the non-retryable substrings, so a
			// vendor whose 429 body mentions an unrelated invalid-request code
			// used to make a throttle look permanent.
			"typed_rate_limit_whose_body_mentions_a_non_retryable_code",
			&llm.RateLimitError{
				Provider: "cerebras", StatusCode: 429, RetryAfter: 5 * time.Second,
				Body: `{"type":"invalid_request_error","message":"quota"}`,
			},
			true,
		},
		{
			"a_503_rate_limit",
			&llm.RateLimitError{Provider: "openai", StatusCode: 503, RetryAfter: 2 * time.Second},
			true,
		},
		{"a_plain_400", errors.New("openai API error (status 400): bad schema"), false},
		{"a_plain_401", errors.New("openai API error (status 401): unauthorized"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableLLMError(tc.err); got != tc.want {
				t.Errorf("isRetryableLLMError = %v, want %v", got, tc.want)
			}
		})
	}
}
