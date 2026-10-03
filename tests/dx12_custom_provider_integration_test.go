package tests

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"

	"github.com/monstercameron/schemaflux/internal/config/configtest"
)

// An application with its own provider type, or a test double of its own, names
// it whatever it likes. The library has no model for a name it does not know and
// will not send one it invented, so a call that names no model is refused before
// anything is sent. These tests reproduce that from the exported API and pin the
// ways a caller can name the model: on the client, on the call, and in the
// environment.

type dx12MineProvider struct {
	mu     sync.Mutex
	models []string
}

func (p *dx12MineProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	p.models = append(p.models, req.Model)
	p.mu.Unlock()
	return schemaflux.CompletionResponse{Content: `{"q1":true}`, Model: req.Model, FinishReason: "stop"}, nil
}

func (p *dx12MineProvider) Name() string                                      { return "mine" }
func (p *dx12MineProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *dx12MineProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *dx12MineProvider) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func dx12ClearModelEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"SCHEMAFLUX_MODEL", "SCHEMAFLUX_MODEL_SMART", "SCHEMAFLUX_MODEL_FAST", "SCHEMAFLUX_MODEL_QUICK"} {
		configtest.Setenv(t, key, "")
	}
}

func TestIntegrationACustomProviderWithNoModelNamedIsRefusedBeforeAnythingIsSent(t *testing.T) {
	dx12ClearModelEnv(t)
	provider := &dx12MineProvider{}
	schemaflux.NewClient("").WithProviderInstance(provider)

	_, err := schemaflux.Is(context.Background(), "a message", "Is this about billing?")
	if err == nil {
		t.Fatal("a call that named no model, to a provider the library has no model for, was sent")
	}
	if !strings.Contains(err.Error(), "no default model for provider") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if !strings.Contains(err.Error(), `provider "mine"`) {
		t.Errorf("the refusal does not name the provider: %v", err)
	}
	if len(provider.seen()) != 0 {
		t.Errorf("the provider was sent %v", provider.seen())
	}
}

func TestIntegrationACustomProviderRunsOnceTheClientNamesAModel(t *testing.T) {
	dx12ClearModelEnv(t)
	provider := &dx12MineProvider{}
	client := schemaflux.NewClient("").WithProviderInstance(provider).WithModel("my-model")

	ok, err := schemaflux.Is(client.Context(context.Background()), "a message", "Is this about billing?")
	if err != nil {
		t.Fatalf("Is: %v", err)
	}
	if !ok {
		t.Error("the scripted answer was true")
	}
	if got := provider.seen(); len(got) != 1 || got[0] != "my-model" {
		t.Errorf("the provider was sent %v, want the client's model", got)
	}
}

func TestIntegrationACustomProviderRunsOnceTheCallNamesAModel(t *testing.T) {
	dx12ClearModelEnv(t)
	provider := &dx12MineProvider{}
	schemaflux.NewClient("").WithProviderInstance(provider)

	_, err := schemaflux.Is(context.Background(), "a message", "Is this about billing?", schemaflux.OpOptions{Model: "call-model"})
	if err != nil {
		t.Fatalf("Is: %v", err)
	}
	if got := provider.seen(); len(got) != 1 || got[0] != "call-model" {
		t.Errorf("the provider was sent %v, want the call's model", got)
	}
}

func TestIntegrationACustomProviderRunsOnceTheEnvironmentNamesAModel(t *testing.T) {
	dx12ClearModelEnv(t)
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "env-model")
	provider := &dx12MineProvider{}
	schemaflux.NewClient("").WithProviderInstance(provider)

	if _, err := schemaflux.Is(context.Background(), "a message", "Is this about billing?"); err != nil {
		t.Fatalf("Is: %v", err)
	}
	if got := provider.seen(); len(got) != 1 || got[0] != "env-model" {
		t.Errorf("the provider was sent %v, want the environment's model", got)
	}
}
