package flow_test

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

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/flow"
)

// The speculative combinators run a branch beside the decision. Their tests say
// what was in flight at each moment by blocking on the decision and looking at what
// had started, and say how long a call took on the bubble's clock.

func TestBranchAheadStartsYesBesideTheDecisionAndWaitsOnceWhenTheDecisionSaysYes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		var got string
		var err error
		elapsed := took(func() {
			got, err = flow.BranchAhead(context.Background(), l.decide(60*time.Millisecond, true, nil),
				l.step("yes", 100*time.Millisecond, "yes", nil), l.step("no", 100*time.Millisecond, "no", nil))
		})
		if err != nil || got != "yes" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if elapsed != 100*time.Millisecond {
			t.Errorf("took %v, want 100ms: yes was running while the decision was made, so the wait is the longer of the two and not their sum", elapsed)
		}
		if l.seen("no started") {
			t.Error("no was started although the decision said yes")
		}
		l.before(t, "yes started", "decide answered")
	})
}

func TestBranchAheadRunsYesAndTheDecisionAtOnceFromTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		answer := make(chan bool)
		decide := func(ctx context.Context) (bool, error) {
			l.note("decide started")
			return <-answer, nil
		}
		done := make(chan string)
		go func() {
			got, _ := flow.BranchAhead(context.Background(), decide, l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
			done <- got
		}()
		synctest.Wait()
		if !l.seen("decide started") || !l.seen("yes started") || l.seen("no started") {
			t.Errorf("with the decision still out: %v; want the decision and yes started and no not", l.names())
		}
		answer <- true
		if got := <-done; got != "yes" {
			t.Errorf("BranchAhead = %q", got)
		}
	})
}

func TestBranchAheadCancelsYesAndRunsNoWhenTheDecisionSaysNo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		var got string
		var err error
		elapsed := took(func() {
			got, err = flow.BranchAhead(context.Background(), l.decide(50*time.Millisecond, false, nil),
				l.step("yes", time.Hour, "yes", nil), l.step("no", 10*time.Millisecond, "no", nil))
		})
		if err != nil || got != "no" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if elapsed != 60*time.Millisecond {
			t.Errorf("took %v, want 60ms: the decision at 50ms and then no", elapsed)
		}
		l.at(t, "no started", 50*time.Millisecond)
		l.at(t, "yes cancelled", 50*time.Millisecond)
		l.at(t, "no finished", 60*time.Millisecond)
	})
}

func TestBranchAheadStartsNoWithoutWaitingForYesToReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		slowToNotice := func(ctx context.Context) (string, error) {
			l.note("yes started")
			<-ctx.Done()
			time.Sleep(3 * time.Second)
			l.note("yes returned")
			return "yes", ctx.Err()
		}
		var got string
		elapsed := took(func() {
			got, _ = flow.BranchAhead(context.Background(), l.decide(time.Second, false, nil), slowToNotice, l.step("no", time.Second, "no", nil))
		})
		if got != "no" {
			t.Errorf("BranchAhead = %q", got)
		}
		l.at(t, "no started", time.Second)
		l.at(t, "no finished", 2*time.Second)
		l.at(t, "yes returned", 4*time.Second)
		if elapsed != 4*time.Second {
			t.Errorf("took %v: no ran while yes was still stopping, and the call waited for yes before it returned, at 4s", elapsed)
		}
	})
}

func TestBranchAheadReturnsTheDecisionsErrorAsItWasGivenAndNeverStartsNo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		got, err := flow.BranchAhead(context.Background(), l.decide(10*time.Millisecond, true, errStep),
			l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
		if err != errStep || got != "" {
			t.Errorf("BranchAhead = %q, %v; want the decision's error as it was given and nothing beside it", got, err)
		}
		if !l.seen("yes cancelled") || l.seen("no started") {
			t.Errorf("events: %v; want yes cancelled and no never started", l.names())
		}
		leaks()
	})
}

func TestBranchAheadReturnsYesErrorAsItWasGivenWhenTheDecisionSaysYes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		got, err := flow.BranchAhead(context.Background(), l.decide(50*time.Millisecond, true, nil),
			l.step("yes", 10*time.Millisecond, "yes", errStep), l.step("no", time.Millisecond, "no", nil))
		if err != errStep || got != "" {
			t.Errorf("BranchAhead = %q, %v; want yes's error as it was given", got, err)
		}
		if l.seen("no started") {
			t.Error("no started")
		}
	})
}

func TestBranchAheadDiscardsYesErrorWhenTheDecisionSaysNo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		got, err := flow.BranchAhead(context.Background(), l.decide(50*time.Millisecond, false, nil),
			l.step("yes", 10*time.Millisecond, "yes", errStep), l.step("no", time.Millisecond, "no", nil))
		if err != nil || got != "no" {
			t.Errorf("BranchAhead = %q, %v; want no's value and no error: yes failed and the decision said no", got, err)
		}
	})
}

