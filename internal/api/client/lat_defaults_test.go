package client

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A client's model and effort reach the context its calls run under, and a
// context taken before a change keeps what it was given.
func TestAClientsModelAndEffortTravelInItsContext(t *testing.T) {
	cases := []struct {
		name   string
		build  func(*Client) *Client
		model  string
		effort types.Effort
	}{
		{"neither set", func(c *Client) *Client { return c }, "", types.EffortDefault},
		{"a model", func(c *Client) *Client { return c.WithModel("gpt-6-luna") }, "gpt-6-luna", types.EffortDefault},
		{"a model with space around it", func(c *Client) *Client { return c.WithModel("  gpt-6-luna ") }, "gpt-6-luna", types.EffortDefault},
		{"a model, then none", func(c *Client) *Client { return c.WithModel("gpt-6-luna").WithModel("") }, "", types.EffortDefault},
		{"a model, then another", func(c *Client) *Client { return c.WithModel("a").WithModel("b") }, "b", types.EffortDefault},
		{"an effort", func(c *Client) *Client { return c.WithEffort(types.EffortLow) }, "", types.EffortLow},
		{"an effort of none", func(c *Client) *Client { return c.WithEffort(types.EffortNone) }, "", types.EffortNone},
		{"an effort, then the default", func(c *Client) *Client { return c.WithEffort(types.EffortHigh).WithEffort(types.EffortDefault) }, "", types.EffortDefault},
		{"both", func(c *Client) *Client { return c.WithModel("gpt-6-luna").WithEffort(types.EffortNone) }, "gpt-6-luna", types.EffortNone},
		{"both, the other way round", func(c *Client) *Client { return c.WithEffort(types.EffortNone).WithModel("gpt-6-luna") }, "gpt-6-luna", types.EffortNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.build(&Client{})
			ctx := client.Context(context.Background())
			if got := ops.ExecModel(ctx); got != tc.model {
				t.Errorf("the context's model is %q, want %q", got, tc.model)
			}
			if got := ops.ExecEffort(ctx); got != tc.effort {
				t.Errorf("the context's effort is %q, want %q", got, tc.effort)
			}

			// A context already taken is not changed by what the client is
			// told afterwards.
			client.WithModel("changed-later").WithEffort(types.EffortMax)
			if ops.ExecModel(ctx) != tc.model || ops.ExecEffort(ctx) != tc.effort {
				t.Error("a context taken earlier changed with the client")
			}
		})
	}
}

// warmable counts how often it was asked to warm, and can be told to fail.
type warmable struct {
	llm.Provider
	warmed int
	fail   error
}

func (w *warmable) Warm(context.Context) error {
	w.warmed++
	return w.fail
}

// A client warms its own provider's connection, and says when it had nothing
// to warm.
func TestAClientWarmsItsProvidersConnection(t *testing.T) {
	down := errors.New("the provider is down")
	local, err := llm.NewLocalProvider(llm.ProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		provider llm.Provider
		warmed   bool
		wantErr  error
		asked    int
	}{
		{"a provider that can warm", &warmable{}, true, nil, 1},
		{"a provider that fails to", &warmable{fail: down}, false, down, 1},
		{"a provider with no connection to open", local, false, nil, 0},
		{"no provider", nil, false, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{provider: tc.provider}
			warmed, err := client.Warm(context.Background())
			if warmed != tc.warmed || !errors.Is(err, tc.wantErr) {
				t.Errorf("Warm = %v, %v; want %v, %v", warmed, err, tc.warmed, tc.wantErr)
			}
			if asked, ok := tc.provider.(*warmable); ok && asked.warmed != tc.asked {
				t.Errorf("the provider was asked to warm %d times, want %d", asked.warmed, tc.asked)
			}
		})
	}
}
