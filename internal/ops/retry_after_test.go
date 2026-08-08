package ops

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// The retry loop computed a backoff that doubles from 500ms and stops at five
// seconds. Providers that rate-limit per minute answer with the exact wait
// they want -- Cerebras' free tier says `Retry-After: 53` -- so every attempt
// landed inside the same closed window and the retry budget bought nothing
// but latency. These pin the fix: when the server states a wait, that wait
// wins outright and is never jittered.

// zeroRand and oneRand are fixed randFloat sources for deterministic
// assertions on decorrelatedJitter's two edges: 0 always lands on the floor
// (base), and a value just under 1 always lands on (or past, before
// capping) the ceiling (3x the previous wait).
func zeroRand() float64 { return 0 }
func oneRand() float64  { return 0.999999 }

func TestNextRetryDelayPrefersTheServersWaitOverJitter(t *testing.T) {
	base := 500 * time.Millisecond
	prevWait := base
	maxDelay := 5 * time.Second

	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{
			"rate_limit_beats_the_backoff",
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429}, RetryAfter: 53 * time.Second},
			53 * time.Second,
		},
		{
			"wrapped_rate_limit_is_still_honoured",
			fmt.Errorf("extraction failed: %w",
				&llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429}, RetryAfter: 30 * time.Second}),
			30 * time.Second,
		},
		{
			"a_shorter_wait_than_the_backoff_is_still_the_servers_call",
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "openai", StatusCode: 429}, RetryAfter: 100 * time.Millisecond},
			100 * time.Millisecond,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// oneRand would push a computed backoff toward its ceiling; if the
			// server's wait were being jittered instead of honoured outright,
			// this would not equal the exact stated wait.
			if got := nextRetryDelay(tc.err, base, prevWait, maxDelay, oneRand); got != tc.want {
				t.Errorf("nextRetryDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

// A rate limit with no stated wait leaves the computed backoff in charge
// rather than substituting a wait of zero and hammering the server.
func TestNextRetryDelayRateLimitWithoutAWaitFallsBackToComputed(t *testing.T) {
	base := 500 * time.Millisecond
	err := &llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429}}

	if got := nextRetryDelay(err, base, base, 5*time.Second, zeroRand); got != base {
		t.Errorf("nextRetryDelay = %v, want the computed floor %v", got, base)
	}
}

// A nil error is not something nextRetryDelay should ever see in production
// (isRetryableLLMError gates the call), but it must not panic and must still
// fall back to the computed delay.
func TestNextRetryDelayNilErrorUsesComputedDelay(t *testing.T) {
	base := 500 * time.Millisecond
	if got := nextRetryDelay(nil, base, base, 5*time.Second, zeroRand); got != base {
		t.Errorf("nextRetryDelay(nil, ...) = %v, want %v", got, base)
	}
}

// decorrelatedJitter's two deterministic edges: randFloat=0 always lands on
// the floor (base) regardless of the previous wait, and randFloat~1 always
// lands on the ceiling (3x the previous wait), capped.
func TestDecorrelatedJitterBounds(t *testing.T) {
	base := 100 * time.Millisecond
	prev := 200 * time.Millisecond
	maxDelay := 5 * time.Second

	if got := decorrelatedJitter(base, prev, maxDelay, zeroRand); got != base {
		t.Errorf("randFloat=0: got %v, want the floor %v", got, base)
	}

	wantCeiling := 3 * prev
	if got := decorrelatedJitter(base, prev, maxDelay, oneRand); got <= base || got > wantCeiling {
		t.Errorf("randFloat~1: got %v, want in (%v, %v]", got, base, wantCeiling)
	}
}

// The ceiling is still capped at maxDelay when 3x the previous wait would
// exceed it -- an already-large previous wait must not make the NEXT one
// larger still, unbounded.
func TestDecorrelatedJitterCapsAtMaxDelay(t *testing.T) {
	base := 100 * time.Millisecond
	prev := 10 * time.Second // already past the cap on its own
	maxDelay := 5 * time.Second

	if got := decorrelatedJitter(base, prev, maxDelay, oneRand); got != maxDelay {
		t.Errorf("got %v, want capped at %v", got, maxDelay)
	}
}

