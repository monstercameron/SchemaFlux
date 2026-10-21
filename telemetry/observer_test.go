package telemetry_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	wrapped "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// The library emits through the package wrapped here: its call path calls
// wrapped.ObserveOperationStart, ObserveOperationNote and ObserveOperationEnd,
// and an observer registered through this package receives what they emit.
// These tests drive those three directly, as the call path does, because the
// client that makes a call through the root does not exist yet.

// stampKey is the context key of the stamp a recording observer puts on the
// context it returns.
type stampKey struct{}

func stampOf(ctx context.Context) string {
	stamp, _ := ctx.Value(stampKey{}).(string)
	return stamp
}

// recorder is an Observer that keeps what it was given. With a stamp it returns
// a context carrying the stamp, and records the stamp of the context each
// finish and note arrived on, so a test can tell whose context it was handed.
type recorder struct {
	mu         sync.Mutex
	stamp      string
	started    []telemetry.Event
	startedOn  []string
	finished   []telemetry.Result
	finishedOn []string
	noted      []telemetry.Note
	notedOn    []string
	log        *orderLog
}

// orderLog is a shared list of what happened and to whom, in the order it
// happened.
type orderLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *orderLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *orderLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

func (r *recorder) Started(ctx context.Context, e telemetry.Event) context.Context {
	r.mu.Lock()
	r.started = append(r.started, e)
	r.startedOn = append(r.startedOn, stampOf(ctx))
	r.mu.Unlock()
	if r.log != nil {
		r.log.add(r.stamp + " started " + e.Scope.String())
	}
	if r.stamp == "" {
		return ctx
	}
	return context.WithValue(ctx, stampKey{}, r.stamp+"/"+e.Scope.String())
}

func (r *recorder) Finished(ctx context.Context, res telemetry.Result) {
	r.mu.Lock()
	r.finished = append(r.finished, res)
	r.finishedOn = append(r.finishedOn, stampOf(ctx))
	r.mu.Unlock()
	if r.log != nil {
		r.log.add(r.stamp + " finished " + res.Scope.String())
	}
}

func (r *recorder) Noted(ctx context.Context, n telemetry.Note) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noted = append(r.noted, n)
	r.notedOn = append(r.notedOn, stampOf(ctx))
}

func (r *recorder) counts() (started, finished, noted int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started), len(r.finished), len(r.noted)
}

// quiet is an Observer that is not a NoteObserver.
type quiet struct{ inner *recorder }

func (q quiet) Started(ctx context.Context, e telemetry.Event) context.Context {
	return q.inner.Started(ctx, e)
}
func (q quiet) Finished(ctx context.Context, res telemetry.Result) { q.inner.Finished(ctx, res) }

// withNoObserver fails a test that begins with an observer some earlier test
// left registered, since every count below would be off by its events.
func withNoObserver(t *testing.T) {
	t.Helper()
	if wrapped.HasObserver() {
		t.Fatal("an observer is registered before the test began; an earlier test did not restore")
	}
	t.Cleanup(func() {
		if wrapped.HasObserver() {
			t.Error("the test left an observer registered")
		}
	})
}

// request and operation are the two events a call of an operation emits.
func request(id string) wrapped.OperationEvent {
	return wrapped.OperationEvent{Operation: "extract", Provider: "openai", Model: "gpt-5", RequestID: id}
}

func operation() wrapped.OperationEvent {
	return wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "extract"}
}

