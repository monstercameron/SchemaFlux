package schemaflux_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// These tests run inside synctest bubbles. The clock in a bubble moves only when
// every goroutine in it is blocked, so a step that waits fifty milliseconds takes
// exactly that and no real time, and a test that asserts how long a call took, or
// what was in flight at a moment, cannot be disturbed by a busy machine.

// flowWait is a step that takes d on the bubble's clock and then returns value. It
// gives up when its context ends, with the context's error.
func flowWait[T any](d time.Duration, value T) schemaflux.Step[T] {
	return func(ctx context.Context) (T, error) {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return value, nil
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// flowStubborn is a step that takes d whatever happens to its context.
func flowStubborn[T any](d time.Duration, value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		time.Sleep(d)
		return value, nil
	}
}

// flowUntilCancelled is a step that waits for its context to end and returns what
// it ended with, noting that it did.
func flowUntilCancelled[T any](saw *atomic.Int32) schemaflux.Step[T] {
	return func(ctx context.Context) (T, error) {
		<-ctx.Done()
		saw.Add(1)
		var zero T
		return zero, ctx.Err()
	}
}

// flowGoexit is a step that ends its goroutine without returning, as t.FailNow
// does.
func flowGoexit[T any](context.Context) (T, error) {
	runtime.Goexit()
	panic("not reached")
}

// flowTook runs f and says how long it took on the bubble's clock.
func flowTook(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

func TestGatherReturnsTheResultsInTheOrderOfTheStepsAndTakesAsLongAsTheSlowest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		var err error
		took := flowTook(func() {
			got, err = schemaflux.Gather(context.Background(),
				flowWait(30*time.Millisecond, "slow"),
				flowWait(10*time.Millisecond, "fast"),
				flowWait(20*time.Millisecond, "middle"))
		})
		if err != nil || !reflect.DeepEqual(got, []string{"slow", "fast", "middle"}) {
			t.Fatalf("Gather = %v, %v", got, err)
		}
		if took != 30*time.Millisecond {
			t.Errorf("took %v, want 30ms: the steps run at once, so the call takes as long as the slowest", took)
		}
	})
}

func TestGatherRunsEveryStepAtOnce(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10, 200} {
		t.Run(fmt.Sprint(n, " steps"), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var started atomic.Int32
				steps := make([]schemaflux.Step[int], n)
				for i := range steps {
					steps[i] = func(context.Context) (int, error) {
						started.Add(1)
						<-release
						return i, nil
					}
				}
				done := make(chan []int)
				go func() {
					got, err := schemaflux.Gather(context.Background(), steps...)
					if err != nil {
						t.Error(err)
					}
					done <- got
				}()
				synctest.Wait()
				if started.Load() != int32(n) {
					t.Errorf("%d of %d steps were in flight while none had finished", started.Load(), n)
				}
				close(release)
				got := <-done
				for i, v := range got {
					if v != i {
						t.Fatalf("result %d is %d", i, v)
					}
				}
			})
		})
	}
}

func TestGatherReturnsTheFirstFailureAsItWasGivenAndCancelsTheOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cancelled atomic.Int32
		got, err := schemaflux.Gather(context.Background(),
			flowUntilCancelled[string](&cancelled),
			flowFails[string](errFlowStep),
			flowUntilCancelled[string](&cancelled))
		if err != errFlowStep {
			t.Errorf("err = %v, want the failing step's error as it was given", err)
		}
		if got != nil {
			t.Errorf("Gather returned %v beside an error", got)
		}
		if cancelled.Load() != 2 {
			t.Errorf("%d of the other 2 steps saw their context cancelled", cancelled.Load())
		}
	})
}

func TestGatherDoesNotReturnTheErrorsOfStepsItCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, err := schemaflux.Gather(context.Background(),
			flowWait(time.Hour, 1),
			flowWait(time.Hour, 2),
			flowFails[int](errFlowStep))
		if err != errFlowStep || errors.Is(err, context.Canceled) {
			t.Errorf("err = %v; the error of the step that failed first is the one returned, not a cancellation", err)
		}
	})
}

func TestGatherReturnsTheFirstOfTwoFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		errLater := errors.New("fails because it was cancelled")
		later := func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, errLater
		}
		_, err := schemaflux.Gather(context.Background(), later, flowFails[int](errFlowStep))
		if err != errFlowStep {
			t.Errorf("err = %v, want the failure that came first", err)
		}
	})
}

func TestGatherReturnsNothingBesideAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got, err := schemaflux.Gather(context.Background(), flowReturns("kept?"), flowFails[string](errFlowStep))
		if err == nil || got != nil {
			t.Errorf("Gather = %v, %v; want no results beside the error", got, err)
		}
	})
}

func TestGatherRefusesANilStepAndStartsNone(t *testing.T) {
	for position := range 4 {
		t.Run(fmt.Sprint("nil at ", position), func(t *testing.T) {
			var ran atomic.Int32
			steps := []schemaflux.Step[int]{flowCounts(&ran, 1), flowCounts(&ran, 2), flowCounts(&ran, 3), flowCounts(&ran, 4)}
			steps[position] = nil
			got, err := schemaflux.Gather(context.Background(), steps...)
			e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "gather" || got != nil || ran.Load() != 0 {
				t.Errorf("Op = %q, results = %v, steps run = %d", e.Op, got, ran.Load())
			}
			if !strings.Contains(err.Error(), fmt.Sprint("step ", position+1, " is nil")) {
				t.Errorf("the error does not say which step: %v", err)
			}
		})
	}
}

func TestGatherTurnsAPanicIntoAnInternalErrorThatHoldsNothingOfIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const canary = "panic-payload-4471"
		var cancelled atomic.Int32
		got, err := schemaflux.Gather(context.Background(),
			flowUntilCancelled[int](&cancelled),
			func(context.Context) (int, error) { panic(canary) })
		e := flowWantKind(t, err, schemaflux.ErrInternal)
		if got != nil || cancelled.Load() != 1 {
			t.Errorf("results = %v, steps cancelled = %d", got, cancelled.Load())
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
			t.Errorf("the panic's value is in the error: %v", err)
		}
	})
}

func TestGatherTurnsAStepThatEndsItsGoroutineIntoAnInternalError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		got, err := schemaflux.Gather(context.Background(), flowWait(time.Hour, 1), flowGoexit[int])
		flowWantKind(t, err, schemaflux.ErrInternal)
		if got != nil {
			t.Errorf("results = %v", got)
		}
	})
}

func TestGatherOfNoStepsIsEmptyAndNotNil(t *testing.T) {
	got, err := schemaflux.Gather[string](context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Gather() = %#v, %v; want an empty list that is not nil", got, err)
	}
}

func TestGatherRefusesANilContextAndAContextThatIsOverAndStartsNone(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	got, err := schemaflux.Gather(nilContext, flowCounts(&ran, 1))
	flowWantKind(t, err, schemaflux.ErrInvalidRequest)
	if got != nil {
		t.Errorf("results = %v", got)
	}
	for _, done := range flowDone() {
		got, err := schemaflux.Gather(done.ctx, flowCounts(&ran, 1), flowCounts(&ran, 2))
		flowWantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || got != nil {
			t.Errorf("%s: err = %v, results = %v", done.name, err, got)
		}
		if _, err := schemaflux.Gather[int](done.ctx); err == nil {
			t.Errorf("%s: no steps and a context that is over succeeded", done.name)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestGatherWaitsForAStepThatIgnoresItsContextAndNothingOutlivesTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := flowNoLeak(t)
		var err error
		took := flowTook(func() {
			_, err = schemaflux.Gather(context.Background(), flowStubborn(5*time.Second, 1), flowFails[int](errFlowStep))
		})
		if err != errFlowStep {
			t.Errorf("err = %v", err)
		}
		if took != 5*time.Second {
			t.Errorf("took %v: Gather returns when every step has, so a step that ignores its context holds it for 5s", took)
		}
		leaks()
	})
}

