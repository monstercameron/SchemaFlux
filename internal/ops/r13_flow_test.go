package ops

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Review round 13 of flow.go: a step that ends its goroutine is an error and
// not a zero result delivered as success, a nil step is refused before any other
// step starts, a panic is classified, a nil context is the same everywhere, and
// RouteBy refuses a single route itself.
//
// Events are ordered by channels and by the calls' own completion. Where a wall
// clock bound cannot be avoided it is a multiple of what the code needs.

// aheadSafety is how long a test waits for something that is expected to happen
// at once, before it gives up. It is never what a passing test takes.
const aheadSafety = 10 * time.Second

func aheadGoexit[T any]() Step[T] {
	return func(context.Context) (T, error) {
		runtime.Goexit()
		var zero T
		return zero, nil
	}
}

// aheadWaitForCancel is a step that returns when its context ends, and fails
// the test's wait if that never happens.
func aheadWaitForCancel(started chan<- struct{}) Step[string] {
	return func(ctx context.Context) (string, error) {
		if started != nil {
			started <- struct{}{}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(aheadSafety):
			return "never cancelled", nil
		}
	}
}

// aheadCounted is a step that counts how many times it ran.
func aheadCounted(ran *atomic.Int32) Step[string] {
	return func(context.Context) (string, error) {
		ran.Add(1)
		return "ran", nil
	}
}

func aheadWantClassifiedFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("a step did not return and no error was returned")
	}
	if kind := types.KindOf(err); kind != types.KindInvariantViolation {
		t.Errorf("the kind of the error is %v, want %v: %v", kind, types.KindInvariantViolation, err)
	}
}

// A step that ends its goroutine with runtime.Goexit, as t.FailNow does, is an
// error. It was a zero result and a nil error, because nothing recovers a
// Goexit and the code after the step never ran.
func TestAheadR13GoexitInAStepIsAnError(t *testing.T) {
	ctx := context.Background()
	fine := func(context.Context) (string, error) { return "fine", nil }
	number := func(context.Context) (int, error) { return 7, nil }
	cases := []struct {
		name string
		run  func() (leftover bool, err error)
	}{
		{"Gather, the first of three", func() (bool, error) {
			got, err := Gather(ctx, aheadGoexit[string](), fine, fine)
			return got != nil, err
		}},
		{"Gather, the last of three", func() (bool, error) {
			got, err := Gather(ctx, fine, fine, aheadGoexit[string]())
			return got != nil, err
		}},
		{"Gather, the only step", func() (bool, error) {
			got, err := Gather(ctx, aheadGoexit[string]())
			return got != nil, err
		}},
		{"Gather, every step", func() (bool, error) {
			got, err := Gather(ctx, aheadGoexit[string](), aheadGoexit[string]())
			return got != nil, err
		}},
		{"Gather2, the first", func() (bool, error) {
			a, b, err := Gather2(ctx, aheadGoexit[string](), number)
			return a != "" || b != 0, err
		}},
		{"Gather2, the second", func() (bool, error) {
			a, b, err := Gather2(ctx, fine, aheadGoexit[int]())
			return a != "" || b != 0, err
		}},
		{"Gather3, the middle", func() (bool, error) {
			a, b, c, err := Gather3(ctx, fine, aheadGoexit[int](), number)
			return a != "" || b != 0 || c != 0, err
		}},
		{"Gather4, the last", func() (bool, error) {
			a, b, c, d, err := Gather4(ctx, fine, number, fine, aheadGoexit[int]())
			return a != "" || b != 0 || c != "" || d != 0, err
		}},
		{"Each, one item of many", func() (bool, error) {
			got, err := Each(ctx, []int{1, 2, 3, 4, 5, 6}, func(_ context.Context, n int) (int, error) {
				if n == 3 {
					runtime.Goexit()
				}
				return n, nil
			})
			return got != nil, err
		}},
		{"Each, a single item", func() (bool, error) {
			got, err := Each(ctx, []int{1}, func(context.Context, int) (int, error) {
				runtime.Goexit()
				return 0, nil
			})
			return got != nil, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leftover, err := tc.run()
			aheadWantClassifiedFailure(t, err)
			if leftover {
				t.Error("results were returned beside the error")
			}
		})
	}
}

