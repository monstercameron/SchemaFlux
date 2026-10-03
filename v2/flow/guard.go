package flow

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/ops"
)

// GuardResult is the verdict of Guard.
type GuardResult struct {
	// CanProceed is true when every check passed. It is also true for no checks.
	CanProceed bool

	// FailedChecks holds the message of each check that failed, in the order of
	// the checks. It is empty when CanProceed is true.
	FailedChecks []string
}

// Guard runs the checks against state and reports which of them failed.
//
//	verdict := flow.Guard(order,
//		func(o Order) (bool, string) { return o.Total > 0, "the total is not positive" },
//		func(o Order) (bool, string) { return o.Address != "", "there is no address" })
//	if !verdict.CanProceed { ... verdict.FailedChecks ... }
//
// Every check runs, in order, on the caller's goroutine, whether or not an earlier
// one failed, so the verdict lists every failure and not the first. A check
// returns whether it passed and the message to report when it did not; the
// message is written by the caller's own code and may quote its state, so Guard
// passes it on and nothing else does.
//
// Guard asks no model and sends nothing anywhere: a caller who hands it a list of
// Go predicates has every reason to believe it runs Go predicates. It takes no
// context for the same reason, and adds no Record. A panic in a check is the
// caller's panic.
//
// A nil check does not pass. It is reported as a failed check, with a message of
// the library's own that names its position, and no check runs, so that a
// verdict is never given on the strength of a check that was not there.
func Guard[T any](state T, checks ...func(T) (bool, string)) GuardResult {
	for i, check := range checks {
		if check == nil {
			return GuardResult{FailedChecks: []string{fmt.Sprintf("check %d is nil", i+1)}}
		}
	}
	verdict := ops.Guard(context.Background(), state, checks...)
	return GuardResult{CanProceed: verdict.CanProceed, FailedChecks: verdict.FailedChecks}
}
