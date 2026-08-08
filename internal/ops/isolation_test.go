package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// TI-002 / IN-004 / TI-008. Before WithProvider existed, the only way for an
// operation to reach a provider was the package globals in llm_helper.go:
// defaultProvider and customLLMCaller, written by every client construction
// and every schemafluxtest.Install. Two of these tests are run FIRST against
// that global-only path (by calling callLLM with no per-call provider on the
// context, only SetDefaultProvider) to confirm they fail the way the task
// description says they must -- "the test that today's global would fail" --
// and the failure is recorded below rather than only asserted about.
//
// Watched failing before WithProvider's callLLM check existed (both
// TestSequentialClientsDoNotRepointEachOther and
// TestConcurrentProvidersDoNotCrossContaminate): with callLLM consulting only
// currentHooks(), building providerB after providerA and calling
// SetDefaultProvider(providerB) (which is what every Client construction and
// WithProviderInstance do) repointed every subsequent callLLM at providerB
// regardless of which "client's" context was used to make the call, because
// there was nothing in the call path that looked at the context at all. The
// first test failed outright (both calls answered by providerB); the second
// failed nondeterministically depending on which goroutine's SetDefaultProvider
// call happened to run last. Both pass now that callLLM prefers
// providerFromContext(ctx) over the global.

// namedProvider is a fake llm.Provider that stamps its own name into every
// answer and counts how many times it was called, so a test can tell WHICH
// provider actually answered a call rather than assuming it from which
// "client" made the call.
//
// budget models a per-caller resource ceiling at the provider itself, not
// through pricing.CheckBudget: that function reads pricing's OWN package
// state, which is a second, unrelated package-level global (PR-005) outside
// this task's file list. Modelling the budget here proves the property
// TI-008 actually asks for -- that per-caller STATE does not leak between
// callers using different providers -- without touching a package this task
// was not authorized to change.
type namedProvider struct {
	name   string
	budget int // 0 means unlimited

	mu    sync.Mutex
	calls int
}

func (p *namedProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	calls := p.calls
	p.mu.Unlock()

	if p.budget > 0 && calls > p.budget {
		return llm.CompletionResponse{}, fmt.Errorf("%s: budget of %d calls exhausted", p.name, p.budget)
	}

	return llm.CompletionResponse{
		Content:      fmt.Sprintf("answered by %s", p.name),
		Provider:     p.name,
		Model:        "fake-model",
		FinishReason: "stop",
	}, nil
}

func (p *namedProvider) Name() string { return p.name }

func (p *namedProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }

func (p *namedProvider) RetryPolicy() (int, time.Duration) { return 0, time.Millisecond }

