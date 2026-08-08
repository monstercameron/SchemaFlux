package fluent

import (
	"context"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// IN-004's real finding in this package (recorded in reach_test.go and
// SECURITY.md): ChooseBy, FilterBy, and SortBy take no context.Context, so
// ops.WithProvider(ctx, p) -- the seam TI-002 built so a caller can pin an
// operation to a specific provider -- cannot reach them. They can only
// resolve the process-wide default (ops.SetDefaultProvider), which means two
// callers in one process, each wanting a different provider, get whichever
// one was installed last for every call made through those three spellings.
//
// entrypoints.go adds ChooseByContext / FilterByContext / SortByContext
// rather than changing the existing three: a required-context signature
// change would break every existing call site of ChooseBy/FilterBy/SortBy
// outside this package (the root schemaflux package re-exports them
// verbatim in fluent.go, and provider_integration_test.go already documents
// the same constraint for Run's own signature). The old spellings stay
// exactly as broken as reach_test.go found them; the new ones are the fix.
//
// This test is the proof the task asked for: two providers, each reached
// only through its own context, running concurrently -- not one client
// working, but two clients not stepping on each other. The equivalent test
// at the Client level already exists
// (client_isolation_test.go:TestClientsWithDifferentProvidersRunConcurrentlyWithoutInterference,
// root package, TI-008); it cannot be duplicated here because Client lives in
// the root package, which imports this one, so importing it back would be a
// cycle. ops.WithProvider is the same primitive Client.Context(ctx) itself
// calls, so exercising it directly proves the same guarantee at the layer
// this task owns.
func TestShorthandContextVariantsIsolateConcurrentProviders(t *testing.T) {
	items := []string{"first", "second", "third"}
	sortedBody := `{"ids":["i-000001","i-000002","i-000003"]}`

	providerA := &countingProvider{body: sortedBody}
	providerB := &countingProvider{body: sortedBody}

	// The process-wide default is a third, distinct provider that neither
	// call below should ever reach. If SortByContext silently fell back to
	// it the way the plain SortBy is documented to, the call counts for A and
	// B below would still come out right by coincidence (each goroutine only
	// checks its own error channel) -- this catch is what makes the test
	// prove isolation rather than just "it still returns no error."
	defaultProvider := &countingProvider{body: sortedBody}
	restoreDefault := installStubProvider(t, defaultProvider)
	defer restoreDefault()

	const iterations = 25
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)

	run := func(p *countingProvider) {
		defer wg.Done()
		ctx := ops.WithProvider(context.Background(), p)
		for i := 0; i < iterations; i++ {
			if _, err := SortByContext(ctx, items, "alphabetically"); err != nil {
				errs <- err
			}
		}
	}

	wg.Add(2)
	go run(providerA)
	go run(providerB)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("SortByContext: %v", err)
	}

	if n := providerA.calls.Load(); n != iterations {
		t.Errorf("providerA.calls = %d, want %d -- SortByContext(ctxA, ...) did not reach providerA's own calls", n, iterations)
	}
	if n := providerB.calls.Load(); n != iterations {
		t.Errorf("providerB.calls = %d, want %d -- SortByContext(ctxB, ...) did not reach providerB's own calls", n, iterations)
	}
	if n := defaultProvider.calls.Load(); n != 0 {
		t.Errorf("the process-wide default provider was reached %d time(s); SortByContext must never fall through to it when a per-call provider is set", n)
	}
}

// The same guarantee for ChooseByContext and FilterByContext, run once each
// rather than under concurrency -- SortByContext's test above is where the
// concurrent proof lives; these two confirm the same wiring exists on the
// other two shorthand entrypoints rather than only on Sort.
func TestChooseByAndFilterByContextReachTheirOwnProvider(t *testing.T) {
	items := []string{"first", "second", "third"}

	t.Run("ChooseByContext", func(t *testing.T) {
		own := &countingProvider{body: `{"id":"i-000001"}`}
		other := &countingProvider{body: `{"id":"i-000001"}`}
		restore := installStubProvider(t, other)
		defer restore()

		ctx := ops.WithProvider(context.Background(), own)
		if _, err := ChooseByContext(ctx, items, "the best one"); err != nil {
			t.Fatalf("ChooseByContext: %v", err)
		}
		if n := own.calls.Load(); n != 1 {
			t.Errorf("own.calls = %d, want 1", n)
		}
		if n := other.calls.Load(); n != 0 {
			t.Errorf("the process-wide default was reached %d time(s); want 0", n)
		}
	})

	t.Run("FilterByContext", func(t *testing.T) {
		own := &countingProvider{body: `{"ids":["i-000001"]}`}
		other := &countingProvider{body: `{"ids":["i-000001"]}`}
		restore := installStubProvider(t, other)
		defer restore()

		ctx := ops.WithProvider(context.Background(), own)
		if _, err := FilterByContext(ctx, items, "the good ones"); err != nil {
			t.Fatalf("FilterByContext: %v", err)
		}
		if n := own.calls.Load(); n != 1 {
			t.Errorf("own.calls = %d, want 1", n)
		}
		if n := other.calls.Load(); n != 0 {
			t.Errorf("the process-wide default was reached %d time(s); want 0", n)
		}
	})
}
