package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A client can name the model and the reasoning effort its calls use, once.
//
// The model could be named on a call or in the environment, and the effort on
// a call only. An application that makes its decisions through a dozen
// operations had a dozen option values to set them on, each built a different
// way, and an operation whose options it did not build itself could not be
// reached at all: the benchmark application pinned its model through
// SCHEMAFLUX_MODEL for that reason.
//
// What a call names wins, then what the client names, then the library's own
// choice: the tier's model, and for the effort an operation's preference. The
// effort a call names is a requirement, refused where it cannot be sent; the
// client's, like an operation's, is a preference, dropped there.
func TestACallTakesItsModelFromTheCallThenTheClientThenTheTier(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_FAST", "tier-fast")
	configtest.Setenv(t, "SCHEMAFLUX_MODEL_SMART", "tier-smart")

	cases := []struct {
		name   string
		client string
		opts   types.OpOptions
		want   string
	}{
		{"nothing named: the tier's", "", types.OpOptions{Intelligence: types.Fast}, "tier-fast"},
		{"nothing named, the smart tier", "", types.OpOptions{Intelligence: types.Smart}, "tier-smart"},
		{"the client's", "client-model", types.OpOptions{Intelligence: types.Fast}, "client-model"},
		{"the client's, whatever the tier", "client-model", types.OpOptions{Intelligence: types.Smart}, "client-model"},
		{"the call's", "", types.OpOptions{Model: "call-model", Intelligence: types.Fast}, "call-model"},
		{"the call's over the client's", "client-model", types.OpOptions{Model: "call-model", Intelligence: types.Fast}, "call-model"},
		{"the call's over the client's, smart tier", "client-model", types.OpOptions{Model: "call-model", Intelligence: types.Smart}, "call-model"},
		{"a client's name with space around it", "  client-model  ", types.OpOptions{Intelligence: types.Fast}, "client-model"},
		{"a blank client name is no name", "   ", types.OpOptions{Intelligence: types.Fast}, "tier-fast"},
		{"a client with no model and a call with none", "", types.OpOptions{Intelligence: types.Quick}, config_quickModel(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop"}}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: tc.client})
			opts := tc.opts
			opts.ResponseFormat = "json"
			if _, err := callLLM(ctx, "system", "user", opts); err != nil {
				t.Fatalf("callLLM: %v", err)
			}
			if provider.req.Model != tc.want {
				t.Errorf("the request names %q, want %q", provider.req.Model, tc.want)
			}
			if name, model, known := plannedRoute(ctx, tc.opts); !known || model != tc.want || name != provider.Name() {
				t.Errorf("plannedRoute = %q, %q, %v; want the same route the call takes", name, model, known)
			}
		})
	}
}

// config_quickModel is whatever the quick tier maps to for the fake provider,
// read through a call so the test does not restate the mapping.
func config_quickModel(t *testing.T) string {
	t.Helper()
	provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop"}}
	if _, err := callLLM(WithProvider(context.Background(), provider), "system", "user", types.OpOptions{Intelligence: types.Quick, ResponseFormat: "json"}); err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	return provider.req.Model
}

func TestACallTakesItsEffortFromTheCallThenTheClientThenTheOperation(t *testing.T) {
	cases := []struct {
		name      string
		client    types.Effort
		call      types.Effort
		operation types.Effort // what the operation prefers
		want      string
		preferred bool
	}{
		{"nothing set", types.EffortDefault, types.EffortDefault, types.EffortDefault, "", false},
		{"the operation's preference", types.EffortDefault, types.EffortDefault, types.EffortNone, "none", true},
		{"the client's, which is a preference", types.EffortLow, types.EffortDefault, types.EffortDefault, "low", true},
		{"the client's over the operation's preference", types.EffortLow, types.EffortDefault, types.EffortNone, "low", true},
		{"the call's", types.EffortDefault, types.EffortHigh, types.EffortDefault, "high", false},
		{"the call's over the client's", types.EffortLow, types.EffortHigh, types.EffortDefault, "high", false},
		{"the call's over both", types.EffortLow, types.EffortHigh, types.EffortNone, "high", false},
		{"the client asks for none", types.EffortNone, types.EffortDefault, types.EffortDefault, "none", true},
		{"the client asks for none, the call for medium", types.EffortNone, types.EffortMedium, types.EffortDefault, "medium", false},
		{"the client's is what the operation would have preferred", types.EffortNone, types.EffortDefault, types.EffortNone, "none", true},
		{"the call asks for none over a client's high", types.EffortHigh, types.EffortNone, types.EffortDefault, "none", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"ok":true}`, FinishReason: "stop"}}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Effort: tc.client})
			ctx = preferEffort(ctx, tc.operation)
			opts := types.OpOptions{Model: "gpt-6-luna", ResponseFormat: "json", Effort: tc.call}
			if _, err := callLLM(ctx, "system", "user", opts); err != nil {
				t.Fatalf("callLLM: %v", err)
			}
			if provider.req.ReasoningEffort != tc.want || provider.req.ReasoningEffortPreferred != tc.preferred {
				t.Errorf("the request carries effort %q (preferred %v), want %q (preferred %v)",
					provider.req.ReasoningEffort, provider.req.ReasoningEffortPreferred, tc.want, tc.preferred)
			}
		})
	}
}

// A decision made through a client that names a model and an effort is sent
// with both, with nothing set on the call.
func TestADecisionUsesTheClientsModelAndEffort(t *testing.T) {
	provider := &captureProvider{resp: llm.CompletionResponse{Content: `{"q1":true}`, FinishReason: "stop"}}
	ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "gpt-6-luna", Effort: types.EffortNone})
	yes, err := Is(ctx, "order 7731 was charged twice", "Is this about billing?")
	if err != nil || !yes {
		t.Fatalf("Is = %v, %v", yes, err)
	}
	if provider.req.Model != "gpt-6-luna" || provider.req.ReasoningEffort != "none" || !provider.req.ReasoningEffortPreferred {
		t.Errorf("model %q, effort %q, preferred %v", provider.req.Model, provider.req.ReasoningEffort, provider.req.ReasoningEffortPreferred)
	}
}
