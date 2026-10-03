package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// A provider type of the caller's own, injected under a name of their own, has
// no model in the library's table, and a call that names none fails: the library
// will not send a model it invented for a name it does not know. These tests pin
// both halves of that. The refusal stays, classified as a configuration problem
// and sent nowhere, and each way of naming a model reaches the provider.

type dx12NamedProvider struct {
	name  string
	calls int
	model string
}

func (p *dx12NamedProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	p.model = req.Model
	return llm.CompletionResponse{Content: `{"ok":true}`, Model: req.Model, FinishReason: "stop"}, nil
}

func (p *dx12NamedProvider) Name() string                               { return p.name }
func (p *dx12NamedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *dx12NamedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

func TestAProviderWithNoModelTableNeedsAModelNamed(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		clientMod string
		call      types.OpOptions
		env       map[string]string
		wantModel string // "" means the call is refused
	}{
		{name: "nothing named is refused", provider: "mine", call: types.OpOptions{Intelligence: types.Fast}},
		{name: "a name with capitals and space is as unknown as any other", provider: " Mine ", call: types.OpOptions{Intelligence: types.Fast}},
		{name: "a model on the call", provider: "mine", call: types.OpOptions{Model: "call-model", Intelligence: types.Fast}, wantModel: "call-model"},
		{name: "a model on the client", provider: "mine", clientMod: "client-model", call: types.OpOptions{Intelligence: types.Fast}, wantModel: "client-model"},
		{name: "the call's model over the client's", provider: "mine", clientMod: "client-model", call: types.OpOptions{Model: "call-model", Intelligence: types.Smart}, wantModel: "call-model"},
		{name: "a blank model on the call names nothing", provider: "mine", call: types.OpOptions{Model: "   ", Intelligence: types.Fast}},
		{name: "a blank model on the client names nothing", provider: "mine", clientMod: "   ", call: types.OpOptions{Intelligence: types.Fast}},
		{name: "the environment's model", provider: "mine", env: map[string]string{"SCHEMAFLUX_MODEL": "env-model"}, call: types.OpOptions{Intelligence: types.Fast}, wantModel: "env-model"},
		{name: "the environment's model for the tier asked for", provider: "mine", env: map[string]string{"SCHEMAFLUX_MODEL_FAST": "env-fast"}, call: types.OpOptions{Intelligence: types.Fast}, wantModel: "env-fast"},
		{name: "the environment's model for another tier is no help", provider: "mine", env: map[string]string{"SCHEMAFLUX_MODEL_SMART": "env-smart"}, call: types.OpOptions{Intelligence: types.Fast}},
		{name: "the library's own double keeps its table entry", provider: "local", call: types.OpOptions{Intelligence: types.Fast}, wantModel: "local-mock"},
		{name: "a provider with a table keeps its entry", provider: "mistral", call: types.OpOptions{Intelligence: types.Smart}, wantModel: "mistral-large-latest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
				configtest.Setenv(t, key, tc.env[key])
			}

			provider := &dx12NamedProvider{name: tc.provider}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: tc.clientMod})
			opts := tc.call
			opts.ResponseFormat = "json"

			_, err := callLLM(ctx, "system", "user", opts)

			if tc.wantModel != "" {
				if err != nil {
					t.Fatalf("callLLM: %v", err)
				}
				if provider.model != tc.wantModel {
					t.Errorf("the request names %q, want %q", provider.model, tc.wantModel)
				}
				return
			}

			if err == nil {
				t.Fatalf("a call with no model reached the provider as %q", provider.model)
			}
			if provider.calls != 0 {
				t.Errorf("the provider was called %d times for a call that was refused", provider.calls)
			}
			if kind := llm.Classify(err); kind != types.KindConfiguration {
				t.Errorf("the refusal is classified %v, want a configuration error", kind)
			}
			if !strings.Contains(err.Error(), "no default model for provider") {
				t.Errorf("the refusal does not say what is missing: %v", err)
			}
		})
	}
}

// The error a caller reads is classified as configuration whichever way it was
// built, and keeps the phrase the classifier matches on.
func TestNoDefaultModelErrorIsClassifiedAsConfiguration(t *testing.T) {
	for _, provider := range []string{"mine", "qwen", "my-internal-gateway", "", "模型"} {
		err := config.NoDefaultModelError(provider)
		if kind := llm.Classify(err); kind != types.KindConfiguration {
			t.Errorf("Classify(NoDefaultModelError(%q)) = %v, want a configuration error", provider, kind)
		}
	}
}
