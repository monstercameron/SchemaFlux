package telemetry

import (
	"context"
	"time"

	wrapped "github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Scope says which unit of work an event describes.
type Scope uint8

const (
	// ScopeRequest is one logical request to a provider: every attempt at it and
	// the waits between them. It is the zero value.
	ScopeRequest Scope = iota

	// ScopeOperation is one call of an operation: the requests it makes, and the
	// decoding, checking and repairing of what comes back. Its Result carries
	// the outcome of the whole call, which a request's cannot.
	ScopeOperation
)

// String returns "request" or "operation".
func (s Scope) String() string {
	if s == ScopeOperation {
		return "operation"
	}
	return "request"
}

// An Event describes a unit of work that is starting. It carries no prompt, no
// input, no answer and no credential: names, identifiers and settings only. A
// string that a caller or a provider chose arrives cut to at most 128 bytes
// (an id, the model, the schema name) or 64 (the rest), with each byte that is
// not printable ASCII replaced by "?".
type Event struct {
	// Scope is whether this is a request to a provider or a call of an
	// operation. The zero value is ScopeRequest.
	Scope Scope

	// Operation is the operation's name in lower case: "extract", "sort". On a
	// request it names the operation the request serves. Empty on a request
	// means the operation that made it does not name itself; it is never empty
	// on an operation's event.
	Operation string

	// Provider and Model are what a request was sent to. Both are empty on an
	// operation's event, since an operation may use several. An empty Model on a
	// request means the provider chose.
	Provider string
	Model    string

	// RequestID and CorrelationID are the ids the request went out under: the
	// caller's when it put them on the context, the library's otherwise. Empty
	// means none was in use.
	RequestID     string
	CorrelationID string

	// Mode and Tier are the request's settings. Zero means the request did not
	// carry one.
	Mode schemaflux.Mode
	Tier schemaflux.Tier

	// SchemaName is the name of the schema the request asks the provider to
	// answer in, as it goes on the wire. It is the shape's name, never its
	// contents and never a value decoded into it. Empty means the request
	// declares no schema.
	SchemaName string

	// ResponseFormat is what the request declared it needs back: "json" or
	// "text". Empty means the operation did not say. It is a setting the library
	// chose and not anything the caller supplied.
	ResponseFormat string

	// Repair is true for a request that asks again because the answer to an
	// earlier request of the same call was refused. False is a first request.
	Repair bool

	// Stream is true for a request whose answer is delivered as it is produced.
	// False is a request answered whole.
	Stream bool
}

// A Result describes a unit of work that has finished. Its figures describe
// the unit: a request's are those of its attempts, an operation's of the call.
type Result struct {
	// Scope is the scope of the start event this result finishes.
	Scope Scope

	// Operation is the name the start event carried.
	Operation string

	// Duration is the wall-clock time of the whole unit. For a request that is
	// every attempt and the waits between them. Zero means it was not measured.
	Duration time.Duration

	// ProviderDuration is the part of a request's Duration spent inside
	// provider calls, summed over the attempts. The rest is the library's own
	// wait before asking again, so a reader can tell a slow provider from a
	// request that waited. Zero means it was not measured, and is always zero on
	// an operation's result.
	ProviderDuration time.Duration

	// Usage is the token count of the answer the request ended with. The zero
	// Usage means no provider answered or none reported a count: it is not a
	// count of zero. An operation's result carries none.
	Usage schemaflux.Usage

	// Cost is what that answer came to. The zero Cost means no price was worked
	// out, which is not a cost of nothing; a Cost with a Currency and no Priced
	// means a price was looked for and none was found. An operation's result
	// carries none.
	Cost schemaflux.Cost

	// Attempts is the number of provider calls a request made, retries
	// included. Zero means none was made, and it is always zero on an
	// operation's result.
	Attempts int

	// Provider and Model name what actually answered, which is not always what
	// was asked for: the Event carries the request's version of both. Empty
	// means no provider answered.
	Provider string
	Model    string

	// FinishReason is the provider's own word for why generation stopped
	// ("stop", "length"). It is a provider enum and not content. A truncated
	// answer arrives with the same Kind as a complete one, so this is what tells
	// them apart. Empty means no answer, or none that said.
	FinishReason string

	// Cached is true on a request that a response cache answered (mw.Cache): no
	// provider was called, so Attempts is zero, Usage and Cost are the zero
	// values and Provider and Model are empty, since nothing answered. A hit is
	// not paid for.
	Cached bool

	// AskedFirst, AskedFirstModel and Escalated say that a decision provider was
	// asked before the provider that answered (mw.Decisions) and why its answer
	// was not used. AskedFirst is the provider, AskedFirstModel the model it was
	// asked under, and Escalated one of the library's fixed reasons: "below the
	// floor", "unavailable" or "unsupported". All three are empty when nobody was
	// asked first. Provider and Model name who answered, and Usage, Cost and
	// Attempts include the call that was asked first when it answered, since it
	// was made and paid for. The question is never carried.
	AskedFirst      string
	AskedFirstModel string
	Escalated       string

	// Requests is the number of requests to a provider that an operation's call
	// made, including those of the operations it ran itself. It is zero on a
	// request's result, and on an operation that was refused before anything was
	// sent.
	Requests int

	// Items, Succeeded, Failed and Cancelled say what became of the items of an
	// operation that ran over many: Items is how many it was given, and the other
	// three split them and add up to it. All four are zero on any other result,
	// which is how a run is told from a call of one item. A run that fails as a
	// whole and returns no item reports every item as failed. They are counts
	// only: no item's data, identifier or error is carried.
	Items     int
	Succeeded int
	Failed    int
	Cancelled int

	// Kind is how the unit ended. The zero Kind means it succeeded; any other
	// value is the kind of the failure, as in the Error a caller of the
	// operation holds. A request that got an answer that was later refused
	// succeeded, and its operation did not.
	//
	// A unit that ended because the code running it panicked has ErrInternal,
	// the panic continues after the finish, and a failure that carried no kind of
	// its own, which only code outside the library can produce, has ErrInternal
	// as well. A cancelled call (ErrCanceled) and an expired deadline
	// (ErrTimeout) are told apart from a failure of the provider.
	//
	// The Kind is the one the *Error the caller holds has, for every kind: an
	// error that reaches the caller as ErrInternal, a middleware's copy that
	// panicked among them, is ErrInternal here too, and not an answer that was
	// invalid. A run over many items in which every item failed and that returned
	// a result, with no error, carries the Kind of the first item's failure; one
	// in which some item succeeded carries none, and its counts say the rest.
	Kind schemaflux.Kind
}

// The names a Note can carry.
const (
	// NoteRetry says an attempt of the request failed and another will be
	// made. Attempt is the attempt that failed, Kind why, and Wait how long the
	// library waits before the next.
	NoteRetry = "retry"

	// NoteHedge says a second copy of the request was sent because the first
	// had not answered in time. Attempt is the copy's number, 2.
	NoteHedge = "hedge"
)

// A Note is something that happened inside a request while it was open. It
// carries no text of the caller's or the provider's.
type Note struct {
	// Name is NoteRetry or NoteHedge.
	Name string

	// Attempt is the attempt the note is about, counted from 1. Zero means the
	// note does not say.
	Attempt int

	// Kind is the kind of the failure that caused a retry. The zero Kind means
	// the note is not about a failure, as a hedge is not.
	Kind schemaflux.Kind

	// Wait is the pause before the next attempt of a retry. Zero means no
	// pause, and is always zero on a hedge.
	Wait time.Duration
}

// An Observer receives what the library did. Implementations must be safe for
// concurrent use and must not block: both methods run on the goroutine making
// the call, so an observer that stalls stalls the operation it observes. One
// with slow work to do queues it and returns.
//
// Started returns the context the unit runs on, so that an implementation that
// opens a span can put it there; one with nothing to add returns ctx. The
// context it returns is used only if it descends from ctx and ends when ctx
// does, and any other is ignored, so that an observer cannot drop the caller's
// deadline, cancellation or client from the call. Finished is given the
// context that implementation's own Started returned for the same unit.
//
// A panic in either method is recovered and logged, and reaches neither the
// call nor another observer.
type Observer interface {
	Started(ctx context.Context, e Event) context.Context
	Finished(ctx context.Context, r Result)
}

// A NoteObserver is an Observer that also wants the notes of open requests.
// Noted is given the context the observer's own Started returned for the request
// the note belongs to.
type NoteObserver interface {
	Noted(ctx context.Context, n Note)
}

// RegisterObserver registers o beside the observers already registered and
// returns the function that removes this registration and no other. Any number
// of observers can be registered, each is isolated from the others' panics, and
// the removals may run in any order and more than once.
//
// A unit that started while o was registered is still finished to it after the
// removal, so an observer that opens a span in Started always gets to end it;
// one registered after a unit started never sees it. Registering the same
// observer twice is two registrations, each removed by its own function. A nil
// observer registers nothing, and its remove does nothing.
func RegisterObserver(o Observer) (restore func()) {
	if o == nil {
		return func() {}
	}
	registered := &observer{observer: o}
	registered.noted, _ = o.(NoteObserver)
	return wrapped.AddObserver(registered)
}

// observer is a registered Observer as the package it wraps sees it. It hands
// the observer's context back exactly as the observer returned it: the check
// that the context descends from the one given is the wrapped package's, and
// anything done to the context here would stand between the two.
type observer struct {
	observer Observer
	noted    NoteObserver
}

func (o *observer) OperationStarted(ctx context.Context, e wrapped.OperationEvent) context.Context {
	return o.observer.Started(ctx, eventFrom(e))
}

func (o *observer) OperationFinished(ctx context.Context, r wrapped.OperationResult) {
	o.observer.Finished(ctx, resultFrom(r))
}

// OperationNoted is on every registered observer, so that the package it wraps
// finds it; an observer that is not a NoteObserver is given nothing.
func (o *observer) OperationNoted(ctx context.Context, n wrapped.Note) {
	if o.noted != nil {
		o.noted.Noted(ctx, noteFrom(n))
	}
}
