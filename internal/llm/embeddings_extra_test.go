package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOpenAIEmbedRateLimited proves a 429 from the embeddings endpoint is
// reported through the same RateLimitError path Complete uses, carrying the
// Retry-After the server sent -- Embed's isRateLimited branch was untested.
func TestOpenAIEmbedRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}})
	if err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	wait, ok := RetryAfterFrom(err)
	if !ok {
		t.Fatalf("err = %v, want a *RateLimitError recoverable via RetryAfterFrom", err)
	}
	if wait.Seconds() != 3 {
		t.Errorf("RetryAfter = %s, want 3s", wait)
	}
}

// TestOpenAIEmbedUndecodableBodyIsAnError proves a 200 whose body is not
// valid JSON at all fails to decode rather than silently producing an empty
// response.
func TestOpenAIEmbedUndecodableBodyIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json at all {{{"))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	if _, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}}); err == nil {
		t.Fatal("expected an error for an undecodable body")
	}
}

// TestOpenAIEmbedIndexOutOfRangeIsAnError proves a response whose "index"
// field names a slot outside the input range is refused rather than causing
// an out-of-bounds write into the vectors slice.
func TestOpenAIEmbedIndexOutOfRangeIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [{"embedding": [1, 2], "index": 5}],
			"model": "text-embedding-3-small",
			"usage": {"prompt_tokens": 1, "total_tokens": 1}
		}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"only-one"}})
	if err == nil {
		t.Fatal("expected an error for an out-of-range index")
	}
}

// TestOpenAIEmbedMissingVectorIsAnError proves a response that names the
// right count of vectors but skips an index (leaving a nil slot after
// sorting) is refused rather than handing the caller a nil vector silently.
//
// Two entries claiming the SAME index satisfies "len(data) == len(input)"
// while leaving one input's slot never written -- the len-mismatch check
// earlier in Embed cannot catch this, only the missing-vector scan can.
func TestOpenAIEmbedMissingVectorIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [{"embedding": [1, 2], "index": 0}, {"embedding": [3, 4], "index": 0}],
			"model": "text-embedding-3-small",
			"usage": {"prompt_tokens": 2, "total_tokens": 2}
		}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"first", "second"}})
	if err == nil {
		t.Fatal("expected an error for a response missing a vector for one input")
	}
}
