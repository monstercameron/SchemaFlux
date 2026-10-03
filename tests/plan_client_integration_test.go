package tests

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// plannedClientProvider is a named provider that answers any request and counts
// them, so a test can tell which client a batch was sent to.
type plannedClientProvider struct {
	name  string
	calls atomic.Int32
}

func (p *plannedClientProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.calls.Add(1)
	return schemaflux.CompletionResponse{Content: "ignored: Merge does not read the body", Provider: p.name, Model: req.Model, FinishReason: "stop"}, nil
}
func (p *plannedClientProvider) Name() string                                      { return p.name }
func (p *plannedClientProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *plannedClientProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// RunOpMany plans for the client on its context. Two clients with different
// models and context windows get the same items cut into different chunks, each
// sent to its own provider, and a context with no client is planned for the
// process default as before. Planning used to ask the process default whatever
// the context carried, so a client with a small model got chunks cut for a large
// one.
func TestIntegrationRunOpManyPlansForTheClientOnItsContext(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	// An item is 3 tokens, and costs 8 more of protocol (its id is 14 characters
	// long) and 24 of answer in a chunk, and every request carries the batch's
	// system prompt, 9 tokens, and the reinforcement block the call path puts in
	// front of it, 54 tokens, which the window has to hold too: a window of 4000 holds
	// 112 items and a window of 2000 holds 55. (Before the planner counted the block
	// and the ids were sequential, they held 120 and 60.)
	llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "planned-small", Model: "small-model", ContextWindow: 2000})
	llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "planned-large", Model: "large-model", ContextWindow: 4000})

	previous := schemaflux.GetDefaultClient()
	t.Cleanup(func() { schemaflux.SetDefaultClient(previous) })
	small, large := &plannedClientProvider{name: "planned-small"}, &plannedClientProvider{name: "planned-large"}
	smallCtx := schemaflux.NewClient("k").WithProviderInstance(small).WithModel("small-model").Context(context.Background())
	largeCtx := schemaflux.NewClient("k").WithProviderInstance(large).WithModel("large-model").Context(context.Background())
	process := testfixtures.WithScriptedProvider(t, "ignored: Merge does not read the body", nil)

	items := make([]string, 200)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}
	req := schemaflux.PlanRequest{Options: schemaflux.OpOptions{MaxOutputTokens: 100000}}

	cases := []struct {
		name       string
		ctx        context.Context
		wantChunks []int
		wantModel  string
		calls      *atomic.Int32
	}{
		{"the small client", smallCtx, []int{55, 55, 55, 35}, "small-model", &small.calls},
		{"the large client", largeCtx, []int{112, 88}, "large-model", &large.calls},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.calls.Load()
			results, plan, err := schemaflux.RunOpMany(tc.ctx, batchEchoOp(), items, req)
			if err != nil {
				t.Fatalf("RunOpMany: %v", err)
			}
			if len(results) != len(items) || results[0].Value != "GOT: item-000" || results[199].Value != "GOT: item-199" {
				t.Fatalf("results are not one per item in order: %d of them", len(results))
			}
			var sizes []int
			for _, chunk := range plan.Chunks {
				sizes = append(sizes, chunk.ItemCount)
			}
			if fmt.Sprint(sizes) != fmt.Sprint(tc.wantChunks) {
				t.Errorf("chunks = %v, want %v", sizes, tc.wantChunks)
			}
			if plan.Capability.Model != tc.wantModel {
				t.Errorf("the plan is for model %q, want %q", plan.Capability.Model, tc.wantModel)
			}
			if sent := int(tc.calls.Load() - before); sent != len(tc.wantChunks) {
				t.Errorf("the client's provider got %d requests, want one per chunk (%d)", sent, len(tc.wantChunks))
			}
		})
	}

	t.Run("no client on the context", func(t *testing.T) {
		_, plan, err := schemaflux.RunOpMany(context.Background(), batchEchoOp(), items, req)
		if err != nil {
			t.Fatalf("RunOpMany: %v", err)
		}
		if len(plan.Chunks) != 1 || plan.Chunks[0].ItemCount != 200 {
			t.Errorf("chunks = %+v, want one of 200 for the process default", plan.Chunks)
		}
		if plan.Capability.Provider != "local" {
			t.Errorf("the plan is for %q, want the process default", plan.Capability.Provider)
		}
		if got := len(process.Requests()); got != 1 {
			t.Errorf("the process default got %d requests, want 1", got)
		}
	})

	t.Run("Preflight has no context and plans for the process default", func(t *testing.T) {
		plan, err := schemaflux.Preflight(batchEchoOp(), items, req)
		if err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		if len(plan.Chunks) != 1 || plan.Capability.Provider != "local" {
			t.Errorf("Preflight planned %d chunks for %q, want one for the default", len(plan.Chunks), plan.Capability.Provider)
		}
	})
}