func TestGatherLeavesNoGoroutineBehind(t *testing.T) {
	cases := []struct {
		name  string
		steps []schemaflux.Step[int]
	}{
		{"all succeed", []schemaflux.Step[int]{flowWait(time.Second, 1), flowWait(2*time.Second, 2)}},
		{"one fails", []schemaflux.Step[int]{flowWait(time.Hour, 1), flowFails[int](errFlowStep)}},
		{"one panics", []schemaflux.Step[int]{flowWait(time.Hour, 1), func(context.Context) (int, error) { panic("x") }}},
		{"one ends its goroutine", []schemaflux.Step[int]{flowWait(time.Hour, 1), flowGoexit[int]}},
		{"one ignores cancellation", []schemaflux.Step[int]{flowStubborn(time.Minute, 1), flowFails[int](errFlowStep)}},
		{"a single step", []schemaflux.Step[int]{flowReturns(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := flowNoLeak(t)
				_, _ = schemaflux.Gather(context.Background(), tc.steps...)
				leaks()
			})
		})
	}
}

func TestGatherHandsEveryStepTheCallersValuesAndAContextTheCallerCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), flowContextKey{}, "mine"))
		defer cancel()
		var cancelled atomic.Int32
		waits := func(ctx context.Context) (string, error) {
			if fmt.Sprint(ctx.Value(flowContextKey{})) != "mine" {
				return "", errors.New("the context lost the caller's values")
			}
			<-ctx.Done()
			cancelled.Add(1)
			return "", ctx.Err()
		}
		result := make(chan error)
		go func() {
			_, err := schemaflux.Gather(ctx, waits, waits)
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; err != context.Canceled {
			t.Errorf("err = %v; steps that return the context's error return it as it is", err)
		}
		if cancelled.Load() != 2 {
			t.Errorf("%d of 2 steps saw the caller cancel", cancelled.Load())
		}
	})
}

func TestGatherOfBranchesOfEachesReturnsTheInnermostErrorAsItWasGiven(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		each := func(ctx context.Context) (int, error) {
			got, err := schemaflux.Each(ctx, []int{1, 2, 3, 4}, func(_ context.Context, i int) (int, error) {
				if i == 3 {
					return 0, errFlowStep
				}
				return i, nil
			})
			return len(got), err
		}
		branch := func(ctx context.Context) (int, error) { return schemaflux.Branch(ctx, true, each, flowReturns(0)) }
		for _, wrap := range []func(context.Context) (int, error){
			branch,
			func(ctx context.Context) (int, error) { return schemaflux.Branch(ctx, false, flowReturns(0), branch) },
			func(ctx context.Context) (int, error) {
				a, b, err := schemaflux.Gather2(ctx, branch, flowWait(time.Hour, 0))
				return a + b, err
			},
			func(ctx context.Context) (int, error) {
				got, err := schemaflux.Gather(ctx, branch)
				return len(got), err
			},
		} {
			_, err := schemaflux.Gather(context.Background(), flowWait(time.Hour, 0), wrap)
			if err != errFlowStep {
				t.Errorf("err = %v (%T), want the innermost step's error as it was given", err, err)
			}
		}
	})
}

func TestGatherReturnsTheErrorTheStepsGaveWhateverItsType(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var typed *schemaflux.Error = &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded, Op: "extract"}
		for _, failure := range []error{errFlowStep, fmt.Errorf("wrapped: %w", errFlowStep), typed, context.DeadlineExceeded, errors.Join(errFlowStep, typed)} {
			_, err := schemaflux.Gather(context.Background(), flowFails[int](failure))
			if err != failure {
				t.Errorf("err = %v (%T), want the very value %v", err, err, failure)
			}
		}
	})
}

func TestGather2ReturnsTwoResultsOfDifferentTypes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			a    string
			b    int
			err  error
			took = flowTook(func() {
				a, b, err = schemaflux.Gather2(context.Background(), flowWait(20*time.Millisecond, "text"), flowWait(10*time.Millisecond, 42))
			})
		)
		if err != nil || a != "text" || b != 42 || took != 20*time.Millisecond {
			t.Errorf("Gather2 = %q, %d, %v after %v", a, b, err, took)
		}
	})
}

