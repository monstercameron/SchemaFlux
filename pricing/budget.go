package pricing

import (
	"math"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	inner "github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/setting"
)

// Limits are spend limits for the process, over calendar periods of the
// process's local time zone. A limit is reported, not enforced, unless Enforce
// is set: the zero Limits has no limit at all, and a Limits with a Daily of 5
// and nothing else only tells you when the day's spend nears $5.
type Limits struct {
	// Daily is the most to spend in a calendar day, in US dollars. Zero means
	// no daily limit: it raises no alert and, under Enforce, refuses nothing. A
	// number below zero, one that is not a number and an infinite one are refused
	// by SetLimits.
	Daily float64

	// Weekly is the most to spend in an ISO calendar week (Monday to Sunday),
	// in US dollars. Zero means no weekly limit. Negative, nonnumeric and infinite
	// values are refused by SetLimits.
	Weekly float64

	// Monthly is the most to spend in a calendar month, in US dollars. Zero means
	// no monthly limit. Negative, nonnumeric and infinite values are refused by
	// SetLimits.
	Monthly float64

	// Enforce makes a limit a limit: once a period's spend has reached its
	// limit, a call is refused with an error of kind ErrBudgetExceeded before
	// anything is sent. It applies to every client, including one that has a
	// Budget of its own, which is held to its own ceiling as well: the lower of
	// the two binds. Spend that is not priced is not counted, so on a model with
	// no price a limit never refuses; the error says how many such calls the
	// period holds. False, the zero value, means the limits only report.
	Enforce bool

	// OnThreshold, when not nil, is called once when a period's spend first
	// reaches 80 percent of its limit and once when it first reaches the limit,
	// with the spend, the limit and the period's name: "daily", "weekly" or
	// "monthly". Each alert is sent once for each day, week and month: tomorrow
	// alerts again. Reset, and a new SetLimits, also begin again.
	//
	// It is called on the goroutine of the call that took the spend over, after
	// that call's cost is in the ledger and with no lock held, so it may call
	// any function of this package. Alerts are delivered one at a time, in the
	// order the thresholds were crossed. When another goroutine is delivering,
	// the alert is handed to it, so the call that caused an alert may return
	// before it has been delivered, and an alert is never left for a later call.
	// A panic in it is recovered and does not reach the call; it should not be
	// relied on to stop one.
	OnThreshold func(spent, limit float64, period string)
}

// budgetLayers holds the limits that calls to SetLimits have put in force. It
// writes the ledger package's own state under its own lock, so that what is in
// force there is always the newest limits nobody has restored.
var budgetLayers = setting.Layers[*Limits]{
	Apply: apply,
	Base:  func() *Limits { return nil },
}

// SetLimits sets the process's spend limits, replacing the ones in force, and
// returns a function that puts back the ones that were in force before: none, if
// none had been set. It starts the alerts afresh, so that a threshold crossed
// under the old limits is announced again under the new ones.
//
// It changes process state, for every client, whatever Budget the client has of
// its own: a call is refused when either ceiling has been reached, so the lower
// of the two binds. A limit that is zero is no limit for its period. A limit that
// is negative, is not a number or is infinite cannot be enforced, and SetLimits
// refuses it with an *Error of kind ErrConfiguration that names the field, as a
// client does for a Budget.Limit that is the same, and changes nothing: the
// limits in force stay as they were, and the restore is nil.
//
// The returned function undoes this call: it puts back what was in force when
// the call was made, unless something has been set since, in which case that
// later setting stays in force until it is restored too, and then what is in
// force is the newest setting not yet restored, or the original if there is none.
// It does nothing when called a second time, may be called in any order relative
// to the restore functions of other calls, and may be called from several
// goroutines. The function only knows the limits set through this function.
//
//	restore, err := pricing.SetLimits(pricing.Limits{Daily: 5, Enforce: true})
//	if err != nil {
//		return err
//	}
//	defer restore()
func SetLimits(b Limits) (restore func(), err error) {
	for _, limit := range []struct {
		field string
		value float64
	}{{"Daily", b.Daily}, {"Weekly", b.Weekly}, {"Monthly", b.Monthly}} {
		if math.IsNaN(limit.value) || math.IsInf(limit.value, 0) || limit.value < 0 {
			return nil, &schemaflux.Error{
				Kind:    schemaflux.ErrConfiguration,
				Op:      "pricing.setlimits",
				Message: "Limits." + limit.field + " must be a number that is not negative",
			}
		}
	}
	held := b
	return budgetLayers.Set(&held), nil
}

// apply puts limits in force in the ledger package, replacing what is there; nil
// is no limits. It clears the record of the alerts that were sent. It is called
// by the layers, with their lock held.
func apply(l *Limits) {
	if l == nil {
		inner.ResetBudget()
		return
	}
	inner.ReplaceBudget(l.Daily, l.Weekly, l.Monthly, l.OnThreshold, l.Enforce)
}