// panic(nil) is a panic too. Since Go 1.21 it is recovered as a value of its
// own; the step must not be read as having completed.
func TestAheadR13PanicNilIsAnError(t *testing.T) {
	var nothing any
	got, err := Gather(context.Background(), func(context.Context) (string, error) { panic(nothing) }, aheadCounted(new(atomic.Int32)))
	aheadWantClassifiedFailure(t, err)
	if got != nil {
		t.Errorf("Gather = %v beside an error", got)
	}
	_, err = Each(context.Background(), []int{1, 2}, func(context.Context, int) (int, error) { panic(nothing) })
	aheadWantClassifiedFailure(t, err)
}

type aheadPanicValue struct{ secret string }

type aheadPanicError struct{}

func (aheadPanicError) Error() string { return "an error value that was panicked with: 7731" }

// runGuarded turns whatever a step panics with into one classified error that
// carries none of it.
func TestAheadR13RunGuardedClassifiesEveryPanic(t *testing.T) {
	var nilMap map[string]int
	var nilPointer *aheadPanicValue
	var nothing any
	short := []int{1}
	closed := make(chan int)
	close(closed)
	cases := []struct {
		name string
		step func()
	}{
		{"a string", func() { panic("a string with 7731 in it") }},
		{"an error", func() { panic(errors.New("an error with 7731 in it")) }},
		{"a typed error", func() { panic(aheadPanicError{}) }},
		{"nil", func() { panic(nothing) }},
		{"an integer", func() { panic(7731) }},
		{"a struct", func() { panic(aheadPanicValue{"7731"}) }},
		{"a pointer to a struct", func() { panic(&aheadPanicValue{"7731"}) }},
		{"a write to a nil map", func() { nilMap["7731"] = 1 }},
		{"a nil pointer", func() { _ = nilPointer.secret }},
		{"an index out of range", func() { _ = short[len(short)+7731] }},
		{"a closed channel", func() { close(closed) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runGuarded(context.Background(), "gather", 0, func(context.Context, int) error {
				tc.step()
				return nil
			})
			aheadWantClassifiedFailure(t, err)
			if strings.Contains(err.Error(), "7731") {
				t.Errorf("the error carries the panic's value: %v", err)
			}
		})
	}

	t.Run("a step that returns", func(t *testing.T) {
		if err := runGuarded(context.Background(), "gather", 3, func(_ context.Context, i int) error {
			if i != 3 {
				t.Errorf("the step was given index %d", i)
			}
			return nil
		}); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a step that fails", func(t *testing.T) {
		failing := errors.New("the step failed")
		if err := runGuarded(context.Background(), "gather", 0, func(context.Context, int) error { return failing }); err != failing {
			t.Errorf("err = %v; the step's own error is returned as it was", err)
		}
	})
}

// A step that calls runtime.Goexit cannot be recovered, so runGuarded never
// returns for it; what the callers do about that is tested through them.
func TestAheadR13RunGuardedDoesNotSwallowAGoexit(t *testing.T) {
	var returned atomic.Bool
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_ = runGuarded(context.Background(), "gather", 0, func(context.Context, int) error {
			runtime.Goexit()
			return nil
		})
		returned.Store(true)
	}()
	select {
	case <-ended:
	case <-time.After(aheadSafety):
		t.Fatal("the goroutine did not end")
	}
	if returned.Load() {
		t.Error("runGuarded returned for a step that ended its goroutine")
	}
}

