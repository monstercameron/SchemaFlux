package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LAT-007. BranchAhead, BranchBoth and RouteAhead.
//
// The steps here record when they started, when they were cancelled and when
// they finished, and the tests assert on the order of those events and on how
// long a call took. Each test runs in a synctest bubble, where the clock is a
// fake one that moves only when every goroutine is blocked: a step that waits a
// hundred milliseconds takes exactly that and no real time, and a call that
// overlaps two such steps takes exactly one wait. A busy machine cannot change
// what a test sees.

// aheadWait is how long a step that stands for a call to a provider takes.
const aheadWait = 120 * time.Millisecond

// aheadFar is a step that would take far longer than any test is willing to
// wait, so a test that returns quickly proves the step was cut short.
const aheadFar = 5 * time.Second

type aheadEvent struct {
	name string
	at   time.Time
}

// aheadLog records what the steps of one test did.
type aheadLog struct {
	mu      sync.Mutex
	events  []aheadEvent
	started atomic.Int32
	exited  atomic.Int32
}

func (l *aheadLog) note(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, aheadEvent{name, time.Now()})
}

// when is the time of the first event of that name, and zero when there was
// none.
func (l *aheadLog) when(name string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, event := range l.events {
		if event.name == name {
			return event.at
		}
	}
	return time.Time{}
}

func (l *aheadLog) seen(name string) bool { return !l.when(name).IsZero() }

func (l *aheadLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, len(l.events))
	for i, event := range l.events {
		names[i] = event.name
	}
	return names
}

// aheadSleep waits for d, and reports whether the context ended first.
func aheadSleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return false
	case <-ctx.Done():
		return true
	}
}

// step is a step that takes d, then returns value or fails with fail. It stops
// when its context ends, and says so.
func (l *aheadLog) step(name string, d time.Duration, value string, fail error) Step[string] {
	return func(ctx context.Context) (string, error) {
		l.started.Add(1)
		defer l.exited.Add(1)
		l.note(name + " started")
		if aheadSleep(ctx, d) {
			l.note(name + " cancelled")
			return "", ctx.Err()
		}
		if fail != nil {
			l.note(name + " failed")
			return "", fail
		}
		l.note(name + " finished")
		return value, nil
	}
}

// stubborn is a step that ignores its context and takes d whatever happens.
func (l *aheadLog) stubborn(name string, d time.Duration, value string) Step[string] {
	return func(context.Context) (string, error) {
		l.started.Add(1)
		defer l.exited.Add(1)
		l.note(name + " started")
		time.Sleep(d)
		l.note(name + " finished")
		return value, nil
	}
}

// decide is a decision that takes d and then answers or fails.
func (l *aheadLog) decide(d time.Duration, answer bool, fail error) Step[bool] {
	return func(ctx context.Context) (bool, error) {
		l.started.Add(1)
		defer l.exited.Add(1)
		l.note("decide started")
		if aheadSleep(ctx, d) {
			l.note("decide cancelled")
			return false, ctx.Err()
		}
		if fail != nil {
			l.note("decide failed")
			return false, fail
		}
		l.note("decide answered")
		return answer, nil
	}
}

// pick is a decision among labelled routes.
func (l *aheadLog) pick(d time.Duration, label string, fail error) Step[string] {
	return func(ctx context.Context) (string, error) {
		l.started.Add(1)
		defer l.exited.Add(1)
		l.note("decide started")
		if aheadSleep(ctx, d) {
			l.note("decide cancelled")
			return "", ctx.Err()
		}
		if fail != nil {
			l.note("decide failed")
			return "", fail
		}
		l.note("decide answered")
		return label, nil
	}
}

// aheadTimed runs f and says how long it took.
func aheadTimed[T any](f func() (T, error)) (T, time.Duration, error) {
	start := time.Now()
	value, err := f()
	return value, time.Since(start), err
}

// aheadBefore asserts that two events happened, in that order. The order is
// the order they were recorded in, which is the order they happened in: two
// events at the same instant of the bubble's clock are still told apart.
func aheadBefore(t *testing.T, l *aheadLog, first, second string) {
	t.Helper()
	a, b := -1, -1
	for i, name := range l.names() {
		if name == first && a < 0 {
			a = i
		}
		if name == second && b < 0 {
			b = i
		}
	}
	if a < 0 || b < 0 {
		t.Errorf("%q and %q did not both happen: %v", first, second, l.names())
		return
	}
	if b < a {
		t.Errorf("%q came before %q: %v", second, first, l.names())
	}
}

// aheadTook asserts how long a call took on the bubble's clock. The clock moves
// only to the next time something is waiting for, so the figure is the sum of
// the waits the call had to sit through and nothing else.
func aheadTook(t *testing.T, elapsed, want time.Duration, why string) {
	t.Helper()
	if elapsed != want {
		t.Errorf("took %v, want exactly %v: %s", elapsed, want, why)
	}
}

// aheadQuiet asserts that every step that started has returned: nothing is
// left running after the call that started it.
func aheadQuiet(t *testing.T, l *aheadLog) {
	t.Helper()
	if started, exited := l.started.Load(), l.exited.Load(); started != exited {
		t.Errorf("%d steps started and %d had returned when the call did: %v", started, exited, l.names())
	}
}

func aheadPanicking[T any](message string) Step[T] {
	return func(context.Context) (T, error) { panic(message) }
}

