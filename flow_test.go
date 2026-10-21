package schemaflux_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// errFlowStep is the failure a step of these tests returns. A test that compares
// what comes back with == is holding the library to returning a caller's error
// as it was given.
var errFlowStep = errors.New("the step failed")

// flowReturns is a step that returns value.
func flowReturns[T any](value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) { return value, nil }
}

// flowFails is a step that fails with err.
func flowFails[T any](err error) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		var zero T
		return zero, err
	}
}

// flowCounts is a step that counts its runs and returns value.
func flowCounts[T any](runs *atomic.Int32, value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		runs.Add(1)
		return value, nil
	}
}

// flowWantKind fails the test unless err is the library's *Error of kind want,
// which errors.Is must also report.
func flowWantKind(t *testing.T, err error, want schemaflux.Kind) *schemaflux.Error {
	t.Helper()
	var got *schemaflux.Error
	if !errors.As(err, &got) {
		t.Fatalf("err = %v (%T), want a *schemaflux.Error of kind %v", err, err, want)
	}
	if got.Kind != want {
		t.Fatalf("err = %v, kind %v, want %v", err, got.Kind, want)
	}
	if !errors.Is(err, want) {
		t.Errorf("errors.Is(%v, %v) is false", err, want)
	}
	return got
}

// flowNoLeak notes the goroutines there are now, inside a synctest bubble, and
// returns the function that fails the test when a call left more behind. It is
// the check that no goroutine outlives a call.
func flowNoLeak(t *testing.T) func() {
	t.Helper()
	synctest.Wait()
	before := runtime.NumGoroutine()
	return func() {
		t.Helper()
		synctest.Wait()
		// The runtime's finalizer and cleanup goroutine counts while it runs a
		// callback, so a count above the first one is looked at again before it is
		// believed. A goroutine the call left behind is blocked or running for
		// good, and the count stays up however many times the others are let run.
		after := runtime.NumGoroutine()
		for tries := 0; after > before && tries < 2000; tries++ {
			runtime.Gosched()
			synctest.Wait()
			after = runtime.NumGoroutine()
		}
		if after > before {
			stacks := make([]byte, 1<<16)
			stacks = stacks[:runtime.Stack(stacks, true)]
			t.Errorf("%d goroutines before the call and %d after it:\n%s", before, after, stacks)
		}
	}
}

// flowDone returns contexts that are already over, one cancelled and one past its
// deadline, with the kind each is expected to give.
func flowDone() []struct {
	name string
	ctx  context.Context
	kind schemaflux.Kind
	is   error
} {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	stop()
	return []struct {
		name string
		ctx  context.Context
		kind schemaflux.Kind
		is   error
	}{
		{"cancelled", cancelled, schemaflux.ErrCanceled, context.Canceled},
		{"past its deadline", expired, schemaflux.ErrTimeout, context.DeadlineExceeded},
	}
}

// flowContextKey is the key of a value the tests put on a context to see that a
// step is handed the context it was given.
type flowContextKey struct{}

func TestBranchRunsThenWhenYesAndOtherwiseWhenNo(t *testing.T) {
	cases := []struct {
		name            string
		yes             bool
		then, otherwise schemaflux.Step[string]
		want            string
	}{
		{"yes", true, flowReturns("then"), flowReturns("otherwise"), "then"},
		{"no", false, flowReturns("then"), flowReturns("otherwise"), "otherwise"},
		{"yes, and the other would fail", true, flowReturns("then"), flowFails[string](errFlowStep), "then"},
		{"no, and the other would fail", false, flowFails[string](errFlowStep), flowReturns("otherwise"), "otherwise"},
		{"yes, with an empty answer", true, flowReturns(""), flowReturns("otherwise"), ""},
		{"no, with an empty answer", false, flowReturns("then"), flowReturns(""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := schemaflux.Branch(context.Background(), tc.yes, tc.then, tc.otherwise)
			if err != nil || got != tc.want {
				t.Errorf("Branch = %q, %v; want %q, nil", got, err, tc.want)
			}
		})
	}
}