func TestARegisteredObserverReceivesTheStartAndTheFinishOfAUnit(t *testing.T) {
	cases := []struct {
		name  string
		scope wrapped.Scope
		want  telemetry.Scope
	}{
		{"a request", wrapped.ScopeRequest, telemetry.ScopeRequest},
		{"an operation", wrapped.ScopeOperation, telemetry.ScopeOperation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			rec := &recorder{stamp: "a"}
			restore := telemetry.RegisterObserver(rec)
			defer restore()

			ctx := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: tc.scope, Operation: "sort", Model: "m"})
			wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Scope: tc.scope, Operation: "sort", Attempts: 1})

			started, finished, _ := rec.counts()
			if started != 1 || finished != 1 {
				t.Fatalf("%d starts and %d finishes, want one of each", started, finished)
			}
			if rec.started[0].Scope != tc.want || rec.finished[0].Scope != tc.want {
				t.Errorf("scopes %v and %v, want %v", rec.started[0].Scope, rec.finished[0].Scope, tc.want)
			}
			if rec.started[0].Operation != "sort" || rec.finished[0].Operation != "sort" {
				t.Errorf("operation names %q and %q, want sort", rec.started[0].Operation, rec.finished[0].Operation)
			}
			if rec.finishedOn[0] != "a/"+tc.want.String() {
				t.Errorf("the finish arrived on the context stamped %q, want the one the start returned", rec.finishedOn[0])
			}
		})
	}
}

func TestRegisteringNilRegistersNothing(t *testing.T) {
	withNoObserver(t)
	restore := telemetry.RegisterObserver(nil)
	if restore == nil {
		t.Fatal("RegisterObserver(nil) returned a nil restore")
	}
	if wrapped.HasObserver() {
		t.Error("a nil observer was registered")
	}
	restore()
	restore()
}

func TestARestoreRemovesItsOwnRegistrationAndNoOther(t *testing.T) {
	withNoObserver(t)
	first, second, third := &recorder{stamp: "1"}, &recorder{stamp: "2"}, &recorder{stamp: "3"}
	restoreFirst := telemetry.RegisterObserver(first)
	restoreSecond := telemetry.RegisterObserver(second)
	restoreThird := telemetry.RegisterObserver(third)
	defer restoreFirst()
	defer restoreThird()

	restoreSecond()
	ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{})

	for _, tc := range []struct {
		rec  *recorder
		want int
	}{{first, 1}, {second, 0}, {third, 1}} {
		if started, finished, _ := tc.rec.counts(); started != tc.want || finished != tc.want {
			t.Errorf("observer %s saw %d starts and %d finishes, want %d of each", tc.rec.stamp, started, finished, tc.want)
		}
	}
}

func TestRestoresRunInAnyOrderAndLeaveNothingRegistered(t *testing.T) {
	orders := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
		{0, 0, 1, 1, 2, 2}, {2, 2, 1, 0, 0, 1},
	}
	for _, order := range orders {
		t.Run(fmtOrder(order), func(t *testing.T) {
			withNoObserver(t)
			recs := []*recorder{{stamp: "0"}, {stamp: "1"}, {stamp: "2"}}
			var restores []func()
			for _, rec := range recs {
				restores = append(restores, telemetry.RegisterObserver(rec))
			}
			for _, index := range order {
				restores[index]()
				ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
				wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{})
			}
			if wrapped.HasObserver() {
				t.Error("an observer is still registered after every restore ran")
			}
			// An emission is made after each restore, so an observer saw one
			// for every restore that ran before its own.
			for position, rec := range recs {
				want := slices.Index(order, position)
				if started, finished, _ := rec.counts(); started != want || finished != want {
					t.Errorf("observer %d saw %d starts and %d finishes, want %d of each", position, started, finished, want)
				}
			}
		})
	}
}

func fmtOrder(order []int) string {
	var out []string
	for _, index := range order {
		out = append(out, strconv.Itoa(index))
	}
	return strings.Join(out, "-")
}

func TestRegisteringTheSameObserverTwiceIsTwoRegistrations(t *testing.T) {
	withNoObserver(t)
	rec := &recorder{stamp: "x"}
	restoreFirst := telemetry.RegisterObserver(rec)
	restoreSecond := telemetry.RegisterObserver(rec)
	defer restoreSecond()

	ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{})
	if started, _, _ := rec.counts(); started != 2 {
		t.Fatalf("%d starts with two registrations, want 2", started)
	}

	restoreFirst()
	restoreFirst()
	ctx = wrapped.ObserveOperationStart(context.Background(), request("r"))
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{})
	if started, _, _ := rec.counts(); started != 3 {
		t.Errorf("%d starts after one of two registrations was restored twice, want 3: the second restore must remove nothing more", started)
	}
}