func aheadCheckPanicError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a step panicked and no error was returned")
	}
	if types.KindOf(err) != types.KindInvariantViolation {
		t.Errorf("the kind of the error is %v", types.KindOf(err))
	}
	if strings.Contains(err.Error(), "7731") {
		t.Errorf("the error carries the panic's value: %v", err)
	}
}

var aheadBoom = errors.New("the step failed")

// A decision that says yes costs one wait where Branch after a decision costs
// two.
func TestAheadBranchYesCostsOneWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()

		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(ctx, l.decide(aheadWait, true, nil), l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		})
		if err != nil || got != "yes value" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "a yes costs one wait: the decision and the branch overlap")
		if l.seen("no started") || l.seen("yes cancelled") {
			t.Errorf("a yes started no or cancelled yes: %v", l.names())
		}
		aheadBefore(t, &l, "yes started", "decide answered")
		aheadQuiet(t, &l)

		// The same two waits, one after the other, as Branch costs them.
		var sequential aheadLog
		_, taken, err := aheadTimed(func() (string, error) {
			yes, err := sequential.decide(aheadWait, true, nil)(ctx)
			if err != nil {
				return "", err
			}
			return Branch(ctx, yes, sequential.step("yes", aheadWait, "yes value", nil), sequential.step("no", aheadWait, "no value", nil))
		})
		if err != nil {
			t.Errorf("Branch after a decision: %v", err)
		}
		aheadTook(t, taken, 2*aheadWait, "Branch after a decision waits for the decision and then for the branch")
		if elapsed >= taken {
			t.Errorf("BranchAhead (%v) was no faster than Branch after a decision (%v)", elapsed, taken)
		}
	})
}

func TestAheadBranchYesWaitsForASlowerBranch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(context.Background(), l.decide(30*time.Millisecond, true, nil), l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		})
		if err != nil || got != "yes value" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "yes is the slower of the two and the call waits for it")
		aheadBefore(t, &l, "decide answered", "yes finished")
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchYesThatFinishedBeforeTheDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchAhead(context.Background(), l.decide(aheadWait, true, nil), l.step("yes", 25*time.Millisecond, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		if err != nil || got != "yes value" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadBefore(t, &l, "yes finished", "decide answered")
		aheadQuiet(t, &l)
	})
}

// A no cancels the speculative step's context, discards what it would have
// returned, and returns the other branch.
func TestAheadBranchNoCancelsYesAndRunsNo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		var noSawCancelled atomic.Bool
		no := func(ctx context.Context) (string, error) {
			noSawCancelled.Store(ctx.Err() != nil)
			return l.step("no", 25*time.Millisecond, "no value", nil)(ctx)
		}
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(context.Background(), l.decide(50*time.Millisecond, false, nil), l.step("yes", aheadFar, "yes value", nil), no)
		})
		if err != nil || got != "no value" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 75*time.Millisecond, "the decision's 50 ms and then no's 25; yes was cancelled and not waited for")
		if !l.seen("yes cancelled") || l.seen("yes finished") {
			t.Errorf("yes was not cancelled: %v", l.names())
		}
		aheadBefore(t, &l, "decide answered", "yes cancelled")
		aheadBefore(t, &l, "decide answered", "no started")
		if noSawCancelled.Load() {
			t.Error("no ran under a context that was already cancelled")
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchNoStartsOnlyAfterTheDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		if _, err := BranchAhead(context.Background(), l.decide(aheadWait, false, nil), l.step("yes", aheadWait, "yes value", nil), l.step("no", 25*time.Millisecond, "no value", nil)); err != nil {
			t.Fatal(err)
		}
		names := l.names()
		answered := -1
		for i, name := range names {
			if name == "decide answered" {
				answered = i
			}
			if name == "no started" && answered < 0 {
				t.Errorf("no started before the decision: %v", names)
			}
		}
		if answered < 0 {
			t.Errorf("the decision never answered: %v", names)
		}
	})
}

func TestAheadBranchNoAfterTheDecisionIsAsFastAsBranch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		_, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(context.Background(), l.decide(aheadWait, false, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		})
		if err != nil {
			t.Fatal(err)
		}
		// Two waits, as without speculation. A cancelled yes does not make it slower.
		aheadTook(t, elapsed, 2*aheadWait, "the decision and then no")
	})
}

func TestAheadBranchNoDiscardsWhatYesReturned(t *testing.T) {
	cases := []struct {
		name string
		yes  Step[string]
	}{
		{"an error", func(context.Context) (string, error) { return "stale", aheadBoom }},
		{"a value", func(context.Context) (string, error) { return "stale", nil }},
		{"a panic", aheadPanicking[string]("7731")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, err := BranchAhead(context.Background(), l.decide(aheadWait, false, nil), tc.yes, l.step("no", 25*time.Millisecond, "no value", nil))
				if err != nil || got != "no value" {
					t.Errorf("BranchAhead = %q, %v; yes was a branch not taken", got, err)
				}
			})
		})
	}
}

func TestAheadBranchNoThatFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchAhead(context.Background(), l.decide(25*time.Millisecond, false, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", 25*time.Millisecond, "", aheadBoom))
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchDecisionThatFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(context.Background(), l.decide(50*time.Millisecond, false, aheadBoom), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		})
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 50*time.Millisecond, "the decision failed at 50 ms and yes was cancelled and not waited for")
		if !l.seen("yes cancelled") || l.seen("no started") {
			t.Errorf("a failed decision left yes running or started no: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

// A decision that fails while yes has already succeeded returns the failure and
// nothing of yes beside it.
func TestAheadBranchDecisionThatFailsAfterYesFinished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchAhead(context.Background(), l.decide(aheadWait, false, aheadBoom), l.step("yes", 25*time.Millisecond, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchYesThatFailsBeforeTheDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchAhead(context.Background(), l.decide(aheadWait, true, nil), l.step("yes", 25*time.Millisecond, "", aheadBoom), l.step("no", aheadWait, "no value", nil))
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Errorf("BranchAhead = %q, %v; the decision said yes and yes failed", got, err)
		}
		aheadBefore(t, &l, "yes failed", "decide answered")
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchYesThatFailsAfterTheDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchAhead(context.Background(), l.decide(25*time.Millisecond, true, nil), l.step("yes", aheadWait, "", aheadBoom), l.step("no", aheadWait, "no value", nil))
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchCallerCancelsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(40*time.Millisecond, cancel)
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(ctx, l.decide(aheadFar, true, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		})
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 40*time.Millisecond, "the caller cancelled at 40 ms")
		if !l.seen("yes cancelled") || !l.seen("decide cancelled") {
			t.Errorf("the steps were not both stopped: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		var l aheadLog
		_, err := BranchAhead(ctx, l.decide(aheadFar, true, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
		aheadQuiet(t, &l)
	})
}

// A decision that does not look at its context and says yes after the caller
// has gone is not an answer: the caller gets its context's error.
func TestAheadBranchDecisionThatAnswersAfterTheCallerHasGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		var l aheadLog
		got, err := BranchAhead(ctx, func(context.Context) (bool, error) {
			time.Sleep(80 * time.Millisecond)
			return true, nil
		}, l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchCallerAlreadyGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var l aheadLog
		got, err := BranchAhead(ctx, l.decide(aheadWait, true, nil), l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchPanics(t *testing.T) {
	cases := []struct {
		name    string
		decide  func(l *aheadLog) Step[bool]
		yes, no func(l *aheadLog) Step[string]
		wantErr bool
		want    string
	}{
		{"in yes, and yes is chosen",
			func(l *aheadLog) Step[bool] { return l.decide(aheadWait, true, nil) },
			func(*aheadLog) Step[string] { return aheadPanicking[string]("7731") },
			func(l *aheadLog) Step[string] { return l.step("no", aheadWait, "no value", nil) }, true, ""},
		{"in yes, and no is chosen",
			func(l *aheadLog) Step[bool] { return l.decide(aheadWait, false, nil) },
			func(*aheadLog) Step[string] { return aheadPanicking[string]("7731") },
			func(l *aheadLog) Step[string] { return l.step("no", 25*time.Millisecond, "no value", nil) }, false, "no value"},
		{"in no",
			func(l *aheadLog) Step[bool] { return l.decide(25*time.Millisecond, false, nil) },
			func(l *aheadLog) Step[string] { return l.step("yes", aheadFar, "yes value", nil) },
			func(*aheadLog) Step[string] { return aheadPanicking[string]("7731") }, true, ""},
		{"in the decision",
			func(*aheadLog) Step[bool] { return aheadPanicking[bool]("7731") },
			func(l *aheadLog) Step[string] { return l.step("yes", aheadFar, "yes value", nil) },
			func(l *aheadLog) Step[string] { return l.step("no", aheadWait, "no value", nil) }, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, err := BranchAhead(context.Background(), tc.decide(&l), tc.yes(&l), tc.no(&l))
				if tc.wantErr {
					aheadCheckPanicError(t, err)
				} else if err != nil {
					t.Errorf("err = %v", err)
				}
				if got != tc.want {
					t.Errorf("BranchAhead = %q, want %q", got, tc.want)
				}
				aheadQuiet(t, &l)
			})
		})
	}
}

func TestAheadBranchNilSteps(t *testing.T) {
	ctx := context.Background()
	var l aheadLog

	t.Run("a nil decision starts nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := BranchAhead(ctx, nil, l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadWait, "no value", nil))
			if types.KindOf(err) != types.KindConfiguration || got != "" || l.started.Load() != 0 {
				t.Errorf("BranchAhead = %q, %v; %d started", got, err, l.started.Load())
			}
		})
	})
	t.Run("a nil yes, chosen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := BranchAhead(ctx, l.decide(25*time.Millisecond, true, nil), nil, l.step("no", aheadWait, "no value", nil))
			if err != nil || got != "" {
				t.Errorf("BranchAhead = %q, %v", got, err)
			}
		})
	})
	t.Run("a nil yes, not chosen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := BranchAhead(ctx, l.decide(25*time.Millisecond, false, nil), nil, l.step("no", 25*time.Millisecond, "no value", nil))
			if err != nil || got != "no value" {
				t.Errorf("BranchAhead = %q, %v", got, err)
			}
		})
	})
	t.Run("a nil no, chosen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := BranchAhead(ctx, l.decide(25*time.Millisecond, false, nil), l.step("yes", aheadFar, "yes value", nil), nil)
			if err != nil || got != "" {
				t.Errorf("BranchAhead = %q, %v", got, err)
			}
		})
	})
	t.Run("both nil", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := BranchAhead(ctx, l.decide(25*time.Millisecond, true, nil), Step[string](nil), nil)
			if err != nil || got != "" {
				t.Errorf("BranchAhead = %q, %v", got, err)
			}
		})
	})
	aheadQuiet(t, &l)
}