// Every step is checked before the first one starts: a program that passes a nil
// step has not paid for the others.
func TestAheadR13NilStepStartsNothing(t *testing.T) {
	ctx := context.Background()
	var ran atomic.Int32
	step, number := aheadCounted(&ran), func(context.Context) (int, error) { ran.Add(1); return 1, nil }
	cases := []struct {
		name string
		run  func() error
	}{
		{"Gather, the first of three", func() error { _, err := Gather(ctx, nil, step, step); return err }},
		{"Gather, the middle of three", func() error { _, err := Gather(ctx, step, nil, step); return err }},
		{"Gather, the last of three", func() error { _, err := Gather(ctx, step, step, nil); return err }},
		{"Gather, the last of two", func() error { _, err := Gather(ctx, step, nil); return err }},
		{"Gather, only nil steps", func() error { _, err := Gather(ctx, Step[string](nil), nil); return err }},
		{"Gather2, the second", func() error { _, _, err := Gather2(ctx, step, Step[int](nil)); return err }},
		{"Gather2, the first", func() error { _, _, err := Gather2(ctx, Step[string](nil), number); return err }},
		{"Gather2, both", func() error { _, _, err := Gather2(ctx, Step[string](nil), Step[int](nil)); return err }},
		{"Gather3, the third", func() error { _, _, _, err := Gather3(ctx, step, number, Step[bool](nil)); return err }},
		{"Gather3, the second", func() error { _, _, _, err := Gather3(ctx, step, Step[int](nil), Step[string](nil)); return err }},
		{"Gather3, the first", func() error { _, _, _, err := Gather3(ctx, Step[string](nil), number, step); return err }},
		{"Gather4, the fourth", func() error { _, _, _, _, err := Gather4(ctx, step, number, step, Step[int](nil)); return err }},
		{"Gather4, the first", func() error { _, _, _, _, err := Gather4(ctx, Step[string](nil), number, step, number); return err }},
		{"Gather4, the third", func() error { _, _, _, _, err := Gather4(ctx, step, number, Step[string](nil), number); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran.Store(0)
			if err := tc.run(); types.KindOf(err) != types.KindConfiguration {
				t.Errorf("err = %v, want a configuration error", err)
			}
			if ran.Load() != 0 {
				t.Errorf("%d steps ran before the nil one was refused", ran.Load())
			}
		})
	}
}

// A panic is the invariant of "a step returns" broken, not a gap: retry and
// escalation can tell it from a transient failure.
func TestAheadR13APanicIsNotAGap(t *testing.T) {
	ctx := context.Background()
	boom := func(context.Context) (string, error) { panic("7731") }
	cases := []struct {
		name string
		run  func() error
	}{
		{"Gather", func() error { _, err := Gather(ctx, boom, boom); return err }},
		{"Gather, one step", func() error { _, err := Gather(ctx, boom); return err }},
		{"Gather2", func() error { _, _, err := Gather2(ctx, boom, boom); return err }},
		{"Gather3", func() error { _, _, _, err := Gather3(ctx, boom, boom, boom); return err }},
		{"Gather4", func() error { _, _, _, _, err := Gather4(ctx, boom, boom, boom, boom); return err }},
		{"Each", func() error {
			_, err := Each(ctx, []int{1, 2, 3}, func(context.Context, int) (string, error) { panic("7731") })
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aheadWantClassifiedFailure(t, tc.run())
		})
	}
}

