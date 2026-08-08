package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
)

// End-to-end: the repair loop, observed through the public API.
//
// Extract retries a malformed answer rather than failing on it, and the
// envelope reports how many calls that took. Both halves were tested inside the
// package that implements them and neither was ever observed from outside — and
// this is the machinery that had a real bug: a request that burned its whole
// retry budget used to report Attempts of 0, because nothing published a call
// record on the failure path. A caller asking "how many attempts did that take"
// got the right answer only for the calls that worked, which is the opposite of
// when the question is asked.

// flakyProvider fails a fixed number of times, then answers.
type flakyProvider struct {
	mu        sync.Mutex
	badBodies int    // how many unusable answers to give first
	good      string // the answer once the bad ones are exhausted
	calls     int
}

func (p *flakyProvider) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++

	body := p.good
	if p.calls <= p.badBodies {
		body = "this is not JSON at all"
	}
	return schemaflux.CompletionResponse{
		Content:      body,
		Provider:     "local",
		Model:        req.Model,
		FinishReason: "stop",
	}, nil
}

func (p *flakyProvider) Name() string                                      { return "local" }
func (p *flakyProvider) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (p *flakyProvider) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func (p *flakyProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestOneUnusableAnswerIsRepairedAndTheEnvelopeSaysSo(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &flakyProvider{badBodies: 1, good: `{"number":"INV-4417","vendor":"Northwind"}`}
	ctx := ops.WithProvider(context.Background(), provider)

	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)

	result, err := schemaflux.ExtractResult[invoiceDoc]("the raw document", opts)
	if err != nil {
		t.Fatalf("Extract gave up on an answer it was able to repair: %v", err)
	}

	if result.Value.Number != "INV-4417" {
		t.Errorf("value = %+v, want the repaired answer", result.Value)
	}
	if provider.callCount() < 2 {
		t.Errorf("the provider saw %d call(s); one unusable answer should have produced a second", provider.callCount())
	}

	// The envelope has to say the retry happened. A result that took two calls
	// and reports one is the same class of lie as a cost that omits a retry.
	if result.Meta.Attempts < 2 {
		t.Errorf("Meta.Attempts = %d after %d provider calls; the envelope under-reports what the answer cost",
			result.Meta.Attempts, provider.callCount())
	}
}

// A request that never gets a usable answer must report the attempts it burned.
// Reporting zero is what this originally did, and it is the reading that matters
// most: nobody asks how many attempts a successful call took.
func TestAnExhaustedRequestStillReportsTheAttemptsItBurned(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	// Never returns anything usable.
	provider := &flakyProvider{badBodies: 1000, good: `{"number":"never"}`}
	ctx := ops.WithProvider(context.Background(), provider)

	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)

	result, err := schemaflux.ExtractResult[invoiceDoc]("the raw document", opts)
	if err == nil {
		t.Fatal("Extract accepted an answer that never parsed")
	}

	calls := provider.callCount()
	if calls == 0 {
		t.Fatal("the provider was never called")
	}
	if result.Meta.Attempts == 0 {
		t.Errorf("Meta.Attempts = 0 after %d provider calls; a failed request that reports no attempts is the exact bug the failure-path call record was added to fix", calls)
	}
}

// The repair budget is finite. A provider that never produces a usable answer
// must not be retried indefinitely — an unbounded repair loop against a paid
// endpoint is the expensive kind of hang.
func TestRepairsAreBoundedRatherThanEndless(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &flakyProvider{badBodies: 1000, good: `{}`}
	ctx := ops.WithProvider(context.Background(), provider)

	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = schemaflux.ExtractResult[invoiceDoc]("the raw document", opts)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Extract did not give up within 30s against a provider that never answers usably")
	}

	if calls := provider.callCount(); calls > 20 {
		t.Errorf("the provider was called %d times for one Extract; the repair budget is not bounding anything", calls)
	}
}