// A step that does not look at its context holds the call until it returns, and
// is never left running behind it.
func TestAheadBranchStubbornStepHoldsTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchAhead(context.Background(), l.decide(25*time.Millisecond, false, nil), l.stubborn("yes", aheadWait, "yes value"), l.step("no", 25*time.Millisecond, "no value", nil))
		})
		if err != nil || got != "no value" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "the step that ignored its context holds the call until it returns")
		aheadQuiet(t, &l)
	})
}

func TestAheadBranchStepsRunUnderTheCallersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type key struct{}
		ctx := context.WithValue(context.Background(), key{}, "carried")
		var seen [3]atomic.Value
		note := func(i int, ctx context.Context) { seen[i].Store(fmt.Sprint(ctx.Value(key{}))) }
		_, err := BranchAhead(ctx,
			func(ctx context.Context) (bool, error) { note(0, ctx); return true, nil },
			func(ctx context.Context) (string, error) { note(1, ctx); return "yes", nil },
			func(ctx context.Context) (string, error) { note(2, ctx); return "no", nil })
		if err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"decision", "yes"} {
			if seen[i].Load() != "carried" {
				t.Errorf("the %s did not see the caller's context values", name)
			}
		}
		_, err = BranchAhead(ctx,
			func(context.Context) (bool, error) { return false, nil },
			func(context.Context) (string, error) { return "yes", nil },
			func(ctx context.Context) (string, error) { note(2, ctx); return "no", nil })
		if err != nil || seen[2].Load() != "carried" {
			t.Errorf("no did not see the caller's context values: %v", err)
		}
	})
}

// The caller's context is not cancelled by the call: cancelling yes cancels its
// own context and nothing above it.
func TestAheadBranchDoesNotCancelTheCallersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for _, answer := range []bool{true, false} {
			var l aheadLog
			if _, err := BranchAhead(ctx, l.decide(25*time.Millisecond, answer, nil), l.step("yes", 25*time.Millisecond, "yes", nil), l.step("no", 25*time.Millisecond, "no", nil)); err != nil {
				t.Fatal(err)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("after a %v the caller's context is %v", answer, err)
			}
		}
	})
}

// BranchBoth.

func TestAheadBothYesCostsOneWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchBoth(context.Background(), l.decide(aheadWait, true, nil), l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		})
		if err != nil || got != "yes value" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "a yes costs one wait: the decision and both branches overlap")
		aheadBefore(t, &l, "no started", "decide answered")
		aheadBefore(t, &l, "decide answered", "no cancelled")
		if l.seen("no finished") {
			t.Errorf("no was not cancelled: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothNoCostsOneWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchBoth(context.Background(), l.decide(aheadWait, false, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadWait, "no value", nil))
		})
		if err != nil || got != "no value" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "a no costs one wait: the decision and both branches overlap")
		aheadBefore(t, &l, "yes started", "decide answered")
		aheadBefore(t, &l, "decide answered", "yes cancelled")
		if l.seen("yes finished") {
			t.Errorf("yes was not cancelled: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothStartsAllThreeAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		if _, err := BranchBoth(context.Background(), l.decide(aheadWait, true, nil), l.step("yes", 25*time.Millisecond, "yes value", nil), l.step("no", 25*time.Millisecond, "no value", nil)); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"decide started", "yes started", "no started"} {
			aheadBefore(t, &l, name, "decide answered")
		}
	})
}

func TestAheadBothTheBranchNotTakenIsDiscarded(t *testing.T) {
	cases := []struct {
		name   string
		answer bool
		yes    Step[string]
		no     Step[string]
		want   string
	}{
		{"no fails, yes is chosen", true, func(context.Context) (string, error) { return "yes value", nil }, func(context.Context) (string, error) { return "stale", aheadBoom }, "yes value"},
		{"yes fails, no is chosen", false, func(context.Context) (string, error) { return "stale", aheadBoom }, func(context.Context) (string, error) { return "no value", nil }, "no value"},
		{"no panics, yes is chosen", true, func(context.Context) (string, error) { return "yes value", nil }, aheadPanicking[string]("7731"), "yes value"},
		{"yes panics, no is chosen", false, aheadPanicking[string]("7731"), func(context.Context) (string, error) { return "no value", nil }, "no value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, err := BranchBoth(context.Background(), l.decide(aheadWait, tc.answer, nil), tc.yes, tc.no)
				if err != nil || got != tc.want {
					t.Errorf("BranchBoth = %q, %v; want %q", got, err, tc.want)
				}
				aheadQuiet(t, &l)
			})
		})
	}
}

func TestAheadBothTheBranchTakenFails(t *testing.T) {
	for _, answer := range []bool{true, false} {
		t.Run(fmt.Sprint("answer ", answer), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, err := BranchBoth(context.Background(), l.decide(aheadWait, answer, nil), l.step("yes", 25*time.Millisecond, "", aheadBoom), l.step("no", 25*time.Millisecond, "", aheadBoom))
				if !errors.Is(err, aheadBoom) || got != "" {
					t.Errorf("BranchBoth = %q, %v", got, err)
				}
				aheadQuiet(t, &l)
			})
		})
	}
}

func TestAheadBothDecisionThatFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchBoth(context.Background(), l.decide(50*time.Millisecond, true, aheadBoom), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		})
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 50*time.Millisecond, "the decision failed at 50 ms and both branches were cancelled")
		if !l.seen("yes cancelled") || !l.seen("no cancelled") {
			t.Errorf("a failed decision left a branch running: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothCallerCancelsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(40*time.Millisecond, cancel)
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchBoth(ctx, l.decide(aheadFar, true, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		})
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 40*time.Millisecond, "the caller cancelled at 40 ms")
		for _, name := range []string{"decide cancelled", "yes cancelled", "no cancelled"} {
			if !l.seen(name) {
				t.Errorf("%q did not happen: %v", name, l.names())
			}
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothDecisionThatAnswersAfterTheCallerHasGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		var l aheadLog
		got, err := BranchBoth(ctx, func(context.Context) (bool, error) {
			time.Sleep(80 * time.Millisecond)
			return false, nil
		}, l.step("yes", aheadFar, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Errorf("BranchBoth = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothPanics(t *testing.T) {
	cases := []struct {
		name    string
		answer  bool
		yes, no Step[string]
		decide  Step[bool]
	}{
		{"in yes, chosen", true, aheadPanicking[string]("7731"), nil, nil},
		{"in no, chosen", false, nil, aheadPanicking[string]("7731"), nil},
		{"in the decision", true, nil, nil, aheadPanicking[bool]("7731")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				yes, no, decide := tc.yes, tc.no, tc.decide
				if yes == nil {
					yes = l.step("yes", aheadWait, "yes value", nil)
				}
				if no == nil {
					no = l.step("no", aheadWait, "no value", nil)
				}
				if decide == nil {
					decide = l.decide(25*time.Millisecond, tc.answer, nil)
				}
				got, err := BranchBoth(context.Background(), decide, yes, no)
				aheadCheckPanicError(t, err)
				if got != "" {
					t.Errorf("BranchBoth = %q beside an error", got)
				}
				aheadQuiet(t, &l)
			})
		})
	}
}

func TestAheadBothNilSteps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		var l aheadLog
		if got, err := BranchBoth(ctx, nil, l.step("yes", aheadWait, "yes value", nil), l.step("no", aheadWait, "no value", nil)); types.KindOf(err) != types.KindConfiguration || got != "" || l.started.Load() != 0 {
			t.Errorf("a nil decision: %q, %v; %d started", got, err, l.started.Load())
		}
		if got, err := BranchBoth(ctx, l.decide(25*time.Millisecond, true, nil), nil, l.step("no", aheadFar, "no value", nil)); err != nil || got != "" {
			t.Errorf("a nil yes: %q, %v", got, err)
		}
		if got, err := BranchBoth(ctx, l.decide(25*time.Millisecond, false, nil), l.step("yes", aheadFar, "yes value", nil), nil); err != nil || got != "" {
			t.Errorf("a nil no: %q, %v", got, err)
		}
		if got, err := BranchBoth(ctx, l.decide(25*time.Millisecond, false, nil), l.step("yes", aheadFar, "yes value", nil), l.step("no", 25*time.Millisecond, "no value", nil)); err != nil || got != "no value" {
			t.Errorf("both given: %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadBothStubbornLoserHoldsTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return BranchBoth(context.Background(), l.decide(25*time.Millisecond, true, nil), l.step("yes", 25*time.Millisecond, "yes value", nil), l.stubborn("no", aheadWait, "no value"))
		})
		if err != nil || got != "yes value" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "the branch that ignored its context holds the call until it returns")
		aheadQuiet(t, &l)
	})
}

func TestAheadBothWinnerFinishedBeforeTheDecision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := BranchBoth(context.Background(), l.decide(aheadWait, true, nil), l.step("yes", 25*time.Millisecond, "yes value", nil), l.step("no", aheadFar, "no value", nil))
		if err != nil || got != "yes value" {
			t.Fatalf("BranchBoth = %q, %v", got, err)
		}
		aheadBefore(t, &l, "yes finished", "decide answered")
		aheadBefore(t, &l, "decide answered", "no cancelled")
	})
}

// RouteAhead.

func aheadRoutes(l *aheadLog, d map[string]time.Duration) []Route[string] {
	return []Route[string]{
		To("billing", l.step("billing", d["billing"], "to billing", nil)),
		To("technical", l.step("technical", d["technical"], "to technical", nil)),
		To("shipping", l.step("shipping", d["shipping"], "to shipping", nil)),
	}
}

func TestAheadRouteLikelyIsNamedCostsOneWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return RouteAhead(context.Background(), l.pick(aheadWait, "billing", nil), "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadWait, "technical": aheadFar, "shipping": aheadFar})...)
		})
		if err != nil || got != "to billing" {
			t.Fatalf("RouteAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "the decision and the likely route overlap")
		if l.seen("technical started") || l.seen("shipping started") || l.seen("billing cancelled") {
			t.Errorf("a route other than the likely one started, or it was cancelled: %v", l.names())
		}
		aheadBefore(t, &l, "billing started", "decide answered")
		aheadQuiet(t, &l)
	})
}

