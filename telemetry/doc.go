// Package telemetry is for a host that wants to export what the library does:
// spans, through an observer, and the library's own metrics, through a sink.
// It is the seam the library emits through. It imports no tracing vendor and
// configures none; the subpackage telemetry/otel is the adapter for
// OpenTelemetry, and an adapter for another vendor is an Observer of the same
// kind.
//
// # Events
//
// The library describes its work at two scopes, named by Scope. A request
// (ScopeRequest) is one logical request to a provider: every attempt at it and
// the waits between them. An operation (ScopeOperation) is one call of an
// operation, which may make several requests and then decodes, checks and
// repairs the answer. Each unit is bracketed by a start (an Event) and a finish
// (a Result), and a request that is still open can be sent a Note: a retry, a
// hedged copy.
//
// A request made under an operation starts from the context the operation's
// start returned. An observer that opens a span in Started and returns the
// context that carries it therefore sees every request of the call as the
// span's child, and the operation's finish carries the outcome of the whole
// call, which no request's can: a request whose answer is later refused
// finished without a failure. A call that makes no request, because it was
// refused before anything was sent, has an operation and no request.
//
// # Observers
//
// RegisterObserver registers an observer and returns the function that
// removes it:
//
//	restore := telemetry.RegisterObserver(myObserver)
//	defer restore()
//
// Any number can be registered at once. The start of a unit records which
// observers it called, and the finish calls exactly those, in the reverse
// order, whatever has been registered or removed in between: a unit that an
// observer saw begin is always one it sees end, and one it did not see begin it
// never sees end. An observer that also implements NoteObserver is sent the
// notes of the requests it saw begin.
//
// An observer cannot change what a call does. Both methods run on the
// goroutine making the call, so an observer must not block: one that has slow
// work to do queues it and returns. A panic in either is recovered and logged
// without the panic's value, which a host's formatting bug can fill with the
// caller's data, and reaches neither the call nor another observer; the observer
// that panicked in Started is not told the unit finished. The context Started
// returns is used only if it descends from the one it was given and ends when
// that one does, which is what a context.WithValue, a WithCancel or a span
// started from it satisfies. Any other context, context.Background() being the
// usual mistake, would drop the caller's deadline, cancellation and client from
// the call, so it is passed over: the call goes on with the context it had, and
// a warning is logged once for that observer.
//
// With no observer registered the library's emission costs an atomic load and
// allocates nothing.
//
// # What an event holds
//
// No event, at either scope, carries a prompt, an input, an answer, a steering
// text, a credential or a provider's free text: names, identifiers, counts, the
// library's own kinds and the settings of the call, nothing else. The strings
// that a caller or a provider chose are the request and correlation ids, the
// model, the provider, the schema name, the operation of a custom operation and
// the finish reason; each arrives cut to a fixed length (128 bytes for an id, a
// model and a schema name, 64 for the rest) with every byte that is not
// printable ASCII replaced by "?", so an observer can write one to a log or a
// span without a second look. How a call ended is a schemaflux.Kind, never the
// error's message, which can quote the request back.
//
// # Metrics
//
// The library records its own metrics as it works: requests by outcome, their
// durations, token counts and costs, and the durations of operations. They are
// aggregated in the process while at least one Sink is registered, and
// Snapshots reads the aggregates:
//
//	remove := telemetry.RegisterSink(mySink)
//	defer remove()
//
// A Sink receives each observation as it is recorded, in the order the sinks
// were registered. Like an observer it runs on the goroutine that recorded the
// metric, which is usually the one making a call, so it must not block; a panic
// in one is recovered and logged by the metric's name alone. Registering the first
// sink is what turns recording on, with no other setting, and recording stops,
// and the aggregates stop changing, when the last sink is removed, unless the
// process turned recording on itself (SCHEMAFLUX_METRICS=true). Each sink is
// given a map of tags of its own.
//
// The registry is bounded in two ways. Each tag keeps at most 64 distinct values,
// whatever metric they are recorded with: a value it has not held, once it holds
// that many, is recorded as "other", so a tag that takes a new value for every
// request, a model name that a gateway makes up, cannot fill the registry for
// everything else. And it keeps at most 2048 series, one for each distinct name
// and set of tags; an observation that would start a series past that limit is
// counted in a series named "schemaflux_metric_series_overflow" instead, which
// has no tags and whose Count and Sum are the number of observations turned
// away. A sink is still given every observation, with the tag values as they
// were.
//
// # Process state
//
// The observers, the sinks and the readers of SetSpanIDSources belong to the
// process, because the library's call path emits to them from every call of
// every client. Each registering function returns the function that undoes
// exactly that registration, and the undoing functions may run in any order and
// more than once. A test that registers one restores it, and does not run in
// parallel with another that registers one.
package telemetry
