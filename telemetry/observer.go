// Package telemetry is what the library emits through: the events that say
// what it did, the observers a host installs to receive them, and the
// library's own in-process metrics.
//
// Nothing here imports a telemetry vendor and nothing here initialises one. A
// library that builds exporters, picks an endpoint and a sampler, and sets the
// process-wide tracer provider has configured its host's telemetry, and a host
// that had configured its own loses to whichever ran last. So the direction is
// the other way: the library emits events to observers, and an adapter that
// knows a vendor lives in its own package (telemetry/otel) and is handed what
// the host built.
//
// # Events
//
// The library describes its work at two levels, named by Scope. A request
// (ScopeRequest) is one logical request to a provider: every attempt at it and
// the waits between them. An operation (ScopeOperation) is one call of an
// operation, which may make several requests and then decodes, checks and
// repairs the answer. A request made under an operation starts from the context
// the operation's start returned, so an observer that opens spans sees it as a
// child.
//
// Each unit is bracketed by a start (OperationEvent) and a finish
// (OperationResult), and a request that is still open can be sent a Note: a
// retry, a hedged copy. No event carries a prompt, an input, an answer, a
// credential or a provider's free text: names, identifiers, counts and the
// library's own error kinds only.
//
// # Observers
//
// AddObserver installs an observer and returns the function that removes it.
// Any number can be installed at once. The start of a unit records, in the
// context it returns, which observers it called, and the finish calls exactly
// those, whatever has been installed or removed in between: a unit that an
// observer saw begin is always the one it sees end. An observer cannot change
// what a call does: a panic in one is contained, and the context it returns is
// used only if it descends from the one it was given (see
// ObserveOperationStart). With no observer installed the emission functions do
// nothing and allocate nothing.
package telemetry

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Scope says which unit of work an event describes.
type Scope uint8

const (
	// ScopeRequest is one logical request to a provider: every attempt at it
	// and the waits between them. It is the zero value, and what every event
	// described before operations had events of their own.
	ScopeRequest Scope = iota

	// ScopeOperation is one call of an operation: the requests it makes, and
	// the decoding, checking and repairing of what comes back. Its result
	// carries the outcome of the whole call, which a request's cannot: a
	// request whose answer is later refused finished without an error.
	ScopeOperation
)

// String returns "request" or "operation".
func (s Scope) String() string {
	if s == ScopeOperation {
		return "operation"
	}
	return "request"
}

// ErrorKindInternal is the ErrorKind of a unit that ended because the code
// running it panicked: the provider's, a validator's or the library's own. A
// panic that reaches the unit returns no error; the event is finished with this
// kind and the panic continues. It is also the kind of an error that carries the
// mark of a recovered panic (types.NarrowPanic), which is how a middleware's
// copy that panicked, a step of a Gather and a provider that reports an internal
// failure reach the caller: filed under "invariant violation", which is also
// what a decoded answer that broke its own rules is. An event names the first
// apart, since the second is a fault of the model's answer and the first a
// defect in code.
const ErrorKindInternal = "internal"

// ErrorKindRefused is the ErrorKind of a unit that ended because the model
// declined to answer, or the provider's content filter withheld the answer. The
// library files a refusal under "policy violation" in its error kinds; an event
// names it apart, since a policy violation is also what a data policy that
// forbids a route is, and a host that alerts on one does not want the other.
const ErrorKindRefused = "refused"