func TestBranchRefusesANilStepWhicheverItWouldHaveChosenAndCallsNoStep(t *testing.T) {
	var ran atomic.Int32
	cases := []struct {
		name            string
		yes             bool
		then, otherwise schemaflux.Step[string]
	}{
		{"then is nil and the condition holds", true, nil, flowCounts(&ran, "otherwise")},
		{"then is nil and the condition does not hold", false, nil, flowCounts(&ran, "otherwise")},
		{"otherwise is nil and the condition does not hold", false, flowCounts(&ran, "then"), nil},
		{"otherwise is nil and the condition holds", true, flowCounts(&ran, "then"), nil},
		{"neither given, yes", true, nil, nil},
		{"neither given, no", false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := schemaflux.Branch(context.Background(), tc.yes, tc.then, tc.otherwise)
			e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "branch" || got != "" || ran.Load() != 0 {
				t.Errorf("Op = %q, got = %q, steps run = %d; want a refusal and no step called", e.Op, got, ran.Load())
			}
		})
	}
}

func TestBranchRunsAStepThatReturnsTheZeroValueForOtherwiseNothing(t *testing.T) {
	nothing := func(context.Context) (string, error) { return "", nil }
	for _, yes := range []bool{true, false} {
		got, err := schemaflux.Branch(context.Background(), yes, nothing, nothing)
		if err != nil || got != "" {
			t.Errorf("yes=%v: Branch = %q, %v", yes, got, err)
		}
	}
}

func TestBranchCallsOnlyTheStepItChooses(t *testing.T) {
	for _, yes := range []bool{true, false} {
		var then, otherwise atomic.Int32
		_, err := schemaflux.Branch(context.Background(), yes, flowCounts(&then, "a"), flowCounts(&otherwise, "b"))
		if err != nil {
			t.Fatal(err)
		}
		wantThen, wantOtherwise := int32(1), int32(0)
		if !yes {
			wantThen, wantOtherwise = 0, 1
		}
		if then.Load() != wantThen || otherwise.Load() != wantOtherwise {
			t.Errorf("yes=%v: then ran %d times and otherwise %d; want %d and %d", yes, then.Load(), otherwise.Load(), wantThen, wantOtherwise)
		}
	}
}

func TestBranchReturnsTheStepsErrorAsItWasGiven(t *testing.T) {
	wrapped := fmt.Errorf("while handling: %w", errFlowStep)
	own := &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Op: "extract"}
	for _, failure := range []error{errFlowStep, wrapped, own, context.Canceled} {
		for _, yes := range []bool{true, false} {
			got, err := schemaflux.Branch(context.Background(), yes, flowFails[string](failure), flowFails[string](failure))
			if err != failure {
				t.Errorf("yes=%v: err = %v (%T), want the very value %v", yes, err, err, failure)
			}
			if got != "" {
				t.Errorf("yes=%v: Branch returned %q beside an error", yes, got)
			}
		}
	}
}

func TestBranchReturnsNothingBesideAnError(t *testing.T) {
	type answer struct{ Total int }
	step := func(context.Context) (answer, error) { return answer{Total: 5}, errFlowStep }
	got, err := schemaflux.Branch(context.Background(), true, step, step)
	if err != errFlowStep || got != (answer{}) {
		t.Errorf("Branch = %+v, %v; want the zero value and the step's error", got, err)
	}
}

func TestBranchHandsTheStepTheContextItWasGiven(t *testing.T) {
	ctx := context.WithValue(context.Background(), flowContextKey{}, "mine")
	for _, yes := range []bool{true, false} {
		read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(flowContextKey{})), nil }
		got, err := schemaflux.Branch(ctx, yes, read, read)
		if err != nil || got != "mine" {
			t.Errorf("yes=%v: Branch = %q, %v", yes, got, err)
		}
	}
}