func TestTwoObserversEachKeepTheirOwnTreeOfContexts(t *testing.T) {
	withNoObserver(t)
	log := &orderLog{}
	first := &recorder{stamp: "first", log: log}
	second := &recorder{stamp: "second", log: log}
	defer telemetry.RegisterObserver(first)()
	defer telemetry.RegisterObserver(second)()

	caller := context.WithValue(context.Background(), stampKey{}, "caller")
	operationCtx := wrapped.ObserveOperationStart(caller, operation())
	requestCtx := wrapped.ObserveOperationStart(operationCtx, request("r1"))
	wrapped.ObserveOperationEnd(requestCtx, wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(operationCtx, wrapped.OperationResult{Scope: wrapped.ScopeOperation})

	// The second observer is started on the context the first returned, so it
	// sees the first's stamp where the first sees the caller's.
	if first.startedOn[0] != "caller" || second.startedOn[0] != "first/operation" {
		t.Errorf("the operation start gave the first observer %q and the second %q", first.startedOn[0], second.startedOn[0])
	}
	if first.startedOn[1] != "second/operation" || second.startedOn[1] != "first/request" {
		t.Errorf("the request start gave the first observer %q and the second %q, want each the context its own operation start returned",
			first.startedOn[1], second.startedOn[1])
	}
	// Each finish carries the context that observer's own start returned.
	want := map[string][]string{"first": {"first/request", "first/operation"}, "second": {"second/request", "second/operation"}}
	if !slices.Equal(first.finishedOn, want["first"]) || !slices.Equal(second.finishedOn, want["second"]) {
		t.Errorf("finishes arrived on %v and %v, want %v and %v", first.finishedOn, second.finishedOn, want["first"], want["second"])
	}
	// A finish goes to the observers in the reverse of the order they started.
	wantLog := []string{
		"first started operation", "second started operation",
		"first started request", "second started request",
		"second finished request", "first finished request",
		"second finished operation", "first finished operation",
	}
	if got := log.all(); !slices.Equal(got, wantLog) {
		t.Errorf("order of events:\n got  %v\n want %v", got, wantLog)
	}
}

func TestAPanicInOneObserverReachesNeitherTheCallNorAnotherObserver(t *testing.T) {
	cases := []struct {
		name   string
		panics func(bad *panicker)
		// startedAfter, finishedAfter: whether the observer registered after the
		// bad one still got the event.
		badFinishes bool
	}{
		{"in Started", func(bad *panicker) { bad.onStart = true }, false},
		{"in Finished", func(bad *panicker) { bad.onFinish = true }, true},
		{"in Noted", func(bad *panicker) { bad.onNote = true }, true},
		{"in all three", func(bad *panicker) { bad.onStart, bad.onFinish, bad.onNote = true, true, true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			before, after := &recorder{stamp: "before"}, &recorder{stamp: "after"}
			bad := &panicker{}
			tc.panics(bad)
			defer telemetry.RegisterObserver(before)()
			defer telemetry.RegisterObserver(bad)()
			defer telemetry.RegisterObserver(after)()

			ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
			wrapped.ObserveOperationNote(ctx, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1})
			wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Attempts: 2})

			for _, rec := range []*recorder{before, after} {
				started, finished, noted := rec.counts()
				if started != 1 || finished != 1 || noted != 1 {
					t.Errorf("observer %s saw %d starts, %d finishes and %d notes, want one of each: another observer's panic reached it",
						rec.stamp, started, finished, noted)
				}
			}
			if got := bad.finishes.Load() == 1; got != tc.badFinishes {
				t.Errorf("the observer that panicked got a finish = %v, want %v: one whose start panicked has nothing to finish", got, tc.badFinishes)
			}
		})
	}
}

// panicker panics where it is told to, with a value that holds the sort of text
// a host's formatting bug puts there.
type panicker struct {
	onStart, onFinish, onNote bool
	finishes                  atomic.Int32
}