// OperationEvent describes a unit of work that is starting. It carries no
// payload: names, shapes, and identifiers only, because an observer's output
// is a log line or a span, and the caller's data belongs in neither.
type OperationEvent struct {
	// Scope is whether this is a request to a provider or a call of an
	// operation.
	Scope Scope

	// Operation is the operation's name ("extract", "sort"). On a request it
	// names the operation the request serves, and is empty when the request
	// was made by an operation that does not name itself.
	Operation string

	// Provider and Model are what the request was sent to. Both are empty on
	// an operation's event: an operation may use several.
	Provider string
	Model    string

	// RequestID and CorrelationID are the identifiers the request went out
	// under: the caller's when it supplied them, the library's otherwise.
	RequestID     string
	CorrelationID string

	// Mode and Intelligence are the request's settings, as their names.
	Mode         string
	Intelligence string

	// SchemaName is the name of the schema this request asks the provider to
	// answer in, the same string that goes on the wire as the structured
	// output's name. It is the shape's name, never its contents and never a
	// value decoded into it.
	SchemaName string

	// ResponseFormat is what the request declared it needs back: "json",
	// "text", or empty when the operation did not say. It is a setting the
	// library chose, not anything the caller supplied.
	ResponseFormat string

	// Repair is true for a request that asks again because the answer to an
	// earlier request of the same operation call was refused.
	Repair bool

	// Stream is true for a request whose answer is delivered as it is
	// produced.
	Stream bool
}

// OperationResult describes a unit of work that has finished.
type OperationResult struct {
	// Scope is the scope of the start event this result finishes.
	Scope Scope

	// Operation is the name the start event carried.
	Operation string

	// Duration is wall-clock time for the whole unit. For a request that is
	// every attempt, plus the retry backoff between them.
	Duration time.Duration

	// ProviderDuration is the part of a request's Duration spent inside
	// provider calls, summed across attempts. The difference between the two
	// is the library's own backoff, and a reader looking at a slow request
	// has to be able to tell a slow provider from a request that waited
	// before asking again.
	ProviderDuration time.Duration

	// Usage and Cost are those of the answer the request ended with. They are
	// meaningful only when a provider answered, which Provider says.
	Usage types.TokenUsage
	Cost  types.CostInfo

	// Attempts is every provider call a request made.
	Attempts int

	// Provider and Model name what actually answered, which is not always what
	// was asked for: a provider may serve a request with a different model
	// than the one named, and the start event carries the request's version of
	// both. Empty when no provider ever answered.
	Provider string
	Model    string

	// FinishReason is the provider's own word for why generation stopped
	// ("stop", "length", and each provider's variants). It is a provider
	// enum, not content. A truncated answer arrives with the same success
	// status as a complete one and this is the only field that distinguishes
	// them.
	FinishReason string

	// Cached is true on a request that a response cache answered (mw.Cache): no
	// provider was called for it, so Attempts is zero, Usage and Cost are the
	// zero values, and Provider and Model are empty, since nothing answered. The
	// cache's own hit is not a provider call and is not paid for.
	Cached bool

	// AskedFirst, AskedFirstModel and Escalated say that a decision provider was
	// asked before the one that answered (mw.Decisions) and why its answer was
	// not used. AskedFirst is that provider, AskedFirstModel the model it was
	// asked under, and Escalated one of the library's fixed reasons ("below the
	// floor", "unavailable", "unsupported"). All three are empty on a request
	// nobody was asked first. Provider and Model name who answered; Usage, Cost
	// and Attempts include the call that was asked first when it answered, since
	// it was made and paid for. The question itself is never carried.
	AskedFirst      string
	AskedFirstModel string
	Escalated       string

	// Requests is the number of requests to a provider that an operation's call
	// made, counting every request of every operation it ran itself: each is a
	// request unit of its own. It is meaningful on a result of ScopeOperation
	// and zero on a request's.
	Requests int

	// Items, Succeeded, Failed and Cancelled say what became of the items of an
	// operation that ran over many (RunOpMany, RunOpManyPartial,
	// RunOpManyRecover): Items is how many it was given, and the other three
	// split them. Succeeded, Failed and Cancelled add up to Items. All four are
	// zero on a result of any other kind, which is how a run is told from a call
	// of one item. A run that fails as a whole and returns no item, as RunOpMany
	// does, reports every item as failed. They are counts only: no item's data,
	// identifier or error is carried.
	Items     int
	Succeeded int
	Failed    int
	Cancelled int

	// ErrorKind names the failure in the library's own taxonomy, empty on
	// success. It is the kind rather than the message, because a message can
	// quote a provider's echo of the caller's input and a kind cannot.
	//
	// The taxonomy separates a cancelled call ("canceled") and an expired
	// deadline ("timeout") from a provider failure, so a host does not have to
	// guess which of the three it is looking at. The name is the one the error a
	// caller holds has, with two readings of its mark: a refusal is
	// ErrorKindRefused and a failure marked as a recovered panic is
	// ErrorKindInternal. A run over many items that returns a result in which
	// every item failed carries the kind of the first item's failure, though the
	// call itself returned no error; a run in which some item succeeded carries
	// none.
	ErrorKind string
}