// A nil context is the background context for every combinator, as it is for
// Gather and Each.
func TestAheadR13NilContextIsNormalisedEverywhere(t *testing.T) {
	var nilContext context.Context
	var seen atomic.Pointer[context.Context]
	see := func(ctx context.Context) { seen.Store(&ctx) }
	reset := func() { seen.Store(nil) }
	got := func() context.Context {
		if p := seen.Load(); p != nil {
			return *p
		}
		return nil
	}
	step := func(ctx context.Context) (string, error) { see(ctx); return "ok", nil }
	yes := func(context.Context) (bool, error) { return true, nil }

	routeStep := To("b", step)
	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"Branch, yes", func(*testing.T) error { _, err := Branch(nilContext, true, step, nil); return err }},
		{"Branch, no", func(*testing.T) error { _, err := Branch(nilContext, false, nil, step); return err }},
		{"Whether", func(t *testing.T) error {
			answering(t, `{"q1":true}`)
			_, err := Whether(nilContext, cfMessage, "Is it?", step, nil)
			return err
		}},
		{"Select", func(*testing.T) error { _, err := Select(nilContext, "a", To("a", step)); return err }},
		{"RouteBy", func(t *testing.T) error {
			answering(t, `{"q1":2}`)
			_, err := RouteBy(nilContext, cfMessage, "Which?", []Route[string]{To("a", step), routeStep})
			return err
		}},
		{"Gather", func(*testing.T) error { _, err := Gather(nilContext, step, step); return err }},
		{"Gather2", func(*testing.T) error { _, _, err := Gather2(nilContext, step, step); return err }},
		{"Gather3", func(*testing.T) error { _, _, _, err := Gather3(nilContext, step, step, step); return err }},
		{"Gather4", func(*testing.T) error { _, _, _, _, err := Gather4(nilContext, step, step, step, step); return err }},
		{"Each", func(*testing.T) error {
			_, err := Each(nilContext, []int{1, 2}, func(ctx context.Context, _ int) (string, error) { return step(ctx) })
			return err
		}},
		{"BranchAhead, yes", func(*testing.T) error { _, err := BranchAhead(nilContext, yes, step, step); return err }},
		{"BranchAhead, no", func(*testing.T) error {
			_, err := BranchAhead(nilContext, func(context.Context) (bool, error) { return false, nil }, step, step)
			return err
		}},
		{"BranchBoth", func(*testing.T) error { _, err := BranchBoth(nilContext, yes, step, step); return err }},
		{"RouteAhead", func(*testing.T) error {
			_, err := RouteAhead(nilContext, func(context.Context) (string, error) { return "a", nil }, "a", To("a", step))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			if err := tc.run(t); err != nil {
				t.Fatalf("err = %v", err)
			}
			if got() == nil {
				t.Error("the step was given a nil context, or was not run")
			}
		})
	}
}

// RouteBy with one route would ask a question with one possible answer, and the
// refusal came from another operation, about "question 1". It is refused here,
// before any call, and says what RouteBy needs.
func TestAheadR13RouteByNeedsTwoRoutes(t *testing.T) {
	step := func(context.Context) (string, error) { return "ran", nil }

	t.Run("one route", func(t *testing.T) {
		sent := answering(t, `{"q1":1}`)
		got, err := RouteBy(context.Background(), cfMessage, "Which?", []Route[string]{To("a", step)})
		if types.KindOf(err) != types.KindConfiguration || got != "" || sent.calls != 0 {
			t.Fatalf("RouteBy = %q, %v after %d calls", got, err, sent.calls)
		}
		if !strings.Contains(err.Error(), "at least two routes") {
			t.Errorf("the error does not say what RouteBy needs: %v", err)
		}
		var op *types.OperationError
		if !errors.As(err, &op) || op.Op != "route" {
			t.Errorf("the error is from %v, not from RouteBy", err)
		}
	})
	t.Run("two routes", func(t *testing.T) {
		answering(t, `{"q1":2}`)
		got, err := RouteBy(context.Background(), cfMessage, "Which?", []Route[string]{To("a", step), To("b", step)})
		if err != nil || got != "ran" {
			t.Errorf("RouteBy = %q, %v", got, err)
		}
	})
	t.Run("no routes", func(t *testing.T) {
		sent := answering(t, `{"q1":1}`)
		if _, err := RouteBy[string](context.Background(), cfMessage, "Which?", nil); types.KindOf(err) != types.KindConfiguration || sent.calls != 0 {
			t.Errorf("err = %v after %d calls", err, sent.calls)
		}
	})
	t.Run("Select and RouteAhead take one route, since the choice is made elsewhere", func(t *testing.T) {
		if got, err := Select(context.Background(), "a", To("a", step)); err != nil || got != "ran" {
			t.Errorf("Select = %q, %v", got, err)
		}
		got, err := RouteAhead(context.Background(), func(context.Context) (string, error) { return "a", nil }, "a", To("a", step))
		if err != nil || got != "ran" {
			t.Errorf("RouteAhead = %q, %v", got, err)
		}
	})
}

