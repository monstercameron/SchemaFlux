package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// controlFlowLogBuffer is a buffer the logger of a snapshot can write to from
// the goroutines the combinators start.
type controlFlowLogBuffer struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (b *controlFlowLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *controlFlowLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

// controlFlowSnapshotLog returns a context whose snapshot carries a logger that
// writes to the returned buffer at every level, and a function that renders
// what the process logger was given while the test ran. The process logger is
// replaced for the length of the test, so a line that went there is seen.
func controlFlowSnapshotLog(t *testing.T) (ctx context.Context, snapshot *controlFlowLogBuffer, process func() string) {
	t.Helper()
	process = captureLogs(t)
	snapshot = &controlFlowLogBuffer{}
	named := telemetry.FromSlog(slog.New(slog.NewTextHandler(snapshot, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return snapshotCtx(ExecConfig{Logger: named}), snapshot, process
}

// Every error that leaves flow.go has a kind, its text is the text it had
// before the kinds were declared, and the sentinel errors.Is matches is the one
// of that kind.
func TestFlowErrorsKeepTheirTextAndHaveTheirKind(t *testing.T) {
	ctx := context.Background()
	yes := func(context.Context) (string, error) { return "yes", nil }
	stepOK := Step[string](yes)
	decideTrue := Step[bool](func(context.Context) (bool, error) { return true, nil })
	decideLabel := func(label string) Step[string] {
		return func(context.Context) (string, error) { return label, nil }
	}
	cases := []struct {
		name     string
		run      func() error
		text     string
		kind     types.ErrorKind
		sentinel error
		panicked bool
	}{
		{"Select with no routes", func() error { _, err := Select[string](ctx, "a"); return err },
			"select: configuration: there are no routes to choose between", types.KindConfiguration, types.ErrConfiguration, false},
		{"Select with a blank label", func() error { _, err := Select(ctx, "a", To(" ", stepOK)); return err },
			"select: configuration: route 1 has no label", types.KindConfiguration, types.ErrConfiguration, false},
		{"Select with a repeated label", func() error { _, err := Select(ctx, "a", To("a", stepOK), To("a ", stepOK)); return err },
			"select: configuration: route 2 repeats the label of an earlier one", types.KindConfiguration, types.ErrConfiguration, false},
		{"Select with a route that has nothing to run", func() error { _, err := Select(ctx, "a", To("a", Step[string](nil))); return err },
			"select: configuration: route 1 has nothing to run", types.KindConfiguration, types.ErrConfiguration, false},
		{"Select with a label no route takes", func() error { _, err := Select(ctx, "c", To("a", stepOK), To("b", stepOK)); return err },
			"select: invariant violation: the label is not the label of any of the 2 routes", types.KindInvariantViolation, types.ErrInvariantViolation, false},
		{"RouteBy with no routes", func() error { _, err := RouteBy[string](ctx, "s", "q?", nil); return err },
			"route: configuration: there are no routes to choose between", types.KindConfiguration, types.ErrConfiguration, false},
		{"RouteBy with one route", func() error { _, err := RouteBy(ctx, "s", "q?", []Route[string]{To("a", stepOK)}); return err },
			"route: configuration: RouteBy needs at least two routes to choose between, and was given 1", types.KindConfiguration, types.ErrConfiguration, false},
		{"Gather with a nil step", func() error { _, err := Gather(ctx, stepOK, nil); return err },
			"gather: configuration: step 2 is nil", types.KindConfiguration, types.ErrConfiguration, false},
		{"Gather2 with a nil step", func() error { _, _, err := Gather2(ctx, stepOK, Step[int](nil)); return err },
			"gather: configuration: step 2 is nil", types.KindConfiguration, types.ErrConfiguration, false},
		{"Gather3 with a nil step", func() error { _, _, _, err := Gather3(ctx, Step[int](nil), stepOK, stepOK); return err },
			"gather: configuration: step 1 is nil", types.KindConfiguration, types.ErrConfiguration, false},
		{"Gather4 with a nil step", func() error {
			_, _, _, _, err := Gather4(ctx, stepOK, stepOK, stepOK, Step[int](nil))
			return err
		}, "gather: configuration: step 4 is nil", types.KindConfiguration, types.ErrConfiguration, false},
		{"BranchAhead with no decision", func() error { _, err := BranchAhead(ctx, nil, stepOK, stepOK); return err },
			"branch_ahead: configuration: there is no decision to wait for", types.KindConfiguration, types.ErrConfiguration, false},
		{"BranchBoth with no decision", func() error { _, err := BranchBoth(ctx, nil, stepOK, stepOK); return err },
			"branch_both: configuration: there is no decision to wait for", types.KindConfiguration, types.ErrConfiguration, false},
		{"RouteAhead with no decision", func() error { _, err := RouteAhead(ctx, nil, "a", To("a", stepOK)); return err },
			"route_ahead: configuration: there is no decision to wait for", types.KindConfiguration, types.ErrConfiguration, false},
		{"RouteAhead with no routes", func() error { _, err := RouteAhead[string](ctx, decideLabel("a"), "a"); return err },
			"route_ahead: configuration: there are no routes to choose between", types.KindConfiguration, types.ErrConfiguration, false},
		{"RouteAhead with a likely label no route has", func() error {
			_, err := RouteAhead(ctx, decideLabel("a"), "z", To("a", stepOK), To("b", stepOK))
			return err
		}, "route_ahead: configuration: the likely label is not the label of any of the 2 routes", types.KindConfiguration, types.ErrConfiguration, false},
		{"RouteAhead decided on a label no route takes", func() error {
			_, err := RouteAhead(ctx, decideLabel("z"), "a", To("a", stepOK), To("b", stepOK))
			return err
		}, "select: invariant violation: the label is not the label of any of the 2 routes", types.KindInvariantViolation, types.ErrInvariantViolation, false},
		{"Each with no step", func() error { _, err := Each[int, int](ctx, []int{1}, nil); return err },
			"each: configuration: there is no step to run", types.KindConfiguration, types.ErrConfiguration, false},
		{"Gather with a step that panics", func() error {
			_, err := Gather(ctx, Step[string](func(context.Context) (string, error) { panic("secret") }))
			return err
		}, "gather: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"Gather with a step that ends its goroutine", func() error {
			_, err := Gather(ctx, Step[string](func(context.Context) (string, error) { runtime.Goexit(); return "", nil }))
			return err
		}, "gather: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"Each with a step that panics", func() error {
			_, err := Each(ctx, []int{1, 2}, func(context.Context, int) (int, error) { panic("secret") })
			return err
		}, "each: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"BranchAhead with a decision that panics", func() error {
			_, err := BranchAhead(ctx, Step[bool](func(context.Context) (bool, error) { panic("secret") }), stepOK, stepOK)
			return err
		}, "branch_ahead: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"BranchAhead with the early step panicking", func() error {
			_, err := BranchAhead(ctx, decideTrue, Step[string](func(context.Context) (string, error) { panic("secret") }), stepOK)
			return err
		}, "branch_ahead: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"BranchAhead with the late step panicking", func() error {
			_, err := BranchAhead(ctx, Step[bool](func(context.Context) (bool, error) { return false, nil }), stepOK,
				Step[string](func(context.Context) (string, error) { panic("secret") }))
			return err
		}, "branch_ahead: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"BranchBoth with a branch that panics", func() error {
			_, err := BranchBoth(ctx, decideTrue, Step[string](func(context.Context) (string, error) { panic("secret") }), stepOK)
			return err
		}, "branch_both: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
		{"RouteAhead with a route that panics", func() error {
			_, err := RouteAhead(ctx, decideLabel("a"), "a",
				To("a", Step[string](func(context.Context) (string, error) { panic("secret") })), To("b", stepOK))
			return err
		}, "route_ahead: invariant violation: a step panicked or ended its goroutine without returning", types.KindInvariantViolation, types.ErrInvariantViolation, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if err.Error() != tc.text {
				t.Errorf("text = %q, want %q", err.Error(), tc.text)
			}
			if kind := types.KindOf(err); kind != tc.kind {
				t.Errorf("kind = %v, want %v", kind, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			wantNarrow := types.NarrowNone
			if tc.panicked {
				wantNarrow = types.NarrowPanic
			}
			if got := types.NarrowingOf(err); got != wantNarrow {
				t.Errorf("narrowing = %v, want %v", got, wantNarrow)
			}
		})
	}
}

