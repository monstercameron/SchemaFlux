package schemaflux

import (
	"math"
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Budget is a spend ceiling and the ledger that holds a client to it. The zero
// Budget has no ceiling and still counts, so a client can be given one only to
// learn what it spent. A Budget is safe for concurrent use and must not be
// copied after first use; clients share one by holding the same pointer. Make
// one per tenant and keep it: a Budget made where the call is made is a new
// ledger each time and its limit never binds.
type Budget struct {
	// Limit is the ceiling in US dollars. Zero means no ceiling. A call is
	// refused once the recorded spend has reached it; calls already in flight
	// when it is reached are not recalled, so the spend can pass the limit by
	// what they cost. A negative limit, or one that is not a number, is a
	// configuration error on every call.
	Limit float64

	// AllowUnpriced lets a call on a model the library has no price for go ahead
	// under a Limit. Such a call adds nothing to the ledger, so it is not held to
	// the Limit and the spend can pass it without the ledger knowing: register a
	// price with pricing.Register instead where the model is one you use. False,
	// the zero value, refuses the call before it is sent, with an error of kind
	// ErrBudgetExceeded that says the model has no price. It has no effect on a
	// Budget with no Limit.
	AllowUnpriced bool

	once   sync.Once
	ledger *ops.ClientBudget
}

// Spent reports what the ledger has recorded, in US dollars, and whether that
// figure is complete. It is not complete once any call was made on a model the
// library has no price for: the figure is then a floor. The Limit and
// AllowUnpriced are read when the ledger is first used, which is the first call,
// WithClient or SetDefault that holds the Budget, or the first call of Spent.
func (b *Budget) Spent() (usd float64, complete bool) {
	if b == nil {
		return 0, true
	}
	return b.counter().Spent()
}

// counter is the ledger, made the first time it is asked for.
func (b *Budget) counter() *ops.ClientBudget {
	b.once.Do(func() {
		b.ledger = ops.NewClientBudget(b.Limit).KeepProcessCeiling()
		if !b.AllowUnpriced {
			b.ledger.RefuseUnpriced()
		}
	})
	return b.ledger
}

// check returns the configuration error of a limit no ledger can enforce.
func (b *Budget) check() error {
	if b == nil {
		return nil
	}
	if math.IsNaN(b.Limit) || math.IsInf(b.Limit, 0) || b.Limit < 0 {
		return types.Newf(types.KindConfiguration, "client", "Budget.Limit must be a number that is not negative")
	}
	return nil
}