// checkRoutes. A label that differs from another only by space around it is the
// same label: it is refused as a duplicate, since a caller who wrote both has
// two routes they cannot tell apart in a log or a prompt.
func TestAheadR13CheckRoutes(t *testing.T) {
	step := func(context.Context) (string, error) { return "", nil }
	ok := func(labels ...string) []Route[string] {
		routes := make([]Route[string], len(labels))
		for i, label := range labels {
			routes[i] = To(label, step)
		}
		return routes
	}
	cases := []struct {
		name   string
		routes []Route[string]
		want   types.ErrorKind // KindUnknown for accepted
	}{
		{"none", nil, types.KindConfiguration},
		{"an empty list", []Route[string]{}, types.KindConfiguration},
		{"one", ok("a"), types.KindUnknown},
		{"two", ok("a", "b"), types.KindUnknown},
		{"a blank label", ok("a", ""), types.KindConfiguration},
		{"a label of spaces", ok("a", "   "), types.KindConfiguration},
		{"a label of a tab and a newline", ok("a", "\t\n"), types.KindConfiguration},
		{"a label twice", ok("a", "a"), types.KindConfiguration},
		{"a label twice, one with a space after", ok("a", "a "), types.KindConfiguration},
		{"a label twice, one with a space before", ok(" a", "a"), types.KindConfiguration},
		{"a label twice, both with space", ok(" a ", "a  "), types.KindConfiguration},
		{"labels that differ in case", ok("a", "A"), types.KindUnknown},
		{"labels that differ inside", ok("a b", "a  b"), types.KindUnknown},
		{"a nil step", []Route[string]{To("a", step), To[string]("b", nil)}, types.KindConfiguration},
		{"a nil step first", []Route[string]{To[string]("a", nil), To("b", step)}, types.KindConfiguration},
		{"many", ok("a", "b", "c", "d", "e", "f", "g"), types.KindUnknown},
		{"many, the last a repeat", ok("a", "b", "c", "d", "e", "f", "a"), types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRoutes("test", tc.routes)
			if got := types.KindOf(err); got != tc.want || (tc.want == types.KindUnknown) != (err == nil) {
				t.Errorf("checkRoutes = %v (kind %v), want kind %v", err, got, tc.want)
			}
		})
	}
}

// To keeps the label as it was given and the step as it was given.
func TestAheadR13ToBuildsARoute(t *testing.T) {
	for _, label := range []string{"billing", "", " ", " billing ", "Billing", "a b", "billing/returns", "résumé", "日本語", strings.Repeat("x", 500)} {
		t.Run(fmt.Sprintf("%.12q", label), func(t *testing.T) {
			var calls atomic.Int32
			route := To(label, func(context.Context) (int, error) { calls.Add(1); return 42, nil })
			if route.Label != label {
				t.Errorf("Label = %q", route.Label)
			}
			if got, err := route.Run(context.Background()); err != nil || got != 42 || calls.Load() != 1 {
				t.Errorf("Run = %d, %v after %d calls", got, err, calls.Load())
			}
		})
	}
	if route := To[int]("a", nil); route.Run != nil || route.Label != "a" {
		t.Errorf("a nil step is kept as nil: %+v", route)
	}
}