func TestBranchAheadReturnsNosErrorAsItWasGiven(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		got, err := flow.BranchAhead(context.Background(), l.decide(time.Millisecond, false, nil),
			l.step("yes", time.Hour, "yes", nil), l.step("no", time.Millisecond, "no", errStep))
		if err != errStep || got != "" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
	})
}

func TestBranchAheadRefusesANilBranchWhicheverTheDecisionWouldHaveChosenAndStartsNothing(t *testing.T) {
	var ran atomic.Int32
	cases := []struct {
		name    string
		answer  bool
		yes, no schemaflux.Step[string]
	}{
		{"yes is nil and the decision says yes", true, nil, counts(&ran, "no")},
		{"yes is nil and the decision says no", false, nil, counts(&ran, "no")},
		{"no is nil and the decision says no", false, counts(&ran, "yes"), nil},
		{"no is nil and the decision says yes", true, counts(&ran, "yes"), nil},
		{"both are nil and the decision says yes", true, nil, nil},
		{"both are nil and the decision says no", false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				got, err := flow.BranchAhead(context.Background(), func(context.Context) (bool, error) { ran.Add(1); return tc.answer, nil }, tc.yes, tc.no)
				e := wantKind(t, err, schemaflux.ErrInvalidRequest)
				if e.Op != "flow.branchahead" || got != "" || ran.Load() != 0 {
					t.Errorf("Op = %q, got = %q, steps run = %d; want a refusal and nothing started", e.Op, got, ran.Load())
				}
				leaks()
			})
		})
	}
}

func TestBranchAheadRefusesANilDecisionAndStartsNothing(t *testing.T) {
	var ran atomic.Int32
	got, err := flow.BranchAhead[string](context.Background(), nil, counts(&ran, "yes"), counts(&ran, "no"))
	e := wantKind(t, err, schemaflux.ErrInvalidRequest)
	if e.Op != "flow.branchahead" || got != "" || ran.Load() != 0 {
		t.Errorf("Op = %q, got = %q, steps run = %d", e.Op, got, ran.Load())
	}
}

func TestBranchAheadRefusesANilContextAndAContextThatIsOverAndStartsNothing(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	_, err := flow.BranchAhead(nilContext, returns(true), counts(&ran, "yes"), counts(&ran, "no"))
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		got, err := flow.BranchAhead(done.ctx, returns(true), counts(&ran, "yes"), counts(&ran, "no"))
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || got != "" {
			t.Errorf("%s: err = %v, got = %q", done.name, err, got)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestBranchAheadStopsBothAndReturnsTheContextsErrorWhenTheCallerCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error)
		go func() {
			_, err := flow.BranchAhead(ctx, l.decide(time.Hour, true, nil), l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
			result <- err
		}()
		synctest.Wait()
		cancel()
		err := <-result
		wantKind(t, err, schemaflux.ErrCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) is false for %v", err)
		}
		if !l.seen("decide cancelled") || !l.seen("yes cancelled") || l.seen("no started") {
			t.Errorf("events: %v", l.names())
		}
	})
}

func TestBranchAheadReturnsATimeoutWhenTheCallersDeadlinePasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		var err error
		elapsed := took(func() {
			_, err = flow.BranchAhead(ctx, l.decide(time.Hour, true, nil), l.step("yes", time.Hour, "yes", nil), returns("no"))
		})
		wantKind(t, err, schemaflux.ErrTimeout)
		if !errors.Is(err, context.DeadlineExceeded) || elapsed != 40*time.Millisecond {
			t.Errorf("err = %v after %v", err, elapsed)
		}
	})
}

func TestBranchAheadReturnsTheContextsErrorEvenWhenAStepFailedBecauseOfIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failed := errors.New("failed when the context went")
		yes := func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", failed
		}
		decide := func(ctx context.Context) (bool, error) {
			<-ctx.Done()
			return true, nil
		}
		result := make(chan error)
		go func() {
			_, err := flow.BranchAhead(ctx, decide, yes, returns("no"))
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, schemaflux.ErrCanceled) || err == failed {
			t.Errorf("err = %v; a caller whose context is done gets the context's error and never an answer", err)
		}
	})
}

func TestBranchAheadTurnsAPanicInTheDecisionIntoAnInternalErrorAndCancelsYes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const canary = "panic-payload-4471"
		l := newEventLog()
		leaks := noLeak(t)
		got, err := flow.BranchAhead(context.Background(), panics[bool](canary), l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
		e := wantKind(t, err, schemaflux.ErrInternal)
		if got != "" || !l.seen("yes cancelled") || l.seen("no started") {
			t.Errorf("got = %q, events: %v", got, l.names())
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
			t.Errorf("the panic's value is in the error: %v", err)
		}
		leaks()
	})
}

