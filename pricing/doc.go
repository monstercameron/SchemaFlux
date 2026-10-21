// Package pricing is the library's price table and the ledger of what a
// process has spent through it.
//
// A call to a model costs money, and the library reports what it came to: in a
// Record's Cost, in this package's ledger, and, for a client that has one, in
// its Budget. Everything here is in US dollars, and nothing is ever invented:
// where the library has no price the figure says so.
//
// # Reading a bill
//
// Price turns a token count into a cost, and Spent, Entries, Summarize and
// Breakdown read what the process has spent so far:
//
//	usd, complete := pricing.Spent(time.Now().Add(-24*time.Hour), nil)
//	if !complete {
//		// some call in the last day was on a model with no price: usd is a
//		// floor, not the bill.
//	}
//
// The ledger holds the last 10,000 calls (SetHistoryLimit changes that), each
// as an Entry. A filter narrows a read to the entries whose request_id,
// correlation_id, model, provider or operation equals the value given, or whose
// tag of that name does; an entry must match every key of the filter. A nil
// filter and a zero time select everything.
//
// # What no price means
//
// A model the table has no price for is not free. Price reports its cost as
// not Priced, with a Total of zero that means "unknown", and the ledger counts
// the call as unpriced: Spent reports the figure as incomplete, Summarize counts
// the call in Unpriced, and a Cost summed over such a call is not Priced. A
// figure that is not complete is a floor under what was spent. The same holds
// for a transport that is not billed per token, such as the codex provider,
// whatever model it runs.
//
// A model is priced by its exact name (without regard to case) or when it is a
// dated snapshot of one that is: "gpt-5-2025-08-07" is "gpt-5" at the same
// price. A name that only resembles a priced one ("gpt-5-pro") has its own
// price or none, and is never given its sibling's. Register adds a price, for a
// model the table does not know or for rates a contract changed.
//
// # Budgets
//
// SetLimits gives the process spend limits for the day, the week and the month,
// and a function to call as they are approached. A limit is reported and not
// enforced, unless Limits.Enforce is set, in which case a call is refused with
// ErrBudgetExceeded before it is sent. A limit of zero is no limit; one that is
// negative, not a number or infinite is refused with an error of kind
// ErrConfiguration, as a client's Budget.Limit is. Spend that is not priced is
// not counted toward a limit, so on a model with no price the limit never
// refuses: register a price.
//
// The process ledger's ceiling applies to every client. A client that has a
// Budget of its own is held to that Budget as well, and a call is refused when
// either ceiling has been reached, so the lower binds; what the client spends is
// recorded here too.
//
// # Process state
//
// The table, the ledger and the limits belong to the process, and four
// functions change them. Register adds a price, SetLimits replaces the limits
// and SetHistoryLimit changes how many entries the ledger keeps, and each returns
// the function that undoes it; Reset empties the ledger and has no undo.
//
// A restore function undoes its call: it puts back what was in force when the
// call was made, unless something has been set since, in which case that later
// setting stays in force until it is restored too, and then what is in force is
// the newest setting not yet restored, or the original if there is none. It does
// nothing when called a second time, may be called in any order relative to the
// restore functions of other calls, and may be called from several goroutines.
//
// A test that uses any of them restores what it changed, and does not run in
// parallel with another test that reads the ledger. A parallel test that
// registers a price gives its calls a model name of its own, because a price is
// looked up by the model's name alone.
package pricing
