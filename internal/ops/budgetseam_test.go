package ops

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// IN-004's seam, tested where it lives.
//
// It was only exercised from the root package's client tests, which are an
// external package, so `go test ./internal/ops/` saw none of it — the budget
// that decides whether a tenant may spend money had no coverage in its own
// package. That is the wrong place for a gap: a per-client ledger is the piece
// that stops one tenant's exhausted allowance from refusing another tenant's
// calls, and it is enforced before the request rather than after the invoice.

func TestAClientBudgetRefusesAtItsCeilingAndNotBefore(t *testing.T) {
	budget := NewClientBudget(1.00)

	if err := budget.Check(); err != nil {
		t.Fatalf("a fresh ledger refused a call: %v", err)
	}

	budget.Record(0.99, true)
	if err := budget.Check(); err != nil {
		t.Errorf("refused at 0.99 of a 1.00 ceiling: %v", err)
	}

	budget.Record(0.01, true)
	err := budget.Check()
	if err == nil {
		t.Fatal("a ledger exactly at its ceiling permitted a call")
	}
	if !errors.Is(err, ErrClientBudgetExhausted) {
		t.Errorf("err = %v, want ErrClientBudgetExhausted", err)
	}
}

// A ceiling of zero or less means "no ceiling", which is what a client that
// never asked for a budget gets. It must not mean "refuse everything" -- that
// would make an unconfigured client unusable rather than unlimited.
func TestANonPositiveCeilingEnforcesNothing(t *testing.T) {
	for _, ceiling := range []float64{0, -1} {
		budget := NewClientBudget(ceiling)
		budget.Record(1000, true)

		if err := budget.Check(); err != nil {
			t.Errorf("a ceiling of %v refused a call after spending 1000; a non-positive ceiling means unlimited, not zero", ceiling)
		}
		if got := budget.Ceiling(); got != ceiling {
			t.Errorf("Ceiling() = %v, want %v", got, ceiling)
		}
	}
}

// The expensive mistake this type exists to avoid: an unpriced model reports a
// cost of zero, and a ledger that accumulates those keeps reporting a spend of
// nothing while real money is being spent.
func TestAnUnpricedCallIsCountedRatherThanTreatedAsFree(t *testing.T) {
	budget := NewClientBudget(10)

	budget.Record(0, false)
	budget.Record(0, false)

	spent, complete := budget.Spent()
	if spent != 0 {
		t.Errorf("Spent = %v; an unpriced call has no known cost to add", spent)
	}
	if complete {
		t.Error("Spent reported a COMPLETE total after two unpriced calls; the figure is a floor and has to say so")
	}
	if got := budget.UnpricedCalls(); got != 2 {
		t.Errorf("UnpricedCalls = %d, want 2 -- 'your total is incomplete' is not actionable without 'and this is how incomplete'", got)
	}
}

// An unpriced call must not open the gate either: once the ledger has seen one,
// its total is a floor, and a floor at the ceiling is still at the ceiling.
func TestUnpricedCallsDoNotReopenAnExhaustedLedger(t *testing.T) {
	budget := NewClientBudget(1.00)
	budget.Record(1.00, true)

	if err := budget.Check(); err == nil {
		t.Fatal("precondition: the ledger should be exhausted")
	}

	budget.Record(0, false)

	err := budget.Check()
	if err == nil {
		t.Fatal("an unpriced call reopened an exhausted ledger")
	}
	// The refusal should say the total is a floor, so a reader is not misled
	// into thinking the number is exact.
	if !errors.Is(err, ErrClientBudgetExhausted) {
		t.Errorf("err = %v, want ErrClientBudgetExhausted", err)
	}
	if got := budget.UnpricedCalls(); got != 1 {
		t.Errorf("UnpricedCalls = %d, want 1", got)
	}
}