func TestBranchRefusesANilContextAndCallsNoStep(t *testing.T) {
	var ran atomic.Int32
	var nilContext context.Context
	got, err := schemaflux.Branch(nilContext, true, flowCounts(&ran, "a"), flowCounts(&ran, "b"))
	e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
	if e.Op != "branch" || got != "" || ran.Load() != 0 {
		t.Errorf("Op = %q, got = %q, steps run = %d", e.Op, got, ran.Load())
	}
}

func TestBranchRefusesAContextThatIsAlreadyOverAndCallsNoStep(t *testing.T) {
	for _, done := range flowDone() {
		t.Run(done.name, func(t *testing.T) {
			var ran atomic.Int32
			got, err := schemaflux.Branch(done.ctx, true, flowCounts(&ran, "a"), flowCounts(&ran, "b"))
			flowWantKind(t, err, done.kind)
			if !errors.Is(err, done.is) {
				t.Errorf("errors.Is(err, %v) is false for %v", done.is, err)
			}
			if got != "" || ran.Load() != 0 {
				t.Errorf("got = %q, steps run = %d", got, ran.Load())
			}
		})
	}
}

func TestBranchLetsAPanicInTheStepReachTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the step" {
			t.Errorf("recovered %v, want the step's own panic", recovered)
		}
	}()
	_, _ = schemaflux.Branch(context.Background(), false, flowReturns("then"), func(context.Context) (string, error) { panic("from the step") })
	t.Error("Branch returned after a panic in the step")
}

func TestBranchNestsAndKeepsTheInnermostError(t *testing.T) {
	inner := func(context.Context) (string, error) { return "", errFlowStep }
	outer := func(ctx context.Context) (string, error) {
		return schemaflux.Branch(ctx, false, flowReturns("then"), inner)
	}
	got, err := schemaflux.Branch(context.Background(), true, outer, flowReturns("otherwise"))
	if err != errFlowStep || got != "" {
		t.Errorf("Branch = %q, %v; want the innermost error as it was given", got, err)
	}
}

func TestToBuildsARoute(t *testing.T) {
	step := flowReturns("billing")
	route := schemaflux.To("billing", step)
	if route.Label != "billing" || route.Run == nil {
		t.Fatalf("route = %+v", route)
	}
	if got, _ := route.Run(context.Background()); got != "billing" {
		t.Errorf("the route's step returned %q", got)
	}
	if empty := schemaflux.To[string]("", nil); empty.Label != "" || empty.Run != nil {
		t.Errorf("To builds a route as given even when it is not usable, and Select refuses it: %+v", empty)
	}
}

func flowRoutes(ran *[3]atomic.Int32) []schemaflux.Route[string] {
	return []schemaflux.Route[string]{
		schemaflux.To("billing", flowCounts(&ran[0], "to billing")),
		schemaflux.To("technical", flowCounts(&ran[1], "to technical")),
		schemaflux.To("shipping", flowCounts(&ran[2], "to shipping")),
	}
}

func TestSelectRunsTheRouteThatIsNamed(t *testing.T) {
	cases := []struct {
		label string
		want  string
		ran   int
	}{
		{"billing", "to billing", 0},
		{"technical", "to technical", 1},
		{"shipping", "to shipping", 2},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var ran [3]atomic.Int32
			got, err := schemaflux.Select(context.Background(), tc.label, flowRoutes(&ran)...)
			if err != nil || got != tc.want {
				t.Fatalf("Select = %q, %v; want %q", got, err, tc.want)
			}
			for i := range ran {
				want := int32(0)
				if i == tc.ran {
					want = 1
				}
				if ran[i].Load() != want {
					t.Errorf("route %d ran %d times, want %d", i, ran[i].Load(), want)
				}
			}
		})
	}
}

func TestSelectRefusesALabelThatIsNoRoutesAndRunsNothing(t *testing.T) {
	for _, label := range []string{"returns", "", "Billing", " billing ", "bill", "billing\n", "technical,shipping"} {
		t.Run(fmt.Sprintf("%q", label), func(t *testing.T) {
			var ran [3]atomic.Int32
			got, err := schemaflux.Select(context.Background(), label, flowRoutes(&ran)...)
			e := flowWantKind(t, err, schemaflux.ErrInvalidRequest)
			if e.Op != "select" || got != "" {
				t.Errorf("Op = %q, got = %q", e.Op, got)
			}
			for i := range ran {
				if ran[i].Load() != 0 {
					t.Errorf("route %d ran for a label that is none of them", i)
				}
			}
		})
	}
}

