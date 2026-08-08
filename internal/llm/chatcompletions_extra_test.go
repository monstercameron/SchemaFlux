package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCompatibleProviderRateLimited proves a 429 from a chat-completions
// dialect vendor is reported through the RateLimitError path -- the
// isRateLimited branch inside completeViaChatCompletions was untested by the
// existing "surfaces the error body" (400) and "undecodable body" (502)
// cases, neither of which is a rate-limited status.
func TestCompatibleProviderRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"too many requests"}}`))
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider("testvendor", ProviderConfig{
		APIKey: "k", BaseURL: server.URL, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}

	_, err = provider.Complete(context.Background(), CompletionRequest{Model: "m", UserPrompt: "x"})
	if err == nil {
		t.Fatal("a 429 must be an error")
	}
	wait, ok := RetryAfterFrom(err)
	if !ok {
		t.Fatalf("err = %v, want a *RateLimitError recoverable via RetryAfterFrom", err)
	}
	if wait.Seconds() != 7 {
		t.Errorf("RetryAfter = %s, want 7s", wait)
	}
}
