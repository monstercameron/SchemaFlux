package flow_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The tests of this package that involve concurrency run inside synctest
// bubbles. The clock of a bubble moves only when every goroutine in it is blocked,
// so a step that waits fifty milliseconds takes exactly that and no real time, and
// a test can assert what was in flight at a moment and how long a call took
// without a sleep and without a busy machine disturbing it.

// errStep is the failure the steps of these tests return. A test that compares
// what comes back with == is holding the library to returning a caller's error as
// it was given.
var errStep = errors.New("the step failed")

func returns[T any](value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) { return value, nil }
}

func fails[T any](err error) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		var zero T
		return zero, err
	}
}

// counts is a step that counts its runs and returns value.
func counts[T any](runs *atomic.Int32, value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		runs.Add(1)
		return value, nil
	}
}

// panics is a step that panics with value.
func panics[T any](value any) schemaflux.Step[T] {
	return func(context.Context) (T, error) { panic(value) }
}

// goexit is a step that ends its goroutine without returning, as t.FailNow does.
func goexit[T any](context.Context) (T, error) {
	runtime.Goexit()
	panic("not reached")
}

// wait is a step that takes d on the bubble's clock and then returns value. It
// gives up when its context ends, with the context's error.
func wait[T any](d time.Duration, value T) schemaflux.Step[T] {
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

// stubborn is a step that takes d whatever happens to its context.
func stubborn[T any](d time.Duration, value T) schemaflux.Step[T] {
	return func(context.Context) (T, error) {
		time.Sleep(d)
		return value, nil
	}
}

// took runs f and says how long it took on the bubble's clock.
func took(f func()) time.Duration {
	start := time.Now()
	f()
	return time.Since(start)
}

// wantKind fails the test unless err is the library's *Error of kind want, which
// errors.Is must report too, and returns it.
func wantKind(t *testing.T, err error, want schemaflux.Kind) *schemaflux.Error {
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

// noLeak lets all work in the bubble settle after the call. synctest.Test
// itself checks that every goroutine in this bubble exits before the test
// returns; a process-wide count can include unrelated runtime goroutines and
// made this check fail even when the bubble had no goroutine left behind.
func noLeak(t *testing.T) func() {
	t.Helper()
	synctest.Wait()
	return func() {
		t.Helper()
		synctest.Wait()
	}
}

// doneContexts are contexts that are already over, one cancelled and one past its
// deadline, with the kind each gives and the context error that has to be
// beneath it.
type doneContext struct {
	name string
	ctx  context.Context
	kind schemaflux.Kind
	is   error
}

func doneContexts() []doneContext {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	stop()
	return []doneContext{
		{"cancelled", cancelled, schemaflux.ErrCanceled, context.Canceled},
		{"past its deadline", expired, schemaflux.ErrTimeout, context.DeadlineExceeded},
	}
}

// contextKey is the key of a value the tests put on a context to see that a step
// is handed the context it was given.
type contextKey struct{}

// event is something a step of a test did, and when, on the bubble's clock.
type event struct {
	name string
	at   time.Duration
}

// eventLog records what the steps of one test did, so that a test can assert on
// the order of the events and on the instant of each.
type eventLog struct {
	mu     sync.Mutex
	start  time.Time
	events []event
}

func newEventLog() *eventLog { return &eventLog{start: time.Now()} }

func (l *eventLog) note(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event{name, time.Since(l.start)})
}

// when is the instant of the first event of that name, and false when there was
// none.
func (l *eventLog) when(name string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.name == name {
			return e.at, true
		}
	}
	return 0, false
}

func (l *eventLog) seen(name string) bool {
	_, ok := l.when(name)
	return ok
}

func (l *eventLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, len(l.events))
	for i, e := range l.events {
		names[i] = e.name
	}
	return names
}

// at asserts that the event happened at the instant given.
func (l *eventLog) at(t *testing.T, name string, want time.Duration) {
	t.Helper()
	got, ok := l.when(name)
	if !ok {
		t.Errorf("%q did not happen: %v", name, l.names())
		return
	}
	if got != want {
		t.Errorf("%q happened at %v, want %v: %v", name, got, want, l.names())
	}
}

// before asserts that both events happened and that the first was recorded first.
func (l *eventLog) before(t *testing.T, first, second string) {
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

// step is a step that takes d, then returns value or fails with fail. It stops
// when its context ends, and says so.
func (l *eventLog) step(name string, d time.Duration, value string, fail error) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		l.note(name + " started")
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			if fail != nil {
				l.note(name + " failed")
				return "", fail
			}
			l.note(name + " finished")
			return value, nil
		case <-ctx.Done():
			l.note(name + " cancelled")
			return "", ctx.Err()
		}
	}
}

// writer is a step that stands for one with a side effect: whether it finishes or
// is cancelled it writes before it returns, which is what cancelling being a
// request means.
func (l *eventLog) writer(name string, d time.Duration, value string) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		l.note(name + " started")
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			l.note(name + " wrote")
			return value, nil
		case <-ctx.Done():
			l.note(name + " saw the cancellation")
			l.note(name + " wrote")
			return "", ctx.Err()
		}
	}
}

// stubborn is a step that takes d and ignores its context.
func (l *eventLog) stubborn(name string, d time.Duration, value string) schemaflux.Step[string] {
	return func(context.Context) (string, error) {
		l.note(name + " started")
		time.Sleep(d)
		l.note(name + " finished")
		return value, nil
	}
}

// decide is a decision that takes d and then answers or fails.
func (l *eventLog) decide(d time.Duration, answer bool, fail error) schemaflux.Step[bool] {
	return func(ctx context.Context) (bool, error) {
		l.note("decide started")
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			if fail != nil {
				l.note("decide failed")
				return false, fail
			}
			l.note("decide answered")
			return answer, nil
		case <-ctx.Done():
			l.note("decide cancelled")
			return false, ctx.Err()
		}
	}
}

// pick is a decision among labelled routes.
func (l *eventLog) pick(d time.Duration, label string, fail error) schemaflux.Step[string] {
	return func(ctx context.Context) (string, error) {
		l.note("decide started")
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			if fail != nil {
				l.note("decide failed")
				return "", fail
			}
			l.note("decide answered")
			return label, nil
		case <-ctx.Done():
			l.note("decide cancelled")
			return "", ctx.Err()
		}
	}
}
