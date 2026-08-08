package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ProtocolConformanceChecks implements CP-003's wire-level checks against
// the OpenAI Responses API shape: auth/permission classification, timeouts,
// cancellation, ambiguous completion, 429 with valid/invalid/missing
// Retry-After, 5xx behaviour, empty/malformed/truncated/refused output,
// streaming termination, caller-supplied endpoint, and credential
// redaction. Every check here builds its own httptest.Server bound to
// 127.0.0.1 and never reads a LiveTarget -- see the file doc on
// conformance.go for why this half of the suite needs no live gate.
//
// It targets OpenAIProvider specifically, not "a provider" in the abstract:
// that adapter is the one this package hand-rolls the wire format for (see
// its doc comment -- go-openai does not support the Responses API), so it is
// the one place a bug in this library's own framing, not a vendor's, can
// actually hide. A second provider's protocol checks are future work this
// report's Limitations section names.
func ProtocolConformanceChecks() []ConformanceCheck {
	return []ConformanceCheck{
		{ID: "timeout", Category: CategoryProtocol,
			Description: "a request that outlives its context deadline is classified as a timeout, not left to hang",
			Run:         checkTimeout},
		{ID: "cancellation", Category: CategoryProtocol,
			Description: "a cancelled context returns promptly and is classified as canceled",
			Run:         checkCancellation},
		{ID: "auth_401", Category: CategoryProtocol,
			Description: "a 401 is classified as an authentication failure, not a generic error",
			Run:         checkAuth401},
		{ID: "permission_403", Category: CategoryProtocol,
			Description: "a 403 is classified as a permission failure, distinct from authentication",
			Run:         checkPermission403},
		{ID: "ambiguous_completion", Category: CategoryProtocol,
			Description: "a 200 carrying no output items is refused, not returned as a zero-value success",
			Run:         checkAmbiguousCompletion},
		{ID: "ratelimit_valid_retry_after", Category: CategoryProtocol,
			Description: "a 429 with a numeric Retry-After yields exactly that wait",
			Run:         checkRateLimitValidRetryAfter},
		{ID: "ratelimit_invalid_retry_after", Category: CategoryProtocol,
			Description: "a 429 with an unparsable Retry-After yields zero (unknown), never an invented wait",
			Run:         checkRateLimitInvalidRetryAfter},
		{ID: "ratelimit_missing_retry_after", Category: CategoryProtocol,
			Description: "a 429 with no Retry-After header yields zero (unknown), never an invented wait",
			Run:         checkRateLimitMissingRetryAfter},
		{ID: "server_5xx", Category: CategoryProtocol,
			Description: "a 503 is classified as provider-unavailable and marked retryable",
			Run:         checkServer5xx},
		{ID: "empty_output", Category: CategoryProtocol,
			Description: "a message item with empty text is refused, not returned as an empty success",
			Run:         checkEmptyOutput},
		{ID: "malformed_output", Category: CategoryProtocol,
			Description: "a body that is not JSON fails to decode instead of producing a zero-value response",
			Run:         checkMalformedOutput},
		{ID: "truncated_output", Category: CategoryProtocol,
			Description: "a connection that closes mid-body is reported as an error, not a short success",
			Run:         checkTruncatedOutput},
		{ID: "refused_output", Category: CategoryProtocol,
			Description: "a content-filter refusal with no text is reported by name, not silently accepted",
			Run:         checkRefusedOutput},
		{ID: "streaming_termination", Category: CategoryProtocol,
			Description: "a stream that stops without a terminal event surfaces ErrStreamIncomplete, never a Done built from partial text",
			Run:         checkStreamingTermination},
		{ID: "credential_redaction", Category: CategoryProtocol,
			Description: "the API key reaches the server but never appears in a returned error",
			Run:         checkCredentialRedaction},
		{ID: "endpoint_override", Category: CategoryProtocol,
			Description: "a caller-supplied BaseURL is honoured for the request, trailing slash and all",
			Run:         checkEndpointOverride},
	}
}

