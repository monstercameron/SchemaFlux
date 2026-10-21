package telemetry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The library emits through an interface it owns, and nothing here knows a
// tracing vendor exists. These cover the seam: which observers a start calls,
// which a finish calls, and what an observer that misbehaves can and cannot do
// to the operation and to the other observers.

// recordingObserver keeps what it was sent. With a stamp it returns a context
// carrying the stamp, and records the stamp of the context each finish and
// note was delivered on, so a test can tell whose context an event arrived
// with.
type recordingObserver struct {
	mu          sync.Mutex
	started     []OperationEvent
	finished    []OperationResult
	finishedOn  []string
	notes       []Note
	notedOn     []string
	stamp       string
	startedWith []string
}

type stampKey struct{}

func stampOf(ctx context.Context) string {
	stamp, _ := ctx.Value(stampKey{}).(string)
	return stamp
}

func (o *recordingObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	o.mu.Lock()
	o.started = append(o.started, event)
	o.startedWith = append(o.startedWith, stampOf(ctx))
	o.mu.Unlock()

	if o.stamp != "" {
		return context.WithValue(ctx, stampKey{}, o.stamp+"/"+event.Scope.String())
	}
	return ctx
}

func (o *recordingObserver) OperationFinished(ctx context.Context, result OperationResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, result)
	o.finishedOn = append(o.finishedOn, stampOf(ctx))
}

func (o *recordingObserver) OperationNoted(ctx context.Context, note Note) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notes = append(o.notes, note)
	o.notedOn = append(o.notedOn, stampOf(ctx))
}

func (o *recordingObserver) counts() (started, finished int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.started), len(o.finished)
}

// noNoteObserver implements Observer and not NoteObserver.
type noNoteObserver struct{ inner *recordingObserver }

func (o noNoteObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	return o.inner.OperationStarted(ctx, event)
}

func (o noNoteObserver) OperationFinished(ctx context.Context, result OperationResult) {
	o.inner.OperationFinished(ctx, result)
}

// withNoObserver fails a test that begins with an observer some earlier test
// left installed, since every count below would be off by its events.
func withNoObserver(t *testing.T) {
	t.Helper()
	if HasObserver() {
		t.Fatal("an observer is installed before the test began; an earlier test did not remove its own")
	}
}

func TestWithNothingInstalledTheEmissionFunctionsDoNothing(t *testing.T) {
	withNoObserver(t)

	if CurrentObserver() == nil {
		t.Fatal("CurrentObserver returned nil")
	}
	parent := context.WithValue(context.Background(), stampKey{}, "parent")
	started := ObserveOperationStart(parent, OperationEvent{Operation: "extract"})
	if started != parent {
		t.Error("with no observer installed the start must return the context it was given")
	}
	ObserveOperationEnd(started, OperationResult{Operation: "extract"})
	ObserveOperationNote(started, Note{Name: NoteRetry})

	//lint:ignore SA1012 a nil context is what an emission site with none passes
	if got := ObserveOperationStart(nil, OperationEvent{}); got != nil {
		t.Errorf("a nil context with no observer came back as %v", got)
	}
	//lint:ignore SA1012 as above
	ObserveOperationEnd(nil, OperationResult{})
	//lint:ignore SA1012 as above
	ObserveOperationNote(nil, Note{})
}

// The emission path is on every call of every operation. With nobody listening
// it must not allocate.
func TestWithNothingInstalledEmissionAllocatesNothing(t *testing.T) {
	withNoObserver(t)

	ctx := context.Background()
	event := OperationEvent{Operation: "extract", Provider: "openai", Model: "m", RequestID: "r"}
	result := OperationResult{Operation: "extract", Attempts: 1}
	note := Note{Name: NoteRetry, Attempt: 1, ErrorKind: "rate limited"}

	allocations := testing.AllocsPerRun(200, func() {
		started := ObserveOperationStart(ctx, event)
		ObserveOperationNote(started, note)
		ObserveOperationEnd(started, result)
		_ = HasObserver()
	})
	if allocations != 0 {
		t.Errorf("start, note and finish with no observer installed allocate %.1f times, want 0", allocations)
	}
}

// The same holds after an observer has been installed and removed, which is
// the state of a host that traced for a while and stopped.
func TestAfterTheLastObserverIsRemovedEmissionAllocatesNothing(t *testing.T) {
	withNoObserver(t)
	AddObserver(&recordingObserver{})()

	ctx := context.Background()
	allocations := testing.AllocsPerRun(200, func() {
		started := ObserveOperationStart(ctx, OperationEvent{Operation: "extract"})
		ObserveOperationEnd(started, OperationResult{Operation: "extract"})
	})
	if allocations != 0 {
		t.Errorf("emission after the observer was removed allocates %.1f times, want 0", allocations)
	}
}

func TestAnInstalledObserverSeesBothEnds(t *testing.T) {
	observer := &recordingObserver{}
	defer AddObserver(observer)()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{
		Operation:    "extract",
		Provider:     "openai",
		Model:        "gpt-5.6-luna",
		RequestID:    "req-1",
		Mode:         "strict",
		Intelligence: "smart",
	})
	ObserveOperationEnd(ctx, OperationResult{
		Operation: "extract",
		Duration:  25 * time.Millisecond,
		Attempts:  2,
		Usage:     types.TokenUsage{PromptTokens: 100, TotalTokens: 130},
	})

	started, finished := observer.counts()
	if started != 1 || finished != 1 {
		t.Fatalf("started=%d finished=%d, want 1 and 1", started, finished)
	}
	if observer.started[0].Model != "gpt-5.6-luna" {
		t.Errorf("Model = %q", observer.started[0].Model)
	}
	if observer.finished[0].Attempts != 2 {
		t.Errorf("Attempts = %d", observer.finished[0].Attempts)
	}
}

// An observer that opens a span needs the rest of the operation to run inside
// it, so the context it returns has to be the one that is used.
func TestTheContextAnObserverReturnsIsTheOneUsed(t *testing.T) {
	observer := &recordingObserver{stamp: "from-the-observer"}
	defer AddObserver(observer)()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})

	if got := stampOf(ctx); got != "from-the-observer/request" {
		t.Errorf("the observer's context was discarded (value = %q)", got)
	}
}

