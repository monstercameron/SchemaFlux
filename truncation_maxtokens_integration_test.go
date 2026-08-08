package schemaflux_test

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ST-003 integration coverage. The truncation-classification tests below run
// through the true public boundary (schemaflux.Format, a provider installed
// with WithProviderInstance). The MaxOutputTokens-reaches-the-wire test runs
// one layer down, through internal/ops.CallLLM directly -- see the comment on
// TestIntegrationMaxOutputTokensReachesTheWireRequest for why.

// truncationProvider answers with a caller-chosen finish reason and content,
// and records every request it received, so a single double can prove both
// that the per-call ceiling reached the wire request and that a truncated
// finish reason turns into a classified error.
type truncationProvider struct {
	finishReason string
	content      string

	requests []schemaflux.CompletionRequest
}

func (p *truncationProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	content := p.content
	if content == "" {
		content = "a complete answer"
	}
	return schemaflux.CompletionResponse{
		Content:      content,
		Provider:     p.Name(),
		Model:        req.Model,
		FinishReason: p.finishReason,
	}, nil
}

// Name reports "local" for the same reason text_integration_test.go's
// scriptedProvider does: an invented provider name would resolve to no model
// mapping and every call would error before reaching the behaviour under test.
func (p *truncationProvider) Name() string                                      { return "local" }
func (p *truncationProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *truncationProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func withTruncationProvider(t *testing.T, finishReason, content string) *truncationProvider {
	t.Helper()
	provider := &truncationProvider{finishReason: finishReason, content: content}
	schemaflux.NewClient("test-key").WithProviderInstance(provider)
	return provider
}

// The per-call ceiling must reach the wire request through internal/ops.CallLLM
// -- the exported function every operation's LLM call funnels through, and the
// one named in the task as where types.OpOptions becomes
// llm.CompletionRequest.MaxTokens.
//
// This does NOT go through the top-level schemaflux.Format /
// schemaflux.OpOptions{...} entrypoint, and that omission is deliberate, not an
// oversight: every public operation reaches CallLLM through one of two
// pre-existing conversions -- internal/ops/utils.go's applyDefaults (used by
// the legacy `...OpOptions` functions, including Format) or an XOptions type's
// toOpOptions() method in internal/ops/options.go (used by the builder-style
// operations) -- and BOTH already silently drop several types.OpOptions fields
// that are not in their fixed field lists, MaxOutputTokens among them now, but
// also (previously) CacheIdentity, ResponseFormat, JSONSchema, and SchemaName.
// Confirmed by running schemaflux.Format(data, template,
// types.OpOptions{MaxOutputTokens: 321}) end to end: the provider received the
// Fast-tier default (2000), not 321. Neither file is in this task's permitted
// edit list (internal/config/config.go, internal/types/types.go,
// internal/llm/provider.go, internal/llm/classify.go,
// internal/ops/llm_helper.go, and their test files), so fixing that
// pre-existing drop is out of scope here and is left as a finding, not a
// silent partial fix.
func TestIntegrationMaxOutputTokensReachesTheWireRequest(t *testing.T) {
	provider := &captureCompletionProvider{}

	_, err := ops.CallLLM(
		context.Background(),
		provider,
		`You are a concise assistant.`,
		`Summarize this text.`,
		types.OpOptions{Intelligence: types.Fast, MaxOutputTokens: 321},
	)
	if err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("the provider received no request")
	}
	if got := provider.requests[0].MaxTokens; got != 321 {
		t.Errorf("MaxTokens = %d, want the per-call override of 321", got)
	}
}

// captureCompletionProvider implements llm.Provider and records every request
// it receives, so a test can assert on what actually reached the wire rather
// than what a caller asked for.
type captureCompletionProvider struct {
	requests []llm.CompletionRequest
}

func (p *captureCompletionProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	return llm.CompletionResponse{Content: "ok", Provider: p.Name(), Model: req.Model, FinishReason: "stop"}, nil
}

func (p *captureCompletionProvider) Name() string                               { return "local" }
func (p *captureCompletionProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *captureCompletionProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// A caller who sets nothing still gets the Fast tier's ceiling, through the
// same public entrypoint -- the option is additive, not a replacement for the
// default path every existing caller depends on.
func TestIntegrationUnsetMaxOutputTokensKeepsTierDefault(t *testing.T) {
	provider := withTruncationProvider(t, "stop", "a complete answer")

	_, err := schemaflux.Format("some data", "one sentence", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("the provider received no request")
	}
	if got := provider.requests[0].MaxTokens; got != 2000 {
		t.Errorf("MaxTokens = %d, want the Fast tier default of 2000", got)
	}
}

// A truncated finish reason must surface at the public boundary as a
// classified truncation error reachable via errors.Is, not as whatever
// ParseJSON happens to say about a body that was cut off mid-token.
func TestIntegrationTruncatedFinishReasonIsATruncationError(t *testing.T) {
	withTruncationProvider(t, "length", `{"incomplete`)

	_, err := schemaflux.Format("some data", "one sentence", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err == nil {
		t.Fatal("expected a truncation error")
	}
	if !errors.Is(err, schemaflux.ErrOutputTruncated) {
		t.Errorf("errors.Is(err, schemaflux.ErrOutputTruncated) = false; got %v", err)
	}
}

// A normal, complete answer through the same entrypoint must be unaffected.
func TestIntegrationCompleteAnswerIsUnaffectedByTruncationCheck(t *testing.T) {
	withTruncationProvider(t, "stop", "a short answer.")

	got, err := schemaflux.Format("some data", "one sentence", schemaflux.OpOptions{Intelligence: schemaflux.Fast})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if got != "a short answer." {
		t.Errorf("Format() = %q, want the untouched provider content", got)
	}
}