// A recovered panic in a step is marked as one, so the boundary that tells a
// panic from the other invariant violations can; the kind and the sentinel do
// not move.
func TestStepPanickedIsMarkedAsAPanic(t *testing.T) {
	for _, op := range []string{"gather", "each", "branch_ahead", "branch_both", "route_ahead", "mapreduce", ""} {
		t.Run("op "+op, func(t *testing.T) {
			err := stepPanicked(op)
			if types.NarrowingOf(err) != types.NarrowPanic {
				t.Errorf("narrowing = %v, want the panic mark", types.NarrowingOf(err))
			}
			if types.KindOf(err) != types.KindInvariantViolation || !errors.Is(err, types.ErrInvariantViolation) {
				t.Errorf("kind = %v; the kind is the one it had", types.KindOf(err))
			}
			var typed *types.OperationError
			if !errors.As(err, &typed) || typed.Op != op {
				t.Errorf("operation = %+v, want %q", typed, op)
			}
		})
	}
}

// An error a caller's step returns is the error the combinator returns: not
// wrapped, not re-kinded, the same value.
func TestFlowReturnsACallersStepErrorAsItIs(t *testing.T) {
	ctx := context.Background()
	plain := errors.New("the caller's own failure")
	kinded := &types.OperationError{Kind: types.KindBudgetExceeded, Op: "theirs", Message: "spent"}
	for _, failure := range []error{plain, kinded} {
		fail := Step[string](func(context.Context) (string, error) { return "", failure })
		ok := Step[string](func(context.Context) (string, error) { return "ok", nil })
		slowOK := Step[string](func(ctx context.Context) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
				return "ok", nil
			}
		})
		decideYes := Step[bool](func(context.Context) (bool, error) { return true, nil })
		decideNo := Step[bool](func(context.Context) (bool, error) { return false, nil })
		decideFails := Step[bool](func(context.Context) (bool, error) { return false, failure })
		label := func(l string) Step[string] { return func(context.Context) (string, error) { return l, nil } }
		cases := []struct {
			name string
			run  func() error
		}{
			{"Branch, then", func() error { _, err := Branch(ctx, true, fail, ok); return err }},
			{"Branch, otherwise", func() error { _, err := Branch(ctx, false, ok, fail); return err }},
			{"Select", func() error { _, err := Select(ctx, "a", To("a", fail), To("b", ok)); return err }},
			{"Gather", func() error { _, err := Gather(ctx, slowOK, fail); return err }},
			{"Gather2", func() error {
				_, _, err := Gather2(ctx, slowOK, Step[int](func(context.Context) (int, error) { return 0, failure }))
				return err
			}},
			{"Gather3", func() error {
				_, _, _, err := Gather3(ctx, slowOK, fail, Step[int](func(context.Context) (int, error) { return 0, nil }))
				return err
			}},
			{"Gather4", func() error {
				_, _, _, _, err := Gather4(ctx, slowOK, slowOK, slowOK, fail)
				return err
			}},
			{"Each", func() error {
				_, err := Each(ctx, []int{1, 2, 3}, func(context.Context, int) (int, error) { return 0, failure })
				return err
			}},
			{"BranchAhead, yes taken", func() error { _, err := BranchAhead(ctx, decideYes, fail, ok); return err }},
			{"BranchAhead, no taken", func() error { _, err := BranchAhead(ctx, decideNo, ok, fail); return err }},
			{"BranchAhead, decision fails", func() error { _, err := BranchAhead(ctx, decideFails, ok, ok); return err }},
			{"BranchBoth, yes taken", func() error { _, err := BranchBoth(ctx, decideYes, fail, ok); return err }},
			{"BranchBoth, no taken", func() error { _, err := BranchBoth(ctx, decideNo, ok, fail); return err }},
			{"BranchBoth, decision fails", func() error { _, err := BranchBoth(ctx, decideFails, ok, ok); return err }},
			{"RouteAhead, likely taken", func() error {
				_, err := RouteAhead(ctx, label("a"), "a", To("a", fail), To("b", ok))
				return err
			}},
			{"RouteAhead, another taken", func() error {
				_, err := RouteAhead(ctx, label("b"), "a", To("a", ok), To("b", fail))
				return err
			}},
			{"RouteAhead, decision fails", func() error {
				_, err := RouteAhead(ctx, Step[string](func(context.Context) (string, error) { return "", failure }), "a", To("a", ok), To("b", ok))
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.run(); err != failure {
					t.Errorf("err = %#v, want the very value the step returned", err)
				}
			})
		}
	}
}

