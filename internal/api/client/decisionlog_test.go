package client

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// A client's decision observer reaches the context its calls run under, and a
// context taken before a change keeps what it was given.
func TestAClientsDecisionObserverTravelsInItsContext(t *testing.T) {
	var told []string
	first := func(_ context.Context, record ops.DecisionRecord) { told = append(told, "first:"+record.Operation) }
	second := func(_ context.Context, record ops.DecisionRecord) { told = append(told, "second:"+record.Operation) }

	cases := []struct {
		name  string
		build func(*Client) *Client
		want  []string
	}{
		{"none registered", func(c *Client) *Client { return c }, nil},
		{"one", func(c *Client) *Client { return c.WithDecisionObserver(first) }, []string{"first:is"}},
		{"one, then another", func(c *Client) *Client { return c.WithDecisionObserver(first).WithDecisionObserver(second) }, []string{"second:is"}},
		{"one, then none", func(c *Client) *Client { return c.WithDecisionObserver(first).WithDecisionObserver(nil) }, nil},
		{"after other options", func(c *Client) *Client { return c.WithModel("m").WithDecisionObserver(first).WithModel("n") }, []string{"first:is"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			told = nil
			client := tc.build(&Client{})
			ctx := client.Context(context.Background())

			observer := ops.ExecDecisionObserver(ctx)
			if (observer != nil) != (tc.want != nil) {
				t.Fatalf("the context's observer is %v, want one: %v", observer != nil, tc.want != nil)
			}
			if observer != nil {
				observer(ctx, ops.DecisionRecord{Operation: "is"})
			}
			if len(told) != len(tc.want) || (len(told) == 1 && told[0] != tc.want[0]) {
				t.Errorf("the observer called was %v, want %v", told, tc.want)
			}

			// A context already taken is not changed by what the client is told
			// afterwards.
			told = nil
			client.WithDecisionObserver(second)
			if got := ops.ExecDecisionObserver(ctx); (got != nil) != (tc.want != nil) {
				t.Error("a context taken earlier changed with the client")
			}
			if again := ops.ExecDecisionObserver(client.Context(context.Background())); again == nil {
				t.Error("the observer set afterwards did not reach a new context")
			}
		})
	}
}