func TestBranchAheadTurnsAPanicInEitherBranchIntoAnInternalError(t *testing.T) {
	cases := []struct {
		name    string
		answer  bool
		yes, no schemaflux.Step[string]
	}{
		{"yes panics and the decision says yes", true, panics[string]("x"), returns("no")},
		{"no panics and the decision says no", false, wait(time.Hour, "yes"), panics[string]("x")},
		{"yes ends its goroutine and the decision says yes", true, goexit[string], returns("no")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				got, err := flow.BranchAhead(context.Background(), returns(tc.answer), tc.yes, tc.no)
				wantKind(t, err, schemaflux.ErrInternal)
				if got != "" {
					t.Errorf("got = %q", got)
				}
				leaks()
			})
		})
	}
}

func TestBranchAheadDiscardsAPanicInYesWhenTheDecisionSaysNo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		panicsAtOnce := func(context.Context) (string, error) { panic("yes was a bad idea") }
		got, err := flow.BranchAhead(context.Background(), wait(10*time.Millisecond, false), panicsAtOnce, returns("no"))
		if err != nil || got != "no" {
			t.Errorf("BranchAhead = %q, %v; yes was not taken and its failure is discarded", got, err)
		}
	})
}

func TestBranchAheadLetsTheBranchThatLostCompleteItsSideEffectBeforeItReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		got, err := flow.BranchAhead(context.Background(), wait(10*time.Millisecond, false), l.writer("yes", time.Hour, "yes"), l.step("no", time.Millisecond, "no", nil))
		if err != nil || got != "no" {
			t.Fatalf("BranchAhead = %q, %v", got, err)
		}
		if !l.seen("yes wrote") {
			t.Errorf("events: %v; cancelling is a request, so the branch that lost may still write, and it has by the time the call returns", l.names())
		}
		l.before(t, "yes saw the cancellation", "yes wrote")
	})
}

func TestBranchAheadWaitsForAYesThatIgnoresItsContextBeforeItReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		var got string
		elapsed := took(func() {
			got, _ = flow.BranchAhead(context.Background(), wait(time.Millisecond, false), l.stubborn("yes", 5*time.Second, "yes"), returns("no"))
		})
		if got != "no" || elapsed != 5*time.Second {
			t.Errorf("BranchAhead = %q after %v; the call waits for a step that ignores its context, 5s", got, elapsed)
		}
		if !l.seen("yes finished") {
			t.Error("yes had not finished when the call returned")
		}
		leaks()
	})
}

func TestBranchAheadLeavesNoGoroutineBehind(t *testing.T) {
	cases := []struct {
		name    string
		decide  schemaflux.Step[bool]
		yes, no schemaflux.Step[string]
	}{
		{"yes taken", wait(time.Second, true), wait(2*time.Second, "yes"), returns("no")},
		{"no taken", wait(time.Second, false), wait(time.Hour, "yes"), wait(time.Second, "no")},
		{"decision fails", fails[bool](errStep), wait(time.Hour, "yes"), returns("no")},
		{"yes fails first", wait(time.Second, true), fails[string](errStep), returns("no")},
		{"decision panics", panics[bool]("x"), wait(time.Hour, "yes"), returns("no")},
		{"yes ignores cancellation", wait(time.Millisecond, false), stubborn(time.Minute, "yes"), returns("no")},
		{"nothing to run", returns(true), nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				_, _ = flow.BranchAhead(context.Background(), tc.decide, tc.yes, tc.no)
				leaks()
			})
		})
	}
}

func TestBranchAheadHandsEveryStepTheCallersValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.WithValue(context.Background(), contextKey{}, "mine")
		read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(contextKey{})), nil }
		decide := func(ctx context.Context) (bool, error) { return fmt.Sprint(ctx.Value(contextKey{})) == "mine", nil }
		got, err := flow.BranchAhead(ctx, decide, read, read)
		if err != nil || got != "mine" {
			t.Errorf("BranchAhead = %q, %v", got, err)
		}
	})
}

func TestBranchAheadReturnsTheErrorTheStepGaveWhateverItsType(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "extract"}
	for _, failure := range []error{errStep, fmt.Errorf("wrapped: %w", errStep), typed, context.DeadlineExceeded} {
		synctest.Test(t, func(t *testing.T) {
			_, err := flow.BranchAhead(context.Background(), returns(true), fails[string](failure), returns("no"))
			if err != failure {
				t.Errorf("yes: err = %v (%T), want the very value %v", err, err, failure)
			}
			_, err = flow.BranchAhead[string](context.Background(), fails[bool](failure), returns("yes"), returns("no"))
			if err != failure {
				t.Errorf("decision: err = %v (%T), want the very value %v", err, err, failure)
			}
		})
	}
}

