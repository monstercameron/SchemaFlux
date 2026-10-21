package mw

import (
	"math"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/types"

	impl "github.com/monstercameron/schemaflux/v2/internal/runtime/mw"
)

// BudgetOptions are the settings of Budget. The zero value is the default.
type BudgetOptions struct {
	// Cost prices a call, twice: the estimate made before the call is sent, from
	// the request, and the cost measured when it is over, from the tokens the
	// provider reports for it. It is given the tokens, the model and the
	// provider. A Cost with Priced false is a call nobody could price (see
	// AllowUnpriced), and one that is not a finite number of at least zero is
	// treated the same way. Nil is the library's price table (the prices
	// registered with the pricing package), which prices a model it lists and no
	// other.
	//
	// A budget is only as good as its prices: register the price of a model the
	// table does not list, or give Cost.
	Cost func(usage schemaflux.Usage, model, provider string) schemaflux.Cost

	// AllowUnpriced lets a call that cannot be priced go through and count as
	// nothing: a model the Cost has no price for, and an answer that reports no
	// usage. The zero value refuses the first before it is sent, as a budget
	// error, and counts the second at what it was reserved at, since a call nobody
	// can price is not a free one and a ceiling that never refuses and never says
	// so is not a ceiling. It loosens the budget, so it can be set in the first
	// options value only.
	AllowUnpriced bool
}

// Budget refuses a request that would take what has been spent past limit, a
// fixed ceiling in the unit Cost measures in, dollars for the price table. It is
// the budget of one chain, with no period and no process-wide state: a program
// that wants a ceiling for each tenant or each batch makes one Budget for each.
// It is separate from a client's own Budget and from the ceiling the pricing
// package keeps: each of them is checked on its own.
//
// A limit of zero is no limit: nothing is checked and nothing is counted. A
// limit that is negative, not a number or infinite is not an amount a ceiling
// can be made of, and every request fails with an error of kind
// schemaflux.ErrConfiguration before the provider is asked; a program that
// computes what is left of a budget has to test for zero itself, since zero left
// is no ceiling here.
//
// # How a request is held to the limit
//
// A request is reserved before it is sent. The budget estimates it from the
// request itself, its prompt at four bytes to a token and its answer at the
// output ceiling it names (500 tokens when it names none), priced by Cost, and
// holds that estimate against the limit while the request is in flight. A request
// whose reservation would take the ledger past the limit is refused before
// anything is sent, with an error of kind schemaflux.ErrBudgetExceeded, which
// nothing retries and nothing fails over from. When the request ends, the
// reservation is replaced by the cost measured from the usage the provider
// reported. One that fails is not counted: the ledger keeps no charge for a call
// that returned an error, and one that reported no usage is counted at its
// reservation.
//
// The bound on the spend is this. The ledger holds the measured cost of the
// requests that have ended and the reservations of those in flight, and a request
// is admitted only when the ledger and its own reservation are within the limit.
// So with any number of callers at once the spend can pass the limit only by what
// requests cost beyond their estimates, and by at most the sum of those
// under-estimates over the requests that were in flight together when the limit
// was reached: with a limit of 10, requests reserved at 1 that cost 3, and a
// thousand callers at once, ten are admitted and the spend is 30. An estimate
// that is exact, or too high, never takes the spend past the limit. The
// estimate is of tokens at four bytes each, so a prompt of dense text, JSON or a
// script that needs several bytes a character is under-estimated; leave headroom
// for that, and for the copies of a Hedge, which the provider bills and no
// ledger sees.
//
// A request that cannot be priced, because Cost has no price for its model, is
// refused before it is sent, and an answer that reports no usage is counted at
// its reservation: see AllowUnpriced for the other way. A request is not
// estimated when the limit is zero.
//
// What the ledger sees depends on where the budget stands, and the package
// comment says where: around a Hedge it counts the answer that was used and not
// the copy that was cancelled, which the provider bills; around a Fallback it
// counts whichever route answered; around a Decisions pair it counts the
// generating provider's answer and not the decision provider's.
//
// The ledger belongs to the provider this middleware builds, not to the
// Middleware: applied to two providers it gives each its own. Budget does not
// forward streaming: a wrapped provider cannot stream.
func Budget(limit float64, opts ...BudgetOptions) Middleware {
	o, problem := mergeBudgetOptions(opts)
	if math.IsNaN(limit) || math.IsInf(limit, 0) || limit < 0 {
		problem = "the limit must be zero, which is no limit, or a positive amount"
	}

	// The budget reserves what it estimates from the request before it sends it,
	// and prices the estimate and the answer with the same function.
	settings := []impl.BudgetOption{impl.WithBudgetRequestEstimate()}
	if o.Cost != nil {
		settings = append(settings, impl.WithBudgetCostFunc(internalCost(o.Cost)))
	}
	if o.AllowUnpriced {
		settings = append(settings, impl.AllowUnpricedCalls())
	}
	build := impl.Budget(limit, settings...)

	return func(next schemaflux.Provider) schemaflux.Provider {
		return wrap("mw.budget", next, problem, build)
	}
}

// internalCost is cost as the budget of the wrapped package takes it.
func internalCost(cost func(schemaflux.Usage, string, string) schemaflux.Cost) func(*types.TokenUsage, string, string) *types.CostInfo {
	return func(usage *types.TokenUsage, model, provider string) *types.CostInfo {
		info := bind.CostToInternal(cost(bind.UsageFromInternal[schemaflux.Usage](*usage), model, provider))
		return &info
	}
}

// mergeBudgetOptions merges option values left to right, a later Cost that is
// not nil winning. AllowUnpriced loosens the budget, so only the first value may
// set it: in a later one it is a problem, and the second result says so, for the
// caller to make a configuration error of.
func mergeBudgetOptions(opts []BudgetOptions) (BudgetOptions, string) {
	var merged BudgetOptions
	var problem string
	for i, o := range opts {
		if o.Cost != nil {
			merged.Cost = o.Cost
		}
		if o.AllowUnpriced {
			if i == 0 {
				merged.AllowUnpriced = true
			} else if problem == "" {
				problem = "AllowUnpriced is set in a later options value, which would loosen a budget an earlier one set: set it in the first"
			}
		}
	}
	return merged, problem
}