// The blocker this file exists to hold: a unit that an observer saw start is
// finished to that observer, whatever is installed when it ends. The finish
// used to go to whichever observer was installed at the time, so a call in
// flight across a removal left its span open for ever, and one in flight
// across an install finished a span the new observer had never opened.
func TestTheObserversThatSawAStartAreTheOnesThatSeeItsFinish(t *testing.T) {
	type step struct {
		name string
		// between runs after the start and before the finish, with the
		// functions that remove the first and add the second observer.
		between      func(removeFirst func(), addSecond func() func())
		firstFinish  int
		secondFinish int
	}
	cases := []step{
		{"nothing changes", func(func(), func() func()) {}, 1, 0},
		{"the observer is removed before the finish", func(remove func(), _ func() func()) { remove() }, 1, 0},
		{"the observer is removed twice", func(remove func(), _ func() func()) { remove(); remove() }, 1, 0},
		{"another is installed before the finish", func(_ func(), add func() func()) { add() }, 1, 0},
		{"another is installed and removed", func(_ func(), add func() func()) { add()() }, 1, 0},
		{"the first is swapped for another", func(remove func(), add func() func()) { remove(); add() }, 1, 0},
		{"another is installed, then the first removed", func(remove func(), add func() func()) { add(); remove() }, 1, 0},
		{"the first is removed and installed again", func(remove func(), _ func() func()) { remove() }, 1, 0},
		{"two more are installed", func(_ func(), add func() func()) { add(); add() }, 1, 0},
		{"everything is removed", func(remove func(), add func() func()) { undo := add(); remove(); undo() }, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			first, second := &recordingObserver{}, &recordingObserver{}
			removeFirst := AddObserver(first)
			var undo []func()
			t.Cleanup(func() {
				removeFirst()
				for _, remove := range undo {
					remove()
				}
			})

			ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
			tc.between(removeFirst, func() func() {
				remove := AddObserver(second)
				undo = append(undo, remove)
				return remove
			})
			ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})

			if _, finished := first.counts(); finished != tc.firstFinish {
				t.Errorf("the observer that saw the start saw %d finishes, want %d", finished, tc.firstFinish)
			}
			if started, finished := second.counts(); started != 0 || finished != tc.secondFinish {
				t.Errorf("the observer installed after the start saw %d starts and %d finishes, want 0 and %d",
					started, finished, tc.secondFinish)
			}
		})
	}
}

// A finish with no start recorded in its context reaches nobody: there is no
// observer that was told the unit began.
func TestAFinishWithNoStartReachesNoObserver(t *testing.T) {
	observer := &recordingObserver{}
	defer AddObserver(observer)()

	ObserveOperationEnd(context.Background(), OperationResult{Operation: "extract"})
	ObserveOperationNote(context.Background(), Note{Name: NoteRetry})

	if _, finished := observer.counts(); finished != 0 || len(observer.notes) != 0 {
		t.Errorf("an unstarted unit delivered %d finishes and %d notes", finished, len(observer.notes))
	}
}

func TestAUnitIsFinishedOnce(t *testing.T) {
	observer := &recordingObserver{}
	defer AddObserver(observer)()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
	derived := context.WithValue(ctx, stampKey{}, "derived")
	ObserveOperationEnd(derived, OperationResult{Operation: "extract"})
	ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})
	ObserveOperationEnd(derived, OperationResult{Operation: "extract"})

	if _, finished := observer.counts(); finished != 1 {
		t.Errorf("three finishes of one unit delivered %d, want 1", finished)
	}
}

// Several observers at once: each install adds one and each removal takes out
// that one, in whatever order the removals run.
func TestObserversAreAddedAndRemovedIndependently(t *testing.T) {
	cases := []struct {
		name    string
		removed []int // the installs removed before the event, by position
		want    []bool
	}{
		{"none removed", nil, []bool{true, true, true}},
		{"the last", []int{2}, []bool{true, true, false}},
		{"the first", []int{0}, []bool{false, true, true}},
		{"the middle", []int{1}, []bool{true, false, true}},
		{"last then middle", []int{2, 1}, []bool{true, false, false}},
		{"middle then last", []int{1, 2}, []bool{true, false, false}},
		{"first then middle", []int{0, 1}, []bool{false, false, true}},
		{"first then last", []int{0, 2}, []bool{false, true, false}},
		{"all, last in first out", []int{2, 1, 0}, []bool{false, false, false}},
		{"all, first in first out", []int{0, 1, 2}, []bool{false, false, false}},
		{"all, middle first", []int{1, 0, 2}, []bool{false, false, false}},
		{"the first removed twice", []int{0, 0}, []bool{false, true, true}},
		{"the last removed twice around the middle", []int{2, 1, 2}, []bool{true, false, false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			observers := []*recordingObserver{{}, {}, {}}
			var removes []func()
			for _, observer := range observers {
				removes = append(removes, AddObserver(observer))
			}
			t.Cleanup(func() {
				for _, remove := range removes {
					remove()
				}
			})

			for _, index := range tc.removed {
				removes[index]()
			}
			ObserveOperationEnd(
				ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"}),
				OperationResult{Operation: "extract"})

			for index, observer := range observers {
				started, finished := observer.counts()
				want := 0
				if tc.want[index] {
					want = 1
				}
				if started != want || finished != want {
					t.Errorf("observer %d saw %d starts and %d finishes, want %d of each", index, started, finished, want)
				}
			}
			if HasObserver() != slicesContains(tc.want, true) {
				t.Errorf("HasObserver = %v with installs %v", HasObserver(), tc.want)
			}
		})
	}
}

func slicesContains(values []bool, want bool) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The same Observer value installed twice is two installs: it is called twice,
// and each removal takes one of them out.
func TestTheSameObserverInstalledTwiceIsTwoInstalls(t *testing.T) {
	withNoObserver(t)
	observer := &recordingObserver{}
	removeFirst := AddObserver(observer)
	removeSecond := AddObserver(observer)
	defer removeFirst()
	defer removeSecond()

	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{})
	if started, finished := observer.counts(); started != 2 || finished != 2 {
		t.Fatalf("installed twice it saw %d starts and %d finishes, want 2 and 2", started, finished)
	}

	removeFirst()
	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{})
	if started, finished := observer.counts(); started != 3 || finished != 3 {
		t.Errorf("after one removal it saw %d starts and %d finishes in all, want 3 and 3", started, finished)
	}
}

func TestANilObserverInstallsNothing(t *testing.T) {
	withNoObserver(t)
	remove := AddObserver(nil)
	if HasObserver() {
		t.Error("a nil observer was installed")
	}
	remove()
	remove()

	restore := SetObserver(nil)
	if HasObserver() {
		t.Error("SetObserver(nil) installed something")
	}
	restore()
}

// SetObserver is the earlier name of AddObserver and behaves as it does.
func TestSetObserverAddsAsAddObserverDoes(t *testing.T) {
	withNoObserver(t)
	first, second := &recordingObserver{}, &recordingObserver{}
	restoreFirst := SetObserver(first)
	restoreSecond := SetObserver(second)
	defer restoreFirst()
	defer restoreSecond()

	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{})
	for index, observer := range []*recordingObserver{first, second} {
		if started, finished := observer.counts(); started != 1 || finished != 1 {
			t.Errorf("observer %d saw %d starts and %d finishes, want 1 and 1", index, started, finished)
		}
	}
}

// CurrentObserver delivers to whatever is installed when its methods run.
func TestCurrentObserverDeliversToEveryInstalledObserver(t *testing.T) {
	withNoObserver(t)
	current := CurrentObserver()
	first, second := &recordingObserver{}, &recordingObserver{}
	defer AddObserver(first)()
	defer AddObserver(second)()

	current.OperationFinished(
		current.OperationStarted(context.Background(), OperationEvent{Operation: "extract"}),
		OperationResult{Operation: "extract"})

	for index, observer := range []*recordingObserver{first, second} {
		if started, finished := observer.counts(); started != 1 || finished != 1 {
			t.Errorf("observer %d saw %d starts and %d finishes, want 1 and 1", index, started, finished)
		}
	}
}

