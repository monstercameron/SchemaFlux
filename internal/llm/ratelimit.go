package llm

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A 429 was retried on a blind exponential backoff -- a few seconds, doubling.
// Providers that rate-limit per minute answer with `Retry-After: 53`, and a
// backoff measured in seconds cannot clear a window measured in minutes, so
// every attempt was spent inside the same closed window and the call failed
// with the retries having achieved nothing but latency.
//
// The server states exactly how long to wait. Substituting a guess for it is
// the difference between a request that succeeds a minute late and one that
// fails after burning its whole retry budget.

// RateLimitError reports a throttled request, a 429 or a 503, along with the
// wait the server asked for. It is an APIError first: a 429 is a request that
// failed with a status like any other, and a caller branching on the status
// should not have to know that this one carries an extra field. The kind of
// failure follows the status and not the type: see Classify.
type RateLimitError struct {
	*APIError

	// RetryAfter is the wait the server asked for, zero if it did not say.
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return e.APIError.render(e.RetryAfter, detailEnabled())
}

// Detail is APIError.Detail with the wait the server asked for.
func (e *RateLimitError) Detail() string {
	return e.APIError.detail(e.RetryAfter)
}

// String, GoString, LogValue and MarshalJSON are APIError's, with the wait. They
// are defined here and not left to the embedded pointer because the promoted
// ones would leave the wait out, and because a value of this type whose
// embedded pointer is nil has to print as nothing rather than panic.
func (e RateLimitError) String() string { return (&e).Error() }

// GoString is what %#v prints.
func (e RateLimitError) GoString() string { return (&e).Error() }

// LogValue is what slog logs for a RateLimitError that is not logged as an error.
func (e RateLimitError) LogValue() slog.Value { return slog.StringValue((&e).Error()) }

// MarshalJSON is APIError's, with the wait.
func (e RateLimitError) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.APIError.view(e.RetryAfter, detailEnabled()))
}

// Unwrap exposes the underlying APIError, so errors.As recovers either type.
func (e *RateLimitError) Unwrap() error { return e.APIError }

// MaxRetryAfter bounds how long a server may make this library wait. A
// misconfigured or hostile endpoint answering `Retry-After: 86400` must not
// park a caller's goroutine for a day; past this point failing is the more
// useful answer, and the caller's own context deadline still applies first.
const MaxRetryAfter = 2 * time.Minute

// RetryAfterFrom returns the wait a rate-limited error asked for, and whether
// the error was a rate limit at all.
func RetryAfterFrom(err error) (time.Duration, bool) {
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) {
		return 0, false
	}
	return rateLimit.RetryAfter, true
}

// parseRetryAfter reads the header in both forms the RFC allows: a count of
// seconds, and an HTTP date. Anything else, including a date already in the
// past, yields zero -- meaning "the server did not usefully say", which leaves
// the caller's own backoff in charge rather than substituting a wait of zero.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}

	if seconds, err := strconv.ParseFloat(header, 64); err == nil {
		// ParseFloat accepts "NaN", "Inf" and exponents past the range of a
		// Duration. Converting such a float to an integer gives a different
		// answer on each architecture, so the number is clamped as a float, before
		// it is converted.
		if math.IsNaN(seconds) || seconds <= 0 {
			return 0
		}
		if seconds >= MaxRetryAfter.Seconds() {
			return MaxRetryAfter
		}
		return time.Duration(seconds * float64(time.Second))
	}

	if when, err := http.ParseTime(header); err == nil {
		if wait := when.Sub(now); wait > 0 {
			return clampRetryAfter(wait)
		}
	}

	return 0
}

func clampRetryAfter(wait time.Duration) time.Duration {
	if wait > MaxRetryAfter {
		return MaxRetryAfter
	}
	return wait
}

// carriesRetryAfter reports whether a status is one whose Retry-After header is
// read: 429 and 503, the two that mean "come back later" rather than "this
// request was wrong". It says nothing about the kind of failure. Classify decides
// that from the status, whichever type carries the error: 429 is a rate limit
// and 503 is the provider being unavailable, and both are retried.
func carriesRetryAfter(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// rateLimitError builds the error for a 429 or 503 response, with the wait the
// server asked for. A RateLimitError is the type that carries that wait; the
// kind it is reported as comes from its status (see Classify).
func rateLimitError(provider, model string, resp *http.Response, body string, config ProviderConfig) *RateLimitError {
	return &RateLimitError{
		APIError:   NewAPIError(provider, model, resp.StatusCode, body, configSecrets(config)...),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}