// A zero or negative base is a caller mistake, not a zero wait: retrying
// with no wait at all would hammer the provider on the very failure the
// backoff exists to back off from.
func TestDecorrelatedJitterNonPositiveBaseFloorsAtAMillisecond(t *testing.T) {
	got := decorrelatedJitter(0, 0, 5*time.Second, zeroRand)
	if got != time.Millisecond {
		t.Errorf("got %v, want the 1ms floor", got)
	}
}

// The chained sequence -- each wait's prevWait is the wait before it, the way
// CallLLM's retry loop actually threads it -- grows (or holds at the cap),
// never shrinks, and eventually plateaus at maxDelay rather than growing
// forever.
func TestDecorrelatedJitterChainGrowsThenPlateaus(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 5 * time.Second

	prevWait := base
	previous := time.Duration(0)
	sawTheCap := false

	for attempt := 0; attempt < 8; attempt++ {
		delay := decorrelatedJitter(base, prevWait, maxDelay, oneRand)
		if delay < previous {
			t.Fatalf("attempt %d: delay %v is shorter than the previous %v", attempt, delay, previous)
		}
		if delay > maxDelay {
			t.Fatalf("attempt %d: delay %v exceeds maxDelay %v", attempt, delay, maxDelay)
		}
		if delay == maxDelay {
			sawTheCap = true
		}
		previous = delay
		prevWait = delay
	}

	if !sawTheCap {
		t.Error("the chain never reached maxDelay across 8 attempts at the jitter ceiling")
	}
}

// Two callers retrying the identical failure from the identical state (same
// base, same prevWait) must not compute the identical wait -- a synchronized
// second wave is exactly what decorrelated jitter exists to prevent, and
// A-008's verify line asks for this driven by an injected rand rather than by
// timing, since asserting on wall-clock timing is what makes a jitter test
// flaky.
func TestJitterSpreadsConcurrentRetriesApart(t *testing.T) {
	base := 200 * time.Millisecond
	prevWait := base
	maxDelay := 5 * time.Second
	err := errors.New("connection reset")

	first := nextRetryDelay(err, base, prevWait, maxDelay, func() float64 { return 0.1 })
	second := nextRetryDelay(err, base, prevWait, maxDelay, func() float64 { return 0.9 })

	if first == second {
		t.Fatalf("two callers retrying from the same state computed the identical delay (%v); "+
			"concurrent retries would still land on the same instant", first)
	}
}