func TestGather3ReturnsThreeResultsOfDifferentTypes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b, c, err := schemaflux.Gather3(context.Background(),
			flowWait(30*time.Millisecond, "text"), flowWait(20*time.Millisecond, 42), flowWait(10*time.Millisecond, []string{"x"}))
		if err != nil || a != "text" || b != 42 || !reflect.DeepEqual(c, []string{"x"}) {
			t.Errorf("Gather3 = %q, %d, %v, %v", a, b, c, err)
		}
	})
}

func TestGather4ReturnsFourResultsOfDifferentTypes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b, c, d, err := schemaflux.Gather4(context.Background(),
			flowReturns("text"), flowReturns(42), flowReturns(true), flowReturns(2.5))
		if err != nil || a != "text" || b != 42 || !c || d != 2.5 {
			t.Errorf("Gather4 = %q, %d, %v, %v, %v", a, b, c, d, err)
		}
	})
}

// flowFixedGather runs Gather2, Gather3 or Gather4 over steps of which the one at
// position is the given step and the rest wait on the context, and returns what
// the call returned and how many of the others saw it cancelled.
func flowFixedGather(arity, position int, ctx context.Context, special schemaflux.Step[int], cancelled *atomic.Int32) (int, error) {
	steps := make([]schemaflux.Step[int], arity)
	for i := range steps {
		steps[i] = flowUntilCancelled[int](cancelled)
	}
	steps[position] = special
	var total int
	var err error
	switch arity {
	case 2:
		var a, b int
		a, b, err = schemaflux.Gather2(ctx, steps[0], steps[1])
		total = a + b
	case 3:
		var a, b, c int
		a, b, c, err = schemaflux.Gather3(ctx, steps[0], steps[1], steps[2])
		total = a + b + c
	case 4:
		var a, b, c, d int
		a, b, c, d, err = schemaflux.Gather4(ctx, steps[0], steps[1], steps[2], steps[3])
		total = a + b + c + d
	}
	return total, err
}

func TestGatherTwoThreeAndFourReturnTheFailureOfAnyPositionAsItWasGivenAndCancelTheRest(t *testing.T) {
	for arity := 2; arity <= 4; arity++ {
		for position := range arity {
			t.Run(fmt.Sprintf("Gather%d, step %d fails", arity, position+1), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var cancelled atomic.Int32
					total, err := flowFixedGather(arity, position, context.Background(), flowFails[int](errFlowStep), &cancelled)
					if err != errFlowStep || total != 0 {
						t.Errorf("total = %d, err = %v; want zero values and the error as it was given", total, err)
					}
					if cancelled.Load() != int32(arity-1) {
						t.Errorf("%d of the other %d steps saw their context cancelled", cancelled.Load(), arity-1)
					}
				})
			})
		}
	}
}

func TestGatherTwoThreeAndFourRefuseANilStepAtAnyPositionAndStartNone(t *testing.T) {
	for arity := 2; arity <= 4; arity++ {
		for position := range arity {
			t.Run(fmt.Sprintf("Gather%d, nil at %d", arity, position+1), func(t *testing.T) {
				var started atomic.Int32
				total, err := flowFixedGather(arity, position, context.Background(), nil, &started)
				e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
				if e.Op != "gather" || total != 0 || started.Load() != 0 {
					t.Errorf("Op = %q, total = %d, steps started = %d", e.Op, total, started.Load())
				}
			})
		}
	}
}

func TestGatherTwoThreeAndFourTurnAPanicAtAnyPositionIntoAnInternalError(t *testing.T) {
	for arity := 2; arity <= 4; arity++ {
		for position := range arity {
			t.Run(fmt.Sprintf("Gather%d, step %d panics", arity, position+1), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					leaks := flowNoLeak(t)
					var cancelled atomic.Int32
					_, err := flowFixedGather(arity, position, context.Background(), func(context.Context) (int, error) { panic("x") }, &cancelled)
					flowWantKind(t, err, schemaflux.ErrInternal)
					if cancelled.Load() != int32(arity-1) {
						t.Errorf("%d of the other %d steps were cancelled", cancelled.Load(), arity-1)
					}
					leaks()
				})
			})
		}
	}
}

