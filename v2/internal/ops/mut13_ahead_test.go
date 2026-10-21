package ops

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// A branch started ahead and not wanted has to be cancelled as soon as the
// decision is known, and not only when the call returns: a branch nobody wants
// should not go on running, and being billed for, while the branch that was
// wanted does its work. A test that looks at the log after the call sees the
// cancellation either way, so the branch that is kept looks at the end of its
// own work, while the call is still waiting for it.

// aheadWorksThenSees is what a step that does its work for d does: it waits d,
// and says whether an event of the log has happened by the end of it. d is
// longer than the decision takes. The bubble's clock does not reach the end of
// the work before every goroutine that was woken by the decision has run, so
// what the library was going to do about the other branch is done by then.
func aheadWorksThenSees(l *aheadLog, d time.Duration, event string) bool {
	time.Sleep(d)
	return l.seen(event)
}

func TestAheadTheBranchNotWantedIsStoppedBeforeTheOtherDoesItsWork(t *testing.T) {
	const quick = 25 * time.Millisecond

	t.Run("BranchAhead, the decision says no", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			stopped := false
			no := func(context.Context) (string, error) {
				l.note("no started")
				stopped = aheadWorksThenSees(&l, 2*quick, "yes cancelled")
				return "no", nil
			}
			got, err := BranchAhead(context.Background(), l.decide(quick, false, nil), l.step("yes", time.Hour, "yes", nil), no)
			if err != nil || got != "no" {
				t.Fatalf("got %q, err %v", got, err)
			}
			if !stopped {
				t.Errorf("the branch started ahead was still running while the other did its work: %v", l.names())
			}
		})
	})

	t.Run("BranchBoth, the decision says yes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			stopped := false
			yes := func(context.Context) (string, error) {
				l.note("yes started")
				stopped = aheadWorksThenSees(&l, 2*quick, "no cancelled")
				return "yes", nil
			}
			got, err := BranchBoth(context.Background(), l.decide(quick, true, nil), yes, l.step("no", time.Hour, "no", nil))
			if err != nil || got != "yes" {
				t.Fatalf("got %q, err %v", got, err)
			}
			if !stopped {
				t.Errorf("the branch not picked was still running while the picked one did its work: %v", l.names())
			}
		})
	})

	t.Run("BranchBoth, the decision says no", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			stopped := false
			no := func(context.Context) (string, error) {
				l.note("no started")
				stopped = aheadWorksThenSees(&l, 2*quick, "yes cancelled")
				return "no", nil
			}
			got, err := BranchBoth(context.Background(), l.decide(quick, false, nil), l.step("yes", time.Hour, "yes", nil), no)
			if err != nil || got != "no" {
				t.Fatalf("got %q, err %v", got, err)
			}
			if !stopped {
				t.Errorf("the branch not picked was still running while the picked one did its work: %v", l.names())
			}
		})
	})

	t.Run("RouteAhead, the decision names another route", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			stopped := false
			other := func(context.Context) (string, error) {
				l.note("other started")
				stopped = aheadWorksThenSees(&l, 2*quick, "likely cancelled")
				return "other", nil
			}
			got, err := RouteAhead(context.Background(), l.pick(quick, "b", nil), "a",
				To("a", l.step("likely", time.Hour, "likely", nil)), To("b", Step[string](other)))
			if err != nil || got != "other" {
				t.Fatalf("got %q, err %v", got, err)
			}
			if !stopped {
				t.Errorf("the route started ahead was still running while the named one did its work: %v", l.names())
			}
		})
	})
}