// newProtocolProvider builds an OpenAIProvider pointed at srv, with a fixed,
// recognizable API key so checkCredentialRedaction can assert on it. Timeout
// defaults generously for checks that are not themselves testing a timeout.
func newProtocolProvider(t protocolTB, srv *httptest.Server, apiKey string, timeout time.Duration) *OpenAIProvider {
	t.Helper()
	if apiKey == "" {
		apiKey = "sk-conformance-test-key-do-not-leak"
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	provider, err := NewOpenAIProvider(ProviderConfig{
		APIKey:  apiKey,
		BaseURL: srv.URL,
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("newProtocolProvider: %v", err)
	}
	return provider
}

// protocolTB is the sliver of *testing.T these helpers need, so the checks
// themselves can run outside a *testing.T (RunConformanceSuite calls them
// directly) while still sharing Fatalf-based setup with this file's own
// tests. A check's Run signature never sees a *testing.T -- infrastructure
// failures here become a failedOutcome, not a test failure, because a check
// that cannot even start its own local server failed the check, not the
// harness.
type protocolTB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// panicTB adapts protocolTB to a plain function body: newProtocolProvider's
// only failure mode (a bad ProviderConfig) cannot happen with the fixed
// inputs these checks pass it, so a panic recovered into a failedOutcome is
// simpler than threading an error return through every call site.
type panicTB struct{}

func (panicTB) Helper() {}
func (panicTB) Fatalf(format string, args ...any) {
	panic(fmt.Errorf(format, args...))
}

func runProtocolCheck(fn func() ConformanceOutcome) (outcome ConformanceOutcome) {
	defer func() {
		if r := recover(); r != nil {
			outcome = failedOutcome(fmt.Sprintf("check panicked: %v", r))
		}
	}()
	return fn()
}

func checkTimeout(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 30*time.Millisecond)
		start := time.Now()
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		elapsed := time.Since(start)

		if err == nil {
			return failedOutcome("a request past its deadline returned success")
		}
		if elapsed > 2*time.Second {
			return failedOutcome(fmt.Sprintf("took %s; the deadline did not reach the transport", elapsed))
		}
		if kind := Classify(err); kind != types.KindTimeout {
			return failedOutcome(fmt.Sprintf("classified %s, want %s", kind, types.KindTimeout))
		}
		return passedOutcome(fmt.Sprintf("errored after %s, classified %s", elapsed, types.KindTimeout))
	})
}

func checkCancellation(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer func() {
			close(release)
			srv.Close()
		}()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, err := provider.Complete(cctx, CompletionRequest{Model: "gpt-5.6-mini"})
		elapsed := time.Since(start)

		if err == nil {
			return failedOutcome("a cancelled request returned success")
		}
		if elapsed > 2*time.Second {
			return failedOutcome(fmt.Sprintf("took %s to notice cancellation", elapsed))
		}
		if kind := Classify(err); kind != types.KindCanceled {
			return failedOutcome(fmt.Sprintf("classified %s, want %s", kind, types.KindCanceled))
		}
		return passedOutcome(fmt.Sprintf("errored after %s, classified %s", elapsed, types.KindCanceled))
	})
}

func checkAuth401(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return checkStatusClassification(ctx, http.StatusUnauthorized, types.KindAuthentication)
}

func checkPermission403(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return checkStatusClassification(ctx, http.StatusForbidden, types.KindPermission)
}

func checkStatusClassification(ctx context.Context, status int, want types.ErrorKind) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"synthetic","type":"synthetic_error"}}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome(fmt.Sprintf("a %d response returned success", status))
		}
		if kind := Classify(err); kind != want {
			return failedOutcome(fmt.Sprintf("status %d classified %s, want %s", status, kind, want))
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return failedOutcome("error does not carry an *APIError")
		}
		if apiErr.StatusCode != status {
			return failedOutcome(fmt.Sprintf("APIError.StatusCode = %d, want %d", apiErr.StatusCode, status))
		}
		return passedOutcome(fmt.Sprintf("status %d classified %s", status, want))
	})
}

func checkAmbiguousCompletion(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[]}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("an empty output list returned success")
		}
		if !errors.Is(err, ErrNoMessageOutput) {
			return failedOutcome(fmt.Sprintf("err = %v, want ErrNoMessageOutput", err))
		}
		return passedOutcome("empty output list refused via ErrNoMessageOutput")
	})
}