func TestAheadRouteAnotherIsNamed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return RouteAhead(context.Background(), l.pick(50*time.Millisecond, "technical", nil), "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadFar, "technical": 25 * time.Millisecond, "shipping": aheadFar})...)
		})
		if err != nil || got != "to technical" {
			t.Fatalf("RouteAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 75*time.Millisecond, "the decision's 50 ms and then technical's 25; the likely route was cancelled and not waited for")
		if !l.seen("billing cancelled") || l.seen("billing finished") || l.seen("shipping started") {
			t.Errorf("billing was not the only one cancelled: %v", l.names())
		}
		aheadBefore(t, &l, "decide answered", "technical started")
		aheadBefore(t, &l, "decide answered", "billing cancelled")
		aheadQuiet(t, &l)
	})
}

func TestAheadRouteEveryLabelIsNamedInTurn(t *testing.T) {
	labels := []string{"billing", "technical", "shipping"}
	for _, likely := range labels {
		for _, named := range labels {
			t.Run(likely+" is likely, "+named+" is named", func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var l aheadLog
					got, err := RouteAhead(context.Background(), l.pick(25*time.Millisecond, named, nil), likely,
						aheadRoutes(&l, map[string]time.Duration{"billing": 25 * time.Millisecond, "technical": 25 * time.Millisecond, "shipping": 25 * time.Millisecond})...)
					if want := "to " + named; err != nil || got != want {
						t.Errorf("RouteAhead = %q, %v; want %q", got, err, want)
					}
					if likely != named && !l.seen(likely+" cancelled") && !l.seen(likely+" finished") {
						t.Errorf("the likely route did nothing: %v", l.names())
					}
					aheadQuiet(t, &l)
				})
			})
		}
	}
}

func TestAheadRouteDecidedLabelIsNoRoute(t *testing.T) {
	for _, label := range []string{"returns", "", "Billing", " billing ", "bill"} {
		t.Run(fmt.Sprintf("%q", label), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, elapsed, err := aheadTimed(func() (string, error) {
					return RouteAhead(context.Background(), l.pick(25*time.Millisecond, label, nil), "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadFar, "technical": 25 * time.Millisecond, "shipping": 25 * time.Millisecond})...)
				})
				if types.KindOf(err) != types.KindInvariantViolation || got != "" {
					t.Errorf("RouteAhead = %q, %v", got, err)
				}
				// It is the error Select returns for the same label.
				_, selectErr := Select(context.Background(), label, aheadRoutes(&l, nil)...)
				if types.KindOf(selectErr) != types.KindOf(err) {
					t.Errorf("Select returns %v and RouteAhead %v", types.KindOf(selectErr), types.KindOf(err))
				}
				aheadTook(t, elapsed, 25*time.Millisecond, "the decision answered at 25 ms and the likely route was cancelled and not waited for")
				if !l.seen("billing cancelled") {
					t.Errorf("the likely route was not cancelled: %v", l.names())
				}
			})
		})
	}
}

func TestAheadRouteDecisionThatFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return RouteAhead(context.Background(), l.pick(50*time.Millisecond, "billing", aheadBoom), "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadFar, "technical": aheadWait, "shipping": aheadWait})...)
		})
		if !errors.Is(err, aheadBoom) || got != "" {
			t.Fatalf("RouteAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 50*time.Millisecond, "the decision failed at 50 ms and the likely route was cancelled and not waited for")
		if !l.seen("billing cancelled") || l.seen("technical started") {
			t.Errorf("a failed decision left the likely route running: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadRouteConfigurationErrorsStartNothing(t *testing.T) {
	var l aheadLog
	step := l.step("a", aheadWait, "a", nil)
	cases := []struct {
		name   string
		likely string
		routes []Route[string]
	}{
		{"a likely that is no route's label", "returns", []Route[string]{To("billing", step), To("technical", step)}},
		{"a likely in another case", "Billing", []Route[string]{To("billing", step)}},
		{"a likely with space around it", " billing ", []Route[string]{To("billing", step)}},
		{"a blank likely", "", []Route[string]{To("billing", step)}},
		{"a repeated label", "billing", []Route[string]{To("billing", step), To("billing", step)}},
		{"a repeat with space around it", "billing", []Route[string]{To("billing", step), To(" billing", step)}},
		{"a nil step", "billing", []Route[string]{To("billing", step), To[string]("technical", nil)}},
		{"a nil likely step", "billing", []Route[string]{To[string]("billing", nil), To("technical", step)}},
		{"a blank label", "billing", []Route[string]{To("billing", step), To("  ", step)}},
		{"no routes", "billing", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got, err := RouteAhead(context.Background(), l.pick(25*time.Millisecond, "billing", nil), tc.likely, tc.routes...)
				if types.KindOf(err) != types.KindConfiguration || got != "" {
					t.Errorf("RouteAhead = %q, %v", got, err)
				}
				if l.started.Load() != 0 {
					t.Errorf("%d steps started before the configuration was refused", l.started.Load())
				}
			})
		})
	}
	t.Run("a nil decision", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			got, err := RouteAhead(context.Background(), nil, "billing", To("billing", step))
			if types.KindOf(err) != types.KindConfiguration || got != "" || l.started.Load() != 0 {
				t.Errorf("RouteAhead = %q, %v", got, err)
			}
		})
	})
}