func (p *panicker) Started(ctx context.Context, _ telemetry.Event) context.Context {
	if p.onStart {
		panic("MARK_PANIC the caller's invoice text")
	}
	return ctx
}

func (p *panicker) Finished(context.Context, telemetry.Result) {
	p.finishes.Add(1)
	if p.onFinish {
		panic("MARK_PANIC the caller's invoice text")
	}
}

func (p *panicker) Noted(context.Context, telemetry.Note) {
	if p.onNote {
		panic("MARK_PANIC the caller's invoice text")
	}
}

func TestAFinishReachesOnlyTheObserversThatSawTheStart(t *testing.T) {
	withNoObserver(t)
	early, late := &recorder{stamp: "early"}, &recorder{stamp: "late"}
	restoreEarly := telemetry.RegisterObserver(early)

	ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
	restoreLate := telemetry.RegisterObserver(late) // registered while the unit is open
	defer restoreLate()
	restoreEarly() // removed while the unit is open
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Attempts: 1})

	if started, finished, _ := early.counts(); started != 1 || finished != 1 {
		t.Errorf("the observer removed mid-unit saw %d starts and %d finishes, want 1 and 1: what it saw begin it must see end", started, finished)
	}
	if started, finished, _ := late.counts(); started != 0 || finished != 0 {
		t.Errorf("the observer registered mid-unit saw %d starts and %d finishes, want none: it never saw the start", started, finished)
	}

	// A second finish of the same unit is not delivered again.
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Attempts: 1})
	if _, finished, _ := early.counts(); finished != 1 {
		t.Errorf("a second finish was delivered: %d finishes", finished)
	}
}

// unitLog is what one observer in the churn test saw of each unit, by the id of
// the unit.
type unitLog struct {
	mu       sync.Mutex
	started  map[string]int
	finished map[string]int
}

func newUnitLog() *unitLog { return &unitLog{started: map[string]int{}, finished: map[string]int{}} }

func (u *unitLog) Started(ctx context.Context, e telemetry.Event) context.Context {
	u.mu.Lock()
	u.started[e.RequestID]++
	u.mu.Unlock()
	return ctx
}

func (u *unitLog) Finished(_ context.Context, res telemetry.Result) {
	u.mu.Lock()
	u.finished[res.Operation]++
	u.mu.Unlock()
}

func TestEveryStartHasExactlyOneFinishWhileObserversComeAndGo(t *testing.T) {
	withNoObserver(t)

	var mu sync.Mutex
	var logs []*unitLog
	stop := make(chan struct{})
	var callers sync.WaitGroup
	var units atomic.Int64
	for worker := range 50 {
		callers.Go(func() {
			for sequence := 0; ; sequence++ {
				select {
				case <-stop:
					return
				default:
				}
				// The id of the unit rides on the request id of the start and on
				// the operation name of the finish, so a log can match them.
				id := "w" + strconv.Itoa(worker) + "-" + strconv.Itoa(sequence)
				ctx := wrapped.ObserveOperationStart(context.Background(), wrapped.OperationEvent{Scope: wrapped.ScopeOperation, RequestID: id})
				inner := wrapped.ObserveOperationStart(ctx, wrapped.OperationEvent{RequestID: id})
				wrapped.ObserveOperationEnd(inner, wrapped.OperationResult{Operation: id})
				wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{Scope: wrapped.ScopeOperation, Operation: id})
				units.Add(1)
			}
		})
	}
	for range 40 {
		log := newUnitLog()
		mu.Lock()
		logs = append(logs, log)
		mu.Unlock()
		restore := telemetry.RegisterObserver(log)
		time.Sleep(2 * time.Millisecond)
		restore()
		time.Sleep(time.Millisecond)
	}
	close(stop)
	callers.Wait()

	if units.Load() == 0 {
		t.Fatal("no unit was emitted, so the test observed nothing")
	}
	seen := 0
	for position, log := range logs {
		for id, starts := range log.started {
			seen++
			// Both scopes carry the id, the operation's finish by name and the
			// request's too, so each start has exactly two finishes of its id.
			if got := log.finished[id]; got != starts {
				t.Errorf("observer %d: unit %s started %d times and finished %d times", position, id, starts, got)
			}
		}
		for id, finishes := range log.finished {
			if log.started[id] == 0 {
				t.Errorf("observer %d: unit %s finished %d times and never started: a finish reached an observer that did not see the start", position, id, finishes)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no observer saw any unit start; the churn did not overlap the callers")
	}
}

// addedKey is the context key of a value an observer adds to the context it
// returns.
type addedKey struct{}

// probe is an Observer that records the value of addedKey on the context it is
// given, which tells a test what the observers before it let through.
type probe struct {
	mu   sync.Mutex
	seen []any
}

func (p *probe) Started(ctx context.Context, _ telemetry.Event) context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, ctx.Value(addedKey{}))
	return ctx
}