func TestBranchBothStartsBothBranchesAndTheDecisionAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		answer := make(chan bool)
		decide := func(ctx context.Context) (bool, error) {
			l.note("decide started")
			return <-answer, nil
		}
		done := make(chan string)
		go func() {
			got, _ := flow.BranchBoth(context.Background(), decide, l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
			done <- got
		}()
		synctest.Wait()
		for _, name := range []string{"decide started", "yes started", "no started"} {
			if !l.seen(name) {
				t.Errorf("%q has not happened with the decision still out: %v", name, l.names())
			}
		}
		answer <- false
		if got := <-done; got != "no" {
			t.Errorf("BranchBoth = %q", got)
		}
	})
}

func TestBranchBothKeepsTheBranchTheDecisionPicksInEitherDirectionAndCancelsTheOther(t *testing.T) {
	cases := []struct {
		name      string
		answer    bool
		want      string
		cancelled string
	}{
		{"yes", true, "yes", "no cancelled"},
		{"no", false, "no", "yes cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newEventLog()
				var got string
				var err error
				elapsed := took(func() {
					got, err = flow.BranchBoth(context.Background(), l.decide(50*time.Millisecond, tc.answer, nil),
						l.step("yes", 80*time.Millisecond, "yes", nil), l.step("no", 80*time.Millisecond, "no", nil))
				})
				if err != nil || got != tc.want {
					t.Fatalf("BranchBoth = %q, %v", got, err)
				}
				if elapsed != 80*time.Millisecond {
					t.Errorf("took %v, want 80ms: the branch was running while the decision was made", elapsed)
				}
				l.at(t, tc.cancelled, 50*time.Millisecond)
			})
		})
	}
}

func TestBranchBothCancelsTheLoserWhenTheDecisionAnswersNotWhenTheWinnerFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		_, _ = flow.BranchBoth(context.Background(), l.decide(10*time.Millisecond, true, nil),
			l.step("yes", time.Second, "yes", nil), l.step("no", time.Hour, "no", nil))
		l.at(t, "no cancelled", 10*time.Millisecond)
		l.at(t, "yes finished", time.Second)
	})
}

func TestBranchBothReturnsTheDecisionsErrorAsItWasGivenAndCancelsBoth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		got, err := flow.BranchBoth(context.Background(), l.decide(10*time.Millisecond, true, errStep),
			l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
		if err != errStep || got != "" {
			t.Errorf("BranchBoth = %q, %v", got, err)
		}
		if !l.seen("yes cancelled") || !l.seen("no cancelled") {
			t.Errorf("events: %v; want both branches cancelled", l.names())
		}
		leaks()
	})
}

func TestBranchBothReturnsTheErrorOfTheBranchItKeptAndDiscardsTheOthers(t *testing.T) {
	cases := []struct {
		name    string
		answer  bool
		yes, no schemaflux.Step[string]
		want    string
		wantErr error
	}{
		{"the kept yes fails", true, fails[string](errStep), returns("no"), "", errStep},
		{"the kept no fails", false, returns("yes"), fails[string](errStep), "", errStep},
		{"the dropped no fails", true, returns("yes"), fails[string](errStep), "yes", nil},
		{"the dropped yes fails", false, fails[string](errStep), returns("no"), "no", nil},
		{"the dropped no panics", true, returns("yes"), panics[string]("x"), "yes", nil},
		{"the dropped yes panics", false, panics[string]("x"), returns("no"), "no", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got, err := flow.BranchBoth(context.Background(), wait(10*time.Millisecond, tc.answer), tc.yes, tc.no)
				if err != tc.wantErr || got != tc.want {
					t.Errorf("BranchBoth = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
				}
			})
		})
	}
}

func TestBranchBothLetsTheBranchThatLostCompleteItsSideEffect(t *testing.T) {
	for _, answer := range []bool{true, false} {
		t.Run(fmt.Sprint("decision ", answer), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newEventLog()
				yes, no := l.step("yes", time.Millisecond, "yes", nil), l.step("no", time.Millisecond, "no", nil)
				lost := "no"
				if answer {
					no = l.writer("no", time.Hour, "no")
				} else {
					yes = l.writer("yes", time.Hour, "yes")
					lost = "yes"
				}
				_, err := flow.BranchBoth(context.Background(), wait(10*time.Millisecond, answer), yes, no)
				if err != nil {
					t.Fatal(err)
				}
				if !l.seen(lost + " wrote") {
					t.Errorf("events: %v; the losing branch's write completes before the call returns", l.names())
				}
			})
		})
	}
}

