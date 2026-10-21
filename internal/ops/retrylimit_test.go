package ops

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// retryingProvider answers every request and tells the call path its own retry
// settings.
type retryingProvider struct {
	retries int
	calls   int
}

func (p *retryingProvider) Name() string { return "retrying-edge" }

func (p *retryingProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	return llm.CompletionResponse{Content: "answer", FinishReason: "stop"}, nil
}

func (p *retryingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (p *retryingProvider) RetryPolicy() (int, time.Duration) { return p.retries, time.Millisecond }

func TestARetryCountAtTheTopOfTheIntegersStillSendsTheRequestOnce(t *testing.T) {
	t.Parallel()
	for _, retries := range []int{MaxRetries, MaxRetries + 1, 1 << 40, math.MaxInt - 1, math.MaxInt} {
		provider := &retryingProvider{retries: retries}
		got, err := CallLLM(context.Background(), provider, "system", "user", types.OpOptions{Model: "m"})
		if err != nil || got != "answer" {
			t.Errorf("retries %d: got %q, %v", retries, got, err)
		}
		if provider.calls != 1 {
			t.Errorf("retries %d: %d requests were sent, want 1", retries, provider.calls)
		}
	}
}