// A line written by a step that panicked goes where the call's own context
// says, and where there is no snapshot, to the process logger.
func TestRunGuardedWritesToTheCallsLogger(t *testing.T) {
	panics := Step[string](func(context.Context) (string, error) { panic("secret") })
	exits := Step[string](func(context.Context) (string, error) { runtime.Goexit(); return "", nil })
	cases := []struct {
		name string
		step Step[string]
		line string
	}{
		{"a step that panics", panics, "A step panicked"},
		{"a step that ends its goroutine", exits, "A step ended its goroutine without returning"},
	}
	for _, tc := range cases {
		t.Run(tc.name+", with a snapshot", func(t *testing.T) {
			ctx, snapshot, process := controlFlowSnapshotLog(t)
			if _, err := Gather(ctx, tc.step); err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(snapshot.String(), tc.line) {
				t.Errorf("the snapshot's logger got %q, want a line %q", snapshot.String(), tc.line)
			}
			if got := process(); strings.Contains(got, tc.line) {
				t.Errorf("the process logger got the line: %q", got)
			}
			if strings.Contains(snapshot.String(), "secret") {
				t.Errorf("the panic's value is in the log: %q", snapshot.String())
			}
		})
		t.Run(tc.name+", with no snapshot", func(t *testing.T) {
			process := captureLogs(t)
			if _, err := Gather(context.Background(), tc.step); err == nil {
				t.Fatal("no error")
			}
			if got := process(); !strings.Contains(got, tc.line) {
				t.Errorf("the process logger got %q, want a line %q", got, tc.line)
			}
		})
	}
}

