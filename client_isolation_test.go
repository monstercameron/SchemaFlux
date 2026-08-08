package schemaflux

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
)

// TI-002 / IN-004 / TI-008, exercised through the exported API rather than
// internal/ops directly.
//
// namedFakeProvider mirrors internal/ops's isolation_test.go fake: it stamps
// its own name into the answer and counts calls, so a test can tell WHICH
// provider actually answered rather than assuming it from which client made
// the call.
type namedFakeProvider struct {
	name string

	mu    sync.Mutex
	calls int
}

func (p *namedFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return llm.CompletionResponse{
		Content:      fmt.Sprintf("answered by %s", p.name),
		Provider:     p.name,
		Model:        "fake-model",
		FinishReason: "stop",
	}, nil
}
func (p *namedFakeProvider) Name() string                               { return p.name }
func (p *namedFakeProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *namedFakeProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }
func (p *namedFakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// The simpler statement of the same bug (TI-002's task body): build client A,
// build client B, and A's own operation must still reach A's provider.
//
// Watched failing before Context/ops.WithProvider existed: with
// Format's opts.Context carrying nothing, both calls fell through to
// GetDefaultClient()'s package-level provider, which is whichever of A or B
// was constructed (or WithProviderInstance'd) last -- see
// internal/ops/isolation_test.go's header comment for the exact failure
// recorded when the same underlying check was disabled.
func TestClientsBuiltInSequenceDoNotRepointEachOther(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	providerA := &namedFakeProvider{name: "client-a"}
	clientA := NewClient("").WithProviderInstance(providerA)

	// Building B after A is the sequence that used to repoint A: every
	// WithProviderInstance call (and every Client constructor) writes the one
	// package-level ops.defaultProvider every operation used to read
	// unconditionally.
	providerB := &namedFakeProvider{name: "client-b"}
	clientB := NewClient("").WithProviderInstance(providerB)
	_ = clientB

	got, err := Format("payload", "uppercase", OpOptions{Context: clientA.Context(context.Background())})
	if err != nil {
		t.Fatalf("Format via clientA: %v", err)
	}
	if !strings.Contains(got, "client-a") {
		t.Fatalf("clientA's operation was answered by the wrong provider: %q", got)
	}
	if providerB.callCount() != 0 {
		t.Errorf("providerB was called %d times by an operation routed through clientA's context", providerB.callCount())
	}
}

// TI-002's Verify line, through the exported API: two clients with different
// fake providers run concurrently without interference.
func TestClientsWithDifferentProvidersRunConcurrentlyWithoutInterference(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	providerA := &namedFakeProvider{name: "client-a"}
	clientA := NewClient("").WithProviderInstance(providerA)

	providerB := &namedFakeProvider{name: "client-b"}
	clientB := NewClient("").WithProviderInstance(providerB)

	const iterations = 25
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)

	run := func(client *Client, wantName string) {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			got, err := Format("payload", "uppercase", OpOptions{Context: client.Context(context.Background())})
			if err != nil {
				errs <- fmt.Errorf("Format via %s: %v", wantName, err)
				continue
			}
			if !strings.Contains(got, wantName) {
				errs <- fmt.Errorf("expected an answer from %s, got %q", wantName, got)
			}
		}
	}

	wg.Add(2)
	go run(clientA, "client-a")
	go run(clientB, "client-b")
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	if providerA.callCount() != iterations {
		t.Errorf("providerA.callCount() = %d, want %d", providerA.callCount(), iterations)
	}
	if providerB.callCount() != iterations {
		t.Errorf("providerB.callCount() = %d, want %d", providerB.callCount(), iterations)
	}
}