// Each observer is started on the context the one before it returned, and
// finished on the context it returned itself, in the reverse order.
func TestEachObserverIsFinishedOnTheContextItsOwnStartReturned(t *testing.T) {
	withNoObserver(t)
	first, second := &recordingObserver{stamp: "first"}, &recordingObserver{stamp: "second"}
	var order []string
	trace := func(name string, observer *recordingObserver) Observer {
		return funcObserver{
			started: observer.OperationStarted,
			finished: func(ctx context.Context, result OperationResult) {
				order = append(order, name)
				observer.OperationFinished(ctx, result)
			},
		}
	}
	defer AddObserver(trace("first", first))()
	defer AddObserver(trace("second", second))()

	ctx := ObserveOperationStart(context.Background(), OperationEvent{})
	if got := stampOf(ctx); got != "second/request" {
		t.Errorf("the context that came back carries %q, want the last observer's", got)
	}
	if got := second.startedWith[0]; got != "first/request" {
		t.Errorf("the second observer was started on %q, want the first's context", got)
	}
	ObserveOperationEnd(context.WithValue(ctx, stampKey{}, "a descendant"), OperationResult{})

	if got := first.finishedOn[0]; got != "first/request" {
		t.Errorf("the first observer was finished on %q, want the context it returned", got)
	}
	if got := second.finishedOn[0]; got != "second/request" {
		t.Errorf("the second observer was finished on %q, want the context it returned", got)
	}
	if strings.Join(order, ",") != "second,first" {
		t.Errorf("finish order = %v, want the reverse of the start order", order)
	}
}

// An operation and the requests it makes are separate units: the finish of
// each reaches its own start, whichever context it is called with.
func TestAnOperationAndItsRequestsFinishSeparately(t *testing.T) {
	cases := []struct {
		name     string
		finishes []Scope // in the order they are called, all on the innermost context
		want     []string
	}{
		{"request then operation", []Scope{ScopeRequest, ScopeOperation}, []string{"o/request", "o/operation"}},
		{"operation then request", []Scope{ScopeOperation, ScopeRequest}, []string{"o/operation", "o/request"}},
		{"request only", []Scope{ScopeRequest}, []string{"o/request"}},
		{"operation only", []Scope{ScopeOperation}, []string{"o/operation"}},
		{"request twice", []Scope{ScopeRequest, ScopeRequest}, []string{"o/request"}},
		{"operation twice", []Scope{ScopeOperation, ScopeOperation}, []string{"o/operation"}},
		{"request, operation, request", []Scope{ScopeRequest, ScopeOperation, ScopeRequest}, []string{"o/request", "o/operation"}},
		{"operation twice then request", []Scope{ScopeOperation, ScopeOperation, ScopeRequest}, []string{"o/operation", "o/request"}},
		{"nothing", nil, nil},
		{"request, request, operation, operation", []Scope{ScopeRequest, ScopeRequest, ScopeOperation, ScopeOperation}, []string{"o/request", "o/operation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			observer := &recordingObserver{stamp: "o"}
			defer AddObserver(observer)()

			operation := ObserveOperationStart(context.Background(), OperationEvent{Scope: ScopeOperation, Operation: "extract"})
			request := ObserveOperationStart(operation, OperationEvent{Scope: ScopeRequest, Operation: "extract"})
			if got := observer.startedWith[1]; got != "o/operation" {
				t.Fatalf("the request was started on %q, want the operation's context", got)
			}

			for _, scope := range tc.finishes {
				ObserveOperationEnd(request, OperationResult{Scope: scope})
			}
			if got := strings.Join(observer.finishedOn, ","); got != strings.Join(tc.want, ",") {
				t.Errorf("finishes arrived on %q, want %q", got, strings.Join(tc.want, ","))
			}
		})
	}
}

// A second request under the same operation is its own unit: finishing it does
// not finish the first, and the operation's finish is found from either.
func TestSiblingRequestsUnderOneOperationFinishIndependently(t *testing.T) {
	withNoObserver(t)
	observer := &recordingObserver{}
	defer AddObserver(observer)()

	operation := ObserveOperationStart(context.Background(), OperationEvent{Scope: ScopeOperation})
	first := ObserveOperationStart(operation, OperationEvent{Scope: ScopeRequest, RequestID: "first"})
	second := ObserveOperationStart(operation, OperationEvent{Scope: ScopeRequest, RequestID: "second", Repair: true})

	ObserveOperationEnd(second, OperationResult{Scope: ScopeRequest, Operation: "second"})
	ObserveOperationEnd(first, OperationResult{Scope: ScopeRequest, Operation: "first"})
	ObserveOperationEnd(second, OperationResult{Scope: ScopeOperation, Operation: "whole"})
	ObserveOperationEnd(first, OperationResult{Scope: ScopeOperation, Operation: "whole again"})

	var got []string
	for _, result := range observer.finished {
		got = append(got, result.Operation)
	}
	if strings.Join(got, ",") != "second,first,whole" {
		t.Errorf("finishes = %v, want second, first, whole", got)
	}
}

func TestANoteReachesTheObserversOfTheOpenRequest(t *testing.T) {
	withNoObserver(t)
	noted := &recordingObserver{stamp: "noted"}
	silent := &recordingObserver{}
	defer AddObserver(noted)()
	defer AddObserver(noNoteObserver{inner: silent})()

	operation := ObserveOperationStart(context.Background(), OperationEvent{Scope: ScopeOperation})
	ObserveOperationNote(operation, Note{Name: NoteRetry, Attempt: 9})
	if len(noted.notes) != 0 {
		t.Fatal("a note outside any request was delivered")
	}

	request := ObserveOperationStart(operation, OperationEvent{Scope: ScopeRequest})
	late := &recordingObserver{}
	defer AddObserver(late)()

	want := Note{Name: NoteRetry, Attempt: 1, ErrorKind: "rate limited", Wait: 20 * time.Millisecond}
	ObserveOperationNote(context.WithValue(request, stampKey{}, "inside the provider"), want)
	ObserveOperationNote(request, Note{Name: NoteHedge, Attempt: 2})

	if len(noted.notes) != 2 || noted.notes[0] != want || noted.notes[1].Name != NoteHedge {
		t.Fatalf("notes = %+v, want the retry then the hedge", noted.notes)
	}
	if noted.notedOn[0] != "noted/request" {
		t.Errorf("the note was delivered on %q, want the context the observer returned for the request", noted.notedOn[0])
	}
	if len(silent.notes) != 0 || len(late.notes) != 0 {
		t.Error("a note reached an observer that does not take notes, or one installed after the request started")
	}

	ObserveOperationEnd(request, OperationResult{Scope: ScopeRequest})
	ObserveOperationNote(request, Note{Name: NoteRetry, Attempt: 2})
	if len(noted.notes) != 2 {
		t.Error("a note was delivered after its request had finished")
	}
}