// The names a Note can carry.
const (
	// NoteRetry says an attempt of the request failed and another will be
	// made. Attempt is the attempt that failed, ErrorKind why, and Wait how
	// long the library waits before the next.
	NoteRetry = "retry"

	// NoteHedge says a second copy of the request was sent because the first
	// had not answered in time. Attempt is the copy's number, 2.
	NoteHedge = "hedge"
)

// Note is something that happened inside a request while it was open. Like the
// other events it carries no text of the caller's or the provider's.
type Note struct {
	// Name is NoteRetry or NoteHedge.
	Name string

	// Attempt is the attempt the note is about, counted from 1.
	Attempt int

	// ErrorKind is the kind of the failure that caused a retry.
	ErrorKind string

	// Wait is the pause before the next attempt of a retry.
	Wait time.Duration
}

// Observer receives what the library did. Implementations must be safe for
// concurrent use and must not block: both methods run on the goroutine making
// the call, so an observer that stalls stalls the operation it is observing.
// An implementation with slow work to do should queue it and return. A panic in
// either method is recovered and logged, and reaches neither the operation nor
// another observer.
//
// OperationStarted returns a context so an implementation that opens a span
// can put it there; an implementation with nothing to add returns ctx. What it
// returns must be ctx or a context derived from it (context.WithValue, a span
// started from it): a context that is not is ignored, and logged, so that an
// observer cannot drop the caller's deadline and client from the call.
// OperationFinished is given the context that implementation's own
// OperationStarted returned for the same unit.
type Observer interface {
	OperationStarted(ctx context.Context, event OperationEvent) context.Context
	OperationFinished(ctx context.Context, result OperationResult)
}

// NoteObserver is implemented by an Observer that wants the notes of open
// requests. OperationNoted is given the context the observer's own
// OperationStarted returned for the request the note belongs to.
type NoteObserver interface {
	OperationNoted(ctx context.Context, note Note)
}

// installedObservers is the set the next start will call, in the order they
// were installed. A set is never edited once published; installing or removing
// an observer publishes a new one. The pointer is nil while nothing is
// installed, which is what an emission site checks first.
var installedObservers atomic.Pointer[[]*installedObserver]

// installedObserver is one call to AddObserver. Two installs of the same
// Observer value are two entries, each removed by its own function.
type installedObserver struct {
	observer Observer
	// warned is set once the observer has been reported for returning a context
	// that does not descend from the one it was given.
	warned atomic.Bool
}

// reportUnrelatedContext logs, the first time and not again, that this
// observer returned a context that was passed over. Every call it watches
// would do it again, and a line for each would be the only thing in the log. It
// names the observer's type, which is the host's own, and nothing of the
// call.
func (entry *installedObserver) reportUnrelatedContext() {
	if !entry.warned.CompareAndSwap(false, true) {
		return
	}
	logger.GetLogger().Warn(
		"An installed telemetry observer returned a context that is not derived from the one it was given; the context was ignored and the call went on with its own",
		"observer", fmt.Sprintf("%T", entry.observer),
	)
}

// installMu orders the writers of installedObservers. Readers take no lock.
var installMu sync.Mutex

// everInstalled is set by the first AddObserver and never cleared. A finish
// has to look in its context for the observers its start called, whatever is
// installed now, and this lets a process that never installed one skip that
// lookup.
var everInstalled atomic.Bool