func TestAheadRouteLikelyFailsBeforeTheDecision(t *testing.T) {
	t.Run("and is named", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			routes := []Route[string]{To("billing", l.step("billing", 25*time.Millisecond, "", aheadBoom)), To("technical", l.step("technical", 25*time.Millisecond, "to technical", nil))}
			got, err := RouteAhead(context.Background(), l.pick(aheadWait, "billing", nil), "billing", routes...)
			if !errors.Is(err, aheadBoom) || got != "" {
				t.Errorf("RouteAhead = %q, %v", got, err)
			}
			aheadQuiet(t, &l)
		})
	})
	t.Run("and another is named", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			routes := []Route[string]{To("billing", l.step("billing", 25*time.Millisecond, "", aheadBoom)), To("technical", l.step("technical", 25*time.Millisecond, "to technical", nil))}
			got, err := RouteAhead(context.Background(), l.pick(aheadWait, "technical", nil), "billing", routes...)
			if err != nil || got != "to technical" {
				t.Errorf("RouteAhead = %q, %v", got, err)
			}
			aheadQuiet(t, &l)
		})
	})
	t.Run("and the named one fails too", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l aheadLog
			other := errors.New("the other failed")
			routes := []Route[string]{To("billing", l.step("billing", 25*time.Millisecond, "", aheadBoom)), To("technical", l.step("technical", 25*time.Millisecond, "", other))}
			_, err := RouteAhead(context.Background(), l.pick(aheadWait, "technical", nil), "billing", routes...)
			if !errors.Is(err, other) || errors.Is(err, aheadBoom) {
				t.Errorf("err = %v; the likely route's failure was a route not taken", err)
			}
		})
	})
}

func TestAheadRouteCallerCancelsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(40*time.Millisecond, cancel)
		var l aheadLog
		got, elapsed, err := aheadTimed(func() (string, error) {
			return RouteAhead(ctx, l.pick(aheadFar, "billing", nil), "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadFar, "technical": aheadFar, "shipping": aheadFar})...)
		})
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("RouteAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, 40*time.Millisecond, "the caller cancelled at 40 ms")
		if !l.seen("billing cancelled") || !l.seen("decide cancelled") {
			t.Errorf("not both stopped: %v", l.names())
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadRouteDecisionThatAnswersAfterTheCallerHasGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(20*time.Millisecond, cancel)
		var l aheadLog
		got, err := RouteAhead(ctx, func(context.Context) (string, error) {
			time.Sleep(80 * time.Millisecond)
			return "billing", nil
		}, "billing", aheadRoutes(&l, map[string]time.Duration{"billing": aheadFar, "technical": aheadWait, "shipping": aheadWait})...)
		if !errors.Is(err, context.Canceled) || got != "" {
			t.Errorf("RouteAhead = %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

func TestAheadRoutePanics(t *testing.T) {
	cases := []struct {
		name    string
		decide  func(l *aheadLog) Step[string]
		routes  func(l *aheadLog) []Route[string]
		wantErr bool
		want    string
	}{
		{"in the likely route, and it is named",
			func(l *aheadLog) Step[string] { return l.pick(aheadWait, "billing", nil) },
			func(l *aheadLog) []Route[string] {
				return []Route[string]{To("billing", aheadPanicking[string]("7731")), To("technical", l.step("technical", aheadWait, "to technical", nil))}
			}, true, ""},
		{"in the likely route, and another is named",
			func(l *aheadLog) Step[string] { return l.pick(aheadWait, "technical", nil) },
			func(l *aheadLog) []Route[string] {
				return []Route[string]{To("billing", aheadPanicking[string]("7731")), To("technical", l.step("technical", 25*time.Millisecond, "to technical", nil))}
			}, false, "to technical"},
		{"in the named route",
			func(l *aheadLog) Step[string] { return l.pick(25*time.Millisecond, "technical", nil) },
			func(l *aheadLog) []Route[string] {
				return []Route[string]{To("billing", l.step("billing", aheadFar, "to billing", nil)), To("technical", aheadPanicking[string]("7731"))}
			}, true, ""},
		{"in the decision",
			func(*aheadLog) Step[string] { return aheadPanicking[string]("7731") },
			func(l *aheadLog) []Route[string] {
				return []Route[string]{To("billing", l.step("billing", aheadFar, "to billing", nil)), To("technical", l.step("technical", aheadWait, "to technical", nil))}
			}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var l aheadLog
				got, err := RouteAhead(context.Background(), tc.decide(&l), "billing", tc.routes(&l)...)
				if tc.wantErr {
					aheadCheckPanicError(t, err)
				} else if err != nil {
					t.Errorf("err = %v", err)
				}
				if got != tc.want {
					t.Errorf("RouteAhead = %q, want %q", got, tc.want)
				}
				aheadQuiet(t, &l)
			})
		})
	}
}

func TestAheadRouteStubbornLikelyRouteHoldsTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		routes := []Route[string]{To("billing", l.stubborn("billing", aheadWait, "to billing")), To("technical", l.step("technical", 25*time.Millisecond, "to technical", nil))}
		got, elapsed, err := aheadTimed(func() (string, error) {
			return RouteAhead(context.Background(), l.pick(25*time.Millisecond, "technical", nil), "billing", routes...)
		})
		if err != nil || got != "to technical" {
			t.Fatalf("RouteAhead = %q, %v", got, err)
		}
		aheadTook(t, elapsed, aheadWait, "the route that ignored its context holds the call until it returns")
		aheadQuiet(t, &l)
	})
}