func (p *probe) Finished(context.Context, telemetry.Result) {}

// An observer must not be able to change what a call does. The context it
// returns is used only when it descends from the one it was given.
func TestAnObserverReturningAnUnrelatedContextIsIgnored(t *testing.T) {
	type callerKey struct{}
	cases := []struct {
		name    string
		returns func(given context.Context) context.Context
		// added is what addedKey holds on the context the call runs on.
		added any
	}{
		{"context.Background", func(context.Context) context.Context { return context.Background() }, nil},
		{"context.TODO", func(context.Context) context.Context { return context.TODO() }, nil},
		{"a fresh context with a value", func(context.Context) context.Context {
			return context.WithValue(context.Background(), addedKey{}, "fresh")
		}, nil},
		{"the given context without its cancellation", func(given context.Context) context.Context {
			return context.WithValue(context.WithoutCancel(given), addedKey{}, "detached")
		}, nil},
		{"a fresh context with a later deadline", func(context.Context) context.Context {
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), addedKey{}, "fresh"), 10*time.Hour)
			_ = cancel
			return ctx
		}, nil},
		{"nil", func(context.Context) context.Context { return nil }, nil},
		{"the given context", func(given context.Context) context.Context { return given }, nil},
		{"a value added to the given context", func(given context.Context) context.Context {
			return context.WithValue(given, addedKey{}, "added")
		}, "added"},
		{"the given context made cancellable", func(given context.Context) context.Context {
			ctx, cancel := context.WithCancel(given)
			_ = cancel
			return context.WithValue(ctx, addedKey{}, "added")
		}, "added"},
		{"the given context with a shorter deadline", func(given context.Context) context.Context {
			ctx, cancel := context.WithTimeout(given, time.Minute)
			_ = cancel
			return context.WithValue(ctx, addedKey{}, "added")
		}, "added"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			defer telemetry.RegisterObserver(returning(tc.returns))()
			next := &probe{}
			defer telemetry.RegisterObserver(next)()

			deadline := time.Now().Add(time.Hour)
			caller, cancel := context.WithDeadline(context.WithValue(context.Background(), callerKey{}, "client"), deadline)
			defer cancel()

			got := wrapped.ObserveOperationStart(caller, request("r"))
			if got.Value(callerKey{}) != "client" {
				t.Error("the context the call runs on lost the caller's value")
			}
			if at, ok := got.Deadline(); !ok || at.After(deadline) {
				t.Errorf("the context the call runs on has deadline %v (set: %v), want the caller's or an earlier one", at, ok)
			}
			if got.Value(addedKey{}) != tc.added {
				t.Errorf("the value the observer added reads %v on the call's context, want %v", got.Value(addedKey{}), tc.added)
			}
			// The observer after it is handed what the call carries so far: the
			// caller's context with whatever the first let through.
			if len(next.seen) != 1 || next.seen[0] != tc.added {
				t.Errorf("the next observer was given a context holding %v, want %v", next.seen, tc.added)
			}
			cancel()
			select {
			case <-got.Done():
			default:
				t.Error("cancelling the caller's context did not cancel the context the call runs on")
			}
		})
	}
}

// returning is an Observer whose Started returns what it makes of the context
// it is given.
type returning func(given context.Context) context.Context