func TestGatherTwoThreeAndFourRefuseANilContextAndAContextThatIsOver(t *testing.T) {
	var nilContext context.Context
	for arity := 2; arity <= 4; arity++ {
		var started atomic.Int32
		_, err := flowFixedGather(arity, 0, nilContext, flowCounts(&started, 1), &started)
		flowWantKind(t, err, schemaflux.ErrInvalidRequest)
		for _, done := range flowDone() {
			_, err := flowFixedGather(arity, 0, done.ctx, flowCounts(&started, 1), &started)
			flowWantKind(t, err, done.kind)
		}
		if started.Load() != 0 {
			t.Errorf("Gather%d started %d steps", arity, started.Load())
		}
	}
}

func TestGatherTwoThreeAndFourRunAllTheirStepsAtOnce(t *testing.T) {
	for arity := 2; arity <= 4; arity++ {
		t.Run(fmt.Sprint("Gather", arity), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var started atomic.Int32
				blocked := func(context.Context) (int, error) {
					started.Add(1)
					<-release
					return 1, nil
				}
				var wait sync.WaitGroup
				wait.Go(func() {
					var total int
					var err error
					switch arity {
					case 2:
						a, b, e := schemaflux.Gather2(context.Background(), blocked, blocked)
						total, err = a+b, e
					case 3:
						a, b, c, e := schemaflux.Gather3(context.Background(), blocked, blocked, blocked)
						total, err = a+b+c, e
					case 4:
						a, b, c, d, e := schemaflux.Gather4(context.Background(), blocked, blocked, blocked, blocked)
						total, err = a+b+c+d, e
					}
					if err != nil || total != arity {
						t.Errorf("total = %d, err = %v", total, err)
					}
				})
				synctest.Wait()
				if started.Load() != int32(arity) {
					t.Errorf("%d of %d steps in flight at once", started.Load(), arity)
				}
				close(release)
				wait.Wait()
			})
		})
	}
}

func TestEachReturnsTheResultsInTheOrderOfTheItems(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := []int{5, 1, 4, 2, 3}
		got, err := schemaflux.Each(context.Background(), items, func(ctx context.Context, n int) (string, error) {
			return flowWait(time.Duration(n)*time.Millisecond, fmt.Sprint("item ", n))(ctx)
		})
		want := []string{"item 5", "item 1", "item 4", "item 2", "item 3"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Each = %v, %v; want %v in the order of the items", got, err, want)
		}
	})
}

func TestEachRunsAtMostSixtyFourAtOnce(t *testing.T) {
	cases := []struct {
		items int
		peak  int32
	}{
		{1, 1}, {2, 2}, {10, 10}, {63, 63}, {64, 64}, {65, 64}, {200, 64}, {1000, 64},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.items, " items"), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var running, peak, calls atomic.Int32
				gate := make(chan struct{})
				var wait sync.WaitGroup
				wait.Go(func() {
					_, err := schemaflux.Each(context.Background(), make([]int, tc.items), func(context.Context, int) (int, error) {
						calls.Add(1)
						now := running.Add(1)
						for {
							old := peak.Load()
							if now <= old || peak.CompareAndSwap(old, now) {
								break
							}
						}
						<-gate
						running.Add(-1)
						return 0, nil
					})
					if err != nil {
						t.Error(err)
					}
				})
				synctest.Wait()
				if running.Load() != tc.peak {
					t.Errorf("%d steps in flight with every one blocked, want %d", running.Load(), tc.peak)
				}
				close(gate)
				wait.Wait()
				if peak.Load() != tc.peak || calls.Load() != int32(tc.items) {
					t.Errorf("peak %d and %d calls for %d items; want a peak of %d and one call per item", peak.Load(), calls.Load(), tc.items, tc.peak)
				}
			})
		})
	}
}