// Gather2 and Gather3 with a result of each type.
func TestAheadR13Gather2(t *testing.T) {
	ctx := context.Background()
	failing := errors.New("the step failed")
	text := func(context.Context) (string, error) { return "text", nil }
	number := func(context.Context) (int, error) { return 7, nil }
	failsText := func(context.Context) (string, error) { return "partial", failing }
	failsNumber := func(context.Context) (int, error) { return 3, failing }
	cases := []struct {
		name    string
		first   Step[string]
		second  Step[int]
		a       string
		b       int
		wantErr error
		kind    types.ErrorKind
	}{
		{"both", text, number, "text", 7, nil, types.KindUnknown},
		{"the first fails", failsText, number, "", 0, failing, types.KindUnknown},
		{"the second fails", text, failsNumber, "", 0, failing, types.KindUnknown},
		{"both fail", failsText, failsNumber, "", 0, failing, types.KindUnknown},
		{"the first is nil", nil, number, "", 0, nil, types.KindConfiguration},
		{"the second is nil", text, nil, "", 0, nil, types.KindConfiguration},
		{"both are nil", nil, nil, "", 0, nil, types.KindConfiguration},
		{"the first panics", func(context.Context) (string, error) { panic("7731") }, number, "", 0, nil, types.KindInvariantViolation},
		{"the second panics", text, func(context.Context) (int, error) { panic("7731") }, "", 0, nil, types.KindInvariantViolation},
		{"the first ends its goroutine", aheadGoexit[string](), number, "", 0, nil, types.KindInvariantViolation},
		{"the second ends its goroutine", text, aheadGoexit[int](), "", 0, nil, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, err := Gather2(ctx, tc.first, tc.second)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && tc.kind == types.KindUnknown && err != nil {
				t.Errorf("err = %v", err)
			}
			if tc.kind != types.KindUnknown && types.KindOf(err) != tc.kind {
				t.Errorf("kind of %v is %v, want %v", err, types.KindOf(err), tc.kind)
			}
			if a != tc.a || b != tc.b {
				t.Errorf("Gather2 = %q, %d; want %q, %d", a, b, tc.a, tc.b)
			}
		})
	}

	t.Run("a failure cancels the other and the call waits for it", func(t *testing.T) {
		started := make(chan struct{}, 1)
		var other atomic.Bool
		_, _, err := Gather2(ctx, func(context.Context) (string, error) {
			<-started
			return "", failing
		}, func(ctx context.Context) (int, error) {
			started <- struct{}{}
			<-ctx.Done()
			other.Store(true)
			return 0, ctx.Err()
		})
		if !errors.Is(err, failing) {
			t.Errorf("err = %v; the error of the step that was cancelled is not the one returned", err)
		}
		if !other.Load() {
			t.Error("the call returned before the other step had been cancelled and returned")
		}
	})
}

func TestAheadR13Gather3(t *testing.T) {
	ctx := context.Background()
	failing := errors.New("the step failed")
	text := func(context.Context) (string, error) { return "text", nil }
	number := func(context.Context) (int, error) { return 7, nil }
	flag := func(context.Context) (bool, error) { return true, nil }
	cases := []struct {
		name       string
		a          Step[string]
		b          Step[int]
		c          Step[bool]
		wantFailed error
		kind       types.ErrorKind
	}{
		{"all", text, number, flag, nil, types.KindUnknown},
		{"the first fails", func(context.Context) (string, error) { return "", failing }, number, flag, failing, types.KindUnknown},
		{"the second fails", text, func(context.Context) (int, error) { return 1, failing }, flag, failing, types.KindUnknown},
		{"the third fails", text, number, func(context.Context) (bool, error) { return true, failing }, failing, types.KindUnknown},
		{"the first is nil", nil, number, flag, nil, types.KindConfiguration},
		{"the second is nil", text, nil, flag, nil, types.KindConfiguration},
		{"the third is nil", text, number, nil, nil, types.KindConfiguration},
		{"all are nil", nil, nil, nil, nil, types.KindConfiguration},
		{"the first panics", func(context.Context) (string, error) { panic("7731") }, number, flag, nil, types.KindInvariantViolation},
		{"the third panics", text, number, func(context.Context) (bool, error) { panic("7731") }, nil, types.KindInvariantViolation},
		{"the second ends its goroutine", text, aheadGoexit[int](), flag, nil, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c, err := Gather3(ctx, tc.a, tc.b, tc.c)
			failed := tc.wantFailed != nil || tc.kind != types.KindUnknown
			if failed {
				if err == nil {
					t.Fatal("no error")
				}
				if tc.wantFailed != nil && !errors.Is(err, tc.wantFailed) {
					t.Errorf("err = %v", err)
				}
				if tc.kind != types.KindUnknown && types.KindOf(err) != tc.kind {
					t.Errorf("kind of %v is %v, want %v", err, types.KindOf(err), tc.kind)
				}
				if a != "" || b != 0 || c {
					t.Errorf("Gather3 = %q, %d, %v beside an error", a, b, c)
				}
				return
			}
			if err != nil || a != "text" || b != 7 || !c {
				t.Errorf("Gather3 = %q, %d, %v, %v", a, b, c, err)
			}
		})
	}
}