func TestSelectRefusesRoutesItCannotChooseBetweenBeforeRunningAny(t *testing.T) {
	var ran atomic.Int32
	run := flowCounts(&ran, "x")
	cases := []struct {
		name   string
		routes []schemaflux.Route[string]
	}{
		{"no routes", nil},
		{"a blank label", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("", run)}},
		{"a label of spaces", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("  ", run)}},
		{"a repeated label", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("a", run)}},
		{"a label repeated with space around it", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To("a ", run)}},
		{"a route with nothing to run", []schemaflux.Route[string]{schemaflux.To("a", run), schemaflux.To[string]("b", nil)}},
		{"the first route with nothing to run", []schemaflux.Route[string]{schemaflux.To[string]("a", nil), schemaflux.To("b", run)}},
	}
	for _, tc := range cases {
		for _, label := range []string{"a", "b"} {
			t.Run(tc.name+", label "+label, func(t *testing.T) {
				got, err := schemaflux.Select(context.Background(), label, tc.routes...)
				flowWantKind(t, err, schemaflux.ErrInvalidRequest)
				if got != "" || ran.Load() != 0 {
					t.Errorf("got = %q, routes run = %d", got, ran.Load())
				}
			})
		}
	}
}

func TestSelectAcceptsASingleRoute(t *testing.T) {
	got, err := schemaflux.Select(context.Background(), "only", schemaflux.To("only", flowReturns(7)))
	if err != nil || got != 7 {
		t.Errorf("Select = %d, %v", got, err)
	}
}

func TestSelectReturnsTheRoutesErrorAsItWasGiven(t *testing.T) {
	routes := []schemaflux.Route[string]{
		schemaflux.To("a", flowFails[string](errFlowStep)),
		schemaflux.To("b", flowReturns("b")),
	}
	got, err := schemaflux.Select(context.Background(), "a", routes...)
	if err != errFlowStep || got != "" {
		t.Errorf("Select = %q, %v; want the route's error as it was given", got, err)
	}
	own := &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded}
	_, err = schemaflux.Select(context.Background(), "a", schemaflux.To("a", flowFails[string](own)))
	if err != error(own) {
		t.Errorf("err = %v, want the very *Error the route returned", err)
	}
}

func TestSelectErrorsNameNothingOfTheCaller(t *testing.T) {
	const canary = "label-4471-canary"
	_, err := schemaflux.Select(context.Background(), canary, schemaflux.To("billing", flowReturns("x")))
	flowWantKind(t, err, schemaflux.ErrInvalidRequest)
	if strings.Contains(err.Error(), canary) || strings.Contains(fmt.Sprintf("%+v", err), canary) {
		t.Errorf("the label is in the error: %v", err)
	}
	_, err = schemaflux.Select(context.Background(), "x", schemaflux.To[string](canary, nil))
	if strings.Contains(err.Error(), canary) {
		t.Errorf("a route's label is in the error: %v", err)
	}
}

func TestSelectRefusesANilContextAndAContextThatIsOver(t *testing.T) {
	var ran [3]atomic.Int32
	var nilContext context.Context
	_, err := schemaflux.Select(nilContext, "billing", flowRoutes(&ran)...)
	flowWantKind(t, err, schemaflux.ErrInvalidRequest)
	for _, done := range flowDone() {
		_, err := schemaflux.Select(done.ctx, "billing", flowRoutes(&ran)...)
		flowWantKind(t, err, done.kind)
		if !errors.Is(err, done.is) {
			t.Errorf("%s: errors.Is(err, %v) is false", done.name, done.is)
		}
	}
	for i := range ran {
		if ran[i].Load() != 0 {
			t.Errorf("route %d ran", i)
		}
	}
}

