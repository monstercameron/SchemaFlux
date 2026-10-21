package ops

import (
	"fmt"
	"math"
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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
//
// What it guarantees, and what it does not. Check and Record each hold the
// ledger's lock for the whole of their work, so every Check sees the sum of
// everything Recorded before it, and no two callers can see a total that is
// half updated. A Check refuses once the recorded spend has reached the
// ceiling, and never lets a call through after that.
//
// What it cannot do is price a call before it is made: Check does not know what
// the call will cost, and the cost arrives later, in Record. So calls that were
// admitted and have not yet been recorded are not counted against the ceiling.
// With N calls in flight when the ceiling is reached, the spend can end up past
// it by what those N calls cost, and by no more than that: calls that start
// after the ceiling has been recorded are refused. A ceiling is a limit on
// when new calls are admitted, not a cap on the total of the calls that were
// already running. A caller who needs the second has to bound the number of
// calls in flight, which is what the client's scheduler does.
type ClientBudget struct {
	mu sync.Mutex

	ceilingUSD float64
	spentUSD   float64

	// invalid holds the reason this ledger's ceiling is not one it can enforce,
	// and makes every Check refuse. The constructor has no error to return, and
	// a ceiling that is not a number was making Check allow everything.
	invalid error

	// keepsProcessCeiling is set for a ledger that does not lift the process-wide
	// ceiling: a call it admits is still held to the ceiling of the pricing
	// package, where one is enforced. The default is the one a client has always
	// had, which is to replace it.
	keepsProcessCeiling bool

	// refusesUnpriced is set for a ledger that has a ceiling and is not told that
	// a call it cannot price may go ahead: such a call is refused before it is
	// sent, since it would add nothing to the spend the ceiling is held to.
	refusesUnpriced bool

	// unpricedCalls counts calls whose cost could not be known. It is the
	// reason Spent reports completeness alongside a number: a ledger that has
	// seen unpriced calls has a floor, not a total, and the difference decides
	// whether "you have $2 left" is information or a guess.
	unpricedCalls int
}

// NewClientBudget returns a ledger with the given ceiling.
//
// A ceiling of zero means no ceiling, which is what a client that never asked
// for one gets. A ceiling that is negative, not a number or infinite is not
// one a ledger can enforce, and the ledger it returns refuses every call with
// a configuration error: those values used to switch the budget off.
func NewClientBudget(ceilingUSD float64) *ClientBudget {
	budget := &ClientBudget{ceilingUSD: ceilingUSD}
	if math.IsNaN(ceilingUSD) || math.IsInf(ceilingUSD, 0) || ceilingUSD < 0 {
		budget.invalid = types.Newf(types.KindConfiguration, "client.budget",
			"the spend ceiling has to be a number that is not negative, so no call is allowed")
	}
	return budget
}

// KeepProcessCeiling makes the ledger hold a call to the process-wide ceiling
// of the pricing package as well as to its own, and returns the ledger. Without
// it a client that has a ledger is held to that ledger alone, which lifts a
// ceiling the process set. It is set before the ledger is used.
func (b *ClientBudget) KeepProcessCeiling() *ClientBudget {
	b.keepsProcessCeiling = true
	return b
}

// RefuseUnpriced makes a ledger that has a ceiling refuse a call on a model the
// library has no price for, before it is sent, and returns the ledger: the spend
// of such a call is unknown, so a ceiling could not hold it. A ledger with no
// ceiling counts and refuses nothing. It is set before the ledger is used.
func (b *ClientBudget) RefuseUnpriced() *ClientBudget {
	b.refusesUnpriced = true
	return b
}

// keepsProcess reports whether the ledger leaves the process-wide ceiling in
// force.
func (b *ClientBudget) keepsProcess() bool { return b.keepsProcessCeiling }

// refusesUnpricedCalls reports whether a call on a model with no price is
// refused: the ledger was told to, and it has a ceiling that is one it can
// enforce.
func (b *ClientBudget) refusesUnpricedCalls() bool {
	return b.refusesUnpriced && b.invalid == nil && b.ceilingUSD > 0
}

// Check refuses a call once the ceiling is reached.
//
// It refuses at the ceiling rather than past it, and it refuses BEFORE the
// request goes out — a budget enforced from the response has already spent the
// money it was supposed to prevent. See the type's comment for what that does
// and does not bound when calls run at once.
//
// An unpriced call does NOT open the gate: once a ledger has seen one, its
// total is a floor, and a floor at the ceiling is still at the ceiling. The
// alternative — treating unknown cost as zero — is how a budget silently stops
// being one.
func (b *ClientBudget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.invalid != nil {
		return b.invalid
	}
	if b.ceilingUSD == 0 {
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
//
// A cost that is not a number, is infinite or is negative is not a cost. It is
// recorded as a call whose cost is unknown, so the total stays a floor and
// stays a number: a NaN added to the total made every later comparison false,
// and the ledger never refused again.
func (b *ClientBudget) Record(costUSD float64, priced bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !priced || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) || costUSD < 0 {
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
