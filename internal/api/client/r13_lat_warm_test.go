package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/mw"
)

// Round 13. Client.Warm on a provider built with mw.Chain did nothing: no
// wrapper passed the call on, and a chain is how the library is documented to
// be composed.
func TestAClientWarmsAProviderThatIsWrappedInAChain(t *testing.T) {
	down := errors.New("the provider is down")
	cases := []struct {
		name    string
		wrap    func(llm.Provider) llm.Provider
		warmed  bool
		wantErr error
	}{
		{"no wrapper", func(p llm.Provider) llm.Provider { return mw.Chain(p) }, true, nil},
		{"a hedge", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.Hedge(time.Second)) }, true, nil},
		{"a cache", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.Cache()) }, true, nil},
		{"a retry", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.Retry()) }, true, nil},
		{"a rate limit", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.RateLimit(5, time.Second)) }, true, nil},
		{"a budget", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.Budget(5)) }, true, nil},
		{"a bulkhead", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.Bulkhead(3)) }, true, nil},
		{"redaction", func(p llm.Provider) llm.Provider { return mw.Chain(p, mw.RedactEgress()) }, true, nil},
		{"several", func(p llm.Provider) llm.Provider {
			return mw.Chain(p, mw.Cache(), mw.Retry(), mw.RateLimit(5, time.Second), mw.Hedge(time.Second))
		}, true, nil},
		{"a failure at the bottom of a chain", func(p llm.Provider) llm.Provider {
			p.(*warmable).fail = down
			return mw.Chain(p, mw.Cache(), mw.Hedge(time.Second))
		}, false, down},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &warmable{Provider: &chainBase{}}
			client := &Client{provider: tc.wrap(base)}
			warmed, err := client.Warm(context.Background())
			if warmed != tc.warmed || !errors.Is(err, tc.wantErr) {
				t.Errorf("Warm = %v, %v; want %v, %v", warmed, err, tc.warmed, tc.wantErr)
			}
			if base.warmed != 1 {
				t.Errorf("the provider at the bottom was asked to warm %d times, want 1", base.warmed)
			}
		})
	}

	t.Run("a chain over a provider with nothing to warm", func(t *testing.T) {
		local, err := llm.NewLocalProvider(llm.ProviderConfig{})
		if err != nil {
			t.Fatal(err)
		}
		client := &Client{provider: mw.Chain(local, mw.Cache(), mw.Hedge(time.Second))}
		if warmed, err := client.Warm(context.Background()); warmed || err != nil {
			t.Errorf("Warm = %v, %v", warmed, err)
		}
	})
}

// chainBase is the provider under a warmable, which has to be one to be wrapped.
type chainBase struct{}

func (*chainBase) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, nil
}
func (*chainBase) Name() string                               { return "fake" }
func (*chainBase) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (*chainBase) RetryPolicy() (int, time.Duration)          { return 0, 0 }