func TestEachCallsTheStepOncePerItemWithThatItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := make([]int, 500)
		for i := range items {
			items[i] = i * 3
		}
		seen := make([]atomic.Int32, len(items))
		got, err := schemaflux.Each(context.Background(), items, func(_ context.Context, n int) (int, error) {
			seen[n/3].Add(1)
			return n + 1, nil
		})
		if err != nil || len(got) != len(items) {
			t.Fatalf("Each = %d results, %v", len(got), err)
		}
		for i := range items {
			if seen[i].Load() != 1 || got[i] != items[i]+1 {
				t.Fatalf("item %d: seen %d times, result %d", i, seen[i].Load(), got[i])
			}
		}
	})
}

func TestEachStopsAtTheFirstFailureAndReturnsItAsItWasGiven(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var started atomic.Int32
		var cancelled atomic.Int32
		got, err := schemaflux.Each(context.Background(), make([]int, 300), func(ctx context.Context, _ int) (int, error) {
			if started.Add(1) == 1 {
				return 0, errFlowStep
			}
			<-ctx.Done()
			cancelled.Add(1)
			return 0, ctx.Err()
		})
		if err != errFlowStep || got != nil {
			t.Errorf("Each = %v, %v; want no results and the error as it was given", got, err)
		}
		if started.Load() > 64 {
			t.Errorf("%d steps started: none after the failure, and no more than 64 in all, may", started.Load())
		}
		if cancelled.Load() != started.Load()-1 {
			t.Errorf("%d of the %d other steps saw their context cancelled", cancelled.Load(), started.Load()-1)
		}
	})
}

func TestEachStartsNoItemAfterTheFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var highest atomic.Int32
		_, err := schemaflux.Each(context.Background(), make([]int, 400), func(ctx context.Context, _ int) (int, error) {
			n := highest.Add(1)
			if n == 5 {
				return 0, errFlowStep
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Hour):
				return 0, nil
			}
		})
		if err != errFlowStep {
			t.Errorf("err = %v", err)
		}
		if highest.Load() > 64 {
			t.Errorf("%d items started; a failure of the fifth stops the rest and at most 64 were in flight", highest.Load())
		}
	})
}

func TestEachReturnsTheErrorTheStepGaveWhateverItsType(t *testing.T) {
	typed := &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Op: "extract"}
	for _, failure := range []error{errFlowStep, fmt.Errorf("item 2: %w", errFlowStep), typed, context.Canceled} {
		got, err := schemaflux.Each(context.Background(), []int{1, 2, 3}, func(_ context.Context, n int) (int, error) {
			if n == 2 {
				return 0, failure
			}
			return n, nil
		})
		if err != failure || got != nil {
			t.Errorf("Each = %v, %v (%T); want no results and %v itself", got, err, err, failure)
		}
	}
}

func TestEachOfNoItemsIsEmptyAndNotNil(t *testing.T) {
	var ran atomic.Int32
	for _, items := range [][]int{nil, {}} {
		got, err := schemaflux.Each(context.Background(), items, func(context.Context, int) (string, error) {
			ran.Add(1)
			return "", nil
		})
		if err != nil || got == nil || len(got) != 0 || ran.Load() != 0 {
			t.Errorf("Each(%#v) = %#v, %v; want an empty list that is not nil and no calls", items, got, err)
		}
	}
}

func TestEachRefusesANilStepEvenForNoItems(t *testing.T) {
	for _, items := range [][]int{nil, {1, 2}} {
		got, err := schemaflux.Each[int, string](context.Background(), items, nil)
		e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
		if e.Op != "each" || got != nil {
			t.Errorf("Op = %q, results = %v", e.Op, got)
		}
	}
}

func TestEachTurnsAPanicIntoAnInternalErrorThatHoldsNothingOfIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const canary = "panic-payload-4471"
		leaks := flowNoLeak(t)
		got, err := schemaflux.Each(context.Background(), []int{1, 2, 3}, func(ctx context.Context, n int) (int, error) {
			if n == 2 {
				panic(canary)
			}
			<-ctx.Done()
			return 0, ctx.Err()
		})
		e := flowWantKind(t, err, schemaflux.ErrInternal)
		if got != nil {
			t.Errorf("results = %v", got)
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v %v", e, e.Err), canary) {
			t.Errorf("the panic's value is in the error: %v", err)
		}
		leaks()
	})
}