// The computed backoff can never clear a per-minute window even at its
// jittered ceiling. This is the whole reason the server's number has to win,
// stated as a test so a later change to the cap cannot quietly reintroduce
// the bug.
func TestTheComputedBackoffCannotClearAPerMinuteWindow(t *testing.T) {
	base := 500 * time.Millisecond
	maxDelay := 5 * time.Second

	var longest time.Duration
	prevWait := base
	for attempt := 0; attempt < 20; attempt++ {
		delay := decorrelatedJitter(base, prevWait, maxDelay, oneRand)
		if delay > longest {
			longest = delay
		}
		prevWait = delay
	}

	if longest >= time.Minute {
		t.Skip("the backoff now exceeds a minute; the server's wait is no longer strictly necessary")
	}

	rateLimited := &llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429}, RetryAfter: 53 * time.Second}
	if got := nextRetryDelay(rateLimited, base, prevWait, maxDelay, oneRand); got <= longest {
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
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429, Body: "quota"}, RetryAfter: time.Second},
			true,
		},
		{
			"typed_rate_limit_wrapped",
			fmt.Errorf("extraction failed: %w", &llm.RateLimitError{APIError: &llm.APIError{Provider: "cerebras", StatusCode: 429}}),
			true,
		},
		{
			// The body is what used to be matched against the non-retryable
			// substrings, so a vendor whose 429 body mentions an unrelated
			// invalid-request code made a throttle look permanent. Deciding
			// from the typed StatusCode (429, via llm.Classify) rather than the
			// body text is what fixes it.
			"typed_rate_limit_whose_body_mentions_a_non_retryable_code",
			&llm.RateLimitError{
				APIError: &llm.APIError{
					Provider: "cerebras", StatusCode: 429,
					Type: "invalid_request_error", Message: "quota",
					Body: `{"type":"invalid_request_error","message":"quota"}`,
				},
				RetryAfter: 5 * time.Second,
			},
			true,
		},
		{
			"a_503_rate_limit",
			&llm.RateLimitError{APIError: &llm.APIError{Provider: "openai", StatusCode: 503}, RetryAfter: 2 * time.Second},
			true,
		},
		{
			// Built the way a provider actually builds a client error -- typed,
			// via llm.NewAPIError -- rather than as a plain fmt.Errorf whose
			// message happens to contain "status 400". A-008 removed the
			// substring lists that used to make the plain-string spelling of
			// this case matter; only the type does now.
			"a_typed_400", llm.NewAPIError("openai", "m", 400, ""), false,
		},
		{
			"a_typed_401", llm.NewAPIError("openai", "m", 401, ""), false,
		},
		{
			// The case A-008's verify line names directly: an error nobody has
			// taught the taxonomy about is not assumed permanent. It gets its
			// retries; the attempt budget bounds what being wrong costs.
			"an_unclassifiable_error_is_retried_not_failed_fast",
			errors.New("the vendor changed its error format again"),
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableLLMError(tc.err); got != tc.want {
				t.Errorf("isRetryableLLMError = %v, want %v", got, tc.want)
			}
		})
	}
}

// The retry decision must come from the status, not from prose. Redaction
// changed what the message says; the classification must not have moved with
// it, and a vendor's wording must not be able to flip it either.
func TestRetryDecisionComesFromTheStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		// A permanent failure whose body happens to use retryable words.
		{"400_mentioning_timeout", 400, `{"error":{"message":"request timeout value is invalid"}}`, false},
		{"401_mentioning_temporary", 401, `{"error":{"message":"temporary key not accepted"}}`, false},
		{"404_mentioning_try_again", 404, `{"error":{"message":"model missing, try again later"}}`, false},

		// A transient failure whose body says nothing useful at all.
		{"500_silent", 500, ``, true},
		{"502_html", 502, `<html>bad gateway</html>`, true},
		{"503_silent", 503, ``, true},
		{"504_silent", 504, ``, true},
		{"408_silent", 408, ``, true},

		// And the boring correct cases.
		{"422_unprocessable", 422, `{"error":{"message":"schema too deep"}}`, false},
		{"403_forbidden", 403, `{"error":{"message":"no access"}}`, false},

		// The cases that separate a typed decision from a textual one. A vendor
		// is free to put any classifier in its body, and a substring matcher
		// reads the WHOLE message including that classifier -- so a transient
		// 500 whose body carries `invalid_request_error` was read as permanent
		// and the caller lost a retry they were entitled to.
		{"500_whose_body_says_invalid_request", 500,
			`{"error":{"type":"invalid_request_error","message":"internal failure"}}`, true},
		{"503_whose_body_says_unauthorized", 503,
			`{"error":{"type":"server_error","message":"unauthorized upstream"}}`, true},
		{"502_whose_body_says_forbidden", 502,
			`{"error":{"message":"forbidden by upstream"}}`, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Built the way a provider builds it, so the vendor's classifiers
			// reach the message exactly as they would in production.
			err := error(llm.NewAPIError("openai", "m", tc.status, tc.body))
			if got := isRetryableLLMError(err); got != tc.want {
				t.Errorf("isRetryableLLMError(status %d) = %v, want %v -- decided from %q",
					tc.status, got, tc.want, err.Error())
			}
		})
	}
}
