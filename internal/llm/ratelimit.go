package llm

import (
	"errors"
	"fmt"
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

// RateLimitError reports a rate-limited request along with the wait the server
// asked for. RetryAfter is zero when the server did not say.
type RateLimitError struct {
	// Provider names who rate-limited the request.
	Provider string

	// StatusCode is the HTTP status, kept so the message reads the same as the
	// generic transport error it replaces.
	StatusCode int

	// RetryAfter is the wait the server asked for, zero if it did not say.
	RetryAfter time.Duration

	// Body is the server's explanation, which is where the quota that was hit
	// is named.
	Body string
}

func (e *RateLimitError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s API error (status %d, retry after %s): %s",
			e.Provider, e.StatusCode, e.RetryAfter, e.Body)
	}
	return fmt.Sprintf("%s API error (status %d): %s", e.Provider, e.StatusCode, e.Body)
}

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
		if seconds <= 0 {
			return 0
		}
		return clampRetryAfter(time.Duration(seconds * float64(time.Second)))
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

// isRateLimited reports whether a status means "come back later" rather than
// "this request was wrong". 503 is included: it is the other status that comes
// with a Retry-After and means the same thing to a caller.
func isRateLimited(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// rateLimitError builds the typed error for a throttled response.
func rateLimitError(provider string, resp *http.Response, body string) *RateLimitError {
	return &RateLimitError{
		Provider:   provider,
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		Body:       strings.TrimSpace(body),
	}
}
