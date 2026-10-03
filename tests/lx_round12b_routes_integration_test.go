package tests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/mw"
)

// Review round 12, the route an answer came by, through the exported API and
// the real provider adapters in front of a local server.

const r12bInvoice = `{"number":"INV-1","vendor":"Northwind"}`

// r12bExtract runs one extraction under the client's context and returns what
// answered it.
func r12bExtract(client *schemaflux.Client) (schemaflux.Result[invoiceDoc], error) {
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(client.Context(context.Background()))
	return schemaflux.ExtractResult[invoiceDoc]("the raw document", opts)
}

// r12bAnswerer is a provider that answers with a body of its own naming.
type r12bAnswerer struct {
	name    string
	fail    error
	respond func(model string) schemaflux.CompletionResponse
	calls   int
}

func (p *r12bAnswerer) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.calls++
	if p.fail != nil {
		return schemaflux.CompletionResponse{}, p.fail
	}
	return p.respond(req.Model), nil
}
func (p *r12bAnswerer) Name() string                                      { return p.name }
func (p *r12bAnswerer) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *r12bAnswerer) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

// A fallback that answers and names nothing is spoken for by the chain, so the
// answer says who produced it. The integration test of the route check passed
// with the stamping removed, because an unnamed answer is accepted as the
// primary's either way: what tells them apart is what the answer is reported to
// have come from.
func TestIntegrationABackupThatNamesNothingIsReportedAsTheBackup(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	down := errors.New("the primary is down")
	eu := []string{"eu"}
	policy := schemaflux.DataPolicy{AllowedProviders: []string{"primary", "backup"}, AllowedRegions: eu}

	cases := []struct {
		name         string
		backupNames  bool
		altModel     string
		wantProvider string
		wantModel    string
	}{
		{"the backup names nothing", false, "backup-model", "backup", "backup-model"},
		{"the backup names its own provider and model", true, "backup-model", "backup", "backup-model"},
		{"the backup names nothing and has no model of its own", false, "", "backup", "test-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			schemaflux.DeclareRoute("primary", "test-model", eu, 0)
			schemaflux.DeclareRoute("backup", "backup-model", eu, 0)
			schemaflux.DeclareRoute("backup", "test-model", eu, 0)

			primary := &r12bAnswerer{name: "primary", fail: down}
			backup := &r12bAnswerer{name: "backup", respond: func(model string) schemaflux.CompletionResponse {
				resp := schemaflux.CompletionResponse{Content: r12bInvoice, FinishReason: "stop",
					Usage: schemaflux.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}}
				if tc.backupNames {
					resp.Provider, resp.Model = "backup", model
				}
				return resp
			}}
			chain := mw.Chain(primary, mw.Fallback([]mw.FallbackRoute{{Handler: backup, Model: tc.altModel}}))
			client := schemaflux.NewClient("").WithProviderInstance(chain).WithDataPolicy(policy)

			result, err := r12bExtract(client)
			if err != nil {
				t.Fatalf("ExtractResult: %v", err)
			}
			if backup.calls != 1 {
				t.Fatalf("the backup was called %d times, want 1", backup.calls)
			}
			if result.Meta.Provider != tc.wantProvider || result.Meta.Model != tc.wantModel {
				t.Errorf("the answer is reported as from %s/%s, want %s/%s", result.Meta.Provider, result.Meta.Model, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

// What the Anthropic adapter sends for a "gpt" model is what the policy checks.
// It sent its own default and the policy checked the name in the request, so a
// route nobody had checked carried the content.
func TestIntegrationThePolicyChecksTheModelTheAnthropicAdapterSends(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4o")
	const sentModel = "claude-3-5-sonnet-20240620"
	eu := []string{"eu"}
	policy := schemaflux.DataPolicy{AllowedProviders: []string{"anthropic"}, AllowedRegions: eu}

	cases := []struct {
		name     string
		declared []string
		sent     bool
		refused  bool
	}{
		{"the model the adapter sends is declared", []string{sentModel}, true, false},
		{"only the name in the request is declared", []string{"gpt-4o"}, false, true},
		{"both are declared", []string{"gpt-4o", sentModel}, true, false},
		{"neither is declared", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for _, model := range tc.declared {
				schemaflux.DeclareRoute("anthropic", model, eu, 0)
			}

			var calls atomic.Int32
			var sent atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				sent.Store(body.Model)
				answer, _ := json.Marshal(map[string]any{
					"content":     []map[string]string{{"type": "text", "text": r12bInvoice}},
					"model":       body.Model,
					"stop_reason": "end_turn",
					"usage":       map[string]int{"input_tokens": 20, "output_tokens": 10},
				})
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(answer)
			}))
			defer server.Close()

			provider, err := schemaflux.NewAnthropicProvider(schemaflux.ProviderConfig{APIKey: "k", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("NewAnthropicProvider: %v", err)
			}
			client := schemaflux.NewClient("").WithProviderInstance(provider).WithDataPolicy(policy)

			result, err := r12bExtract(client)
			if tc.refused {
				if !errors.Is(err, schemaflux.ErrPolicyViolation) || calls.Load() != 0 {
					t.Fatalf("err = %v after %d calls; want a policy refusal before anything was sent", err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractResult: %v", err)
			}
			if got, _ := sent.Load().(string); got != sentModel {
				t.Errorf("the server was sent %q, want %q", got, sentModel)
			}
			if result.Meta.Model != sentModel {
				t.Errorf("the answer is reported as from %q", result.Meta.Model)
			}
		})
	}
}

// OpenRouter answers with the vendor it sent the request to. The answer-route
// check could not see it, so a request allowed to go to the router could be
// served by a provider the policy forbids.
func TestIntegrationThePolicyReadsTheProviderBehindOpenRouter(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	eu, us := []string{"eu"}, []string{"us"}
	policy := schemaflux.DataPolicy{AllowedProviders: []string{"openrouter", "openai", "together"}, AllowedRegions: eu}

	cases := []struct {
		name     string
		upstream string // the raw JSON value of "provider", or "" for the key to be absent
		refused  bool
	}{
		{"no provider reported", "", false},
		{"a provider declared in the allowed region", `"OpenAI"`, false},
		{"a provider the policy does not allow", `"Mistral"`, true},
		{"a provider the policy allows and nobody declared", `"Together"`, true},
		{"a provider declared in another region", `"DeepInfra"`, true},
		{"a provider that is not a string", `{"name":"Mistral"}`, false},
		{"an empty provider", `""`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			schemaflux.DeclareRoute("openrouter", "test-model", eu, 0)
			schemaflux.DeclareRoute("openai", "test-model", eu, 0)
			schemaflux.DeclareRoute("deepinfra", "test-model", us, 0)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				provider := ""
				if tc.upstream != "" {
					provider = `"provider":` + tc.upstream + `,`
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"model":"test-model",` + provider + `"choices":[{"finish_reason":"stop","message":{"content":` +
					r12bJSONString(r12bInvoice) + `}}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`))
			}))
			defer server.Close()

			router, err := schemaflux.NewOpenRouterProvider(schemaflux.ProviderConfig{APIKey: "k", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("NewOpenRouterProvider: %v", err)
			}
			client := schemaflux.NewClient("").WithProviderInstance(router).WithDataPolicy(policy)

			result, err := r12bExtract(client)
			if tc.refused != (err != nil) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
			if tc.refused {
				if !errors.Is(err, schemaflux.ErrPolicyViolation) {
					t.Errorf("err = %v, want a policy refusal", err)
				}
				return
			}
			// What answered is still the router, which is what the library handed
			// the request to.
			if result.Meta.Provider != "openrouter" {
				t.Errorf("the answer is reported as from %q, want the router", result.Meta.Provider)
			}
		})
	}
}

func r12bJSONString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