// AddObserver installs an observer beside whatever is already installed and
// returns the function that removes this install and no other. Removals may
// run in any order, and calling one again does nothing.
//
// A unit that started while the observer was installed is still finished to
// it after the removal, so an observer that opens a span on start always gets
// to end it. A nil observer installs nothing.
func AddObserver(observer Observer) (remove func()) {
	if observer == nil {
		return func() {}
	}
	entry := &installedObserver{observer: observer}

	installMu.Lock()
	var current []*installedObserver
	if set := installedObservers.Load(); set != nil {
		current = *set
	}
	next := append(slices.Clone(current), entry)
	installedObservers.Store(&next)
	everInstalled.Store(true)
	installMu.Unlock()

	return func() {
		installMu.Lock()
		defer installMu.Unlock()

		set := installedObservers.Load()
		if set == nil {
			return
		}
		at := slices.Index(*set, entry)
		if at < 0 {
			return
		}
		if len(*set) == 1 {
			installedObservers.Store(nil)
			return
		}
		remaining := slices.Delete(slices.Clone(*set), at, at+1)
		installedObservers.Store(&remaining)
	}
}

// SetObserver installs next beside the observers already installed and returns
// the function that removes it, exactly as AddObserver does. Despite its name
// it does not set, replace or clear anything: it once installed the one
// observer there was, and SetObserver(nil) once removed it, but now a nil
// observer installs nothing and the way to remove an observer is the function
// this returns.
//
// Deprecated: use AddObserver, which does the same and says so.
func SetObserver(next Observer) (restore func()) {
	return AddObserver(next)
}

// HasObserver reports whether any observer is installed. An emission site
// that has work to do before it can emit, such as reading a clock, asks first.
func HasObserver() bool {
	return installedObservers.Load() != nil
}

// CurrentObserver returns an Observer that delivers to every observer
// installed when its methods are called, as ObserveOperationStart and
// ObserveOperationEnd do. It is never nil, and with nothing installed its
// methods do nothing.
func CurrentObserver() Observer {
	return everyInstalledObserver{}
}

// everyInstalledObserver is what CurrentObserver returns.
type everyInstalledObserver struct{}

func (everyInstalledObserver) OperationStarted(ctx context.Context, event OperationEvent) context.Context {
	return ObserveOperationStart(ctx, event)
}

func (everyInstalledObserver) OperationFinished(ctx context.Context, result OperationResult) {
	ObserveOperationEnd(ctx, result)
}

// startedKey is the context key of the innermost unit that has started.
type startedKey struct{}

// startedUnit is one start: which observers were called for it and the context
// each returned. The finish reads it back, so the observers that are told a
// unit ended are the ones that were told it began.
type startedUnit struct {
	scope Scope
	calls []observerCall
	// outer is the unit this one started inside, if any: a request's
	// operation, an operation's enclosing operation.
	outer *startedUnit
	// finished makes the finish happen once however often it is asked for.
	finished atomic.Bool
}

// observerCall is one observer's part in a start.
type observerCall struct {
	observer Observer
	ctx      context.Context
}