func TestObservationIsSafeUnderConcurrency(t *testing.T) {
	observer := &recordingObserver{}
	defer AddObserver(observer)()

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
				ObserveOperationNote(ctx, Note{Name: NoteRetry})
				ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})
			}
		}()
	}
	wg.Wait()

	started, finished := observer.counts()
	if started != 200 || finished != 200 {
		t.Errorf("started=%d finished=%d, want 200 each", started, finished)
	}
}

// countingObserver counts starts and finishes without a lock, so the test
// below measures the dispatch and not the observer.
type countingObserver struct {
	started, finished atomic.Int64
}

func (o *countingObserver) OperationStarted(ctx context.Context, _ OperationEvent) context.Context {
	o.started.Add(1)
	return ctx
}

func (o *countingObserver) OperationFinished(context.Context, OperationResult) {
	o.finished.Add(1)
}

// Observers are installed and removed while units start and finish on other
// goroutines. Every start an observer was given is matched by exactly one
// finish, which is the property a span-opening observer depends on.
func TestEveryStartIsFinishedWhileObserversComeAndGo(t *testing.T) {
	withNoObserver(t)
	observers := []*countingObserver{{}, {}, {}}

	var workers sync.WaitGroup
	stop := make(chan struct{})
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				operation := ObserveOperationStart(context.Background(), OperationEvent{Scope: ScopeOperation})
				request := ObserveOperationStart(operation, OperationEvent{Scope: ScopeRequest})
				ObserveOperationEnd(request, OperationResult{Scope: ScopeRequest})
				ObserveOperationEnd(operation, OperationResult{Scope: ScopeOperation})
			}
		}()
	}
	for cycle := range 300 {
		remove := AddObserver(observers[cycle%len(observers)])
		if cycle%7 == 0 {
			time.Sleep(50 * time.Microsecond)
		}
		remove()
	}
	close(stop)
	workers.Wait()

	total := int64(0)
	for index, observer := range observers {
		started, finished := observer.started.Load(), observer.finished.Load()
		total += started
		if started != finished {
			t.Errorf("observer %d saw %d starts and %d finishes", index, started, finished)
		}
	}
	if total == 0 {
		t.Skip("no unit happened to start while an observer was installed; nothing was checked")
	}
}

// The events carry names, identifiers, and settings. They must not grow a
// field for the caller's input: an observer's output is a span or a log line,
// which is the least controlled place this library could put somebody's data.
func TestTheEventTypesCarryNoPayloadField(t *testing.T) {
	forbidden := map[string]struct{}{
		"Input": {}, "Payload": {}, "Body": {}, "Data": {},
		"Prompt": {}, "SystemPrompt": {}, "UserPrompt": {}, "Response": {}, "Content": {},
		"Steering": {}, "Message": {}, "Text": {},
	}

	for name, value := range map[string]any{"OperationEvent": OperationEvent{}, "OperationResult": OperationResult{}, "Note": Note{}} {
		for _, field := range fieldNamesOf(value) {
			if _, bad := forbidden[field]; bad {
				t.Errorf("%s.%s carries the caller's payload into an exported span", name, field)
			}
		}
	}
}

// The forbidden-name list above catches a field called Prompt. It does not
// catch a field called Detail, Reason, Sample, or Label that happens to carry
// the caller's data, and the list only grows after somebody has already
// thought of the name.
//
// So this is the check from the other side: every field of the event types has
// to be declared here, with the reason it is not the caller's data. A new field
// fails the build until its author classifies it, which is the moment to notice
// that the thing being added is a value rather than a name.
//
// The admissible classes, and nothing else:
//
//   - name: a string this library or a provider chose from a fixed vocabulary
//     (an operation, a provider, a mode, a finish reason).
//   - shape: an identifier for the structure of a request, never its contents
//     (a schema's name, a response format).
//   - identifier: an opaque correlation key.
//   - count: a number: tokens, attempts, money, elapsed time.
//   - flag: a boolean about how the request was made.
func TestEveryEventFieldIsANameShapeIdentifierCountOrFlag(t *testing.T) {
	declared := map[string]map[string]string{
		"OperationEvent": {
			"Scope":          "name: request or operation",
			"Operation":      "name: the library's own operation name",
			"Provider":       "name: the provider's registered name",
			"Model":          "name: the model identifier requested",
			"RequestID":      "identifier: minted by requesttracking or supplied by the host",
			"CorrelationID":  "identifier: supplied by the host to group calls",
			"Mode":           "name: a Mode enum rendered by String()",
			"Intelligence":   "name: a Speed enum rendered by String()",
			"SchemaName":     "shape: the structured output's name, never its contents",
			"ResponseFormat": "shape: json or text, chosen by the operation",
			"Repair":         "flag: the request asks again after a refused answer",
			"Stream":         "flag: the answer is delivered as it is produced",
		},
		"OperationResult": {
			"Scope":            "name: request or operation",
			"Operation":        "name: the library's own operation name",
			"Duration":         "count: wall-clock elapsed",
			"ProviderDuration": "count: elapsed inside provider calls",
			"Usage":            "count: token counts",
			"Cost":             "count: money, plus the pricing source's name",
			"Attempts":         "count: provider calls made",
			"Provider":         "name: the provider that answered",
			"Model":            "name: the model that answered",
			"FinishReason":     "name: the provider's own stop-reason enum",
			"Cached":           "flag: a response cache answered and no provider was called",
			"AskedFirst":       "name: the decision provider asked before the one that answered",
			"AskedFirstModel":  "name: the model that decision provider was asked under",
			"Escalated":        "name: one of the library's fixed reasons (below the floor, unavailable, unsupported), never the question",
			"Requests":         "count: requests an operation made",
			"Items":            "count: items a run over many was given",
			"Succeeded":        "count: items that succeeded",
			"Failed":           "count: items that failed",
			"Cancelled":        "count: items that were cancelled",
			"ErrorKind":        "name: a member of types.ErrorKind, never a message",
		},
		"Note": {
			"Name":      "name: retry or hedge",
			"Attempt":   "count: which attempt",
			"ErrorKind": "name: a member of types.ErrorKind, never a message",
			"Wait":      "count: the pause before the next attempt",
		},
	}
	values := map[string]any{"OperationEvent": OperationEvent{}, "OperationResult": OperationResult{}, "Note": Note{}}

	for typeName, value := range values {
		fields := fieldNamesOf(value)
		for _, field := range fields {
			if _, ok := declared[typeName][field]; !ok {
				t.Errorf("%s.%s is undeclared: say which of name/shape/identifier/count/flag it is, "+
					"or it is the caller's data on its way into an exported span", typeName, field)
			}
		}
		// The other direction: a declaration left behind by a removed field is a
		// stale exemption, and the next field to reuse that name inherits it.
		assertNoStaleDeclarations(t, typeName, declared[typeName], fields)
	}
}

