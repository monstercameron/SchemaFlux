package ops

import (
	"fmt"
	"sync"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ClientBudget is one client's spend ledger: a ceiling, a running total, and
// the honesty about whether that total means anything.
//
// It is deliberately small. The process-wide budget in `pricing` tracks daily,
// weekly, and monthly windows against a shared map, which is right for a
// process and wrong for a client — a client is a scope, not a calendar, and a
// per-client budget that reset at midnight would let a tenant spend its whole
// allowance twice on either side of a boundary nobody chose.

// ErrClientBudgetExhausted is returned when a client's own ceiling is reached.
// It is separate from pricing.ErrBudgetExceeded so a caller can tell "my
// tenant is out of allowance" from "the process is out of allowance" — the two
// need different responses, and one of them is not the caller's problem.
var ErrClientBudgetExhausted = &types.OperationError{
	Kind:    types.KindBudgetExceeded,
	Op:      "client.budget",
	Message: "this client's spend ceiling has been reached",
}

// ClientBudget implements BudgetChecker for one client.
type ClientBudget struct {
	mu sync.Mutex

	ceilingUSD float64
	spentUSD   float64

	// unpricedCalls counts calls whose cost could not be known. It is the
	// reason Spent reports completeness alongside a number: a ledger that has
	// seen unpriced calls has a floor, not a total, and the difference decides
	// whether "you have $2 left" is information or a guess.
	unpricedCalls int
}

// NewClientBudget returns a ledger with the given ceiling.
func NewClientBudget(ceilingUSD float64) *ClientBudget {
	return &ClientBudget{ceilingUSD: ceilingUSD}
}

// Check refuses a call once the ceiling is reached.
//
// It refuses at the ceiling rather than past it, and it refuses BEFORE the
// request goes out — a budget enforced from the response has already spent the
// money it was supposed to prevent.
//
// An unpriced call does NOT open the gate: once a ledger has seen one, its
// total is a floor, and a floor at the ceiling is still at the ceiling. The
// alternative — treating unknown cost as zero — is how a budget silently stops
// being one.
func (b *ClientBudget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ceilingUSD <= 0 {
		return nil
	}
	if b.spentUSD >= b.ceilingUSD {
		return fmt.Errorf("%w: spent $%.4f of $%.4f%s",
			ErrClientBudgetExhausted, b.spentUSD, b.ceilingUSD, b.unpricedNote())
	}
	return nil
}

// Record adds an observed cost.
//
// priced is passed separately rather than inferred from a non-zero cost,
// because an unpriced model legitimately reports zero and a genuinely free call
// also reports zero. Folding them together would make a ledger that has lost
// track of its spend look identical to one that has spent nothing.
func (b *ClientBudget) Record(costUSD float64, priced bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !priced {
		b.unpricedCalls++
		return
	}
	b.spentUSD += costUSD
}

// Spent reports the recorded spend and whether it is complete.
//
// complete is false once any unpriced call has been recorded. The number is
// still the best available figure — it is a lower bound on real spend — and
// saying so is the difference between a budget a caller can act on and one they
// can only hope about.
func (b *ClientBudget) Spent() (usd float64, complete bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spentUSD, b.unpricedCalls == 0
}

// Ceiling reports the configured ceiling.
func (b *ClientBudget) Ceiling() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ceilingUSD
}

// UnpricedCalls reports how many calls could not be priced. It is exposed
// rather than kept private because "your total is incomplete" is not actionable
// without "and this is how incomplete".
func (b *ClientBudget) UnpricedCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.unpricedCalls
}

func (b *ClientBudget) unpricedNote() string {
	if b.unpricedCalls == 0 {
		return ""
	}
	return fmt.Sprintf(" (plus %d call(s) whose cost is unknown, so the spend is a floor)", b.unpricedCalls)
}