// ObserveOperationStart tells every installed observer that a unit is
// starting and returns the context to run the unit on. Each observer is given
// the context the one before it returned, so the context that comes back
// carries what all of them added, and it records which observers were called.
//
// An observer cannot change what the call does. The context it returns is used
// only when it descends from the one it was given and ends when that one does,
// which is what an observer that adds a span or a value returns. One that
// returns anything else, context.Background() being the usual mistake, would
// drop the caller's deadline, cancellation, client and call record from the
// call and from every observer after it, so its context is passed over: the
// next observer is given the one this observer was given, and the call goes on
// with the context it had. The observer is still told when the unit finishes,
// on the context it returned, and a warning that says so is logged once for
// each observer that does this, with nothing of the call in it. An observer
// that returns no context is given the one it was passed back.
//
// An observer is the host's code, so a panic in one is contained: it is logged
// without the panic value, which a host's formatting bug can fill with the
// caller's data, the other observers are still called, and the one that
// panicked is not told the unit finished, since whatever it began was lost
// with the panic.
//
// With no observer installed it returns ctx and does nothing else.
func ObserveOperationStart(ctx context.Context, event OperationEvent) context.Context {
	set := installedObservers.Load()
	if set == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}

	unit := &startedUnit{scope: event.Scope, calls: make([]observerCall, 0, len(*set))}
	unit.outer, _ = ctx.Value(startedKey{}).(*startedUnit)

	// One mark for each observer, handed to it on the context it is given and
	// looked for on the context it returns.
	marks := make([]byte, len(*set))
	current := ctx
	for position, entry := range *set {
		mark := &marks[position]
		given := context.WithValue(current, descendantKey{}, mark)
		next, delivered := deliverStart(entry.observer, given, event)
		if !delivered {
			continue
		}
		switch {
		case next == nil:
			next = given
		case !descendsFrom(next, given, mark):
			entry.reportUnrelatedContext()
			unit.calls = append(unit.calls, observerCall{observer: entry.observer, ctx: next})
			continue
		}
		unit.calls = append(unit.calls, observerCall{observer: entry.observer, ctx: next})
		current = next
	}
	if len(unit.calls) == 0 {
		return ctx
	}
	return context.WithValue(current, startedKey{}, unit)
}

// descendantKey is the context key of the mark ObserveOperationStart puts on
// the context it gives an observer.
type descendantKey struct{}

// descendsFrom reports whether next was derived from given, which carries
// mark, and is cancelled when given is. A context derived from it by
// context.WithValue, WithCancel or WithTimeout has both; context.Background(),
// a context from elsewhere and context.WithoutCancel(given) lack one or the
// other.
func descendsFrom(next, given context.Context, mark *byte) bool {
	if next.Value(descendantKey{}) != any(mark) {
		return false
	}
	return given.Done() == nil || next.Done() != nil
}

// ObserveOperationEnd tells the observers that were told a unit started that
// it has finished. ctx is the context ObserveOperationStart returned for the
// unit, or one derived from it; the unit is the innermost one of the result's
// Scope. The observers are called in the reverse of the order they were
// started in, each with the context its own start returned, and only once: a
// second finish of the same unit does nothing.
//
// Observers installed since the start are not called, and observers removed
// since are. A context no start is recorded in finishes nothing. A panic in an
// observer is contained as in ObserveOperationStart.
//
// ctx must be the context the start of this unit returned, or one derived from
// it. A context that carries the start of another call finishes that call's
// unit instead, with this call's result, and the finish that was meant is then
// a no-op; the library's own call sites always pass their own.
func ObserveOperationEnd(ctx context.Context, result OperationResult) {
	unit := startedIn(ctx, result.Scope)
	if unit == nil || !unit.finished.CompareAndSwap(false, true) {
		return
	}
	for at := len(unit.calls) - 1; at >= 0; at-- {
		deliverFinish(unit.calls[at].observer, unit.calls[at].ctx, result)
	}
}

// ObserveOperationNote sends a note to the observers of the innermost request
// that started on ctx and has not finished, those of them that implement
// NoteObserver. With no such request it does nothing.
func ObserveOperationNote(ctx context.Context, note Note) {
	unit := startedIn(ctx, ScopeRequest)
	if unit == nil || unit.finished.Load() {
		return
	}
	for _, call := range unit.calls {
		if noted, ok := call.observer.(NoteObserver); ok {
			deliverNote(noted, call.ctx, note)
		}
	}
}

// startedIn returns the innermost unit of the scope recorded in ctx, or nil.
func startedIn(ctx context.Context, scope Scope) *startedUnit {
	if ctx == nil || !everInstalled.Load() {
		return nil
	}
	unit, _ := ctx.Value(startedKey{}).(*startedUnit)
	for unit != nil && unit.scope != scope {
		unit = unit.outer
	}
	return unit
}

// deliverStart calls one observer's OperationStarted and reports whether it
// returned.
func deliverStart(observer Observer, ctx context.Context, event OperationEvent) (next context.Context, delivered bool) {
	defer func() {
		if recover() != nil {
			reportObserverPanic("OperationStarted", event.Operation)
			next, delivered = nil, false
		}
	}()
	return observer.OperationStarted(ctx, event), true
}

