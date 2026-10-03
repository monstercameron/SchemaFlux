package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config/configtest"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/mw"
)

// namedProvider answers a classification, or fails, under a name of its own.
type namedProvider struct {
	name   string
	fail   error
	answer string
	calls  int
}

func (p *namedProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls++
	if p.fail != nil {
		return llm.CompletionResponse{}, p.fail
	}
	answer := p.answer
	if answer == "" {
		answer = `{"category":"billing","confidence":0.9,"reasoning":"r"}`
	}
	return llm.CompletionResponse{
		Content: answer, Model: req.Model, Provider: p.name, FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}, nil
}
func (p *namedProvider) Name() string                               { return p.name }
func (p *namedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *namedProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// Review round 9, through the exported API. A client's data policy holds a
// fallback chain's alternates too. A failure of the primary used to send the
// content on to an alternate the policy forbids, and the call succeeded.
func TestIntegrationADataPolicyHoldsAFallbacksAlternates(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	eu := []string{"eu"}

	cases := []struct {
		name     string
		policy   *schemaflux.DataPolicy
		declare  map[string][]string // provider to its regions
		altCalls int
		wantErr  bool
	}{
		{"no policy: the alternate is used", nil, nil, 1, false},
		{"the alternate's provider is not allowed",
			&schemaflux.DataPolicy{AllowedProviders: []string{"primary"}, AllowedRegions: eu},
			map[string][]string{"primary": eu, "backup": eu}, 0, true},
		{"the alternate is allowed and declared",
			&schemaflux.DataPolicy{AllowedProviders: []string{"primary", "backup"}, AllowedRegions: eu},
			map[string][]string{"primary": eu, "backup": eu}, 1, false},
		{"the alternate is allowed and has declared nothing",
			&schemaflux.DataPolicy{AllowedProviders: []string{"primary", "backup"}, AllowedRegions: eu},
			map[string][]string{"primary": eu}, 0, true},
		{"the alternate runs somewhere that is not allowed",
			&schemaflux.DataPolicy{AllowedProviders: []string{"primary", "backup"}, AllowedRegions: eu},
			map[string][]string{"primary": eu, "backup": {"us"}}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			for provider, regions := range tc.declare {
				schemaflux.DeclareRoute(provider, "test-model", regions, 0)
			}

			down := errors.New("the primary is down")
			primary := &namedProvider{name: "primary", fail: down}
			backup := &namedProvider{name: "backup"}
			chain := mw.Chain(primary, mw.Fallback([]mw.FallbackRoute{{Handler: backup}}))

			client := schemaflux.NewClient("").WithProviderInstance(chain)
			if tc.policy != nil {
				client.WithDataPolicy(*tc.policy)
			}
			err := lx126Classify(client.Context(context.Background()))

			if backup.calls != tc.altCalls {
				t.Fatalf("the alternate was called %d times, want %d", backup.calls, tc.altCalls)
			}
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, down) {
				t.Errorf("err = %v, want the primary's own failure", err)
			}
		})
	}

	// A router this library does not know about, which sends the request on
	// and says so only in the answer: the answer is refused.
	t.Run("an answer from a provider the policy does not allow", func(t *testing.T) {
		llm.ResetCapabilityRegistryForTest()
		t.Cleanup(llm.ResetCapabilityRegistryForTest)
		schemaflux.DeclareRoute("primary", "test-model", eu, 0)

		router := &relabelling{namedProvider: namedProvider{name: "primary"}, answersAs: "somewhere-else"}
		client := schemaflux.NewClient("").WithProviderInstance(router).
			WithDataPolicy(schemaflux.DataPolicy{AllowedProviders: []string{"primary"}, AllowedRegions: eu})
		if err := lx126Classify(client.Context(context.Background())); !errors.Is(err, schemaflux.ErrPolicyViolation) {
			t.Fatalf("err = %v, want a policy refusal", err)
		}
	})
}

// relabelling is a provider under one name whose answers say they came from
// another, which is what a router looks like from outside.
type relabelling struct {
	namedProvider
	answersAs string
}

func (p *relabelling) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	resp, err := p.namedProvider.Complete(ctx, req)
	resp.Provider = p.answersAs
	return resp, err
}

// A client whose data policy does not allow its answers to be kept goes round a
// response cache, through the exported API: its second identical call is paid
// for, and the answer it got is not served to anybody else.
func TestIntegrationADataPolicyDecidesWhetherAClientIsCached(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	schemaflux.DeclareRoute("primary", "test-model", []string{"eu"}, 0)
	policy := func(cache bool) schemaflux.DataPolicy {
		return schemaflux.DataPolicy{AllowedProviders: []string{"primary"}, AllowedRegions: []string{"eu"}, AllowResultCaching: cache}
	}

	cases := []struct {
		name          string
		first, second *schemaflux.DataPolicy
		calls         int
	}{
		{"no policy on either call", nil, nil, 1},
		{"caching allowed on both", ptr(policy(true)), ptr(policy(true)), 1},
		{"caching forbidden on both", ptr(policy(false)), ptr(policy(false)), 2},
		{"forbidden, then a caller with no policy", ptr(policy(false)), nil, 2},
		{"a caller with no policy, then forbidden", nil, ptr(policy(false)), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &namedProvider{name: "primary"}
			cached := mw.Chain(provider, mw.Cache())

			for _, with := range []*schemaflux.DataPolicy{tc.first, tc.second} {
				client := schemaflux.NewClient("").WithProviderInstance(cached)
				if with != nil {
					client.WithDataPolicy(*with)
				}
				if err := lx126Classify(client.Context(context.Background())); err != nil {
					t.Fatalf("Classify: %v", err)
				}
			}
			if provider.calls != tc.calls {
				t.Errorf("the provider was called %d times, want %d", provider.calls, tc.calls)
			}
		})
	}
}

func ptr[T any](value T) *T { return &value }

// The contract floor of a client's data policy, through the exported API. It
// holds for an operation that returns a Result, which is where a contract level
// is worked out: an answer delivered below the floor is an error.
func TestIntegrationAClientsContractFloorHoldsForAResult(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "test-model")
	type person struct {
		Name string `json:"name"`
	}
	base := schemaflux.DataPolicy{AllowedProviders: []string{"primary"}, AllowedRegions: []string{"eu"}}

	cases := []struct {
		name    string
		floor   schemaflux.ContractLevel
		refused bool
	}{
		{"no floor", schemaflux.ContractPromptOnly, false},
		{"a floor the route cannot be shown to meet", schemaflux.ContractFullyGoverned, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			schemaflux.DeclareRoute("primary", "test-model", []string{"eu"}, 0)

			provider := &namedProvider{name: "primary", answer: `{"name":"Sam"}`}
			policy := base
			policy.MinimumContract = tc.floor
			client := schemaflux.NewClient("").WithProviderInstance(provider).WithDataPolicy(policy)

			opts := schemaflux.NewExtractOptions()
			opts.CommonOptions.Context = client.Context(context.Background())
			result, err := schemaflux.ExtractResult[person]("Sam", opts)
			if tc.refused {
				if !errors.Is(err, schemaflux.ErrPolicyViolation) {
					t.Fatalf("err = %v, want a policy refusal", err)
				}
				if result.Value.Name != "" {
					t.Errorf("a refused call returned a value: %+v", result.Value)
				}
				return
			}
			if err != nil || result.Value.Name != "Sam" {
				t.Fatalf("result %+v, err %v", result.Value, err)
			}
		})
	}
}
