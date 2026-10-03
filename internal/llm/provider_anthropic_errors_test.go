package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Every Anthropic failure used to be a plain fmt.Errorf carrying the response
// body. Classify found no type in it and fell back to matching the text, so a
// 401 or a 400 came out as KindUnknown, which the retry loop resends for its
// whole budget; a 429's Retry-After was lost; and the body, which can quote the
// caller's input, landed in the error string.

func anthropicStatusServer(t *testing.T, status int, headers map[string]string, body string) *AnthropicProvider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return newTestAnthropicProvider(t, server.URL)
}

func TestAnthropicNon200IsATypedClassifiedError(t *testing.T) {
	const body = `{"type":"error","error":{"type":"invalid_request_error","message":"synthetic message"}}`

	cases := []struct {
		status    int
		kind      types.ErrorKind
		retryable bool
	}{
		{http.StatusBadRequest, types.KindInvalidRequest, false},
		{http.StatusUnauthorized, types.KindAuthentication, false},
		{http.StatusForbidden, types.KindPermission, false},
		{http.StatusNotFound, types.KindInvalidRequest, false},
		{http.StatusRequestEntityTooLarge, types.KindContextTooLarge, false},
		{http.StatusUnprocessableEntity, types.KindInvalidRequest, false},
		{http.StatusTooManyRequests, types.KindRateLimited, true},
		{http.StatusInternalServerError, types.KindProviderUnavailable, true},
		{http.StatusBadGateway, types.KindProviderUnavailable, true},
		{http.StatusServiceUnavailable, types.KindProviderUnavailable, true},
		{http.StatusGatewayTimeout, types.KindTimeout, true},
		{529, types.KindProviderUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			provider := anthropicStatusServer(t, tc.status, nil, body)
			resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
			if err == nil {
				t.Fatal("a non-200 answer must be an error")
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result came back beside the error: %+v", resp)
			}
			if got := Classify(err); got != tc.kind {
				t.Errorf("Classify = %v, want %v (error: %v)", got, tc.kind, err)
			}
			if status, ok := StatusCodeFrom(err); !ok || status != tc.status {
				t.Errorf("StatusCodeFrom = %d, %v; want %d", status, ok, tc.status)
			}
			retryable := (&types.OperationError{Kind: Classify(err)}).Retryable()
			if retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", retryable, tc.retryable)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Message != "synthetic message" || apiErr.Type != "invalid_request_error" {
				t.Errorf("the vendor's message and type were lost: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic message") {
				t.Errorf("the vendor's message is in the error's text: %v", err)
			}
		})
	}
}

func TestAnthropicRateLimitKeepsRetryAfter(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			provider := anthropicStatusServer(t, status, map[string]string{"Retry-After": "17"}, `{"error":{"message":"slow down"}}`)
			_, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
			wait, ok := RetryAfterFrom(err)
			if !ok || wait != 17*time.Second {
				t.Errorf("RetryAfterFrom = %v, %v; want 17s, true (error: %v)", wait, ok, err)
			}
		})
	}
}

// The body is the caller's input as often as it is an explanation. It stays on
// the error for a caller who asks for it and is not printed.
func TestAnthropicErrorDoesNotPrintTheBody(t *testing.T) {
	const quoted = "PATIENT-RECORD-4471 diagnosis follows"
	provider := anthropicStatusServer(t, http.StatusBadRequest, nil, `<html>`+quoted+`</html>`)
	_, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), quoted) {
		t.Errorf("the response body is in the error string: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, quoted) {
		t.Errorf("the body should be retained on the APIError: %#v", err)
	}
}

// Anthropic's own text for a bad thinking budget says "budget", which the text
// fallback in Classify read as the caller's spend limit.
func TestAnthropicBudgetWordInTheBodyIsNotABudgetError(t *testing.T) {
	provider := anthropicStatusServer(t, http.StatusBadRequest, nil,
		`{"error":{"type":"invalid_request_error","message":"max_tokens must be greater than thinking.budget_tokens"}}`)
	_, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if got := Classify(err); got != types.KindInvalidRequest {
		t.Errorf("Classify = %v, want %v", got, types.KindInvalidRequest)
	}
}

func TestAnthropicErrorBodyReadIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		chunk := []byte(strings.Repeat("x", 64<<10))
		for written := 0; written < 4*maxErrorBodyBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	provider := newTestAnthropicProvider(t, server.URL)
	_, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an *APIError, got %T %v", err, err)
	}
	if len(apiErr.Body) > maxErrorBodyBytes {
		t.Errorf("the error body holds %d bytes, over the %d ceiling", len(apiErr.Body), maxErrorBodyBytes)
	}
}

// Anthropic's buffered answer is read under the same ceiling as the others.
func TestAnthropicBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"`))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for written := 0; written <= maxResponseBodyBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`"}]}`))
	}))
	defer server.Close()
	provider := newTestAnthropicProvider(t, server.URL)
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "claude-x", UserPrompt: "x"})
	if err == nil {
		t.Fatalf("a body past the ceiling was accepted: %d bytes of content", len(resp.Content))
	}
	if got := Classify(err); got != types.KindMalformedOutput {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindMalformedOutput, err)
	}
}

func TestAnthropicAnswerWithNoTextIsClassified(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty content list", `{"content":[],"stop_reason":"end_turn","model":"m"}`},
		{"no content key", `{"stop_reason":"end_turn","model":"m"}`},
		{"only a tool use block", `{"content":[{"type":"tool_use","name":"t"}],"stop_reason":"end_turn","model":"m"}`},
		{"only an empty text block", `{"content":[{"type":"text","text":""}],"stop_reason":"end_turn","model":"m"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := anthropicStatusServer(t, http.StatusOK, nil, tc.body)
			resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
			if err == nil {
				t.Fatalf("an answer with no text was returned as a success: %+v", resp)
			}
			if !isEmptyResponse(resp) {
				t.Errorf("a result came back beside the error: %+v", resp)
			}
			if !errors.Is(err, ErrNoMessageOutput) {
				t.Errorf("the error does not say there was no message: %v", err)
			}
		})
	}
}

// A 200 that does not decode must not return a half-built result.
func TestAnthropicUndecodableAnswerIsAnEmptyResultBesideAnError(t *testing.T) {
	provider := anthropicStatusServer(t, http.StatusOK, nil, `not json`)
	resp, err := provider.Complete(context.Background(), CompletionRequest{UserPrompt: "x"})
	if err == nil || !isEmptyResponse(resp) {
		t.Fatalf("got %+v, %v; want an empty result and an error", resp, err)
	}
}