// deliverFinish calls one observer's OperationFinished.
func deliverFinish(observer Observer, ctx context.Context, result OperationResult) {
	defer func() {
		if recover() != nil {
			reportObserverPanic("OperationFinished", result.Operation)
		}
	}()
	observer.OperationFinished(ctx, result)
}

// deliverNote calls one observer's OperationNoted.
func deliverNote(observer NoteObserver, ctx context.Context, note Note) {
	defer func() {
		if recover() != nil {
			reportObserverPanic("OperationNoted", note.Name)
		}
	}()
	observer.OperationNoted(ctx, note)
}

// reportObserverPanic names which method failed and for which operation. The
// operation name is this library's own string, so it is safe to log; nothing
// from the recovered value is.
func reportObserverPanic(method, operation string) {
	logger.GetLogger().Error(
		"An installed telemetry observer panicked; the operation continued and the observer's event was dropped",
		"observer", method,
		"operation", operation,
	)
}

// SpanIDSource reports the trace or span ID of whatever span is already on the
// context, or "" when there is none.
//
// These are hooks for the same reason the observer is: reading two strings off
// an ambient span would otherwise put a tracing vendor in the build of every
// consumer of this library. The defaults return "", and telemetry/otel
// installs the real readers.
type SpanIDSource func(context.Context) string

var (
	traceIDSource atomic.Pointer[SpanIDSource]
	spanIDSource  atomic.Pointer[SpanIDSource]
)

// spanIDInstall is one call to SetSpanIDSources, chained to the one before it
// so that a removal that runs out of order is skipped over by the one that
// follows it instead of bringing back a pair somebody has already taken out. A
// nil reader means that reader's default.
type spanIDInstall struct {
	trace    *SpanIDSource
	span     *SpanIDSource
	previous *spanIDInstall
	undone   bool
}

var (
	spanIDMu sync.Mutex
	// spanIDHead is the newest install nobody has undone; nil means both
	// readers are at their defaults.
	spanIDHead *spanIDInstall
)

// publishSpanIDSources makes the readers the atomics hold match spanIDHead.
// The caller holds spanIDMu.
func publishSpanIDSources() {
	if spanIDHead == nil {
		traceIDSource.Store(nil)
		spanIDSource.Store(nil)
		return
	}
	traceIDSource.Store(spanIDHead.trace)
	spanIDSource.Store(spanIDHead.span)
}

// SetSpanIDSources installs the readers GetTraceID and GetSpanID use and
// returns a function removing this install. Either reader may be nil, which
// gives that one its default. The newest install that has not been removed is
// the one in effect: removals out of order never bring back a pair whose own
// removal has run, and calling a removal again does nothing.
func SetSpanIDSources(traceID, spanID SpanIDSource) (restore func()) {
	spanIDMu.Lock()
	defer spanIDMu.Unlock()

	install := &spanIDInstall{previous: spanIDHead}
	if traceID != nil {
		install.trace = &traceID
	}
	if spanID != nil {
		install.span = &spanID
	}
	spanIDHead = install
	publishSpanIDSources()

	return func() {
		spanIDMu.Lock()
		defer spanIDMu.Unlock()

		install.undone = true
		for spanIDHead != nil && spanIDHead.undone {
			spanIDHead = spanIDHead.previous
		}
		publishSpanIDSources()
	}
}

// GetTraceID returns the ambient trace ID, or "" when nothing is tracing.
func GetTraceID(ctx context.Context) string { return readSpanID(traceIDSource.Load(), ctx) }

// GetSpanID returns the ambient span ID, or "" when nothing is tracing.
func GetSpanID(ctx context.Context) string { return readSpanID(spanIDSource.Load(), ctx) }

func readSpanID(source *SpanIDSource, ctx context.Context) string {
	if source == nil || ctx == nil {
		return ""
	}
	return (*source)(ctx)
}