func TestBranchBothRefusesANilBranchOrANilDecisionWhicheverWouldHaveBeenChosenAndStartsNothing(t *testing.T) {
	var ran atomic.Int32
	cases := []struct {
		name    string
		decide  schemaflux.Step[bool]
		yes, no schemaflux.Step[string]
	}{
		{"a nil yes that is chosen", returns(true), nil, counts(&ran, "no")},
		{"a nil yes that is not chosen", returns(false), nil, counts(&ran, "no")},
		{"a nil no that is chosen", returns(false), counts(&ran, "yes"), nil},
		{"a nil no that is not chosen", returns(true), counts(&ran, "yes"), nil},
		{"both nil", returns(true), nil, nil},
		{"a nil decision", nil, counts(&ran, "a"), counts(&ran, "b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := flow.BranchBoth(context.Background(), tc.decide, tc.yes, tc.no)
			e := wantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "flow.branchboth" || got != "" || ran.Load() != 0 {
				t.Errorf("Op = %q, got = %q, steps run = %d", e.Op, got, ran.Load())
			}
		})
	}
}

func TestBranchBothRefusesANilContextAndAContextThatIsOverAndStartsNothing(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	_, err := flow.BranchBoth(nilContext, returns(true), counts(&ran, "yes"), counts(&ran, "no"))
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		_, err := flow.BranchBoth(done.ctx, returns(true), counts(&ran, "yes"), counts(&ran, "no"))
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) {
			t.Errorf("%s: errors.Is(err, %v) is false", done.name, done.is)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestBranchBothStopsBothWhenTheCallerCancelsAndReturnsTheContextsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error)
		go func() {
			_, err := flow.BranchBoth(ctx, l.decide(time.Hour, true, nil), l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
			result <- err
		}()
		synctest.Wait()
		cancel()
		err := <-result
		wantKind(t, err, schemaflux.ErrCanceled)
		for _, name := range []string{"decide cancelled", "yes cancelled", "no cancelled"} {
			if !l.seen(name) {
				t.Errorf("%q did not happen: %v", name, l.names())
			}
		}
	})
}

func TestBranchBothTurnsAPanicOrAnEndedGoroutineIntoAnInternalError(t *testing.T) {
	cases := []struct {
		name    string
		decide  schemaflux.Step[bool]
		yes, no schemaflux.Step[string]
	}{
		{"the decision panics", panics[bool]("x"), wait(time.Hour, "y"), wait(time.Hour, "n")},
		{"the kept branch panics", returns(true), panics[string]("x"), wait(time.Hour, "n")},
		{"the kept branch ends its goroutine", returns(false), wait(time.Hour, "y"), goexit[string]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				got, err := flow.BranchBoth(context.Background(), tc.decide, tc.yes, tc.no)
				wantKind(t, err, schemaflux.ErrInternal)
				if got != "" {
					t.Errorf("got = %q", got)
				}
				leaks()
			})
		})
	}
}

func TestBranchBothWaitsForABranchThatIgnoresItsContextAndLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		var got string
		elapsed := took(func() {
			got, _ = flow.BranchBoth(context.Background(), wait(time.Millisecond, true), returns("yes"), l.stubborn("no", 7*time.Second, "no"))
		})
		if got != "yes" || elapsed != 7*time.Second {
			t.Errorf("BranchBoth = %q after %v; the call waits for the branch it dropped, 7s", got, elapsed)
		}
		leaks()
	})
}

func TestBranchBothReturnsTheErrorTheStepGaveWhateverItsType(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "extract"}
	for _, failure := range []error{errStep, fmt.Errorf("wrapped: %w", errStep), typed} {
		synctest.Test(t, func(t *testing.T) {
			_, err := flow.BranchBoth(context.Background(), returns(false), returns("yes"), fails[string](failure))
			if err != failure {
				t.Errorf("err = %v (%T), want the very value %v", err, err, failure)
			}
		})
	}
}

func TestRouteAheadStartsTheLikelyRouteBesideTheDecisionAndWaitsOnceWhenItIsNamed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		var got string
		var err error
		elapsed := took(func() {
			got, err = flow.RouteAhead(context.Background(), l.pick(60*time.Millisecond, "billing", nil), "billing",
				schemaflux.To("billing", l.step("billing", 100*time.Millisecond, "to billing", nil)),
				schemaflux.To("technical", l.step("technical", 100*time.Millisecond, "to technical", nil)))
		})
		if err != nil || got != "to billing" || elapsed != 100*time.Millisecond {
			t.Errorf("RouteAhead = %q, %v after %v; want the likely route's result after 100ms", got, err, elapsed)
		}
		if l.seen("technical started") {
			t.Error("the other route started")
		}
		l.before(t, "billing started", "decide answered")
	})
}