// Gather's no-steps and one-step forms, and the result of Each for no items,
// are alike: an empty list that is not nil.
func TestAheadR13EmptyResultsAreNotNil(t *testing.T) {
	ctx := context.Background()
	if got, err := Gather[string](ctx); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Gather with no steps = %#v, %v", got, err)
	}
	if got, err := Each(ctx, nil, func(context.Context, int) (int, error) { return 0, nil }); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Each with nil items = %#v, %v", got, err)
	}
	if got, err := Each(ctx, []int{}, func(context.Context, int) (int, error) { return 0, nil }); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Each with no items = %#v, %v", got, err)
	}
}

// Each's refusal to go on past a context that is over is classified, as the
// rest of the library's is, and still is the context's error.
func TestAheadR13EachClassifiesAContextThatIsOver(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		is   error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"past its deadline", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran atomic.Int32
			_, err := Each(tc.ctx, make([]int, 10), func(context.Context, int) (int, error) { ran.Add(1); return 0, nil })
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v", err)
			}
			if types.KindOf(err) == types.KindUnknown {
				t.Errorf("the error has no kind: %v", err)
			}
			if ran.Load() != 0 {
				t.Errorf("%d steps ran in a context that was over", ran.Load())
			}
		})
	}
}

// Each runs exactly eachAtOnce items at once when there are more than that and
// the steps hold: the cap is a ceiling the test above checks, and also what is
// reached.
func TestAheadR13EachReachesItsCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		items := make([]int, eachAtOnce+36)
		var running, most atomic.Int32
		entered := make(chan struct{}, len(items))
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := Each(context.Background(), items, func(context.Context, int) (int, error) {
				now := running.Add(1)
				for {
					seen := most.Load()
					if now <= seen || most.CompareAndSwap(seen, now) {
						break
					}
				}
				entered <- struct{}{}
				<-release
				running.Add(-1)
				return 0, nil
			})
			done <- err
		}()
		for i := 0; i < eachAtOnce; i++ {
			select {
			case <-entered:
			case <-time.After(aheadSafety):
				close(release)
				t.Fatalf("only %d steps were running at once; want %d", i, eachAtOnce)
			}
		}
		// The cap is full. Wait until every goroutine has run as far as it can, which
		// gives a further step every chance to start; none does.
		synctest.Wait()
		if running.Load() != eachAtOnce {
			t.Errorf("%d steps are running while %d are held", running.Load(), eachAtOnce)
		}
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Each: %v", err)
			}
		case <-time.After(aheadSafety):
			t.Fatal("Each did not finish")
		}
		if most.Load() != eachAtOnce {
			t.Errorf("at most %d at once; want exactly %d", most.Load(), eachAtOnce)
		}
	})
}

// The speculative forms take the same rules.

