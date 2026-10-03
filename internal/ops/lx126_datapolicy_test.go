package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

func declaredPolicy(policy types.DataPolicy) context.Context {
	return WithExecConfig(context.Background(), &ExecConfig{DataPolicy: policy, DataPolicyDeclared: true})
}

func registerRoute(t *testing.T, caps llm.ProviderCapabilities) {
	t.Helper()
	llm.RegisterCapabilities(caps)
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
}

// LX-126. A client's data policy says which routes its calls may take. It was
// stored on the context and read by nothing, so a client restricted to one
// provider in one region sent its calls wherever the process was configured to
// send them, and nothing said so.
//
// The rules are the ones types.DataPolicy already documents, and they do not
// fail open: a list the policy leaves empty allows nothing, and a route that
// has declared nothing about itself is not eligible.
func TestAClientsDataPolicyDecidesWhichRouteACallMayTake(t *testing.T) {
	euRoute := llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu"}, RetentionDays: 0}
	twoRegions := llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu", "us"}, RetentionDays: 0}
	retaining := llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna", Regions: []string{"eu"}, RetentionDays: 30}
	noRegion := llm.ProviderCapabilities{Provider: "openai", Model: "gpt-6-luna"}

	eu := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: types.RetentionUnspecified}
	cases := []struct {
		name     string
		ctx      context.Context
		route    *llm.ProviderCapabilities
		provider string
		refused  string // what the refusal has to say; "" means the call goes
	}{
		{"no client at all", context.Background(), nil, "openai", ""},
		{"a client that declared no policy", WithExecConfig(context.Background(), &ExecConfig{}), nil, "openai", ""},
		{"a policy that allows the route", declaredPolicy(eu), &euRoute, "openai", ""},
		{"the provider's name in other capitals", declaredPolicy(eu), &euRoute, "OpenAI", ""},
		{"an empty policy allows nothing", declaredPolicy(types.DataPolicy{}), &euRoute, "openai", "provider"},
		{"a provider the policy does not name", declaredPolicy(eu), &euRoute, "anthropic", "provider"},
		{"a policy that names no provider", declaredPolicy(types.DataPolicy{AllowedRegions: []string{"eu"}}), &euRoute, "openai", "provider"},
		{"a route that has declared nothing", declaredPolicy(eu), nil, "openai", "declared"},
		{"a route that declares no region", declaredPolicy(eu), &noRegion, "openai", "region"},
		{"a route in a region the policy does not allow", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us"}}), &euRoute, "openai", "region"},
		{"a route that may run in a region the policy does not allow", declaredPolicy(eu), &twoRegions, "openai", "region"},
		{"both of a route's regions allowed", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"us", "eu"}}), &twoRegions, "openai", ""},
		{"a policy that names no region", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}}), &euRoute, "openai", "region"},
		{"a route that retains, under a policy that says nothing of retention", declaredPolicy(eu), &retaining, "openai", "retention"},
		{"a route that retains longer than the policy allows", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 7}), &retaining, "openai", "retention"},
		{"a route that retains within what the policy allows", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}, MaxRetentionDays: 30}), &retaining, "openai", ""},
		{"zero retention allowed and none performed", declaredPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}), &euRoute, "openai", ""},
	}
	for _, tc := range cases {
		for _, path := range []string{"buffered", "streamed"} {
			t.Run(tc.name+", "+path, func(t *testing.T) {
				llm.ResetCapabilityRegistryForTest()
				if tc.route != nil {
					registerRoute(t, *tc.route)
				}
				provider := &gateProvider{name: tc.provider}

				var err error
				if path == "buffered" {
					_, err = CallLLM(tc.ctx, provider, "system", "the customer's account number", lx126Options)
				} else {
					opts := lx126Options
					opts.ResponseFormat = "text"
					var stream *TextStream
					stream, err = streamWith(tc.ctx, provider, opts)
					if err == nil {
						_, err = stream.Text()
					}
				}

				if tc.refused == "" {
					if err != nil {
						t.Fatalf("the call was refused: %v", err)
					}
					if provider.calls.Load() != 1 {
						t.Fatalf("the provider was called %d times, want 1", provider.calls.Load())
					}
					return
				}
				if !errors.Is(err, types.ErrPolicyViolation) || !errors.Is(err, ErrDataPolicyRefusal) {
					t.Fatalf("err = %v, want a data-policy refusal", err)
				}
				if provider.calls.Load() != 0 {
					t.Fatal("the provider was called for a route the policy forbids")
				}
				if !strings.Contains(err.Error(), tc.refused) {
					t.Errorf("the refusal reads %q, want it to say which rule: %q", err.Error(), tc.refused)
				}
				// It names the route, which is configuration, and nothing of
				// the caller's.
				if strings.Contains(err.Error(), "account number") {
					t.Errorf("the refusal carries the caller's text: %q", err.Error())
				}
			})
		}
	}
}

