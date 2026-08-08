package schemaflux

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// IN-004's Verify line: "two clients with different providers, budgets, and
// policies run concurrently and independently."
//
// A test with one client proves nothing here. The bug was never that a single
// client misbehaved — it was that there was only ever one set of process
// globals, so constructing a second client silently reconfigured the first.
// IN-001's mutexes made that safe and left it wrong: a mutex around a global
// passes `-race` and still fails this test.
//
// So every case below runs TWO clients, and most of them run both at once.

// namedProvider answers with its own name, so a test can tell which client's
// provider actually served a call rather than only that some call happened.
type namedProvider struct {
	name  string
	calls atomic.Int64
	cost  float64
}

func (p *namedProvider) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.calls.Add(1)
	return llm.CompletionResponse{
		Content:      `{"name":"` + p.name + `"}`,
		Provider:     p.name,
		Model:        p.name + "-model",
		FinishReason: "stop",
	}, nil
}

func (p *namedProvider) Name() string                               { return p.name }
func (p *namedProvider) EstimateCost(llm.CompletionRequest) float64 { return p.cost }
func (p *namedProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

type isolationTarget struct {
	Name string `json:"name"`
}

func TestTwoClientsReachTheirOwnProvidersConcurrently(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	first := &namedProvider{name: "alpha"}
	second := &namedProvider{name: "beta"}

	clientA := NewClient("key-a").WithProviderInstance(first)
	clientB := NewClient("key-b").WithProviderInstance(second)

	// The provider is NOT passed to the operation. It has to be resolved from
	// the client's context, because that resolution is the entire thing under
	// test -- handing each call its provider explicitly would pass even with
	// the seam removed.
	extract := func(client *Client) {
		ctx := client.Context(context.Background())
		opts := ops.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, _ = ops.ExtractResult[isolationTarget]("some input", opts)
	}

	const each = 50
	var wg sync.WaitGroup
	for i := 0; i < each; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); extract(clientA) }()
		go func() { defer wg.Done(); extract(clientB) }()
	}
	wg.Wait()

	if got := first.calls.Load(); got != each {
		t.Errorf("client A's provider saw %d calls, want %d", got, each)
	}
	if got := second.calls.Load(); got != each {
		t.Errorf("client B's provider saw %d calls, want %d", got, each)
	}
}

// The half that was genuinely impossible before: two budgets. One client
// exhausting its allowance must not stop the other, which is exactly what a
// single process-wide budget did.
func TestOneClientsExhaustedBudgetDoesNotStopAnother(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	poor := NewClient("key-poor").WithBudget(0.0001)
	rich := NewClient("key-rich").WithBudget(1000)

	// Spend the poor client's allowance directly on its ledger, which is what
	// a real call would have done.
	poorCtx := poor.Context(context.Background())
	if budget := ops.ExecBudget(poorCtx); budget != nil {
		budget.Record(1.0, true)
	} else {
		t.Fatal("the poor client carried no budget into its context")
	}

	if err := ops.ExecBudget(poorCtx).Check(); err == nil {
		t.Error("the exhausted client was still permitted to call")
	}

	richCtx := rich.Context(context.Background())
	richBudget := ops.ExecBudget(richCtx)
	if richBudget == nil {
		t.Fatal("the rich client carried no budget into its context")
	}
	if err := richBudget.Check(); err != nil {
		t.Errorf("the second client was refused by the first client's exhausted budget: %v", err)
	}
}

func TestAClientWithNoBudgetFallsBackToTheProcessBudget(t *testing.T) {
	plain := NewClient("key-plain")

	ctx := plain.Context(context.Background())
	if budget := ops.ExecBudget(ctx); budget != nil {
		t.Error("a client that never asked for a budget carries one; the process-wide budget should apply unchanged")
	}
}

func TestAZeroCeilingClearsTheBudgetRatherThanPinningIt(t *testing.T) {
	client := NewClient("key").WithBudget(5)
	if ops.ExecBudget(client.Context(context.Background())) == nil {
		t.Fatal("precondition: a positive ceiling should install a ledger")
	}

	client.WithBudget(0)
	if ops.ExecBudget(client.Context(context.Background())) != nil {
		t.Error("a zero ceiling pinned the client to a budget it can never spend under; it should clear it")
	}
}

// A call already running keeps the configuration it started with. This is the
// property the word "snapshot" is doing work for: not that the struct lacks
// setters, but that reconfiguring a client cannot reach into a call in flight.
func TestReconfiguringAClientDoesNotChangeACallAlreadyInFlight(t *testing.T) {
	first := &namedProvider{name: "alpha"}
	second := &namedProvider{name: "beta"}

	client := NewClient("key").WithProviderInstance(first)
	inFlight := client.Context(context.Background())

	// Reconfigure after the context was taken.
	client.WithProviderInstance(second)

	if got := ops.ExecProvider(inFlight); got != llm.Provider(first) {
		t.Errorf("the in-flight context now resolves to %v; reconfiguration reached a call already started", got)
	}

	// And a context taken after the change sees the change.
	if got := ops.ExecProvider(client.Context(context.Background())); got != llm.Provider(second) {
		t.Error("a context taken after reconfiguration still resolves to the old provider")
	}
}

