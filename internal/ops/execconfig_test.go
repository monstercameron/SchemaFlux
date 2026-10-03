package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A nil context is a caller's mistake, and context.WithValue panics on it. It
// is read as the background context, as every reader in this file reads one.
func TestWithExecConfigGuardsANilContext(t *testing.T) {
	var nilContext context.Context

	t.Run("a snapshot on a nil context", func(t *testing.T) {
		ctx := WithExecConfig(nilContext, &ExecConfig{Model: " the-model "})
		if ctx == nil {
			t.Fatal("WithExecConfig returned a nil context")
		}
		if got := ExecModel(ctx); got != "the-model" {
			t.Errorf("ExecModel = %q, want the snapshot's model", got)
		}
	})
	t.Run("no snapshot on a nil context", func(t *testing.T) {
		if ctx := WithExecConfig(nilContext, nil); ctx == nil {
			t.Fatal("WithExecConfig returned a nil context for a nil snapshot")
		}
	})
	t.Run("no snapshot leaves a live context as it was", func(t *testing.T) {
		parent := context.WithValue(context.Background(), struct{}{}, "kept")
		if ctx := WithExecConfig(parent, nil); ctx != parent {
			t.Error("a nil snapshot changed the context")
		}
	})
	t.Run("the result is not done", func(t *testing.T) {
		if err := WithExecConfig(nilContext, &ExecConfig{}).Err(); err != nil {
			t.Errorf("the context is done: %v", err)
		}
	})
}

// The policy a context carries is a snapshot. What ExecDataPolicy returns
// shares no slice with it, so a caller that edits what it was given cannot
// change the policy of the calls that follow.
func TestExecDataPolicyReturnsCopiesOfItsLists(t *testing.T) {
	cases := []struct {
		name      string
		providers []string
		regions   []string
	}{
		{"both lists with one entry", []string{"openai"}, []string{"eu"}},
		{"both lists with several entries", []string{"openai", "anthropic", "local"}, []string{"eu", "us", "ap"}},
		{"providers only", []string{"openai", "anthropic"}, nil},
		{"regions only", nil, []string{"eu", "us"}},
		{"neither", nil, nil},
		{"empty lists", []string{}, []string{}},
		{"a single provider and many regions", []string{"local"}, []string{"a", "b", "c", "d", "e"}},
		{"duplicates are kept", []string{"openai", "openai"}, []string{"eu", "eu"}},
		{"entries with spaces", []string{" openai "}, []string{" eu "}},
		{"many providers", []string{"a", "b", "c", "d", "e", "f"}, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := types.DataPolicy{AllowedProviders: tc.providers, AllowedRegions: tc.regions}
			ctx := WithExecConfig(context.Background(), &ExecConfig{DataPolicy: policy, DataPolicyDeclared: true})

			got, declared := ExecDataPolicy(ctx)
			if !declared {
				t.Fatal("the policy was reported as undeclared")
			}
			if len(got.AllowedProviders) != len(tc.providers) || len(got.AllowedRegions) != len(tc.regions) {
				t.Fatalf("the lists changed length: %v and %v", got.AllowedProviders, got.AllowedRegions)
			}

			// Overwrite everything the caller was handed.
			for i := range got.AllowedProviders {
				got.AllowedProviders[i] = "tampered"
			}
			for i := range got.AllowedRegions {
				got.AllowedRegions[i] = "tampered"
			}

			again, _ := ExecDataPolicy(ctx)
			if !equalLists(again.AllowedProviders, tc.providers) {
				t.Errorf("AllowedProviders = %v after the caller edited its copy, want %v", again.AllowedProviders, tc.providers)
			}
			if !equalLists(again.AllowedRegions, tc.regions) {
				t.Errorf("AllowedRegions = %v after the caller edited its copy, want %v", again.AllowedRegions, tc.regions)
			}
		})
	}

	t.Run("the caller's own policy is not shared with the snapshot", func(t *testing.T) {
		providers := []string{"openai"}
		ctx := WithExecConfig(context.Background(), &ExecConfig{
			DataPolicy:         types.DataPolicy{AllowedProviders: providers},
			DataPolicyDeclared: true,
		})
		providers[0] = "tampered"
		got, _ := ExecDataPolicy(ctx)
		if !reflect.DeepEqual(got.AllowedProviders, []string{"openai"}) {
			t.Errorf("AllowedProviders = %v, want the list as it was when the snapshot was taken", got.AllowedProviders)
		}
	})

	t.Run("a context with no policy declared returns the zero policy", func(t *testing.T) {
		got, declared := ExecDataPolicy(WithExecConfig(context.Background(), &ExecConfig{}))
		if declared || got.AllowedProviders != nil || got.AllowedRegions != nil {
			t.Errorf("got %+v, %v for a client that declared nothing", got, declared)
		}
	})
}

// equalLists compares two lists by content, with nil and empty alike.
func equalLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