func TestRouteAheadCancelsTheLikelyRouteAndRunsTheOneTheDecisionNames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		var got string
		var err error
		elapsed := took(func() {
			got, err = flow.RouteAhead(context.Background(), l.pick(50*time.Millisecond, "technical", nil), "billing",
				schemaflux.To("billing", l.step("billing", time.Hour, "to billing", nil)),
				schemaflux.To("technical", l.step("technical", 10*time.Millisecond, "to technical", nil)),
				schemaflux.To("shipping", l.step("shipping", time.Millisecond, "to shipping", nil)))
		})
		if err != nil || got != "to technical" || elapsed != 60*time.Millisecond {
			t.Fatalf("RouteAhead = %q, %v after %v", got, err, elapsed)
		}
		l.at(t, "technical started", 50*time.Millisecond)
		l.at(t, "billing cancelled", 50*time.Millisecond)
		if l.seen("shipping started") {
			t.Error("a route nobody named started")
		}
	})
}

func TestRouteAheadStartsTheLikelyRouteAndTheDecisionAtOnceFromTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		answer := make(chan string)
		decide := func(context.Context) (string, error) {
			l.note("decide started")
			return <-answer, nil
		}
		done := make(chan string)
		go func() {
			got, _ := flow.RouteAhead(context.Background(), decide, "a",
				schemaflux.To("a", l.step("a", time.Hour, "to a", nil)), schemaflux.To("b", l.step("b", time.Hour, "to b", nil)))
			done <- got
		}()
		synctest.Wait()
		if !l.seen("decide started") || !l.seen("a started") || l.seen("b started") {
			t.Errorf("with the decision still out: %v", l.names())
		}
		answer <- "a"
		<-done
	})
}

func TestRouteAheadRefusesALabelTheDecisionNamesThatIsNoRoutesAndCancelsTheLikelyOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const canary = "label-4471-canary"
		l := newEventLog()
		leaks := noLeak(t)
		got, err := flow.RouteAhead(context.Background(), l.pick(10*time.Millisecond, canary, nil), "a",
			schemaflux.To("a", l.step("a", time.Hour, "to a", nil)), schemaflux.To("b", l.step("b", time.Hour, "to b", nil)))
		e := wantKind(t, err, schemaflux.ErrInvalidRequest)
		if got != "" || e.Op != "flow.routeahead" {
			t.Errorf("got = %q, Op = %q", got, e.Op)
		}
		if !l.seen("a cancelled") || l.seen("b started") {
			t.Errorf("events: %v", l.names())
		}
		if strings.Contains(err.Error(), canary) {
			t.Errorf("the label is in the error: %v", err)
		}
		leaks()
	})
}

func TestRouteAheadRefusesRoutesItCannotChooseBetweenBeforeStartingAnything(t *testing.T) {
	var ran atomic.Int32
	run := counts(&ran, "x")
	pick := func(context.Context) (string, error) {
		ran.Add(100)
		return "a", nil
	}
	cases := []struct {
		name   string
		likely string
		routes []schemaflux.Route[string]
	}{
		{"no routes", "a", nil},
		{"a likely label that is no route's", "z", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("b", run)}},
		{"a blank label", "a", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("", run)}},
		{"a repeated label", "a", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("a", run)}},
		{"a label repeated with space around it", "a", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To(" a", run)}},
		{"a route with nothing to run", "a", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To[string]("b", nil)}},
		{"a likely route with nothing to run", "a", []schemaflux.Route[string]{schemaflux.To[string]("a", nil), schemaflux.To("b", run)}},
		{"a likely label with space around it", " a", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("b", run)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := flow.RouteAhead(context.Background(), pick, tc.likely, tc.routes...)
			wantKind(t, err, schemaflux.ErrInvalidRequest)
			if got != "" || ran.Load() != 0 {
				t.Errorf("got = %q, and %d was run", got, ran.Load())
			}
		})
	}
}