func assertNoStaleDeclarations(t *testing.T, typeName string, declared map[string]string, actual []string) {
	t.Helper()

	present := make(map[string]struct{}, len(actual))
	for _, field := range actual {
		present[field] = struct{}{}
	}
	for field := range declared {
		if _, ok := present[field]; !ok {
			t.Errorf("%s.%s is declared here but no longer exists; the exemption would be inherited "+
				"by the next field to take that name", typeName, field)
		}
	}
}

// The result reports the error *kind*, not the message, because a provider's
// message can quote the caller's input back.
func TestTheResultReportsAKindNotAMessage(t *testing.T) {
	for _, field := range fieldNamesOf(OperationResult{}) {
		if field == "Error" || field == "ErrorMessage" || field == "Err" {
			t.Errorf("OperationResult.%s would export a provider's message; the kind is the safe form", field)
		}
	}
}

func TestScopeNames(t *testing.T) {
	cases := []struct {
		scope Scope
		want  string
	}{
		{ScopeRequest, "request"},
		{ScopeOperation, "operation"},
		{Scope(0), "request"},
		{Scope(1), "operation"},
		{Scope(2), "request"},
		{Scope(3), "request"},
		{Scope(7), "request"},
		{Scope(100), "request"},
		{Scope(254), "request"},
		{Scope(255), "request"},
	}
	for _, tc := range cases {
		if got := tc.scope.String(); got != tc.want {
			t.Errorf("Scope(%d).String() = %q, want %q", tc.scope, got, tc.want)
		}
	}
	var zero OperationEvent
	if zero.Scope != ScopeRequest {
		t.Error("the zero OperationEvent is not a request, so an observer written before operations had events would misread it")
	}
}

// fieldNamesOf lists a struct's field names, so the payload checks above walk
// the type rather than a list somebody has to remember to update.
func fieldNamesOf(value any) []string {
	t := reflect.TypeOf(value)
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names = append(names, t.Field(i).Name)
	}
	return names
}

// Before anything installs a reader, GetTraceID and GetSpanID return "", so a
// host that never calls SetSpanIDSources sees no trace ID it did not make.
func TestSpanIDSourcesDefaultToEmpty(t *testing.T) {
	if got := GetTraceID(context.Background()); got != "" {
		t.Errorf("GetTraceID with no source installed = %q, want empty", got)
	}
	if got := GetSpanID(context.Background()); got != "" {
		t.Errorf("GetSpanID with no source installed = %q, want empty", got)
	}
}

// A nil context must not panic a reader that was never taught to expect one.
func TestSpanIDSourcesToleratesNilContext(t *testing.T) {
	restore := SetSpanIDSources(
		func(context.Context) string { return "trace-should-not-be-reached" },
		func(context.Context) string { return "span-should-not-be-reached" },
	)
	defer restore()

	//lint:ignore SA1012 deliberately exercising the nil-ctx guard
	if got := GetTraceID(nil); got != "" {
		t.Errorf("GetTraceID(nil) = %q, want empty without invoking the source", got)
	}
	//lint:ignore SA1012 deliberately exercising the nil-ctx guard
	if got := GetSpanID(nil); got != "" {
		t.Errorf("GetSpanID(nil) = %q, want empty without invoking the source", got)
	}
}

func TestSetSpanIDSourcesInstallsAndRestores(t *testing.T) {
	restore := SetSpanIDSources(
		func(context.Context) string { return "trace-123" },
		func(context.Context) string { return "span-456" },
	)

	if got := GetTraceID(context.Background()); got != "trace-123" {
		t.Errorf("GetTraceID = %q, want trace-123", got)
	}
	if got := GetSpanID(context.Background()); got != "span-456" {
		t.Errorf("GetSpanID = %q, want span-456", got)
	}

	restore()

	if got := GetTraceID(context.Background()); got != "" {
		t.Errorf("after restore, GetTraceID = %q, want empty", got)
	}
	if got := GetSpanID(context.Background()); got != "" {
		t.Errorf("after restore, GetSpanID = %q, want empty", got)
	}
}

// Passing nil for either reader gives that one its default without disturbing
// the other.
func TestSetSpanIDSourcesNilLeavesTheOtherReaderInPlace(t *testing.T) {
	restoreBoth := SetSpanIDSources(
		func(context.Context) string { return "trace-first" },
		func(context.Context) string { return "span-first" },
	)
	defer restoreBoth()

	restoreTraceOnly := SetSpanIDSources(nil, func(context.Context) string { return "span-second" })
	defer restoreTraceOnly()

	if got := GetTraceID(context.Background()); got != "" {
		t.Errorf("GetTraceID after nil = %q, want empty (default restored)", got)
	}
	if got := GetSpanID(context.Background()); got != "span-second" {
		t.Errorf("GetSpanID = %q, want span-second (untouched by the nil trace source)", got)
	}
}

// Whichever order the restores run in, the readers left are those of the
// newest install nobody has undone.
func TestSetSpanIDSourcesRestoresOutOfOrder(t *testing.T) {
	cases := []struct {
		name  string
		order []int
		want  string // trace ID the readers give afterwards; "" is the default
	}{
		{"last in first out", []int{2, 1, 0}, ""},
		{"first in first out", []int{0, 1, 2}, ""},
		{"middle first", []int{1, 2, 0}, ""},
		{"middle then first then last", []int{1, 0, 2}, ""},
		{"first then last then middle", []int{0, 2, 1}, ""},
		{"last then first then middle", []int{2, 0, 1}, ""},
		{"only the last", []int{2}, "t1"},
		{"only the first", []int{0}, "t2"},
		{"only the middle", []int{1}, "t2"},
		{"last and middle", []int{2, 1}, "t0"},
		{"first and middle", []int{0, 1}, "t2"},
		{"first and last", []int{0, 2}, "t1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer SetSpanIDSources(nil, nil)()
			var restores []func()
			for _, id := range []string{"t0", "t1", "t2"} {
				restores = append(restores, SetSpanIDSources(
					func(context.Context) string { return id },
					func(context.Context) string { return "s" + id },
				))
			}
			// A case that restores only some of the installs would leave the rest
			// installed for whichever test runs next. Restoring again does
			// nothing, so every install is released here.
			t.Cleanup(func() {
				for _, restore := range restores {
					restore()
				}
			})

			for _, index := range tc.order {
				restores[index]()
			}

			if got := GetTraceID(context.Background()); got != tc.want {
				t.Errorf("trace ID = %q, want %q", got, tc.want)
			}
			wantSpan := ""
			if tc.want != "" {
				wantSpan = "s" + tc.want
			}
			if got := GetSpanID(context.Background()); got != wantSpan {
				t.Errorf("span ID = %q, want %q", got, wantSpan)
			}
		})
	}
}

// panickingObserver panics in every method with a value that quotes the
// caller's data, which is what a host's formatting bug looks like from here.
type panickingObserver struct{}

func (panickingObserver) OperationStarted(context.Context, OperationEvent) context.Context {
	panic("host exporter failed on invoice acme-77")
}