func TestALedgerTotalsExactlyUnderConcurrentRecords(t *testing.T) {
	budget := NewClientBudget(1e9)

	const goroutines, each = 16, 64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				budget.Record(0.25, true)
			}
		}()
	}
	wg.Wait()

	spent, complete := budget.Spent()
	if !complete {
		t.Error("a ledger that saw only priced calls reported an incomplete total")
	}
	want := 0.25 * goroutines * each
	if diff := spent - want; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("spent = %v, want %v", spent, want)
	}
}

// --- ExecConfig: the per-call snapshot.

func TestAnExecConfigIsCopiedIntoTheContextRatherThanShared(t *testing.T) {
	provider := &scriptedProvider{bodies: []string{"answer"}}
	budget := NewClientBudget(5)

	config := &ExecConfig{Provider: provider, Budget: budget}
	ctx := WithExecConfig(context.Background(), config)

	// Mutating the caller's struct after the fact must not reach the call that
	// already started. Storing the pointer would let a client reconfigured
	// later change a request in flight -- the process-global bug again, in a
	// form that looks like per-call configuration.
	config.Provider = nil
	config.Budget = nil

	if got := ExecProvider(ctx); got == nil {
		t.Error("the in-flight context lost its provider when the caller's struct was mutated")
	}
	if got := ExecBudget(ctx); got == nil {
		t.Error("the in-flight context lost its budget when the caller's struct was mutated")
	}
}

func TestANilExecConfigLeavesTheContextAlone(t *testing.T) {
	base := context.Background()
	ctx := WithExecConfig(base, nil)

	// Storing a nil would make the accessors return a non-nil interface holding
	// nothing, which fails further from the mistake than not storing it.
	if ExecProvider(ctx) != nil {
		t.Error("a nil config produced a non-nil provider")
	}
	if ExecBudget(ctx) != nil {
		t.Error("a nil config produced a non-nil budget")
	}
	if ExecScheduler(ctx) != nil {
		t.Error("a nil config produced a non-nil scheduler")
	}
	if _, ok := ExecDataPolicy(ctx); ok {
		t.Error("a nil config reported carrying a data policy")
	}
}

func TestTheAccessorsReadNothingFromABareContext(t *testing.T) {
	ctx := context.Background()

	if ExecProvider(ctx) != nil || ExecBudget(ctx) != nil || ExecScheduler(ctx) != nil {
		t.Error("a bare context yielded configuration nobody attached")
	}
	//nolint:staticcheck // deliberately passing a nil context: the accessors
	// guard against it, and a caller who has one is exactly who finds out.
	if ExecProvider(nil) != nil {
		t.Error("a nil context yielded a provider")
	}
}

// An empty policy and no policy at all must stay distinguishable. A zero value
// cannot express the difference, and the two must not behave identically once
// policy enforcement is on: one says "allow everything", the other says "nobody
// has decided".
func TestAnEmptyDataPolicyIsDistinguishableFromNone(t *testing.T) {
	declared := WithExecConfig(context.Background(), &ExecConfig{DataPolicy: types.DataPolicy{}})

	policy, ok := ExecDataPolicy(declared)
	if !ok {
		t.Fatal("a client that declared an empty policy reads as no client at all")
	}
	if len(policy.AllowedProviders) != 0 {
		t.Errorf("AllowedProviders = %v, want empty", policy.AllowedProviders)
	}

	if _, ok := ExecDataPolicy(context.Background()); ok {
		t.Error("a bare context reported carrying a policy")
	}
}

func TestASchedulerTravelsOnTheSnapshot(t *testing.T) {
	scheduler := NewScheduler(SchedulerLimits{MaxConcurrent: 2, MaxQueued: 8})
	defer scheduler.Close(context.Background())

	ctx := WithExecConfig(context.Background(), &ExecConfig{Scheduler: scheduler})

	if got := ExecScheduler(ctx); got != scheduler {
		t.Error("the scheduler that came back is not the one attached")
	}
}