func (r returning) Started(ctx context.Context, _ telemetry.Event) context.Context { return r(ctx) }
func (returning) Finished(context.Context, telemetry.Result)                       {}

func TestAnUnrelatedContextStillGetsItsFinish(t *testing.T) {
	withNoObserver(t)
	var finished atomic.Int32
	defer telemetry.RegisterObserver(finishCounter{&finished, context.Background})()

	ctx := wrapped.ObserveOperationStart(context.Background(), request("r"))
	wrapped.ObserveOperationEnd(ctx, wrapped.OperationResult{})
	if finished.Load() != 1 {
		t.Errorf("an observer whose context was passed over saw %d finishes, want 1: it was told of the start", finished.Load())
	}
}

// finishCounter counts its finishes and returns the context make gives it.
type finishCounter struct {
	finished *atomic.Int32
	make     func() context.Context
}

func (f finishCounter) Started(context.Context, telemetry.Event) context.Context { return f.make() }
func (f finishCounter) Finished(context.Context, telemetry.Result)               { f.finished.Add(1) }

func TestARequestMadeUnderAnOperationIsItsChild(t *testing.T) {
	withNoObserver(t)
	rec := &recorder{stamp: "o"}
	defer telemetry.RegisterObserver(rec)()

	caller := context.WithValue(context.Background(), stampKey{}, "caller")
	operationCtx := wrapped.ObserveOperationStart(caller, operation())
	first := wrapped.ObserveOperationStart(operationCtx, request("one"))
	wrapped.ObserveOperationEnd(first, wrapped.OperationResult{Attempts: 1})
	second := wrapped.ObserveOperationStart(operationCtx, wrapped.OperationEvent{Operation: "extract", RequestID: "two", Repair: true})
	wrapped.ObserveOperationEnd(second, wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(operationCtx, wrapped.OperationResult{Scope: wrapped.ScopeOperation})

	// A request starts from the context the operation's start returned, which
	// carries the observer's stamp for the operation; the operation's own start
	// saw only the caller's.
	wantOn := []string{"caller", "o/operation", "o/operation"}
	if !slices.Equal(rec.startedOn, wantOn) {
		t.Errorf("starts arrived on %v, want %v", rec.startedOn, wantOn)
	}
	wantScopes := []telemetry.Scope{telemetry.ScopeOperation, telemetry.ScopeRequest, telemetry.ScopeRequest}
	for position, e := range rec.started {
		if e.Scope != wantScopes[position] {
			t.Errorf("start %d has scope %v, want %v", position, e.Scope, wantScopes[position])
		}
	}
	if rec.started[2].Repair != true || rec.started[1].Repair {
		t.Error("the repair flag did not arrive on the second request only")
	}
	// The operation finishes last, and finishing the operation from a context
	// that carries a finished request still finishes the operation.
	wantFinished := []telemetry.Scope{telemetry.ScopeRequest, telemetry.ScopeRequest, telemetry.ScopeOperation}
	for position, res := range rec.finished {
		if res.Scope != wantFinished[position] {
			t.Errorf("finish %d has scope %v, want %v", position, res.Scope, wantFinished[position])
		}
	}
	if len(rec.finished) != 3 {
		t.Fatalf("%d finishes, want 3", len(rec.finished))
	}
}

func TestAnOperationFinishedFromInsideARequestStillFinishesTheOperation(t *testing.T) {
	withNoObserver(t)
	rec := &recorder{stamp: "o"}
	defer telemetry.RegisterObserver(rec)()

	operationCtx := wrapped.ObserveOperationStart(context.Background(), operation())
	requestCtx := wrapped.ObserveOperationStart(operationCtx, request("r"))
	// The request's context is derived from the operation's; finishing the
	// operation on it names the unit by scope, not by the innermost context.
	wrapped.ObserveOperationEnd(requestCtx, wrapped.OperationResult{Scope: wrapped.ScopeOperation})
	if _, finished, _ := rec.counts(); finished != 1 || rec.finished[0].Scope != telemetry.ScopeOperation {
		t.Fatalf("finishes = %v, want the operation's", rec.finished)
	}
	wrapped.ObserveOperationEnd(requestCtx, wrapped.OperationResult{})
	if _, finished, _ := rec.counts(); finished != 2 {
		t.Errorf("%d finishes, want the operation's and the request's", finished)
	}
}

func TestRetryAndHedgeNotesReachObserversThatWantThemOnTheOpenRequest(t *testing.T) {
	withNoObserver(t)
	listener := &recorder{stamp: "listens"}
	deaf := &recorder{stamp: "deaf"}
	defer telemetry.RegisterObserver(listener)()
	defer telemetry.RegisterObserver(quiet{inner: deaf})()

	operationCtx := wrapped.ObserveOperationStart(context.Background(), operation())
	// A note with no request open on the context belongs to nothing.
	wrapped.ObserveOperationNote(operationCtx, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 9})

	requestCtx := wrapped.ObserveOperationStart(operationCtx, request("r"))
	wrapped.ObserveOperationNote(requestCtx, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: types.KindRateLimited.String(), Wait: 250 * time.Millisecond})
	wrapped.ObserveOperationNote(requestCtx, wrapped.Note{Name: wrapped.NoteHedge, Attempt: 2})
	wrapped.ObserveOperationEnd(requestCtx, wrapped.OperationResult{Attempts: 2})
	// A note after the request finished is dropped.
	wrapped.ObserveOperationNote(requestCtx, wrapped.Note{Name: wrapped.NoteRetry, Attempt: 3})
	wrapped.ObserveOperationEnd(operationCtx, wrapped.OperationResult{Scope: wrapped.ScopeOperation})

	want := []telemetry.Note{
		{Name: telemetry.NoteRetry, Attempt: 1, Kind: schemaflux.ErrRateLimited, Wait: 250 * time.Millisecond},
		{Name: telemetry.NoteHedge, Attempt: 2},
	}
	if !slices.Equal(listener.noted, want) {
		t.Errorf("the observer that wants notes got %+v, want %+v", listener.noted, want)
	}
	if listener.notedOn[0] != "listens/request" {
		t.Errorf("the note arrived on the context stamped %q, want the one this observer's request start returned", listener.notedOn[0])
	}
	if _, _, noted := deaf.counts(); noted != 0 {
		t.Errorf("an observer that is not a NoteObserver was given %d notes", noted)
	}
	if started, finished, _ := deaf.counts(); started != 2 || finished != 2 {
		t.Errorf("the observer that is not a NoteObserver saw %d starts and %d finishes, want 2 and 2", started, finished)
	}
}