func TestAheadRouteWithOneRoute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l aheadLog
		got, err := RouteAhead(context.Background(), l.pick(25*time.Millisecond, "only", nil), "only", To("only", l.step("only", 25*time.Millisecond, "the only one", nil)))
		if err != nil || got != "the only one" {
			t.Errorf("RouteAhead = %q, %v", got, err)
		}
		got, err = RouteAhead(context.Background(), l.pick(25*time.Millisecond, "other", nil), "only", To("only", l.step("only", aheadFar, "the only one", nil)))
		if types.KindOf(err) != types.KindInvariantViolation || got != "" {
			t.Errorf("a label that is no route's: %q, %v", got, err)
		}
		aheadQuiet(t, &l)
	})
}

// No step is left running after the call that started it, whichever way the
// call went: each step signals its return and every signal has been seen when
// the call returns. synctest verifies that the bubble is quiescent when it ends.
func TestAheadNothingIsLeftRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		// short is a duration of a millisecond or three, varied so that the decision
		// and the branch finish in either order.
		short := func(i, offset int) time.Duration { return time.Duration((i+offset)%4) * time.Millisecond }

		for i := 0; i < 200; i++ {
			var l aheadLog
			answer := i%2 == 0
			var fail error
			if i%5 == 0 {
				fail = aheadBoom
			}
			switch i % 3 {
			case 0:
				_, _ = BranchAhead(ctx, l.decide(short(i, 0), answer, nil), l.step("yes", short(i, 1), "yes", fail), l.step("no", short(i, 2), "no", nil))
			case 1:
				_, _ = BranchBoth(ctx, l.decide(short(i, 0), answer, fail), l.step("yes", short(i, 1), "yes", nil), l.step("no", short(i, 2), "no", nil))
			default:
				label := "billing"
				if !answer {
					label = "technical"
				}
				routes := []Route[string]{To("billing", l.step("billing", short(i, 1), "billing", fail)), To("technical", l.step("technical", short(i, 2), "technical", nil))}
				_, _ = RouteAhead(ctx, l.pick(short(i, 0), label, nil), "billing", routes...)
			}
			if started, exited := l.started.Load(), l.exited.Load(); started != exited {
				t.Fatalf("iteration %d: %d steps started and %d had returned when the call did: %v", i, started, exited, l.names())
			}
		}

		// Callers that go away, too.
		for i := 0; i < 200; i++ {
			var l aheadLog
			cctx, cancel := context.WithCancel(ctx)
			time.AfterFunc(time.Duration(i%3)*time.Millisecond, cancel)
			switch i % 3 {
			case 0:
				_, _ = BranchAhead(cctx, l.decide(aheadFar, true, nil), l.step("yes", aheadFar, "yes", nil), l.step("no", aheadFar, "no", nil))
			case 1:
				_, _ = BranchBoth(cctx, l.decide(aheadFar, true, nil), l.step("yes", aheadFar, "yes", nil), l.step("no", aheadFar, "no", nil))
			default:
				_, _ = RouteAhead(cctx, l.pick(aheadFar, "billing", nil), "billing", To("billing", l.step("billing", aheadFar, "billing", nil)))
			}
			cancel()
			if started, exited := l.started.Load(), l.exited.Load(); started != exited {
				t.Fatalf("cancelled iteration %d: %d steps started and %d had returned when the call did: %v", i, started, exited, l.names())
			}
		}
	})
}

// The three are safe to call from many goroutines at once, and each call gets
// its own answer.
func TestAheadConcurrentUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		var wait sync.WaitGroup
		failures := make(chan string, 150)
		var all aheadLog
		for g := 0; g < 50; g++ {
			wait.Add(1)
			go func(g int) {
				defer wait.Done()
				answer := g%2 == 0
				yes, no := fmt.Sprintf("yes %d", g), fmt.Sprintf("no %d", g)
				want := no
				if answer {
					want = yes
				}
				if got, err := BranchAhead(ctx, all.decide(25*time.Millisecond, answer, nil), all.step("yes", 30*time.Millisecond, yes, nil), all.step("no", 30*time.Millisecond, no, nil)); err != nil || got != want {
					failures <- fmt.Sprintf("BranchAhead %d = %q, %v; want %q", g, got, err, want)
				}
				if got, err := BranchBoth(ctx, all.decide(25*time.Millisecond, answer, nil), all.step("yes", 30*time.Millisecond, yes, nil), all.step("no", 30*time.Millisecond, no, nil)); err != nil || got != want {
					failures <- fmt.Sprintf("BranchBoth %d = %q, %v; want %q", g, got, err, want)
				}
				label := "billing"
				if !answer {
					label = "technical"
				}
				routes := []Route[string]{To("billing", all.step("billing", 30*time.Millisecond, yes, nil)), To("technical", all.step("technical", 30*time.Millisecond, no, nil))}
				if got, err := RouteAhead(ctx, all.pick(25*time.Millisecond, label, nil), "billing", routes...); err != nil || got != want {
					failures <- fmt.Sprintf("RouteAhead %d = %q, %v; want %q", g, got, err, want)
				}
			}(g)
		}
		wait.Wait()
		close(failures)
		for failure := range failures {
			t.Error(failure)
		}
		aheadQuiet(t, &all)
	})
}
