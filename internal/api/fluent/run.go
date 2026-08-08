package fluent

import (
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A-013's Revised line ships three things with Run(ctx): RunResult(ctx)
// beside it, value-receiver copy-on-write builders (already true of every
// builder in this package -- see fluent_base.go and wiring_test.go), and a
// documented read-at-run contract for inputs holding maps, slices, or
// pointers.
//
// The contract this package chooses is READ-AT-RUN, not capture-at-build:
// none of the builder constructors (Extracting, Choosing, Filtering, and so
// on) make a defensive copy of a slice, map, or pointer the caller passed in.
// A builder stores the reference, not a snapshot, so Run reads whatever the
// referenced memory holds at the moment it actually executes -- which is
// also, by construction, whatever a concurrent mutation of that memory can
// race with. That is not a defect this task introduces: it is the existing
// behaviour (see, e.g., ChooseRequest.options in entrypoints.go, stored
// as-is since before this task), made explicit and enforced by
// TestRun_ConcurrentMutationIsARace (guarded by -race, run_race_test.go) and
// demonstrated deterministically by TestRun_ObservesAMutationMadeAfterBuild
// (run_contract_test.go), which does not need the detector to show it.
//
// A caller who needs isolation from later mutation has to copy before
// building the request; this package does not do it for them, because doing
// so on every call would cost a copy nobody asked for on the overwhelmingly
// common path where the caller does not mutate their input again.

// runEnvelope executes call and wraps its (T, error) result in a
// types.Result[T] envelope, for the operations that have no ops.XResult twin
// of their own yet -- only Extract does today (ops.ExtractResult, A-006).
//
// What it can honestly report without reaching into ops' unexported call
// recording is the operation's name, the identifiers already on the
// options, and wall-clock elapsed time. Usage, cost, attempts, and
// delivered-contract detail are left at zero rather than guessed:
// AGENTS.md's rule against inventing a number applies to an envelope's
// fields exactly as it applies to an operation's answer.
func runEnvelope[T any](operation, requestID, correlationID string, call func() (T, error)) (types.Result[T], error) {
	start := time.Now()
	value, err := call()

	meta := types.Meta{
		Operation:     operation,
		RequestID:     requestID,
		CorrelationID: correlationID,
		Elapsed:       time.Since(start),
	}
	if err != nil {
		return types.Result[T]{Meta: meta}, err
	}
	meta.Attempts = 1
	return types.Result[T]{Value: value, Meta: meta}, nil
}