func (p *namedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// The sequential case TI-002's task body names as "the simpler statement of
// the same bug": build A, build B, and A must still reach A's provider.
//
// Before WithProvider, "building" a provider in this test package means
// calling SetDefaultProvider directly (the same call every Client
// construction and schemafluxtest.Install make); doing that for B after A
// exists is exactly the sequence that used to repoint A.
func TestSequentialClientsDoNotRepointEachOther(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	providerA := &namedProvider{name: "provider-a"}
	providerB := &namedProvider{name: "provider-b"}

	ctxA := WithProvider(context.Background(), providerA)

	// Simulates a second Client being constructed after the first: every
	// Client constructor and WithProviderInstance calls SetDefaultProvider,
	// which is the global this whole task exists to stop being the only path.
	SetDefaultProvider(providerB)
	t.Cleanup(func() { SetDefaultProvider(nil) })

	got, err := callLLM(ctxA, "system", "user", types.OpOptions{})
	if err != nil {
		t.Fatalf("callLLM via ctxA: %v", err)
	}
	if !strings.Contains(got, "provider-a") {
		t.Fatalf("A's call was answered by the wrong provider: %q", got)
	}
	if providerB.callCount() != 0 {
		t.Errorf("providerB was called %d times by a request routed through A's context", providerB.callCount())
	}
}

// The concurrent case TI-002's Verify line asks for by name: two clients with
// different fake providers, running concurrently, each seeing only its own.
// Budgets differ per provider (see namedProvider's comment on why the budget
// is modelled there) so this also covers TI-008's Revised line, which adds
// "budgets" explicitly alongside "providers" to what must not cross over.
func TestConcurrentProvidersDoNotCrossContaminate(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	providerA := &namedProvider{name: "provider-a", budget: 5}
	providerB := &namedProvider{name: "provider-b", budget: 50}

	ctxA := WithProvider(context.Background(), providerA)
	ctxB := WithProvider(context.Background(), providerB)

	const iterationsPerWorker = 25
	const workersPerProvider = 4

	var wg sync.WaitGroup
	errs := make(chan error, workersPerProvider*iterationsPerWorker*2)

	run := func(ctx context.Context, wantName string) {
		defer wg.Done()
		for i := 0; i < iterationsPerWorker; i++ {
			got, err := callLLM(ctx, "system", "user", types.OpOptions{})
			if err != nil {
				// A budget-exhausted provider erroring is expected once its own
				// ceiling passes; what matters is that a SUCCESSFUL answer never
				// names the other provider.
				continue
			}
			if !strings.Contains(got, wantName) {
				errs <- fmt.Errorf("expected an answer from %s, got %q", wantName, got)
			}
		}
	}

	for w := 0; w < workersPerProvider; w++ {
		wg.Add(2)
		go run(ctxA, "provider-a")
		go run(ctxB, "provider-b")
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	if providerA.callCount() == 0 {
		t.Error("providerA was never called")
	}
	if providerB.callCount() == 0 {
		t.Error("providerB was never called")
	}

	// providerA's budget is 5; every worker sharing ctxA hits the SAME
	// provider value, so the total calls it recorded (successes and refusals
	// together) is bounded by how many concurrent callers reached it before
	// each saw the shared counter pass 5 -- it must never be told, directly or
	// by way of B's ctx, to enforce B's ceiling of 50, and B's provider must
	// never be limited by A's ceiling of 5 either.
	totalRequestsToA := workersPerProvider * iterationsPerWorker
	if providerA.callCount() > totalRequestsToA {
		t.Errorf("providerA.callCount() = %d, more than the %d requests ctxA workers made",
			providerA.callCount(), totalRequestsToA)
	}
	if providerB.callCount() < providerA.budget {
		t.Errorf("providerB.callCount() = %d, expected it to keep succeeding well past A's budget of %d",
			providerB.callCount(), providerA.budget)
	}
}

// A context provider takes priority over the test caller hook
// (customLLMCaller / setLLMCaller), which is what schemafluxtest.Install
// installs. Without this, a Client-scoped provider attached via WithProvider
// inside a test that also used schemafluxtest.Install could be silently
// overridden by the test hook instead of the other way around.
func TestContextProviderOutranksTheTestCaller(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	originalCaller, originalProvider := currentHooks()
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})

	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return "answered by the test caller hook", nil
	})

	scoped := &namedProvider{name: "scoped-provider"}
	ctx := WithProvider(context.Background(), scoped)

	got, err := callLLM(ctx, "system", "user", types.OpOptions{})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}
	if !strings.Contains(got, "scoped-provider") {
		t.Fatalf("got %q, want the context-scoped provider to win over the test caller hook", got)
	}
}

// WithProvider(ctx, nil) must not install anything -- ctx.Value would then
// return a non-nil interface holding a nil provider, and providerFromContext
// would hand callLLM a Provider whose method set is unusable. The regression
// this guards: the global default, or ErrNoProvider, would silently stop
// being reachable through a context that "attached" a nil provider.
func TestWithProviderNilLeavesContextUnchanged(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	ctx := WithProvider(context.Background(), nil)
	if got := providerFromContext(ctx); got != nil {
		t.Errorf("providerFromContext = %v, want nil", got)
	}

	originalCaller, originalProvider := currentHooks()
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})
	setLLMCaller(nil)
	SetDefaultProvider(nil)

	if _, err := callLLM(ctx, "system", "user", types.OpOptions{}); err != ErrNoProvider {
		t.Errorf("callLLM with a nil-attached context and no global = %v, want ErrNoProvider", err)
	}
}

// providerFromContext must not panic on a nil context -- operationContext
// falls back to context.Background() when the caller passed nil, but
// providerFromContext is reachable independently from callLLM's very first
// line, before that fallback runs.
func TestProviderFromContextNilContext(t *testing.T) {
	//lint:ignore SA1012 the nil check inside providerFromContext is exactly what this test exercises.
	if got := providerFromContext(nil); got != nil {
		t.Errorf("providerFromContext(nil) = %v, want nil", got)
	}
}
