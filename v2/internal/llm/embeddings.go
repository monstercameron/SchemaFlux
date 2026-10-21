package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// PS-006. Similar, CheckingSimilarity, Clustering, Deduplicate, and Matching
// all have cheap, deterministic vector implementations -- the maths live in
// internal/ops/vector.go and need no provider at all. What a provider has to
// supply is the vector itself, and that is what this file adds: a capability
// a Provider may or may not have, following the same shape StreamingProvider
// already established (provider.go) rather than adding a required method to
// Provider, which would break every implementation in this repository,
// schemafluxtest's and mw's included.

// EmbeddingRequest asks a provider to turn text into vectors.
type EmbeddingRequest struct {
	// Model names the embedding model. Left empty, an
	// implementation of Embed falls back to a documented default rather than
	// rejecting the request -- the caller asked for "an embedding", not
	// necessarily a specific model.
	Model string

	// Input is the batch of strings to embed, one vector per entry, in
	// order. A provider that cannot preserve that order is not usable here
	// -- see Embed's ordering note on OpenAIProvider.
	Input []string
}

// EmbeddingResponse carries one vector per EmbeddingRequest.Input entry, in
// the same order Input was given in -- Vectors[i] is the embedding of
// Input[i], not a value keyed some other way the caller has to re-sort.
type EmbeddingResponse struct {
	Vectors  [][]float64
	Model    string
	Provider string
	Usage    types.TokenUsage
}

// DefaultOpenAIEmbeddingModel is used when an EmbeddingRequest leaves Model
// empty. It is OpenAI's smallest current embedding model -- cheap enough
// that PS-006's whole point (replacing an LLM round trip with vector maths)
// is not undone by the embedding call itself being expensive.
const DefaultOpenAIEmbeddingModel = "text-embedding-3-small"

// openAIEmbeddingRequestBody is the wire shape of a POST /v1/embeddings
// request.
type openAIEmbeddingRequestBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// openAIEmbeddingResponseBody is the wire shape of a POST /v1/embeddings
// response. Index is what lets Embed restore input order regardless of
// what order the entries arrive in the array -- OpenAI's documented
// behavior is that they arrive in request order already, but the field
// exists on the wire for exactly this reason, and trusting array position
// over the field the API provides for the purpose is the wrong place to
// save a sort.
type openAIEmbeddingResponseBody struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed turns req.Input into vectors with OpenAI's POST /v1/embeddings.
func (provider *OpenAIProvider) Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error) {
	if len(req.Input) == 0 {
		return EmbeddingResponse{}, fmt.Errorf("openai: embedding request carried no input")
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultOpenAIEmbeddingModel
	}

	ctx, cancel := callContext(ctx, provider.config)
	defer cancel()

	url := "https://api.openai.com/v1/embeddings"
	if provider.config.BaseURL != "" {
		url = strings.TrimRight(provider.config.BaseURL, "/") + "/embeddings"
	}

	jsonData, err := json.Marshal(openAIEmbeddingRequestBody{Model: model, Input: req.Input})
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("failed to create embedding request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.config.APIKey)
	if provider.config.OrgID != "" {
		httpReq.Header.Set("OpenAI-Organization", provider.config.OrgID)
	}

	resp, err := provider.httpClient.Do(httpReq)
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("OpenAI embedding request failed: %w", err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return EmbeddingResponse{}, statusError("openai", model, resp, provider.config)
	}

	raw, err := readBounded(resp.Body, maxEmbeddingBodyBytes)
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("failed to read embedding response: %w", err)
	}
	var body openAIEmbeddingResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return EmbeddingResponse{}, fmt.Errorf("failed to decode embedding response: %w", err)
	}

	if len(body.Data) != len(req.Input) {
		return EmbeddingResponse{}, fmt.Errorf(
			"openai: embedding response carried %d vectors for %d inputs", len(body.Data), len(req.Input))
	}

	// Sort by the response's own Index rather than trusting array position,
	// so a vendor response ordered any other way still lines up with
	// req.Input by the field that is actually the contract.
	sort.Slice(body.Data, func(i, j int) bool { return body.Data[i].Index < body.Data[j].Index })

	vectors := make([][]float64, len(body.Data))
	for _, entry := range body.Data {
		if entry.Index < 0 || entry.Index >= len(req.Input) {
			return EmbeddingResponse{}, fmt.Errorf("openai: embedding response index %d out of range for %d inputs", entry.Index, len(req.Input))
		}
		vectors[entry.Index] = entry.Embedding
	}
	// Every input gets a vector of the same, non-zero length. An empty vector is
	// not an embedding of anything, and vectors of different lengths cannot be
	// compared with each other, which is all a caller does with a set of them.
	for i, v := range vectors {
		if v == nil {
			return EmbeddingResponse{}, fmt.Errorf("openai: embedding response missing vector for input %d", i)
		}
		if len(v) == 0 {
			return EmbeddingResponse{}, fmt.Errorf("openai: embedding response carried an empty vector for input %d", i)
		}
		if len(v) != len(vectors[0]) {
			return EmbeddingResponse{}, fmt.Errorf("openai: embedding response carried vectors of %d and %d dimensions", len(vectors[0]), len(v))
		}
	}

	return EmbeddingResponse{
		Vectors:  vectors,
		Model:    body.Model,
		Provider: provider.Name(),
		Usage: types.TokenUsage{
			PromptTokens: body.Usage.PromptTokens,
			TotalTokens:  body.Usage.TotalTokens,
		},
	}, nil
}