func (panickingObserver) OperationFinished(context.Context, OperationResult) {
	panic("host exporter failed on invoice acme-77")
}

func (panickingObserver) OperationNoted(context.Context, Note) {
	panic("host exporter failed on invoice acme-77")
}

// finishPanicsObserver starts normally and panics when told a unit finished
// or was noted.
type finishPanicsObserver struct{ recordingObserver }

func (o *finishPanicsObserver) OperationFinished(ctx context.Context, result OperationResult) {
	o.recordingObserver.OperationFinished(ctx, result)
	panic("host exporter failed on invoice acme-77")
}

func (o *finishPanicsObserver) OperationNoted(ctx context.Context, note Note) {
	o.recordingObserver.OperationNoted(ctx, note)
	panic("host exporter failed on invoice acme-77")
}

// The observer is the host's code running inside this library's call. A panic
// in it must not take down an answer that is already paid for, and the log
// line that reports it must not repeat what the panic said.
func TestAPanickingObserverIsContained(t *testing.T) {
	withNoObserver(t)
	logs := captureLogs(t)
	defer AddObserver(panickingObserver{})()

	parent := context.WithValue(context.Background(), stampKey{}, "parent")

	got := ObserveOperationStart(parent, OperationEvent{Operation: "extract"})
	if got != parent {
		t.Error("a start whose only observer panicked must hand back the context it was given")
	}
	ObserveOperationEnd(got, OperationResult{Operation: "extract"})

	text := logs()
	if strings.Count(text, "panicked") != 1 {
		t.Errorf("want one report, for the start: the observer that panicked there began nothing to finish; log %q", text)
	}
	if strings.Contains(text, "acme-77") {
		t.Errorf("the log repeats the panic value: %q", text)
	}
}

// One observer's panic does not reach another: the observers before and after
// it are still started, finished and noted.
func TestOneObserversPanicDoesNotReachAnother(t *testing.T) {
	cases := []struct {
		name  string
		build func(before, after *recordingObserver) []Observer
		// reports is how many panics are logged over a start, a note and a
		// finish.
		reports int
	}{
		{"panics in the start, installed first", func(_, after *recordingObserver) []Observer {
			return []Observer{panickingObserver{}, after}
		}, 1},
		{"panics in the start, installed last", func(before, _ *recordingObserver) []Observer {
			return []Observer{before, panickingObserver{}}
		}, 1},
		{"panics in the start, installed between", func(before, after *recordingObserver) []Observer {
			return []Observer{before, panickingObserver{}, after}
		}, 1},
		{"panics in the finish and the note, installed first", func(_, after *recordingObserver) []Observer {
			return []Observer{&finishPanicsObserver{}, after}
		}, 2},
		{"panics in the finish and the note, installed last", func(before, _ *recordingObserver) []Observer {
			return []Observer{before, &finishPanicsObserver{}}
		}, 2},
		{"panics in the finish and the note, installed between", func(before, after *recordingObserver) []Observer {
			return []Observer{before, &finishPanicsObserver{}, after}
		}, 2},
		{"two that panic in the start", func(before, after *recordingObserver) []Observer {
			return []Observer{panickingObserver{}, before, panickingObserver{}, after}
		}, 2},
		{"two that panic in the finish", func(before, after *recordingObserver) []Observer {
			return []Observer{&finishPanicsObserver{}, before, &finishPanicsObserver{}, after}
		}, 4},
		{"one of each", func(before, after *recordingObserver) []Observer {
			return []Observer{before, panickingObserver{}, &finishPanicsObserver{}, after}
		}, 3},
		{"none panics", func(before, after *recordingObserver) []Observer {
			return []Observer{before, after}
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			logs := captureLogs(t)
			before, after := &recordingObserver{stamp: "before"}, &recordingObserver{stamp: "after"}
			for _, observer := range tc.build(before, after) {
				defer AddObserver(observer)()
			}

			ctx := ObserveOperationStart(context.Background(), OperationEvent{Operation: "extract"})
			ObserveOperationNote(ctx, Note{Name: NoteRetry})
			ObserveOperationEnd(ctx, OperationResult{Operation: "extract"})

			for name, observer := range map[string]*recordingObserver{"before": before, "after": after} {
				started, finished := observer.counts()
				if len(observer.started) == 0 && len(observer.finished) == 0 && !installedIn(tc.build(before, after), observer) {
					continue
				}
				if started != 1 || finished != 1 || len(observer.notes) != 1 {
					t.Errorf("the observer installed %s the one that panicked saw %d starts, %d notes and %d finishes, want 1 of each",
						name, started, len(observer.notes), finished)
				}
				if finished == 1 && observer.finishedOn[0] != observer.stamp+"/request" {
					t.Errorf("the observer installed %s was finished on %q, want its own context", name, observer.finishedOn[0])
				}
			}
			text := logs()
			if got := strings.Count(text, "panicked"); got != tc.reports {
				t.Errorf("%d panics were reported, want %d; log %q", got, tc.reports, text)
			}
			if strings.Contains(text, "acme-77") {
				t.Errorf("the log repeats the panic value: %q", text)
			}
		})
	}
}

func installedIn(observers []Observer, want *recordingObserver) bool {
	for _, observer := range observers {
		if observer == Observer(want) {
			return true
		}
	}
	return false
}

type nilContextObserver struct{ recordingObserver }

func (o *nilContextObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	o.recordingObserver.OperationStarted(ctx, event)
	return nil
}

// An observer that returns no context would make every later use of it panic
// somewhere that points at the wrong package. It is still finished, on the
// context it was given.
func TestAnObserverReturningNoContextIsGivenBackTheOneItWasPassed(t *testing.T) {
	withNoObserver(t)
	observer := &nilContextObserver{}
	defer AddObserver(observer)()

	parent := context.WithValue(context.Background(), stampKey{}, "parent")
	started := ObserveOperationStart(parent, OperationEvent{})
	if started == nil || stampOf(started) != "parent" {
		t.Fatalf("got %v, want a context derived from the input", started)
	}
	ObserveOperationEnd(started, OperationResult{})
	if _, finished := observer.counts(); finished != 1 || observer.finishedOn[0] != "parent" {
		t.Errorf("finished %d times on %v, want once on the input context", finished, observer.finishedOn)
	}
}

// An emission site with no context to give passes nil. With observers installed
// they must still be handed a context, and the unit must still finish.
func TestAStartWithNoContextGivesTheObserversABackgroundOne(t *testing.T) {
	withNoObserver(t)
	observer := &recordingObserver{stamp: "seen"}
	var given context.Context
	defer AddObserver(funcObserver{
		started: func(ctx context.Context, event OperationEvent) context.Context {
			given = ctx
			return observer.OperationStarted(ctx, event)
		},
		finished: observer.OperationFinished,
	})()

	//lint:ignore SA1012 a nil context is what an emission site with none passes
	started := ObserveOperationStart(nil, OperationEvent{Operation: "extract"})

	if given == nil {
		t.Fatal("the observer was handed a nil context")
	}
	if started == nil || stampOf(started) != "seen/request" {
		t.Fatalf("the start returned %v, want a usable context carrying the observer's value", started)
	}
	ObserveOperationEnd(started, OperationResult{Operation: "extract"})
	if _, finished := observer.counts(); finished != 1 {
		t.Errorf("the unit was finished %d times, want once", finished)
	}
}

