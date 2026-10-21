package ops

import (
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Round 13. A client's defaults are preferences.

// lat13StreamProvider is a streaming provider that keeps the request it was
// given.
type lat13StreamProvider struct {
	mu  sync.Mutex
	req llm.CompletionRequest
}

func (p *lat13StreamProvider) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, io.ErrUnexpectedEOF
}
func (p *lat13StreamProvider) Name() string                               { return "local" }
func (p *lat13StreamProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *lat13StreamProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }
func (p *lat13StreamProvider) CompleteStream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamChunk, error] {
	p.mu.Lock()
	p.req = req
	p.mu.Unlock()
	return func(yield func(llm.StreamChunk, error) bool) {
		if !yield(llm.StreamChunk{Delta: "text"}, nil) {
			return
		}
		yield(llm.StreamChunk{Done: true, Response: llm.CompletionResponse{Content: "text", FinishReason: "stop", Model: req.Model, Provider: "local"}}, nil)
	}
}

func (p *lat13StreamProvider) request() llm.CompletionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// The calls that stream use the same helpers for the model and the effort as
// the buffered ones, and take a client's defaults the same way.
func TestAClientsDefaultsReachTheStreamingPath(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_FAST", "tier-fast")
	cases := []struct {
		name      string
		client    ExecConfig
		opts      types.OpOptions
		model     string
		effort    string
		preferred bool
	}{
		{"nothing set: the tier's model, no effort", ExecConfig{}, types.OpOptions{Intelligence: types.Fast}, "tier-fast", "", false},
		{"the client's model", ExecConfig{Model: "client-model"}, types.OpOptions{Intelligence: types.Fast}, "client-model", "", false},
		{"the client's model with space around it", ExecConfig{Model: "  client-model "}, types.OpOptions{Intelligence: types.Fast}, "client-model", "", false},
		{"the call's model over the client's", ExecConfig{Model: "client-model"}, types.OpOptions{Model: "call-model"}, "call-model", "", false},
		{"the call's model with space around it", ExecConfig{}, types.OpOptions{Model: " call-model "}, "call-model", "", false},
		{"the client's effort, a preference", ExecConfig{Effort: types.EffortLow}, types.OpOptions{Model: "m"}, "m", "low", true},
		{"the client's effort of none", ExecConfig{Effort: types.EffortNone}, types.OpOptions{Model: "m"}, "m", "none", true},
		{"the call's effort over the client's, a requirement", ExecConfig{Effort: types.EffortLow}, types.OpOptions{Model: "m", Effort: types.EffortHigh}, "m", "high", false},
		{"the call's effort alone", ExecConfig{}, types.OpOptions{Model: "m", Effort: types.EffortMedium}, "m", "medium", false},
		{"both of the client's", ExecConfig{Model: "client-model", Effort: types.EffortNone}, types.OpOptions{Intelligence: types.Fast}, "client-model", "none", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &lat13StreamProvider{}
			config := tc.client
			config.Provider = provider
			stream, err := streamLLM(WithExecConfig(context.Background(), &config), "system", "user", tc.opts)
			if err != nil {
				t.Fatalf("streamLLM: %v", err)
			}
			if _, err := stream.Text(); err != nil {
				t.Fatalf("the stream: %v", err)
			}
			req := provider.request()
			if req.Model != tc.model || req.ReasoningEffort != tc.effort || req.ReasoningEffortPreferred != tc.preferred {
				t.Errorf("the stream's request names model %q, effort %q (preferred %v); want %q, %q (%v)",
					req.Model, req.ReasoningEffort, req.ReasoningEffortPreferred, tc.model, tc.effort, tc.preferred)
			}
		})
	}
}

// requestEffort, directly.
func TestRequestEffortDirectly(t *testing.T) {
	cases := []struct {
		name      string
		client    types.Effort
		operation types.Effort
		call      types.Effort
		want      string
		preferred bool
	}{
		{"nothing", types.EffortDefault, types.EffortDefault, types.EffortDefault, "", false},
		{"the call's, a requirement", types.EffortDefault, types.EffortDefault, types.EffortHigh, "high", false},
		{"the call's none, a requirement", types.EffortDefault, types.EffortDefault, types.EffortNone, "none", false},
		{"the client's, a preference", types.EffortLow, types.EffortDefault, types.EffortDefault, "low", true},
		{"the client's none, a preference", types.EffortNone, types.EffortDefault, types.EffortDefault, "none", true},
		{"the operation's, a preference", types.EffortDefault, types.EffortMedium, types.EffortDefault, "medium", true},
		{"the client's over the operation's", types.EffortLow, types.EffortHigh, types.EffortDefault, "low", true},
		{"the call's over the client's", types.EffortLow, types.EffortDefault, types.EffortHigh, "high", false},
		{"the call's over the client's and the operation's", types.EffortLow, types.EffortNone, types.EffortMedium, "medium", false},
		{"the call's none over the client's high", types.EffortHigh, types.EffortDefault, types.EffortNone, "none", false},
		{"the client's high, the call's low: the call's", types.EffortHigh, types.EffortDefault, types.EffortLow, "low", false},
	}
	for _, tc := range cases {
		ctx := WithExecConfig(context.Background(), &ExecConfig{Effort: tc.client})
		ctx = preferEffort(ctx, tc.operation)
		effort, preferred := requestEffort(ctx, types.OpOptions{Effort: tc.call})
		if effort != tc.want || preferred != tc.preferred {
			t.Errorf("%s: %q (preferred %v), want %q (%v)", tc.name, effort, preferred, tc.want, tc.preferred)
		}
	}
	if effort, preferred := requestEffort(nil, types.OpOptions{}); effort != "" || preferred {
		t.Errorf("no context: %q, %v", effort, preferred)
	}
}

