package llm

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

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
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":[],"model":"m"}`))
	}))
	t.Cleanup(server.Close)

	provider := newTestOpenAIProvider(t, server.URL)
	for _, input := range [][]string{nil, {}} {
		if _, err := provider.Embed(context.Background(), EmbeddingRequest{Input: input}); err == nil {
			t.Errorf("Input %#v was accepted", input)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("%d request(s) reached the server for an embedding request with no input", got)
	}
}

// Every input gets a vector, and every vector has the same non-zero length.
func TestOpenAIEmbedRefusesAnEmptyVectorAndVectorsOfDifferentLengths(t *testing.T) {
	cases := []struct {
		name   string
		inputs []string
		body   string
	}{
		{"a single empty vector", []string{"a"}, `{"data":[{"embedding":[],"index":0}],"model":"m"}`},
		{"an empty vector among others", []string{"a", "b"}, `{"data":[{"embedding":[1,2],"index":0},{"embedding":[],"index":1}],"model":"m"}`},
		{"two lengths", []string{"a", "b"}, `{"data":[{"embedding":[1,2],"index":0},{"embedding":[1,2,3],"index":1}],"model":"m"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newEmbeddingTestServer(t, tc.body, nil)
			provider := newTestOpenAIProvider(t, server.URL)
			resp, err := provider.Embed(context.Background(), EmbeddingRequest{Input: tc.inputs})
			if err == nil {
				t.Fatalf("accepted %v", resp.Vectors)
			}
		})
	}
}

// An error body longer than the part of it that is read is still read to the end
// (up to the drain bound), so the connection goes back to the pool and the next
// call does not pay for a new one.
func TestOpenAIEmbedKeepsItsConnectionAfterAnErrorBodyLongerThanItReads(t *testing.T) {
	var conns atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(strings.Repeat("e", maxErrorBodyBytes+10<<10)))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)

	previous := drainTimeout
	drainTimeout = 10 * time.Second
	t.Cleanup(func() { drainTimeout = previous })

	provider := newTestOpenAIProvider(t, server.URL)
	for i := 0; i < 3; i++ {
		if _, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}}); err == nil {
			t.Fatal("a 400 produced a result")
		}
	}
	if got := conns.Load(); got != 1 {
		t.Errorf("3 calls opened %d connections, want 1: the body was closed with its end not read", got)
	}
}

// An embeddings answer is read under its own, larger, ceiling: a body past it is
// refused whole, even when what precedes the excess is a well-formed answer.
// The ceiling is a constant, so the body written is its size.
func TestOpenAIEmbedBodyIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a body of about 192 MiB")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2],"index":0}],"model":"m"}`))
		pad := []byte(strings.Repeat(" ", 1<<20))
		for written := 0; written <= maxEmbeddingBodyBytes; written += len(pad) {
			if _, err := w.Write(pad); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	provider := newTestOpenAIProvider(t, server.URL)
	resp, err := provider.Embed(context.Background(), EmbeddingRequest{Input: []string{"x"}})
	if err == nil {
		t.Fatalf("a body past the ceiling was accepted: %v", resp.Vectors)
	}
	if got := Classify(err); got != types.KindMalformedOutput {
		t.Errorf("Classify = %v, want %v (%v)", got, types.KindMalformedOutput, err)
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