// When the decision rules a branch out, the branch started early is cancelled
// and the other is started at once, without waiting for the cancelled one to
// return, so for a moment both are in flight. The one that lost is still waited
// for before the call returns.
func TestSpeculativeFormsStartTheOtherBranchBeforeTheCancelledOneReturns(t *testing.T) {
	type outcome struct {
		value string
		err   error
	}
	// stubborn is a step that does not look at its context: it holds until it is
	// released, so that it is a cancelled branch that has not returned.
	run := func(t *testing.T, start func(loserStarted <-chan struct{}, loser, winner Step[string]) (string, error)) {
		t.Helper()
		var loserReturned, winnerSawLoserInFlight atomic.Bool
		loserStarted := make(chan struct{})
		winnerRan := make(chan struct{})
		release := make(chan struct{})
		loser := Step[string](func(context.Context) (string, error) {
			close(loserStarted)
			<-release
			loserReturned.Store(true)
			return "loser", nil
		})
		winner := Step[string](func(context.Context) (string, error) {
			winnerSawLoserInFlight.Store(!loserReturned.Load())
			close(winnerRan)
			return "winner", nil
		})
		done := make(chan outcome, 1)
		go func() {
			value, err := start(loserStarted, loser, winner)
			done <- outcome{value, err}
		}()
		select {
		case <-winnerRan:
		case <-time.After(5 * time.Second):
			t.Fatal("the other branch was not started while the cancelled one was still running")
		}
		if !winnerSawLoserInFlight.Load() {
			t.Error("the other branch was started after the cancelled one had returned")
		}
		select {
		case got := <-done:
			t.Fatalf("the call returned (%v) while the cancelled branch was still running", got)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		select {
		case got := <-done:
			if got.err != nil || got.value != "winner" {
				t.Errorf("got (%q, %v), want the other branch's value", got.value, got.err)
			}
			if !loserReturned.Load() {
				t.Error("the call returned before the cancelled branch had")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the call did not return after the cancelled branch did")
		}
	}

	t.Run("BranchAhead", func(t *testing.T) {
		run(t, func(loserStarted <-chan struct{}, loser, winner Step[string]) (string, error) {
			return BranchAhead(context.Background(), func(context.Context) (bool, error) {
				<-loserStarted
				return false, nil
			}, loser, winner)
		})
	})
	t.Run("RouteAhead", func(t *testing.T) {
		run(t, func(loserStarted <-chan struct{}, loser, winner Step[string]) (string, error) {
			return RouteAhead(context.Background(), func(context.Context) (string, error) {
				<-loserStarted
				return "b", nil
			}, "a", To("a", loser), To("b", winner))
		})
	})
	t.Run("BranchBoth, both in flight from the start, the dropped one still waited for", func(t *testing.T) {
		var droppedReturned atomic.Bool
		droppedStarted := make(chan struct{})
		release := make(chan struct{})
		dropped := Step[string](func(context.Context) (string, error) {
			close(droppedStarted)
			<-release
			droppedReturned.Store(true)
			return "dropped", nil
		})
		kept := Step[string](func(context.Context) (string, error) { return "kept", nil })
		done := make(chan string, 1)
		go func() {
			value, err := BranchBoth(context.Background(), func(context.Context) (bool, error) {
				<-droppedStarted
				return true, nil
			}, kept, dropped)
			if err != nil {
				value = err.Error()
			}
			done <- value
		}()
		select {
		case <-droppedStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("the dropped branch was not started")
		}
		select {
		case got := <-done:
			t.Fatalf("the call returned (%q) while the dropped branch was still running", got)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		select {
		case got := <-done:
			if got != "kept" || !droppedReturned.Load() {
				t.Errorf("got %q, dropped returned %v; want the kept value after the dropped branch returned", got, droppedReturned.Load())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the call did not return")
		}
	})
}

func TestRouteFaultNamesWhatIsWrongWithRoutesAndNeverTheirLabels(t *testing.T) {
	run := func(context.Context) (string, error) { return "", nil }
	cases := []struct {
		name   string
		routes []Route[string]
		want   string
	}{
		{"no routes", nil, "there are no routes to choose between"},
		{"an empty list", []Route[string]{}, "there are no routes to choose between"},
		{"a blank label", []Route[string]{To("a", run), To("  ", run)}, "route 2 has no label"},
		{"an empty label", []Route[string]{To("", run)}, "route 1 has no label"},
		{"a repeated label", []Route[string]{To("a", run), To("b", run), To("a", run)}, "route 3 repeats the label of an earlier one"},
		{"a label repeated with space around it", []Route[string]{To("secret-label", run), To(" secret-label ", run)}, "route 2 repeats the label of an earlier one"},
		{"a route with nothing to run", []Route[string]{To("a", run), To[string]("b", nil)}, "route 2 has nothing to run"},
		{"the first fault is the one named", []Route[string]{To[string]("a", nil), To("a", run)}, "route 1 has nothing to run"},
		{"one good route", []Route[string]{To("a", run)}, ""},
		{"two good routes", []Route[string]{To("a", run), To("b", run)}, ""},
		{"labels that differ in case", []Route[string]{To("a", run), To("A", run)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RouteFault(tc.routes); got != tc.want {
				t.Errorf("RouteFault = %q, want %q", got, tc.want)
			}
			err := checkRoutes("test", tc.routes)
			if (err == nil) != (tc.want == "") {
				t.Errorf("checkRoutes = %v for a fault of %q", err, tc.want)
			}
			if err != nil && (types.KindOf(err) != types.KindConfiguration || strings.Contains(err.Error(), "secret-label")) {
				t.Errorf("checkRoutes = %v, want a configuration error that does not carry a label", err)
			}
		})
	}
}
