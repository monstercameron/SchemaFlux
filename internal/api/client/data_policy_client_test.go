package client

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LX-126. A client has a data policy when it was given one, and not because it
// is a client. Every client's context carries a snapshot, and reading "there
// is a snapshot" as "there is a policy" would hold a client that never asked
// for one to the zero policy, which allows no provider.
func TestAClientDeclaresADataPolicyOnlyWhenGivenOne(t *testing.T) {
	policy := types.DataPolicy{AllowedProviders: []string{"openai"}, AllowedRegions: []string{"eu"}}

	cases := []struct {
		name     string
		build    func() *Client
		declared bool
		want     types.DataPolicy
	}{
		{"a new client", func() *Client { return NewClient("k") }, false, types.DataPolicy{}},
		{"a client given a budget only", func() *Client { return NewClient("k").WithBudget(5) }, false, types.DataPolicy{}},
		{"a client given a scheduler only", func() *Client {
			return NewClient("k").WithScheduler(ops.NewScheduler(ops.SchedulerLimits{MaxConcurrent: 1}))
		}, false, types.DataPolicy{}},
		{"a client given a policy", func() *Client { return NewClient("k").WithDataPolicy(policy) }, true, policy},
		{"a client given the empty policy", func() *Client { return NewClient("k").WithDataPolicy(types.DataPolicy{}) }, true, types.DataPolicy{}},
		{"a policy given and then replaced", func() *Client {
			return NewClient("k").WithDataPolicy(types.DataPolicy{}).WithDataPolicy(policy)
		}, true, policy},
		{"a policy given and then removed", func() *Client { return NewClient("k").WithDataPolicy(policy).WithoutDataPolicy() }, false, types.DataPolicy{}},
		{"removed and given again", func() *Client {
			return NewClient("k").WithDataPolicy(policy).WithoutDataPolicy().WithDataPolicy(policy)
		}, true, policy},
		{"removing what was never given", func() *Client { return NewClient("k").WithoutDataPolicy() }, false, types.DataPolicy{}},
		{"a policy and a budget", func() *Client { return NewClient("k").WithBudget(5).WithDataPolicy(policy) }, true, policy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.build()
			if scheduler := ops.ExecScheduler(client.Context(context.Background())); scheduler != nil {
				defer scheduler.Close(context.Background())
			}

			got, declared := ops.ExecDataPolicy(client.Context(context.Background()))
			if declared != tc.declared {
				t.Fatalf("declared = %v, want %v", declared, tc.declared)
			}
			if len(got.AllowedProviders) != len(tc.want.AllowedProviders) || len(got.AllowedRegions) != len(tc.want.AllowedRegions) {
				t.Errorf("the policy on the context is %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A context taken before the policy changed keeps the policy it was taken
// with: a call in flight is governed by what the client was when it started.
func TestADataPolicyChangeDoesNotReachACallAlreadyStarted(t *testing.T) {
	client := NewClient("k").WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"openai"}})
	inFlight := client.Context(context.Background())

	client.WithoutDataPolicy()

	if _, declared := ops.ExecDataPolicy(inFlight); !declared {
		t.Error("removing the client's policy lifted it from a call already started")
	}
	if _, declared := ops.ExecDataPolicy(client.Context(context.Background())); declared {
		t.Error("a context taken after the policy was removed still carries one")
	}
}

// The scheduler a client is given is the one on its context, and a nil one
// takes it off.
func TestAClientsSchedulerIsOnItsContext(t *testing.T) {
	scheduler := ops.NewScheduler(ops.SchedulerLimits{MaxConcurrent: 2})
	defer scheduler.Close(context.Background())

	client := NewClient("k")
	if ops.ExecScheduler(client.Context(context.Background())) != nil {
		t.Error("a new client has a scheduler")
	}
	client.WithScheduler(scheduler)
	if ops.ExecScheduler(client.Context(context.Background())) != scheduler {
		t.Error("the scheduler on the context is not the one the client was given")
	}
	client.WithScheduler(nil)
	if ops.ExecScheduler(client.Context(context.Background())) != nil {
		t.Error("a nil scheduler did not take the bound off")
	}
}

// The policy a client holds is its own copy. It shared its lists with the
// caller, so writing to a slice after WithDataPolicy changed the policy, for
// the client and for calls already in flight.
func TestAClientsDataPolicyIsItsOwnCopy(t *testing.T) {
	providers := []string{"openai"}
	regions := []string{"eu"}
	client := NewClient("k").WithDataPolicy(types.DataPolicy{AllowedProviders: providers, AllowedRegions: regions})
	inFlight := client.Context(context.Background())

	providers[0] = "anthropic"
	regions[0] = "us"

	for name, ctx := range map[string]context.Context{
		"a call already started": inFlight,
		"a call started after":   client.Context(context.Background()),
	} {
		policy, declared := ops.ExecDataPolicy(ctx)
		if !declared {
			t.Fatalf("%s: no policy", name)
		}
		if policy.AllowedProviders[0] != "openai" || policy.AllowedRegions[0] != "eu" {
			t.Errorf("%s: the policy followed the caller's slices: %+v", name, policy)
		}
	}

	// And what a context hands back is not the client's own list either.
	handed, _ := ops.ExecDataPolicy(inFlight)
	handed.AllowedProviders[0] = "tampered"
	if again, _ := ops.ExecDataPolicy(client.Context(context.Background())); again.AllowedProviders[0] != "openai" {
		t.Errorf("writing to a policy read from one context changed the client's: %+v", again)
	}
}
