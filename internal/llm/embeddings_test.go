package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// nonEmbeddingProvider implements Provider (the minimum), and deliberately
// nothing more -- it stands in for every Provider in this repository that
// predates PS-006 and has no Embed method.
type nonEmbeddingProvider struct{ calls int }

func (p *nonEmbeddingProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	p.calls++
	return CompletionResponse{}, nil
}
func (p *nonEmbeddingProvider) Name() string                               { return "non-embedding" }
func (p *nonEmbeddingProvider) EstimateCost(req CompletionRequest) float64 { return 0 }
func (p *nonEmbeddingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// TestRequestEmbeddingReportsUnsupportedCapability proves a provider that
// cannot embed is refused with KindUnsupportedCapability rather than
// silently degrading into a chat call -- the exact failure mode PS-006
// warns against (an embedding-similarity number and a model's self-reported
// score are different kinds of number, and falling back would blur them).
func TestRequestEmbeddingReportsUnsupportedCapability(t *testing.T) {
	provider := &nonEmbeddingProvider{}
	_, err := RequestEmbedding(context.Background(), provider, EmbeddingRequest{Input: []string{"hello"}})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got := types.KindOf(err); got != types.KindUnsupportedCapability {
		t.Errorf("KindOf(err) = %v, want KindUnsupportedCapability", got)
	}
	// Never a fallback: Complete must not have been called on the way to
	// reporting the capability is missing.
	if provider.calls != 0 {
		t.Errorf("Complete was called %d times; RequestEmbedding must never fall back to a chat call", provider.calls)
	}
}

// TestRequestEmbeddingNilProviderIsConfiguration proves a nil provider is
// reported as a configuration problem, distinct from KindUnsupportedCapability
// -- the caller has not chosen a provider at all, rather than having chosen
// one that lacks the capability.
func TestRequestEmbeddingNilProviderIsConfiguration(t *testing.T) {
	_, err := RequestEmbedding(context.Background(), nil, EmbeddingRequest{Input: []string{"hello"}})
	if got := types.KindOf(err); got != types.KindConfiguration {
		t.Errorf("KindOf(err) = %v, want KindConfiguration", got)
	}
}

// fakeEmbeddingProvider implements EmbeddingProvider directly (no network),
// so tests of RequestEmbedding's happy path make zero HTTP calls.
type fakeEmbeddingProvider struct{ nonEmbeddingProvider }

func (p *fakeEmbeddingProvider) Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error) {
	vectors := make([][]float64, len(req.Input))
	for i := range req.Input {
		vectors[i] = []float64{float64(i), 1}
	}
	return EmbeddingResponse{Vectors: vectors, Provider: p.Name(), Model: "fake"}, nil
}

// TestRequestEmbeddingDispatchesToCapableProvider proves a provider that
// does implement EmbeddingProvider is used directly, with the response
// passed through unchanged.
func TestRequestEmbeddingDispatchesToCapableProvider(t *testing.T) {
	provider := &fakeEmbeddingProvider{}
	resp, err := RequestEmbedding(context.Background(), provider, EmbeddingRequest{Input: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("RequestEmbedding: %v", err)
	}
	if len(resp.Vectors) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(resp.Vectors))
	}
	if resp.Vectors[1][0] != 1 {
		t.Errorf("expected vector 1 to preserve its identity, got %v", resp.Vectors[1])
	}
}