func TestEachTurnsAStepThatEndsItsGoroutineIntoAnInternalError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := flowNoLeak(t)
		_, err := schemaflux.Each(context.Background(), []int{1, 2}, func(ctx context.Context, n int) (int, error) {
			if n == 1 {
				return flowGoexit[int](ctx)
			}
			<-ctx.Done()
			return 0, ctx.Err()
		})
		flowWantKind(t, err, schemaflux.ErrInternal)
		leaks()
	})
}

func TestEachRefusesANilContextAndAContextThatIsOverAndStartsNone(t *testing.T) {
	var ran atomic.Int32
	step := func(context.Context, int) (int, error) {
		ran.Add(1)
		return 0, nil
	}
	var nilContext context.Context
	_, err := schemaflux.Each(nilContext, []int{1}, step)
	flowWantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range flowDone() {
		got, err := schemaflux.Each(done.ctx, []int{1, 2, 3}, step)
		flowWantKind(t, err, done.kind)
		if !errors.Is(err, done.is) || got != nil {
			t.Errorf("%s: err = %v, results = %v", done.name, err, got)
		}
		if _, err := schemaflux.Each(done.ctx, []int(nil), step); err == nil {
			t.Errorf("%s: no items and a context that is over succeeded", done.name)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("%d steps ran", ran.Load())
	}
}

func TestEachReturnsTheContextsErrorWhenTheCallerCancelsWhileEveryStepIsWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error)
		go func() {
			_, err := schemaflux.Each(ctx, make([]int, 10), func(ctx context.Context, _ int) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			})
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; err != context.Canceled {
			t.Errorf("err = %v; steps that return the context's error return it as it is", err)
		}
	})
}

func TestEachWaitsForAStepThatIgnoresItsContextAndNothingOutlivesTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		leaks := flowNoLeak(t)
		var err error
		stubbornStarted := make(chan struct{})
		took := flowTook(func() {
			_, err = schemaflux.Each(context.Background(), []int{1, 2}, func(ctx context.Context, n int) (int, error) {
				if n == 1 {
					<-stubbornStarted
					return 0, errFlowStep
				}
				close(stubbornStarted)
				time.Sleep(5 * time.Second)
				return n, nil
			})
		})
		if err != errFlowStep || took != 5*time.Second {
			t.Errorf("err = %v after %v; Each returns when every step that started has, so 5s", err, took)
		}
		leaks()
	})
}

func TestEachLeavesNoGoroutineBehindWhetherOrNotItFails(t *testing.T) {
	cases := []struct {
		name  string
		items int
		step  func(context.Context, int) (int, error)
	}{
		{"all succeed", 100, func(context.Context, int) (int, error) { return 1, nil }},
		{"one item", 1, func(context.Context, int) (int, error) { return 1, nil }},
		{"every step waits a while", 150, func(ctx context.Context, n int) (int, error) { return flowWait(time.Second, n)(ctx) }},
		{"one fails", 100, func(ctx context.Context, n int) (int, error) {
			if n == 40 {
				return 0, errFlowStep
			}
			return flowWait(time.Hour, n)(ctx)
		}},
		{"one panics", 100, func(ctx context.Context, n int) (int, error) {
			if n == 40 {
				panic("x")
			}
			return flowWait(time.Hour, n)(ctx)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				leaks := flowNoLeak(t)
				items := make([]int, tc.items)
				for i := range items {
					items[i] = i
				}
				_, _ = schemaflux.Each(context.Background(), items, tc.step)
				leaks()
			})
		})
	}
}

func TestEachKeepsAFailureInsideTheResultWhenTheStepReturnsItThere(t *testing.T) {
	type outcome struct {
		value int
		err   error
	}
	got, err := schemaflux.Each(context.Background(), []int{1, 2, 3}, func(_ context.Context, n int) (outcome, error) {
		if n == 2 {
			return outcome{err: errFlowStep}, nil
		}
		return outcome{value: n}, nil
	})
	if err != nil || len(got) != 3 || got[0].value != 1 || got[1].err != errFlowStep || got[2].value != 3 {
		t.Errorf("Each = %+v, %v; want every item worked through with the failure in its result", got, err)
	}
}