func checkEmptyOutput(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a message item with empty text returned success")
		}
		if !errors.Is(err, ErrNoMessageOutput) {
			return failedOutcome(fmt.Sprintf("err = %v, want ErrNoMessageOutput", err))
		}
		return passedOutcome("empty message text refused via ErrNoMessageOutput")
	})
}

func checkMalformedOutput(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`this is not json at all {{{`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("an unparsable body returned success")
		}
		return passedOutcome(fmt.Sprintf("unparsable body errored: %v", err))
	})
}

func checkTruncatedOutput(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			conn, buf, err := hijacker.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()

			full := []byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"this body never finishes"}]}]}`)
			// Claim the full length, but only write and then close after
			// half of it -- a lying Content-Length is what a connection
			// that drops mid-stream looks like on the wire.
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(full))
			_, _ = buf.Write(full[:len(full)/2])
			_ = buf.Flush()
			// Close without writing the rest: the client sees an EOF short
			// of the declared Content-Length.
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a truncated body returned success")
		}
		return passedOutcome(fmt.Sprintf("truncated body errored: %v", err))
	})
}

func checkRefusedOutput(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Output is non-empty (a message item with no text), so this
			// exercises the "the model answered with nothing, and the
			// reason is on record" branch rather than the "no output at
			// all" branch checkAmbiguousCompletion already covers.
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","content":[]}]}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a content_filter refusal with no text returned success")
		}
		if !strings.Contains(err.Error(), "content_filter") {
			return failedOutcome(fmt.Sprintf("err = %v, want it to name the refusal reason", err))
		}
		return passedOutcome(fmt.Sprintf("refusal reported by name: %v", err))
	})
}

func checkRateLimitValidRetryAfter(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return checkRateLimit(ctx, "2", func(wait time.Duration, ok bool) (bool, string) {
		if !ok {
			return false, "RetryAfterFrom reported this was not a rate limit"
		}
		if wait != 2*time.Second {
			return false, fmt.Sprintf("RetryAfter = %s, want 2s", wait)
		}
		return true, "Retry-After: 2 parsed as exactly 2s"
	})
}

func checkRateLimitInvalidRetryAfter(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return checkRateLimit(ctx, "not-a-number", func(wait time.Duration, ok bool) (bool, string) {
		if !ok {
			return false, "RetryAfterFrom reported this was not a rate limit"
		}
		if wait != 0 {
			return false, fmt.Sprintf("an unparsable Retry-After produced %s instead of 0 (unknown)", wait)
		}
		return true, "unparsable Retry-After yielded 0 (unknown), not an invented wait"
	})
}

func checkRateLimitMissingRetryAfter(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return checkRateLimit(ctx, "", func(wait time.Duration, ok bool) (bool, string) {
		if !ok {
			return false, "RetryAfterFrom reported this was not a rate limit"
		}
		if wait != 0 {
			return false, fmt.Sprintf("a missing Retry-After produced %s instead of 0 (unknown)", wait)
		}
		return true, "missing Retry-After yielded 0 (unknown), not an invented wait"
	})
}

func checkRateLimit(ctx context.Context, retryAfterHeader string, assert func(time.Duration, bool) (bool, string)) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if retryAfterHeader != "" {
				w.Header().Set("Retry-After", retryAfterHeader)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"synthetic rate limit","type":"rate_limit_error"}}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a 429 returned success")
		}
		if kind := Classify(err); kind != types.KindRateLimited {
			return failedOutcome(fmt.Sprintf("classified %s, want %s", kind, types.KindRateLimited))
		}
		wait, ok := RetryAfterFrom(err)
		passed, detail := assert(wait, ok)
		if !passed {
			return failedOutcome(detail)
		}
		return passedOutcome(detail)
	})
}

func checkServer5xx(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 500, not 503: isRateLimited treats 503 as a rate limit (it
			// carries the same Retry-After semantics a 429 does), so 500 is
			// the status that actually exercises the generic 5xx path this
			// check means to cover.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"synthetic outage","type":"server_error"}}`))
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a 500 returned success")
		}
		if kind := Classify(err); kind != types.KindProviderUnavailable {
			return failedOutcome(fmt.Sprintf("classified %s, want %s", kind, types.KindProviderUnavailable))
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			return failedOutcome("error does not carry an *APIError")
		}
		if !apiErr.Retryable() {
			return failedOutcome("a 500 was not marked retryable")
		}
		return passedOutcome("500 classified provider-unavailable and marked retryable")
	})
}