func TestAheadR13GoexitInASpeculatedStep(t *testing.T) {
	ctx := context.Background()
	decideYes := func(l *aheadLog) Step[bool] { return l.decide(25*time.Millisecond, true, nil) }
	decideNo := func(l *aheadLog) Step[bool] { return l.decide(25*time.Millisecond, false, nil) }
	cases := []struct {
		name    string
		run     func(l *aheadLog) (string, error)
		want    string
		wantErr bool
	}{
		{"BranchAhead, yes ends its goroutine and is chosen", func(l *aheadLog) (string, error) {
			return BranchAhead(ctx, decideYes(l), aheadGoexit[string](), l.step("no", 25*time.Millisecond, "no", nil))
		}, "", true},
		{"BranchAhead, yes ends its goroutine and no is chosen", func(l *aheadLog) (string, error) {
			return BranchAhead(ctx, decideNo(l), aheadGoexit[string](), l.step("no", 25*time.Millisecond, "no", nil))
		}, "no", false},
		{"BranchBoth, yes ends its goroutine and is chosen", func(l *aheadLog) (string, error) {
			return BranchBoth(ctx, decideYes(l), aheadGoexit[string](), l.step("no", 25*time.Millisecond, "no", nil))
		}, "", true},
		{"BranchBoth, no ends its goroutine and is chosen", func(l *aheadLog) (string, error) {
			return BranchBoth(ctx, decideNo(l), l.step("yes", 25*time.Millisecond, "yes", nil), aheadGoexit[string]())
		}, "", true},
		{"BranchBoth, no ends its goroutine and yes is chosen", func(l *aheadLog) (string, error) {
			return BranchBoth(ctx, decideYes(l), l.step("yes", 25*time.Millisecond, "yes", nil), aheadGoexit[string]())
		}, "yes", false},
		{"RouteAhead, the likely route ends its goroutine and is named", func(l *aheadLog) (string, error) {
			return RouteAhead(ctx, l.pick(25*time.Millisecond, "a", nil), "a", To("a", aheadGoexit[string]()), To("b", l.step("b", 25*time.Millisecond, "b", nil)))
		}, "", true},
		{"RouteAhead, the likely route ends its goroutine and another is named", func(l *aheadLog) (string, error) {
			return RouteAhead(ctx, l.pick(25*time.Millisecond, "b", nil), "a", To("a", aheadGoexit[string]()), To("b", l.step("b", 25*time.Millisecond, "b", nil)))
		}, "b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var l aheadLog
			got, err := tc.run(&l)
			if tc.wantErr {
				aheadWantClassifiedFailure(t, err)
			} else if err != nil {
				t.Errorf("err = %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			aheadQuiet(t, &l)
		})
	}
}

// A decision that ends the caller's goroutine, which the library cannot turn
// into an error, still leaves nothing running behind it.
func TestAheadR13GoexitInTheDecisionLeavesNothingRunning(t *testing.T) {
	var l aheadLog
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_, _ = BranchAhead(context.Background(), aheadGoexit[bool](), l.step("yes", aheadFar, "yes", nil), l.step("no", aheadFar, "no", nil))
	}()
	select {
	case <-ended:
	case <-time.After(aheadSafety):
		t.Fatal("the goroutine did not end")
	}
	aheadQuiet(t, &l)
	if !l.seen("yes cancelled") {
		t.Errorf("the speculative step was not cancelled: %v", l.names())
	}
}

// A context that is over is classified in the speculative forms as well.
func TestAheadR13SpeculativeFormsClassifyAContextThatIsOver(t *testing.T) {
	over, cancel := context.WithCancel(context.Background())
	cancel()
	var l aheadLog
	cases := []struct {
		name string
		run  func() error
	}{
		{"BranchAhead", func() error {
			_, err := BranchAhead(over, l.decide(aheadWait, true, nil), l.step("yes", aheadWait, "y", nil), l.step("no", aheadWait, "n", nil))
			return err
		}},
		{"BranchBoth", func() error {
			_, err := BranchBoth(over, l.decide(aheadWait, true, nil), l.step("yes", aheadWait, "y", nil), l.step("no", aheadWait, "n", nil))
			return err
		}},
		{"RouteAhead", func() error {
			_, err := RouteAhead(over, l.pick(aheadWait, "a", nil), "a", To("a", l.step("a", aheadWait, "a", nil)))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if !errors.Is(err, context.Canceled) || types.KindOf(err) == types.KindUnknown {
				t.Errorf("err = %v (kind %v)", err, types.KindOf(err))
			}
		})
	}
	aheadQuiet(t, &l)
}

// The same combinators used at once from many goroutines, with steps that end
// their goroutines, panic and fail, leave nothing behind and answer each caller
// for itself.
func TestAheadR13ConcurrentFailures(t *testing.T) {
	var wait sync.WaitGroup
	var bad atomic.Int32
	var l aheadLog
	for g := 0; g < 50; g++ {
		wait.Add(1)
		go func(g int) {
			defer wait.Done()
			var err error
			switch g % 3 {
			case 0:
				_, err = Gather(context.Background(), aheadGoexit[string](), l.step("a", 5*time.Millisecond, "a", nil))
			case 1:
				_, err = BranchBoth(context.Background(), l.decide(5*time.Millisecond, true, nil), aheadGoexit[string](), l.step("no", 5*time.Millisecond, "no", nil))
			default:
				_, err = Each(context.Background(), []int{1, 2, 3}, func(context.Context, int) (int, error) { panic("7731") })
			}
			if types.KindOf(err) != types.KindInvariantViolation {
				bad.Add(1)
			}
		}(g)
	}
	wait.Wait()
	if bad.Load() != 0 {
		t.Errorf("%d of 50 calls did not report the step that did not return", bad.Load())
	}
	aheadQuiet(t, &l)
}