// The refusal comes before anything else that costs or records: no budget is
// charged, no retry is made, and the policy on the context taken when the call
// started is the one that governs it.
func TestADataPolicyRefusalCostsNothing(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	budget := &recordingBudget{}
	ctx := WithExecConfig(context.Background(), &ExecConfig{
		Budget:             budget,
		DataPolicy:         types.DataPolicy{AllowedProviders: []string{"anthropic"}},
		DataPolicyDeclared: true,
	})
	provider := &gateProvider{retries: 3}

	_, err := CallLLM(ctx, provider, "system", "user", lx126Options)
	if !errors.Is(err, types.ErrPolicyViolation) {
		t.Fatalf("err = %v, want a policy refusal", err)
	}
	if provider.calls.Load() != 0 || len(budget.seen()) != 0 {
		t.Errorf("the provider was called %d times and the budget told %v", provider.calls.Load(), budget.seen())
	}
}

// The accessor: a policy is declared when the client set one, and not merely
// because there is a client. Every client carries a snapshot, so reading "there
// is a snapshot" as "there is a policy" would refuse every call of a client
// that never asked for one.
func TestExecDataPolicyIsDeclaredOnlyWhenSet(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}}
	cases := []struct {
		name     string
		ctx      context.Context
		declared bool
	}{
		{"a bare context", context.Background(), false},
		{"a snapshot with nothing set", WithExecConfig(context.Background(), &ExecConfig{}), false},
		{"a snapshot with a provider only", WithExecConfig(context.Background(), &ExecConfig{Provider: &gateProvider{}}), false},
		{"a policy that was not declared", WithExecConfig(context.Background(), &ExecConfig{DataPolicy: policy}), false},
		{"a declared policy", declaredPolicy(policy), true},
		{"a declared empty policy", declaredPolicy(types.DataPolicy{}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, declared := ExecDataPolicy(tc.ctx)
			if declared != tc.declared {
				t.Fatalf("declared = %v, want %v", declared, tc.declared)
			}
			if !declared && len(got.AllowedProviders) != 0 {
				t.Errorf("a policy nobody declared was returned: %+v", got)
			}
		})
	}
}

// The contract floor of a client's data policy. MinimumContract was enforced
// only for a policy attached to the one call with WithContractPolicy; set on a
// client it was read by nothing.
func TestAClientsDataPolicyCarriesTheContractFloor(t *testing.T) {
	floor := types.DataPolicy{MinimumContract: types.ContractSchemaConstrained}
	stricter := types.DataPolicy{MinimumContract: types.ContractEvidenceChecked}

	cases := []struct {
		name      string
		ctx       context.Context
		delivered types.ContractLevel
		refused   bool
	}{
		{"no client and no policy", context.Background(), types.ContractPromptOnly, false},
		{"a client that declared no policy", WithExecConfig(context.Background(), &ExecConfig{DataPolicy: floor}), types.ContractPromptOnly, false},
		{"a client's floor, not met", declaredPolicy(floor), types.ContractJSONWellFormed, true},
		{"a client's floor, met exactly", declaredPolicy(floor), types.ContractSchemaConstrained, false},
		{"a client's floor, exceeded", declaredPolicy(floor), types.ContractFullyGoverned, false},
		{"a client's policy with no floor", declaredPolicy(types.DataPolicy{}), types.ContractPromptOnly, false},
		// With a floor on the call and one on the client, the stricter holds:
		// a policy on the call cannot loosen what the client requires.
		{"the call's own floor is stricter and not met", WithContractPolicy(declaredPolicy(floor), stricter), types.ContractSchemaConstrained, true},
		{"the call's own floor is stricter and met", WithContractPolicy(declaredPolicy(floor), stricter), types.ContractEvidenceChecked, false},
		{"the call's own floor is looser, and the client's is not met", WithContractPolicy(declaredPolicy(stricter), floor), types.ContractSchemaConstrained, true},
		{"the call's own floor is looser, and the client's is met", WithContractPolicy(declaredPolicy(stricter), floor), types.ContractEvidenceChecked, false},
		{"a floor on the call, and a client that declared no policy", WithContractPolicy(WithExecConfig(context.Background(), &ExecConfig{DataPolicy: stricter}), floor), types.ContractSchemaConstrained, false},
		{"only the call's own floor, not met", WithContractPolicy(context.Background(), floor), types.ContractPromptOnly, true},
		{"only the call's own floor, met", WithContractPolicy(context.Background(), floor), types.ContractSchemaConstrained, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := enforceContractPolicy(tc.ctx, tc.delivered)
			if tc.refused != errors.Is(err, ErrContractDegraded) {
				t.Fatalf("err = %v, want a refusal: %v", err, tc.refused)
			}
		})
	}
}

// A snapshot owns its policy's lists. It copied the struct and shared the
// slices, so the configuration it was taken from could change it afterwards.
func TestAnExecConfigSnapshotOwnsItsPolicysLists(t *testing.T) {
	cfg := &ExecConfig{
		DataPolicy:         types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}},
		DataPolicyDeclared: true,
	}
	ctx := WithExecConfig(context.Background(), cfg)

	cfg.DataPolicy.AllowedProviders[0] = "anthropic"
	cfg.DataPolicy.AllowedRegions[0] = "us"

	policy, declared := ExecDataPolicy(ctx)
	if !declared || policy.AllowedProviders[0] != "openai" || policy.AllowedRegions[0] != "eu" {
		t.Errorf("the snapshot followed the configuration it was taken from: %+v", policy)
	}
}
