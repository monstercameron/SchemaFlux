package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// tierProvider is a provider that knows what each tier means for it.
type tierProvider struct {
	benchInstantProvider
	models map[types.Speed]string
	name   string
}

func (p tierProvider) Name() string { return p.name }
func (p tierProvider) ModelFor(tier types.Speed) string {
	return p.models[tier]
}

// wrappedProvider hides the provider that knows the models behind a wrapper.
type wrappedProvider struct{ llm.Provider }

func (w wrappedProvider) Unwrap() llm.Provider { return w.Provider }

func TestResolveModelOrder(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_SMART", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_FAST", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_QUICK", "")

	knows := tierProvider{name: "custom", models: map[types.Speed]string{
		types.Smart: "custom-smart", types.Fast: "custom-fast", types.Quick: "custom-quick",
	}}
	configured, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k", Models: [3]string{"my-smart", "my-fast", ""}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := llm.NewOpenAIProvider(llm.ProviderConfig{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	clientCtx := WithSharedExecConfig(context.Background(), &ExecConfig{Model: "client-model"})

	cases := []struct {
		name     string
		ctx      context.Context
		opts     types.OpOptions
		provider llm.Provider
		env      map[string]string
		want     string
	}{
		{"the call's pin beats everything", clientCtx, types.OpOptions{Model: " pinned ", Intelligence: types.Fast}, knows, nil, "pinned"},
		{"the client's beats the provider's", clientCtx, types.OpOptions{Intelligence: types.Fast}, knows, nil, "client-model"},
		{"the provider's smart tier", context.Background(), types.OpOptions{Intelligence: types.Smart}, knows, nil, "custom-smart"},
		{"the provider's fast tier", context.Background(), types.OpOptions{Intelligence: types.Fast}, knows, nil, "custom-fast"},
		{"the provider's quick tier", context.Background(), types.OpOptions{Intelligence: types.Quick}, knows, nil, "custom-quick"},
		{"the provider's beats the process variable", context.Background(), types.OpOptions{Intelligence: types.Fast}, knows,
			map[string]string{"SCHEMAFLUX_MODEL": "env-model"}, "custom-fast"},
		{"a provider configured with models, smart", context.Background(), types.OpOptions{Intelligence: types.Smart}, configured, nil, "my-smart"},
		{"a provider configured with models, fast", context.Background(), types.OpOptions{Intelligence: types.Fast}, configured,
			map[string]string{"SCHEMAFLUX_MODEL": "env-model"}, "my-fast"},
		{"an empty entry is the process table's or the built-in", context.Background(), types.OpOptions{Intelligence: types.Quick}, configured, nil,
			config.GetModel(types.Quick, "openai")},
		{"an empty entry gives way to the process variable", context.Background(), types.OpOptions{Intelligence: types.Quick}, configured,
			map[string]string{"SCHEMAFLUX_MODEL": "env-model"}, "env-model"},
		{"a built-in provider with no models: the process variable as before", context.Background(), types.OpOptions{Intelligence: types.Fast}, plain,
			map[string]string{"SCHEMAFLUX_MODEL": "env-model"}, "env-model"},
		{"a built-in provider with no models: the tier variable as before", context.Background(), types.OpOptions{Intelligence: types.Fast}, plain,
			map[string]string{"SCHEMAFLUX_MODEL_FAST": "env-fast"}, "env-fast"},
		{"a built-in provider with no models: the built-in table", context.Background(), types.OpOptions{Intelligence: types.Smart}, plain, nil,
			config.GetModel(types.Smart, "openai")},
		{"a provider behind a wrapper is asked", context.Background(), types.OpOptions{Intelligence: types.Fast}, wrappedProvider{knows}, nil, "custom-fast"},
		{"a provider that names no model for the tier: the process table", context.Background(), types.OpOptions{Intelligence: types.Smart},
			tierProvider{name: "custom", models: map[types.Speed]string{types.Fast: "only-fast"}},
			map[string]string{"SCHEMAFLUX_MODEL_SMART": "env-smart"}, "env-smart"},
		{"no provider: the process table", context.Background(), types.OpOptions{Intelligence: types.Smart}, nil,
			map[string]string{"SCHEMAFLUX_MODEL_SMART": "env-smart"}, "env-smart"},
		{"a blank pin is none", context.Background(), types.OpOptions{Model: "  ", Intelligence: types.Fast}, knows, nil, "custom-fast"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for name, value := range tc.env {
				configtest.Setenv(t, name, value)
			}
			if got := resolveModel(tc.ctx, tc.opts, tc.provider); got != tc.want {
				t.Errorf("resolveModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// The request a call makes carries the model its provider says the tier means.
func TestCallLLMSendsTheProvidersTierModel(t *testing.T) {
	provider := &modelRecordingProvider{benchInstantProvider: benchInstantProvider{answer: `{"ok":true}`}}
	tiered := tierProvider{benchInstantProvider: provider.benchInstantProvider, name: "openai", models: map[types.Speed]string{types.Fast: "the-fast-one"}}
	var sent string
	capture := capturingTier{tierProvider: tiered, sent: &sent}
	if _, err := CallLLM(context.Background(), capture, "Return only valid JSON.", "user", types.OpOptions{Intelligence: types.Fast}); err != nil {
		t.Fatal(err)
	}
	if sent != "the-fast-one" {
		t.Errorf("the request carried %q, want the provider's model for the tier", sent)
	}
}

type capturingTier struct {
	tierProvider
	sent *string
}

func (c capturingTier) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	*c.sent = req.Model
	return c.tierProvider.Complete(ctx, req)
}