func TestTheNoteNamesAreTheLibrarysOwn(t *testing.T) {
	if telemetry.NoteRetry != wrapped.NoteRetry || telemetry.NoteHedge != wrapped.NoteHedge {
		t.Errorf("the note names are %q and %q, the library emits %q and %q",
			telemetry.NoteRetry, telemetry.NoteHedge, wrapped.NoteRetry, wrapped.NoteHedge)
	}
}

func TestEachFinishIsDeliveredOnTheContextOfItsOwnCall(t *testing.T) {
	withNoObserver(t)
	rec := &recorder{stamp: "r"}
	defer telemetry.RegisterObserver(rec)()

	// Two calls at once on two contexts: each finish is delivered on the
	// context of its own call.
	a := wrapped.ObserveOperationStart(context.WithValue(context.Background(), stampKey{}, "call-a"), request("a"))
	b := wrapped.ObserveOperationStart(context.WithValue(context.Background(), stampKey{}, "call-b"), request("b"))
	wrapped.ObserveOperationEnd(b, wrapped.OperationResult{Attempts: 1})
	wrapped.ObserveOperationEnd(a, wrapped.OperationResult{Attempts: 1})

	if !slices.Equal(rec.finishedOn, []string{"r/request", "r/request"}) {
		t.Errorf("finishes arrived on %v", rec.finishedOn)
	}
	if !slices.Equal(rec.startedOn, []string{"call-a", "call-b"}) {
		t.Errorf("starts arrived on %v", rec.startedOn)
	}
}