func TestTwoClientsCarryTheirOwnDataPolicies(t *testing.T) {
	strict := NewClient("key-strict").WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"alpha"}})
	open := NewClient("key-open").WithDataPolicy(types.DataPolicy{AllowedProviders: []string{"beta"}})

	strictPolicy, ok := ops.ExecDataPolicy(strict.Context(context.Background()))
	if !ok {
		t.Fatal("the strict client carried no policy")
	}
	openPolicy, ok := ops.ExecDataPolicy(open.Context(context.Background()))
	if !ok {
		t.Fatal("the open client carried no policy")
	}

	if len(strictPolicy.AllowedProviders) != 1 || strictPolicy.AllowedProviders[0] != "alpha" {
		t.Errorf("strict client's policy = %v", strictPolicy.AllowedProviders)
	}
	if len(openPolicy.AllowedProviders) != 1 || openPolicy.AllowedProviders[0] != "beta" {
		t.Errorf("open client's policy = %v", openPolicy.AllowedProviders)
	}
}

// An unconfigured process and a client that deliberately allows everything must
// stay distinguishable, or "no policy" and "an empty policy" become the same
// thing and one of them will eventually be enforced as the other.
func TestNoClientIsDistinguishableFromAnEmptyPolicy(t *testing.T) {
	if _, ok := ops.ExecDataPolicy(context.Background()); ok {
		t.Error("a bare context reported carrying a policy")
	}

	empty := NewClient("key").WithDataPolicy(types.DataPolicy{})
	if _, ok := ops.ExecDataPolicy(empty.Context(context.Background())); !ok {
		t.Error("a client that declared an empty policy is indistinguishable from no client at all")
	}
}

// The ledger's own honesty: an unpriced call must not read as a free one.
func TestAnUnpricedCallMakesTheSpentFigureAFloor(t *testing.T) {
	client := NewClient("key").WithBudget(10)
	budget := ops.ExecBudget(client.Context(context.Background()))

	budget.Record(1.50, true)
	if spent, complete := client.Spent(); spent != 1.50 || !complete {
		t.Errorf("Spent = (%v, %v), want (1.50, true)", spent, complete)
	}

	budget.Record(0, false)
	spent, complete := client.Spent()
	if complete {
		t.Error("the total is still reported as complete after an unpriced call")
	}
	if spent != 1.50 {
		t.Errorf("spend = %v; an unpriced call changed the figure rather than its completeness", spent)
	}
}

func TestAnExhaustedLedgerRefusesWithATypedError(t *testing.T) {
	budget := ops.NewClientBudget(1.0)
	budget.Record(1.0, true)

	err := budget.Check()
	if err == nil {
		t.Fatal("a ledger at its ceiling permitted a call")
	}
	if !errors.Is(err, ops.ErrClientBudgetExhausted) {
		t.Errorf("err = %v, want ErrClientBudgetExhausted", err)
	}
}

// Concurrency on one ledger: the recorded total must be exact, not
// approximately right.
func TestALedgerUnderConcurrentRecordsTotalsExactly(t *testing.T) {
	budget := ops.NewClientBudget(1e9)

	const goroutines, each = 20, 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				budget.Record(0.01, true)
			}
		}()
	}
	wg.Wait()

	spent, complete := budget.Spent()
	if !complete {
		t.Error("a ledger that saw only priced calls reported an incomplete total")
	}
	want := 0.01 * goroutines * each
	if diff := spent - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("spent = %v, want %v", spent, want)
	}
}

// End to end: an exhausted client budget refuses a real call, and the other
// client's identical call still goes through. This is the case the unit tests
// above cannot make -- they check the ledger, this checks that the call path
// consults it.
func TestAnExhaustedClientBudgetRefusesARealCallWhileAnotherClientProceeds(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	poorProvider := &namedProvider{name: "alpha"}
	richProvider := &namedProvider{name: "beta"}

	poor := NewClient("key-poor").WithProviderInstance(poorProvider).WithBudget(1.0)
	rich := NewClient("key-rich").WithProviderInstance(richProvider).WithBudget(1000)

	// Exhaust the poor client's ledger.
	ops.ExecBudget(poor.Context(context.Background())).Record(1.0, true)

	call := func(client *Client) error {
		ctx := client.Context(context.Background())
		opts := ops.NewExtractOptions()
		opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
		_, err := ops.ExtractResult[isolationTarget]("some input", opts)
		return err
	}

	if err := call(poor); err == nil {
		t.Error("the exhausted client's call succeeded; the call path never consulted its budget")
	}
	if got := poorProvider.calls.Load(); got != 0 {
		t.Errorf("the exhausted client contacted its provider %d time(s); the budget is checked before the request, not after", got)
	}

	if err := call(rich); err != nil {
		t.Errorf("the second client was refused: %v", err)
	}
	if got := richProvider.calls.Load(); got == 0 {
		t.Error("the funded client never reached its provider")
	}
}