func TestSelectHandsTheRouteTheContextItWasGiven(t *testing.T) {
	ctx := context.WithValue(context.Background(), flowContextKey{}, "mine")
	read := func(ctx context.Context) (string, error) { return fmt.Sprint(ctx.Value(flowContextKey{})), nil }
	got, err := schemaflux.Select(ctx, "a", schemaflux.To("a", read), schemaflux.To("b", read))
	if err != nil || got != "mine" {
		t.Errorf("Select = %q, %v", got, err)
	}
}

func TestSelectLetsAPanicInTheRouteReachTheCaller(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "from the route" {
			t.Errorf("recovered %v, want the route's own panic", recovered)
		}
	}()
	_, _ = schemaflux.Select(context.Background(), "a", schemaflux.To("a", func(context.Context) (string, error) { panic("from the route") }))
	t.Error("Select returned after a panic in the route")
}

func TestSelectOverBranchesKeepsTheInnermostError(t *testing.T) {
	branch := func(ctx context.Context) (string, error) {
		return schemaflux.Branch(ctx, true, flowFails[string](errFlowStep), flowReturns(""))
	}
	_, err := schemaflux.Select(context.Background(), "a", schemaflux.To("a", branch))
	if err != errFlowStep {
		t.Errorf("err = %v, want the innermost error as it was given", err)
	}
}

func TestToBuildsARouteOfTheLabelAndTheStepItIsGiven(t *testing.T) {
	cases := []struct {
		name  string
		label string
		step  schemaflux.Step[string]
	}{
		{"a word", "billing", flowReturns("a")},
		{"a label with space in it", "customer billing", flowReturns("b")},
		{"a label with space around it", " billing ", flowReturns("c")},
		{"a label that is not text a person wrote", "\x00\n\t", flowReturns("d")},
		{"a label in another script", "計費", flowReturns("e")},
		{"a long label", strings.Repeat("x", 4096), flowReturns("f")},
		{"a blank label, which Select refuses and To does not", "", flowReturns("g")},
		{"a step that fails", "fails", flowFails[string](errFlowStep)},
		{"no step, which Select refuses and To does not", "none", nil},
		{"a label like a number", "42", flowReturns("h")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := schemaflux.To(tc.label, tc.step)
			if route.Label != tc.label || (route.Run == nil) != (tc.step == nil) {
				t.Fatalf("route = %+v", route)
			}
			if tc.step == nil {
				return
			}
			want, wantErr := tc.step(context.Background())
			got, err := route.Run(context.Background())
			if got != want || err != wantErr {
				t.Errorf("the route's step returned %q, %v; the step it was given returns %q, %v", got, err, want, wantErr)
			}
		})
	}
}

// Only a function that asks a model adds a Record, so a recorder on the context
// sees nothing of the control flow: the steps are the caller's.
func TestTheControlFlowFunctionsAddNoRecord(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var recorder schemaflux.Recorder
		ctx := schemaflux.WithRecorder(context.Background(), &recorder)
		step := flowReturns("x")
		_, _ = schemaflux.Branch(ctx, true, step, step)
		_, _ = schemaflux.Select(ctx, "a", schemaflux.To("a", step))
		_, _ = schemaflux.Gather(ctx, step, step)
		_, _, _ = schemaflux.Gather2(ctx, step, step)
		_, _, _, _ = schemaflux.Gather3(ctx, step, step, step)
		_, _, _, _, _ = schemaflux.Gather4(ctx, step, step, step, step)
		_, _ = schemaflux.Each(ctx, []int{1, 2, 3}, func(context.Context, int) (int, error) { return 0, nil })
		_, _ = schemaflux.Branch(ctx, true, flowFails[string](errFlowStep), flowReturns(""))
		_, _ = schemaflux.Gather(ctx, flowFails[string](errFlowStep))
		if records := recorder.Records(); len(records) != 0 {
			t.Errorf("%d records for control flow that asked no model", len(records))
		}
	})
}
