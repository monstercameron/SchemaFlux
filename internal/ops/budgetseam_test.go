package ops

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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

// A ceiling of zero means "no ceiling", which is what a client that never asked
// for a budget gets. It must not mean "refuse everything" -- that would make an
// unconfigured client unusable rather than unlimited.
func TestAZeroCeilingEnforcesNothing(t *testing.T) {
	budget := NewClientBudget(0)
	budget.Record(1000, true)

	if err := budget.Check(); err != nil {
		t.Errorf("a ceiling of 0 refused a call after spending 1000; zero means unlimited: %v", err)
	}
	if got := budget.Ceiling(); got != 0 {
		t.Errorf("Ceiling() = %v, want 0", got)
	}
}

// A ceiling that is not a usable number used to switch the budget off: NaN is
// not at or below zero and is never reached, and a negative one was read as
// "none". The ledger now refuses every call, with a configuration error, so the
// mistake shows on the first call and not on the invoice.
func TestAnUnusableCeilingRefusesEveryCall(t *testing.T) {
	for _, ceiling := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, -0.01, -1e300} {
		budget := NewClientBudget(ceiling)

		err := budget.Check()
		if err == nil {
			t.Errorf("a ceiling of %v allowed a call", ceiling)
			continue
		}
		if types.KindOf(err) != types.KindConfiguration {
			t.Errorf("a ceiling of %v: kind %v, want KindConfiguration (%v)", ceiling, types.KindOf(err), err)
		}
		// Spending nothing, or recording a cost, changes nothing.
		budget.Record(0, true)
		if budget.Check() == nil {
			t.Errorf("a ceiling of %v allowed a call after a recorded cost", ceiling)
		}
	}
}

// A cost that is not a number, or is negative, used to be added to the total. A
// NaN made every later comparison false, so the ledger never refused again, and
// a negative cost lowered what had been spent. Such a cost is now a call of
// unknown cost: it is counted, the total stays a number, and the ledger still
// refuses at its ceiling.
func TestAnInvalidRecordedCostIsAnUnknownCostAndKeepsTheLedgerWorking(t *testing.T) {
	for _, cost := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.5, -1e300} {
		budget := NewClientBudget(1)

		budget.Record(0.4, true)
		budget.Record(cost, true)

		spent, complete := budget.Spent()
		if spent != 0.4 {
			t.Errorf("after a cost of %v the total is %v, want 0.4 unchanged", cost, spent)
		}
		if complete || budget.UnpricedCalls() != 1 {
			t.Errorf("after a cost of %v: complete %v, unpriced %d, want an incomplete ledger with one unknown", cost, complete, budget.UnpricedCalls())
		}

		budget.Record(0.7, true)
		if err := budget.Check(); !errors.Is(err, ErrClientBudgetExhausted) {
			t.Errorf("after a cost of %v and 1.1 recorded against a ceiling of 1, Check = %v, want the ledger to refuse", cost, err)
		}
	}
}

// What the ledger guarantees under concurrent callers: every caller that passes
// Check and then Records is counted, no call is admitted once the recorded spend
// has reached the ceiling, and so the spend ends above the ceiling by at most
// what the calls already in flight cost -- never by the cost of every caller.
func TestConcurrentCallersOverspendByAtMostTheCallsInFlight(t *testing.T) {
	const (
		callers  = 64
		inFlight = 4 // calls allowed to be running at once, as a scheduler would
		cost     = 0.25
		ceiling  = 2.0
	)
	budget := NewClientBudget(ceiling)

	slots := make(chan struct{}, inFlight)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if budget.Check() != nil {
				return
			}
			mu.Lock()
			admitted++
			mu.Unlock()
			budget.Record(cost, true)
		}()
	}
	wg.Wait()

	spent, complete := budget.Spent()
	if !complete {
		t.Error("the ledger is incomplete although every cost was priced")
	}
	if want := float64(admitted) * cost; spent != want {
		t.Errorf("spent %v for %d admitted calls, want %v: a recorded cost was lost", spent, admitted, want)
	}
	if spent < ceiling {
		t.Errorf("spent %v: the ceiling of %v was never reached by %d callers", spent, ceiling, callers)
	}
	if limit := ceiling + inFlight*cost; spent > limit {
		t.Errorf("spent %v, past the ceiling of %v by more than the %d calls in flight could cost (%v)", spent, ceiling, inFlight, limit)
	}
	if err := budget.Check(); !errors.Is(err, ErrClientBudgetExhausted) {
		t.Errorf("after the ceiling was reached Check = %v, want it to refuse", err)
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
	declared := WithExecConfig(context.Background(), &ExecConfig{DataPolicy: types.DataPolicy{}, DataPolicyDeclared: true})

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

// budgetLockProbe is an error whose text is written while Check holds the
// ledger's lock, because Check formats the sentinel into its refusal.
type budgetLockProbe struct {
	budget   *ClientBudget
	reads    int
	lockFree int
}

func (p *budgetLockProbe) Error() string {
	p.reads++
	if p.budget.mu.TryLock() {
		p.budget.mu.Unlock()
		p.lockFree++
	}
	return "probe"
}

// Check reads the spend and the ceiling under the ledger's lock, so that it sees
// the sum of everything Recorded before it. Without a detector for races that
// is checked at the one point Check calls out of its own code, which is the
// text of the refusal it builds.
func TestClientBudgetCheckHoldsTheLedgerLockWhileItDecides(t *testing.T) {
	budget := NewClientBudget(1)
	budget.Record(2, true)
	probe := &budgetLockProbe{budget: budget}

	previous := ErrClientBudgetExhausted
	ErrClientBudgetExhausted = &types.OperationError{Kind: types.KindBudgetExceeded, Op: "client.budget", Cause: probe}
	t.Cleanup(func() { ErrClientBudgetExhausted = previous })

	if err := budget.Check(); err == nil {
		t.Fatal("a ledger past its ceiling let a call through")
	}
	if probe.reads != 1 || probe.lockFree != 0 {
		t.Errorf("the refusal was written %d times, and found the lock free %d times; want 1 and 0", probe.reads, probe.lockFree)
	}
}