func TestRouteAheadRefusesANilDecisionANilContextAndAContextThatIsOver(t *testing.T) {
	var ran atomic.Int32
	routes := []schemaflux.Route[string]{schemaflux.To("a", counts(&ran, "a")), schemaflux.To("b", counts(&ran, "b"))}
	_, err := flow.RouteAhead(context.Background(), nil, "a", routes...)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	var nilContext context.Context
	_, err = flow.RouteAhead(nilContext, returns("a"), "a", routes...)
	wantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range doneContexts() {
		_, err := flow.RouteAhead(done.ctx, returns("a"), "a", routes...)
		wantKind(t, err, done.kind)
		if !errors.Is(err, done.is) {
			t.Errorf("%s: errors.Is(err, %v) is false", done.name, done.is)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d routes ran", ran.Load())
	}
}

func TestRouteAheadReturnsTheDecisionsErrorAsItWasGivenAndCancelsTheLikelyRoute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		got, err := flow.RouteAhead(context.Background(), l.pick(10*time.Millisecond, "", errStep), "a",
			schemaflux.To("a", l.step("a", time.Hour, "to a", nil)), schemaflux.To("b", l.step("b", time.Hour, "to b", nil)))
		if err != errStep || got != "" || !l.seen("a cancelled") || l.seen("b started") {
			t.Errorf("RouteAhead = %q, %v; events %v", got, err, l.names())
		}
		leaks()
	})
}

func TestRouteAheadReturnsTheErrorOfTheRouteItKeptAndDiscardsTheLikelyOnesOtherwise(t *testing.T) {
	cases := []struct {
		name    string
		named   string
		likely  schemaflux.Step[string]
		other   schemaflux.Step[string]
		want    string
		wantErr error
	}{
		{"the likely route is named and fails", "a", fails[string](errStep), returns("b"), "", errStep},
		{"the other route is named and fails", "b", returns("a"), fails[string](errStep), "", errStep},
		{"the likely route fails and another is named", "b", fails[string](errStep), returns("to b"), "to b", nil},
		{"the likely route panics and another is named", "b", panics[string]("x"), returns("to b"), "to b", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got, err := flow.RouteAhead(context.Background(), wait(10*time.Millisecond, tc.named), "a",
					schemaflux.To("a", tc.likely), schemaflux.To("b", tc.other))
				if err != tc.wantErr || got != tc.want {
					t.Errorf("RouteAhead = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
				}
			})
		})
	}
}

func TestRouteAheadStopsEverythingWhenTheCallerCancelsAndReturnsTheContextsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error)
		go func() {
			_, err := flow.RouteAhead(ctx, l.pick(time.Hour, "b", nil), "a",
				schemaflux.To("a", l.step("a", time.Hour, "to a", nil)), schemaflux.To("b", l.step("b", time.Hour, "to b", nil)))
			result <- err
		}()
		synctest.Wait()
		cancel()
		err := <-result
		wantKind(t, err, schemaflux.ErrCanceled)
		if !errors.Is(err, context.Canceled) || !l.seen("a cancelled") || !l.seen("decide cancelled") || l.seen("b started") {
			t.Errorf("err = %v, events %v", err, l.names())
		}
	})
}

func TestRouteAheadTurnsAPanicInTheDecisionOrARouteIntoAnInternalError(t *testing.T) {
	cases := []struct {
		name   string
		decide schemaflux.Step[string]
		a, b   schemaflux.Step[string]
	}{
		{"the decision panics", panics[string]("x"), wait(time.Hour, "a"), wait(time.Hour, "b")},
		{"the likely route panics and is named", returns("a"), panics[string]("x"), returns("b")},
		{"the named route panics", returns("b"), wait(time.Hour, "a"), panics[string]("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := noLeak(t)
				got, err := flow.RouteAhead(context.Background(), tc.decide, "a", schemaflux.To("a", tc.a), schemaflux.To("b", tc.b))
				wantKind(t, err, schemaflux.ErrInternal)
				if got != "" {
					t.Errorf("got = %q", got)
				}
				leaks()
			})
		})
	}
}

func TestRouteAheadLetsTheLikelyRouteCompleteItsSideEffectWhenAnotherIsNamed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		got, err := flow.RouteAhead(context.Background(), wait(10*time.Millisecond, "b"), "a",
			schemaflux.To("a", l.writer("a", time.Hour, "to a")), schemaflux.To("b", returns("to b")))
		if err != nil || got != "to b" || !l.seen("a wrote") {
			t.Errorf("RouteAhead = %q, %v, events %v; the likely route writes before the call returns", got, err, l.names())
		}
	})
}

func TestRouteAheadWaitsForALikelyRouteThatIgnoresItsContextAndLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newEventLog()
		leaks := noLeak(t)
		var got string
		elapsed := took(func() {
			got, _ = flow.RouteAhead(context.Background(), wait(time.Millisecond, "b"), "a",
				schemaflux.To("a", l.stubborn("a", 4*time.Second, "to a")), schemaflux.To("b", returns("to b")))
		})
		if got != "to b" || elapsed != 4*time.Second {
			t.Errorf("RouteAhead = %q after %v; the call waits for the likely route, 4s", got, elapsed)
		}
		leaks()
	})
}