// An observer is the host's code and must not be able to change what the call
// it watches does. A context it returns that does not descend from the one it
// was given carries none of the caller's deadline, cancellation, client or
// call record, and using it would silently drop all of them.
func TestAnObserverCannotReplaceTheCallsContextWithAnUnrelatedOne(t *testing.T) {
	type key struct{}
	deadline := time.Now().Add(time.Hour)
	unrelatedContexts := map[string]func(ctx context.Context) context.Context{
		"context.Background": func(context.Context) context.Context { return context.Background() },
		"context.TODO":       func(context.Context) context.Context { return context.TODO() },
		"a context with a value of its own": func(context.Context) context.Context {
			return context.WithValue(context.Background(), key{}, "the observer's")
		},
		"a context that outlives the call's cancellation": func(ctx context.Context) context.Context {
			return context.WithoutCancel(ctx)
		},
	}
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "the call's"), deadline)
	defer cancel()
	// A child of the caller's context that is not a child of the one the
	// observer was handed has the caller's deadline, and is still not the
	// context the call was running on.
	unrelatedContexts["a sibling derived from the caller's own context"] = func(context.Context) context.Context {
		return context.WithValue(parent, key{}, "a sibling")
	}
	for name, unrelated := range unrelatedContexts {
		t.Run(name, func(t *testing.T) {
			withNoObserver(t)
			logs := captureLogs(t)

			var finishedOn context.Context
			defer AddObserver(funcObserver{
				started:  func(ctx context.Context, _ OperationEvent) context.Context { return unrelated(ctx) },
				finished: func(ctx context.Context, _ OperationResult) { finishedOn = ctx },
			})()

			got := ObserveOperationStart(parent, OperationEvent{Operation: "extract"})

			if value, _ := got.Value(key{}).(string); value != "the call's" {
				t.Errorf("the call went on with a context whose value is %q, want the caller's", value)
			}
			if at, ok := got.Deadline(); !ok || !at.Equal(deadline) {
				t.Errorf("the call's deadline = %v (set %v), want the caller's %v", at, ok, deadline)
			}
			if got.Done() == nil {
				t.Error("the call can no longer be cancelled")
			}
			cancel()
			select {
			case <-got.Done():
			default:
				t.Error("cancelling the caller's context did not cancel the call's")
			}

			ObserveOperationEnd(got, OperationResult{Operation: "extract"})
			if finishedOn == nil {
				t.Error("the observer that returned an unrelated context was not told the unit finished")
			}
			if text := logs(); strings.Count(text, "not derived") != 1 {
				t.Errorf("want one report of the unrelated context; log %q", text)
			}
		})
	}
}

// With several observers each is given the context the one before it returned,
// and one that returns something unrelated is passed over: the observers after
// it never see what it returned, and the call goes on with what it had.
func TestAnObserverReturningAnUnrelatedContextIsSkippedBetweenTwoGoodOnes(t *testing.T) {
	withNoObserver(t)
	captureLogs(t)
	first, last := &recordingObserver{stamp: "first"}, &recordingObserver{stamp: "last"}
	unrelated := &recordingObserver{}
	var unrelatedFinishedOn context.Context
	defer AddObserver(first)()
	defer AddObserver(funcObserver{
		started: func(ctx context.Context, event OperationEvent) context.Context {
			unrelated.OperationStarted(ctx, event)
			return context.WithValue(context.Background(), stampKey{}, "unrelated")
		},
		finished: func(ctx context.Context, _ OperationResult) { unrelatedFinishedOn = ctx },
	})()
	defer AddObserver(last)()

	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	got := ObserveOperationStart(parent, OperationEvent{Operation: "extract"})

	if want := "first/request"; last.startedWith[0] != want {
		t.Errorf("the observer after the bad one was started on %q, want the good one's %q", last.startedWith[0], want)
	}
	if got := stampOf(got); got != "last/request" {
		t.Errorf("the call went on with %q, want the last good observer's", got)
	}
	if _, ok := got.Deadline(); !ok {
		t.Error("the caller's deadline was lost")
	}

	ObserveOperationEnd(got, OperationResult{Operation: "extract"})
	if first.finishedOn[0] != "first/request" || last.finishedOn[0] != "last/request" {
		t.Errorf("the good observers were finished on %q and %q", first.finishedOn[0], last.finishedOn[0])
	}
	if stampOf(unrelatedFinishedOn) != "unrelated" {
		t.Errorf("the observer that returned an unrelated context was finished on %q, want the context it returned", stampOf(unrelatedFinishedOn))
	}
}

// The report is made once for each observer that does it, not once for each
// call, since an observer with this bug does it on every call.
func TestAnObserverReturningAnUnrelatedContextIsReportedOncePerObserver(t *testing.T) {
	withNoObserver(t)
	logs := captureLogs(t)
	bad := func() Observer {
		return funcObserver{started: func(context.Context, OperationEvent) context.Context { return context.Background() }}
	}
	removeFirst, removeSecond := AddObserver(bad()), AddObserver(bad())
	defer removeFirst()
	defer removeSecond()

	for call := 0; call < 25; call++ {
		parent := context.WithValue(context.Background(), stampKey{}, "invoice acme-77")
		ObserveOperationEnd(ObserveOperationStart(parent, OperationEvent{Operation: "extract", RequestID: "acme-77"}), OperationResult{})
	}

	text := logs()
	if got := strings.Count(text, "not derived"); got != 2 {
		t.Errorf("%d reports over 25 calls and two observers, want 2; log %q", got, text)
	}
	if strings.Contains(text, "acme-77") || strings.Contains(text, "extract") {
		t.Errorf("the report carries something of the call: %q", text)
	}
	if !strings.Contains(strings.ToLower(text), "warn") {
		t.Errorf("the report is not at warn level: %q", text)
	}
}