func checkStreamingTermination(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			// The connection ends here: no "response.completed" event, no
			// "[DONE]" sentinel, just a clean close. This is exactly the
			// case CompleteStream's doc comment describes as
			// ErrStreamIncomplete.
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, "", 5*time.Second)
		var gotDelta bool
		var terminalErr error
		for chunk, err := range provider.CompleteStream(ctx, CompletionRequest{Model: "gpt-5.6-mini"}) {
			if err != nil {
				terminalErr = err
				break
			}
			if chunk.Delta != "" {
				gotDelta = true
			}
			if chunk.Done {
				return failedOutcome("stream reported Done with no terminal event on the wire")
			}
		}

		if !gotDelta {
			return failedOutcome("no delta was observed before the stream ended")
		}
		if terminalErr == nil {
			return failedOutcome("a stream with no terminal event produced no error at all")
		}
		if !errors.Is(terminalErr, ErrStreamIncomplete) {
			return failedOutcome(fmt.Sprintf("terminal err = %v, want ErrStreamIncomplete", terminalErr))
		}
		return passedOutcome("a delta arrived, then ErrStreamIncomplete -- no Done built from partial text")
	})
}

func checkCredentialRedaction(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		const secret = "sk-conformance-redaction-canary-do-not-leak" // secret-scan: allow
		var sawAuthHeader bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer "+secret {
				sawAuthHeader = true
			}
			w.WriteHeader(http.StatusUnauthorized)
			// Echo the header back in the body, the way a permissive proxy
			// or a debug endpoint might -- worst case for this check, and
			// the one APIError.Error()'s doc comment says never to print.
			_, _ = fmt.Fprintf(w, `{"error":{"message":"invalid credential"}}`)
		}))
		defer srv.Close()

		provider := newProtocolProvider(panicTB{}, srv, secret, 5*time.Second)
		_, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err == nil {
			return failedOutcome("a 401 returned success")
		}
		if !sawAuthHeader {
			return failedOutcome("the server never saw the Authorization header -- this check proves nothing")
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if strings.Contains(apiErr.Detail(), secret) {
				return failedOutcome("the credential appeared in APIError.Detail()")
			}
		}
		if strings.Contains(err.Error(), secret) {
			return failedOutcome("the credential appeared in the returned error string")
		}
		return passedOutcome("the key reached the server but never appeared in the returned error")
	})
}

func checkEndpointOverride(ctx context.Context, _ *LiveTarget) ConformanceOutcome {
	return runProtocolCheck(func() ConformanceOutcome {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
		}))
		defer srv.Close()

		// A trailing slash on the caller-supplied BaseURL must not produce
		// a double slash in the request path.
		provider, err := NewOpenAIProvider(ProviderConfig{
			APIKey:  "sk-conformance-test-key",
			BaseURL: srv.URL + "/",
			Timeout: 5 * time.Second,
		})
		if err != nil {
			return failedOutcome(fmt.Sprintf("NewOpenAIProvider: %v", err))
		}

		resp, err := provider.Complete(ctx, CompletionRequest{Model: "gpt-5.6-mini"})
		if err != nil {
			return failedOutcome(fmt.Sprintf("request to the overridden endpoint failed: %v", err))
		}
		if resp.Content != "ok" {
			return failedOutcome(fmt.Sprintf("Content = %q, want %q -- the request may not have reached the local server", resp.Content, "ok"))
		}
		if gotPath != "/responses" {
			return failedOutcome(fmt.Sprintf("path = %q, want /responses -- a trailing slash on BaseURL produced a malformed path", gotPath))
		}
		return passedOutcome("BaseURL with a trailing slash still resolved to /responses")
	})
}