// newEmbeddingTestServer returns an httptest.Server that decodes the
// request body into capturedBody and answers with responseBody -- mirrors
// newCapturingServer in toolcalling_test.go, kept separate because the
// embeddings endpoint has its own request/response shape.
func newEmbeddingTestServer(t *testing.T, responseBody string, capturedBody *openAIEmbeddingRequestBody) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capturedBody != nil {
			if err := json.NewDecoder(r.Body).Decode(capturedBody); err != nil {
				t.Errorf("failed to decode request body: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestOpenAIEmbedSendsInputAndDefaultModel proves a request with no Model
// set reaches the wire with DefaultOpenAIEmbeddingModel, and Input is sent
// verbatim.
func TestOpenAIEmbedSendsInputAndDefaultModel(t *testing.T) {
	var captured openAIEmbeddingRequestBody
	server := newEmbeddingTestServer(t, `{
		"data": [
			{"embedding": [1, 2, 3], "index": 0},
			{"embedding": [4, 5, 6], "index": 1}
		],
		"model": "text-embedding-3-small",
		"usage": {"prompt_tokens": 4, "total_tokens": 4}
	}`, &captured)

	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"first", "second"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if captured.Model != DefaultOpenAIEmbeddingModel {
		t.Errorf("wire model = %q, want %q", captured.Model, DefaultOpenAIEmbeddingModel)
	}
	if len(captured.Input) != 2 || captured.Input[0] != "first" || captured.Input[1] != "second" {
		t.Errorf("wire input = %#v, want [first second]", captured.Input)
	}
	if len(resp.Vectors) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(resp.Vectors))
	}
	if resp.Vectors[0][0] != 1 || resp.Vectors[1][0] != 4 {
		t.Errorf("vectors not in expected order: %#v", resp.Vectors)
	}
}

// TestOpenAIEmbedRestoresOrderFromIndex proves Embed lines a response's
// vectors up by the wire "index" field rather than trusting array position
// -- a server that answers out of order must still hand the caller
// Vectors[i] for req.Input[i].
func TestOpenAIEmbedRestoresOrderFromIndex(t *testing.T) {
	server := newEmbeddingTestServer(t, `{
		"data": [
			{"embedding": [9, 9], "index": 1},
			{"embedding": [1, 1], "index": 0}
		],
		"model": "text-embedding-3-small",
		"usage": {"prompt_tokens": 2, "total_tokens": 2}
	}`, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"first", "second"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if resp.Vectors[0][0] != 1 {
		t.Errorf("Vectors[0] = %v, want the vector for index 0 ([1 1])", resp.Vectors[0])
	}
	if resp.Vectors[1][0] != 9 {
		t.Errorf("Vectors[1] = %v, want the vector for index 1 ([9 9])", resp.Vectors[1])
	}
}

// TestOpenAIEmbedMismatchedCountIsAnError proves a response carrying a
// different number of vectors than inputs is a failure, not a result the
// caller has to notice is short by counting it themselves.
func TestOpenAIEmbedMismatchedCountIsAnError(t *testing.T) {
	server := newEmbeddingTestServer(t, `{
		"data": [{"embedding": [1, 2], "index": 0}],
		"model": "text-embedding-3-small",
		"usage": {"prompt_tokens": 2, "total_tokens": 2}
	}`, nil)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"first", "second"}})
	if err == nil {
		t.Fatal("expected an error for a mismatched vector count, got nil")
	}
}

// TestOpenAIEmbedEmptyInputIsAnError proves an empty Input is refused
// before any request is sent -- a zero-length batch has no defined
// embedding to ask for.
func TestOpenAIEmbedEmptyInputIsAnError(t *testing.T) {
	provider := newTestOpenAIProvider(t, "http://unused.invalid")
	_, err := provider.Embed(context.Background(), EmbeddingRequest{})
	if err == nil {
		t.Fatal("expected an error for empty input, got nil")
	}
}

// TestOpenAIEmbedNon200IsClassifiedError proves a non-200 status is
// reported through the same NewAPIError/rate-limit path Complete uses,
// rather than a bare decode failure once the body turns out not to be an
// embedding response.
func TestOpenAIEmbedNon200IsClassifiedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "invalid model"}}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	_, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}})
	if err == nil {
		t.Fatal("expected an error for a 400 response, got nil")
	}
}

// TestEmbeddingProviderIsAStreamingProviderRelative documents, rather than
// tests behavior of, the two capability interfaces' independence: an
// OpenAIProvider implements both EmbeddingProvider and StreamingProvider,
// but nothing about implementing one requires implementing the other -- see
// nonEmbeddingProvider, which is a Provider and nothing else.
func TestEmbeddingProviderIsAStreamingProviderRelative(t *testing.T) {
	var _ EmbeddingProvider = (*OpenAIProvider)(nil)
	var _ StreamingProvider = (*OpenAIProvider)(nil)
	var _ Provider = (*nonEmbeddingProvider)(nil)
}