// A context an observer derives from the one it was given, however deeply and
// by whatever means, is the one used.
func TestAnObserverMayDeriveFromTheContextItWasGiven(t *testing.T) {
	type key struct{}
	cases := map[string]func(t *testing.T, ctx context.Context) context.Context{
		"returns it":   func(_ *testing.T, ctx context.Context) context.Context { return ctx },
		"adds a value": func(_ *testing.T, ctx context.Context) context.Context { return context.WithValue(ctx, key{}, "x") },
		"adds a value three deep": func(_ *testing.T, ctx context.Context) context.Context {
			return context.WithValue(context.WithValue(context.WithValue(ctx, key{}, 1), key{}, 2), key{}, 3)
		},
		"adds a cancel": func(t *testing.T, ctx context.Context) context.Context {
			derived, cancel := context.WithCancel(ctx)
			t.Cleanup(cancel)
			return context.WithValue(derived, key{}, "x")
		},
		"adds a nearer deadline": func(t *testing.T, ctx context.Context) context.Context {
			derived, cancel := context.WithTimeout(ctx, time.Minute)
			t.Cleanup(cancel)
			return context.WithValue(derived, key{}, "x")
		},
		"adds a cancel cause": func(t *testing.T, ctx context.Context) context.Context {
			derived, cancel := context.WithCancelCause(ctx)
			t.Cleanup(func() { cancel(nil) })
			return context.WithValue(derived, key{}, "x")
		},
	}
	for name, derive := range cases {
		t.Run(name, func(t *testing.T) {
			withNoObserver(t)
			logs := captureLogs(t)
			given, cancelGiven := context.WithCancel(context.WithValue(context.Background(), stampKey{}, "caller"))
			defer cancelGiven()
			defer AddObserver(funcObserver{started: func(ctx context.Context, _ OperationEvent) context.Context { return derive(t, ctx) }})()

			got := ObserveOperationStart(given, OperationEvent{})

			if stampOf(got) != "caller" {
				t.Errorf("the caller's value was lost: %q", stampOf(got))
			}
			if name != "returns it" && got.Value(key{}) == nil {
				t.Error("what the observer added to the context was discarded")
			}
			if text := logs(); strings.Contains(text, "not derived") {
				t.Errorf("a derived context was reported as unrelated: %q", text)
			}
		})
	}
}

type funcObserver struct {
	started  func(context.Context, OperationEvent) context.Context
	finished func(context.Context, OperationResult)
}

func (f funcObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	return f.started(ctx, event)
}

func (f funcObserver) OperationFinished(ctx context.Context, result OperationResult) {
	if f.finished != nil {
		f.finished(ctx, result)
	}
}

// ObserveOperationStart hands back a context carrying what the observer
// returned when that descends from the one it was given, and one carrying what
// it was given in every other case.
func TestObserveOperationStartContextRules(t *testing.T) {
	type key struct{}
	valueOf := func(ctx context.Context) string {
		value, _ := ctx.Value(key{}).(string)
		return value
	}
	parent := context.WithValue(context.Background(), key{}, "parent")
	other := context.WithValue(context.Background(), key{}, "other")
	childOf := func(ctx context.Context) context.Context { return context.WithValue(ctx, key{}, "child") }

	cases := []struct {
		name     string
		observer Observer
		ctx      context.Context
		want     string
		same     bool // the very context passed in comes back
	}{
		{"no observer", nil, parent, "parent", true},
		{"returns its input", funcObserver{started: func(ctx context.Context, _ OperationEvent) context.Context { return ctx }}, parent, "parent", false},
		{"returns a child", funcObserver{started: func(ctx context.Context, _ OperationEvent) context.Context { return childOf(ctx) }}, parent, "child", false},
		{"returns an unrelated context", funcObserver{started: func(context.Context, OperationEvent) context.Context { return other }}, parent, "parent", false},
		{"returns a child of the context the call had before it was handed on", funcObserver{started: func(context.Context, OperationEvent) context.Context { return childOf(parent) }}, parent, "parent", false},
		{"returns nil", funcObserver{started: func(context.Context, OperationEvent) context.Context { return nil }}, parent, "parent", false},
		{"panics with a string", funcObserver{started: func(context.Context, OperationEvent) context.Context { panic("x") }}, parent, "parent", true},
		{"panics with an error", funcObserver{started: func(context.Context, OperationEvent) context.Context { panic(errors.New("x")) }}, parent, "parent", true},
		{"panics with nil", funcObserver{started: func(context.Context, OperationEvent) context.Context { panic(nil) }}, parent, "parent", true},
		{"panics on a nil pointer", funcObserver{started: func(context.Context, OperationEvent) context.Context {
			var observer *recordingObserver
			observer.stamp = "x"
			return nil
		}}, parent, "parent", true},
		{"background in, child out", funcObserver{started: func(ctx context.Context, _ OperationEvent) context.Context { return childOf(ctx) }}, context.Background(), "child", false},
		{"background in, an unrelated context out", funcObserver{started: func(context.Context, OperationEvent) context.Context { return other }}, context.Background(), "", false},
		{"background in, panic", funcObserver{started: func(context.Context, OperationEvent) context.Context { panic("x") }}, context.Background(), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			captureLogs(t)
			defer AddObserver(tc.observer)()

			got := ObserveOperationStart(tc.ctx, OperationEvent{Operation: "extract"})
			if valueOf(got) != tc.want {
				t.Errorf("context carries %q, want %q", valueOf(got), tc.want)
			}
			if tc.same && got != tc.ctx {
				t.Error("with no observer called, the context passed in must come back itself")
			}
		})
	}
}

// ObserveOperationEnd returns normally whatever the observer does, and gives
// the observer exactly the result it was handed.
func TestObserveOperationEndDeliversAndContains(t *testing.T) {
	cases := []struct {
		name   string
		result OperationResult
		panics bool
	}{
		{"empty result", OperationResult{}, false},
		{"named operation", OperationResult{Operation: "extract"}, false},
		{"with attempts", OperationResult{Attempts: 3}, false},
		{"with a duration", OperationResult{Duration: time.Second}, false},
		{"with an error kind", OperationResult{ErrorKind: "timeout"}, false},
		{"with usage", OperationResult{Usage: types.TokenUsage{TotalTokens: 5}}, false},
		{"an operation's result", OperationResult{Scope: ScopeOperation, Operation: "extract", ErrorKind: "repair exhausted"}, false},
		{"empty result, observer panics", OperationResult{}, true},
		{"named operation, observer panics", OperationResult{Operation: "extract"}, true},
		{"with an error kind, observer panics", OperationResult{ErrorKind: "timeout"}, true},
		{"with attempts, observer panics", OperationResult{Attempts: 3}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withNoObserver(t)
			captureLogs(t)
			var got []OperationResult
			defer AddObserver(funcObserver{
				started: func(ctx context.Context, _ OperationEvent) context.Context { return ctx },
				finished: func(_ context.Context, result OperationResult) {
					got = append(got, result)
					if tc.panics {
						panic("x")
					}
				},
			})()

			ctx := ObserveOperationStart(context.Background(), OperationEvent{Scope: tc.result.Scope})
			ObserveOperationEnd(ctx, tc.result)

			if len(got) != 1 || got[0] != tc.result {
				t.Errorf("observer saw %+v, want exactly %+v", got, tc.result)
			}
		})
	}
}

// A panic in one emission must not leave state behind that fails the next.
func TestObservationContinuesAfterAPanickingObserverIsRemoved(t *testing.T) {
	withNoObserver(t)
	captureLogs(t)
	remove := AddObserver(&finishPanicsObserver{})
	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{})
	remove()

	healthy := &recordingObserver{}
	defer AddObserver(healthy)()
	ObserveOperationEnd(ObserveOperationStart(context.Background(), OperationEvent{}), OperationResult{Operation: "after"})

	if _, finished := healthy.counts(); finished != 1 {
		t.Errorf("finished=%d, want 1", finished)
	}
}