// A client that names an effort works on a provider that has none: the effort
// is dropped. The same effort named on a call is refused there.
func TestAClientsEffortDoesNotFailOnAProviderThatTakesNone(t *testing.T) {
	anthropicBody := `{"id":"msg_1","model":"claude-x","content":[{"type":"text","text":"{\"q1\":true,\"confidence\":0.9}"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	chatBody := `{"model":"m","choices":[{"message":{"content":"{\"q1\":true,\"confidence\":0.9}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	cases := []struct {
		name  string
		body  string
		build func(llm.ProviderConfig) (llm.Provider, error)
		model string
	}{
		{"anthropic", anthropicBody, func(c llm.ProviderConfig) (llm.Provider, error) { return llm.NewAnthropicProvider(c) }, "claude-x"},
		{"openrouter", chatBody, func(c llm.ProviderConfig) (llm.Provider, error) { return llm.NewOpenRouterProvider(c) }, "m"},
		{"deepseek", chatBody, func(c llm.ProviderConfig) (llm.Provider, error) { return llm.NewDeepSeekProvider(c) }, "m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider, err := tc.build(llm.ProviderConfig{APIKey: "k", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("building the provider: %v", err)
			}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Effort: types.EffortLow})

			if _, err := callLLM(ctx, "system", "user", types.OpOptions{Model: tc.model, ResponseFormat: "json"}); err != nil {
				t.Fatalf("a client's effort failed the call: %v", err)
			}
			mu.Lock()
			sent := len(bodies)
			mu.Unlock()
			if sent != 1 {
				t.Fatalf("%d requests reached the server", sent)
			}

			// Named on the call itself it is a requirement, and is refused before the
			// network.
			_, err = callLLM(ctx, "system", "user", types.OpOptions{Model: tc.model, ResponseFormat: "json", Effort: types.EffortLow})
			if err == nil || types.KindOf(err) != types.KindUnsupportedCapability {
				t.Fatalf("an effort named on the call was not refused as unsupported: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 1 {
				t.Errorf("the refused call reached the server: %d requests", len(bodies))
			}
		})
	}
}

// The model a call is planned against is the one it will use.
func TestThePlanNamesTheModelTheCallWillUse(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_FAST", "tier-fast")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_SMART", "tier-smart")
	clientCtx := func(model string) context.Context {
		return WithExecConfig(context.Background(), &ExecConfig{Model: model})
	}
	cases := []struct {
		name     string
		ctx      context.Context
		opts     types.OpOptions
		provider string
		want     string
	}{
		{"nothing named: the tier's", context.Background(), types.OpOptions{Intelligence: types.Fast}, "local", "tier-fast"},
		{"the smart tier's", context.Background(), types.OpOptions{Intelligence: types.Smart}, "local", "tier-smart"},
		{"the call's", context.Background(), types.OpOptions{Model: "call-model", Intelligence: types.Fast}, "local", "call-model"},
		{"the call's, with space around it", context.Background(), types.OpOptions{Model: "  call-model  "}, "local", "call-model"},
		{"the call's, whatever the provider", context.Background(), types.OpOptions{Model: "call-model"}, "anthropic", "call-model"},
		{"the call's, with no provider", context.Background(), types.OpOptions{Model: "call-model"}, "", "call-model"},
		{"the client's, when the context carries it", clientCtx("client-model"), types.OpOptions{Intelligence: types.Fast}, "local", "client-model"},
		{"the call's over the client's", clientCtx("client-model"), types.OpOptions{Model: "call-model"}, "local", "call-model"},
		{"a blank call model is no model", context.Background(), types.OpOptions{Model: "   ", Intelligence: types.Fast}, "local", "tier-fast"},
		{"a tab and a newline around the call's", context.Background(), types.OpOptions{Model: "\tcall-model\n"}, "local", "call-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var provider llm.Provider
			if tc.provider != "" {
				provider = &scriptedCapabilityProvider{provider: tc.provider}
			}
			snapshot := capabilitySnapshotFor(tc.ctx, tc.opts, provider)
			if snapshot.Model != tc.want || snapshot.Provider != tc.provider {
				t.Errorf("the snapshot names %q on %q, want %q on %q", snapshot.Model, snapshot.Provider, tc.want, tc.provider)
			}
			if snapshot.Model != resolveModel(tc.ctx, tc.opts, provider) {
				t.Error("the snapshot's model is not the one every call site resolves")
			}
		})
	}

	// Through the entry Preflight uses: the call's own model is honoured whatever
	// provider is installed.
	if got := capabilitySnapshot(context.Background(), types.OpOptions{Model: "call-model"}).Model; got != "call-model" {
		t.Errorf("capabilitySnapshot names %q for a call that named a model", got)
	}
}

func TestResolveModelReturnsTheTrimmedName(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	cases := []struct{ in, want string }{
		{"m", "m"},
		{" m", "m"},
		{"m ", "m"},
		{"  m  ", "m"},
		{"\tm\n", "m"},
		{"gpt-6-luna ", "gpt-6-luna"},
		{" claude-x-1", "claude-x-1"},
		{"a b", "a b"},
		{"  a b  ", "a b"},
		{" m ", "m"},
	}
	for _, tc := range cases {
		if got := resolveModel(context.Background(), types.OpOptions{Model: tc.in}, &scriptedCapabilityProvider{provider: "local"}); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.in, got, tc.want)
		}
	}
}