func TestRouteAheadReturnsTheErrorTheStepGaveWhateverItsType(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "extract"}
	for _, failure := range []error{errStep, fmt.Errorf("wrapped: %w", errStep), typed} {
		synctest.Test(t, func(t *testing.T) {
			_, err := flow.RouteAhead(context.Background(), returns("a"), "a", schemaflux.To("a", fails[string](failure)), schemaflux.To("b", returns("b")))
			if err != failure {
				t.Errorf("err = %v (%T), want the very value %v", err, err, failure)
			}
		})
	}
}

func TestRouteAheadHandsEveryStepTheCallersValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.WithValue(context.Background(), contextKey{}, "mine")
		read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(contextKey{})), nil }
		got, err := flow.RouteAhead(ctx, read, "mine", schemaflux.To("mine", read), schemaflux.To("other", returns("other")))
		if err != nil || got != "mine" {
			t.Errorf("RouteAhead = %q, %v", got, err)
		}
	})
}

func TestSpeculativeCombinatorsNestInsideEachOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := func(ctx context.Context) (string, error) {
			return flow.BranchBoth(ctx, returns(false), returns("yes"), fails[string](errStep))
		}
		outer := func(ctx context.Context) (string, error) {
			return flow.RouteAhead(ctx, returns("a"), "a", schemaflux.To("a", inner), schemaflux.To("b", returns("b")))
		}
		_, err := flow.BranchAhead(context.Background(), returns(true), outer, returns("no"))
		if err != errStep {
			t.Errorf("err = %v, want the innermost step's error as it was given", err)
		}
	})
}

func TestSpeculativeCombinatorsUnderAParentThatStopsStillLeaveNothingRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := noLeak(t)
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() {
			_, _ = flow.BranchBoth(ctx, wait(time.Hour, true), wait(time.Hour, "y"), wait(time.Hour, "n"))
		})
		wg.Go(func() {
			_, _ = flow.BranchAhead(ctx, wait(time.Hour, true), wait(time.Hour, "y"), wait(time.Hour, "n"))
		})
		wg.Go(func() {
			_, _ = flow.RouteAhead(ctx, wait(time.Hour, "a"), "a", schemaflux.To("a", wait(time.Hour, "y")), schemaflux.To("b", wait(time.Hour, "n")))
		})
		synctest.Wait()
		cancel()
		wg.Wait()
		leaks()
	})
}

// The decision, and in BranchAhead and RouteAhead the branch that was not started
// early, run on the caller's goroutine, so one that ends it with runtime.Goexit
// ends the caller's goroutine, as it would from an ordinary call. What the
// combinator owes is that nothing is left running when it does.
func TestADecisionOrBranchThatEndsTheCallersGoroutineLeavesNothingRunning(t *testing.T) {
	cases := []struct {
		name string
		call func(ctx context.Context, l *eventLog)
	}{
		{"BranchAhead, the decision", func(ctx context.Context, l *eventLog) {
			_, _ = flow.BranchAhead(ctx, goexit[bool], l.step("yes", time.Hour, "yes", nil), returns("no"))
		}},
		{"BranchAhead, no", func(ctx context.Context, l *eventLog) {
			_, _ = flow.BranchAhead(ctx, returns(false), l.step("yes", time.Hour, "yes", nil), goexit[string])
		}},
		{"BranchBoth, the decision", func(ctx context.Context, l *eventLog) {
			_, _ = flow.BranchBoth(ctx, goexit[bool], l.step("yes", time.Hour, "yes", nil), l.step("no", time.Hour, "no", nil))
		}},
		{"RouteAhead, the decision", func(ctx context.Context, l *eventLog) {
			_, _ = flow.RouteAhead(ctx, goexit[string], "a", schemaflux.To("a", l.step("a", time.Hour, "a", nil)), schemaflux.To("b", returns("b")))
		}},
		{"RouteAhead, the named route", func(ctx context.Context, l *eventLog) {
			_, _ = flow.RouteAhead(ctx, returns("b"), "a", schemaflux.To("a", l.step("a", time.Hour, "a", nil)), schemaflux.To("b", goexit[string]))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newEventLog()
				leaks := noLeak(t)
				returned := make(chan bool, 1)
				go func() {
					defer func() { returned <- false }()
					tc.call(context.Background(), l)
					returned <- true
				}()
				if <-returned {
					t.Fatal("the call returned although a step ended the goroutine it ran on")
				}
				synctest.Wait()
				if l.seen("yes started") && !l.seen("yes cancelled") || l.seen("a started") && !l.seen("a cancelled") {
					t.Errorf("events: %v; the branch started early was left running", l.names())
				}
				leaks()
			})
		})
	}
}
