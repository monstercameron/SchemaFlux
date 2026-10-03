// Package otel turns what the library does into OpenTelemetry spans on a
// TracerProvider the host passes in.
//
// # Setting it up
//
// The host builds its own OpenTelemetry SDK the way OpenTelemetry documents:
// its exporters, its sampler, its resource, its propagator, configured with
// OpenTelemetry's own OTEL_* variables if it likes, and shut down by the host
// when it exits. Then one call hands the provider to the library:
//
//	provider := sdktrace.NewTracerProvider(...) // the host's, however it builds it
//	restore, err := otel.Install(provider)
//	if err != nil { ... }
//	defer restore()
//
// The import is github.com/monstercameron/schemaflux/v2/telemetry/otel, whose
// name is the same as OpenTelemetry's own otel package; a program that imports
// both gives this one a name of its own, such as fluxotel.
//
// From then on every operation of the library produces spans on that provider,
// as children of whatever span is on the context the operation was given. This
// package builds no exporter, reads no environment variable, picks no sampler,
// and never calls otel.SetTracerProvider or otel.SetTextMapPropagator: those
// are the host's, and a library that sets them cannot be embedded beside
// another one that does. It links no exporter, no gRPC and no protobuf.
//
// # What Install changes, and how to undo it
//
// Install changes two things in the process, both inside this library:
//
//   - it registers an Observer with the library's observers
//     (telemetry.RegisterObserver), which is what produces the spans;
//   - it installs the readers that let the library report the trace and span
//     id of the span a call ran in (InstallIDSources): they are what
//     telemetry.TraceID and telemetry.SpanID return, and what the request id
//     strategy requestid.StrategyTrace reads.
//
// The function Install returns undoes both and nothing else. Installing twice,
// with the same provider or with two, produces spans on each install; the
// restores may run in any order and more than once. A call that is in flight
// when a restore runs still ends the spans it began. New returns the Observer
// alone, for a host that registers it itself.
//
// # Shutting down
//
// Shut down in the reverse order of setting up: call the restore function, so
// that no new call starts a span; wait for the calls still in flight to return,
// since each ends the spans it began; then shut the provider down, which
// flushes what is buffered. A span that ends after the host has shut its
// provider down is dropped without an error, because the provider no longer
// exports. Install starts nothing of its own that needs stopping.
//
// # Finding the library's spans
//
// Every span comes from the instrumentation scope named
// "github.com/monstercameron/schemaflux", at the library's version and with the
// semantic conventions' schema URL. A View, a filter or a sampler rule that
// wants only these spans, or none of them, selects on that name.
//
// # What a trace of one operation looks like
//
// One call of an operation is one span of kind Internal named
// "schemaflux.<operation>", for example "schemaflux.extract". Each request the
// call makes to a provider is a child span of kind Client named
// "chat <model>", by the OpenTelemetry convention for generative AI. A request
// that is retried is still one span: each retry is a span event named "retry"
// on it. A repair, where the answer was refused and the library asks again, is
// a second request span under the same operation span, marked
// schemaflux.repair. A hedged copy of a request (mw.Hedge) is a span event
// named "hedge". So an extraction whose first answer was the wrong shape and
// whose repair worked reads:
//
//	schemaflux.extract            Internal
//	├── chat gpt-5.6-luna         Client
//	└── chat gpt-5.6-luna         Client   schemaflux.repair=true
//
// The operation span carries the outcome of the whole call. When the answer is
// refused, repairs run out, a check fails or the call is cancelled, it has
// status Error, while each request span says only whether that request got an
// answer. Every span of one call names the same operation. A call that is
// refused before anything is sent, a nil input or an option that cannot be
// used, has an operation span with status Error and no request span.
//
// The functions of the root that ask a model, Extract, Summarize, Classify, Is,
// Which, Rate, Assess, Sort, Filter, Validate and the rest of the text, judging
// and collection functions, each have an operation span of their own, opened by
// the call they begin, and a test drives every one it lists with an observer
// registered. The streaming functions do the same through the stream. So do the
// custom operations run through the engines and the engines that run many items
// or stages. A request made by an operation that names no operation of its own is
// a child of the caller's span directly, and carries no schemaflux.operation: a
// trace is never missing its requests, and one made by code that does not open an
// operation may be missing the operation above them.
//
// A run over many items is one operation span named for the operation, whichever
// entry it came in by, and the requests of its items and chunks are its children;
// a staged plan is one span named for the plan, with the operation span of each
// stage under it. So two hundred items look the same from every entry: one span,
// and what the items did is on it as counts (see Attributes), never as a span for
// each item. A run that collects its failures returns a result and no error, and
// the counts are where its failed items show.
//
// A request that a middleware answered some other way says so. A decision
// provider that was asked first (mw.Decisions) is named on the request span with
// the reason its answer was not used, and the request's usage and attempts count
// its call as well as the one that answered, as the call's own record does. A
// response cache's hit (mw.Cache) is marked, and says nothing was paid. A request
// that went out twice (mw.Hedge) is marked, and has an event for the second copy.
//
// A call with an observer registered takes the path that reports it, the one a
// call takes when it has a recorder or a contract floor, and not the plain path
// of one that has none of the three. What a call does and returns is meant to be
// the same on both, and a difference would be a defect; the cost is the second
// path's, which is the work of reporting.
//
// Two adapters exist, this one and the first version's. They are fed by the same
// dispatcher and write the same spans, except that the first names the tier
// attribute after its own word for it. A program that registers both gets two
// spans for each unit, and one that moves from the first to this loses any
// dashboard keyed on the first's name for the tier.
//
// A streamed answer is one request span, started when the stream's request is
// made and ended when the stream ends, fails, is cancelled, or the reader
// leaves it by returning from or panicking out of its loop. A stream that
// nobody reads to its end and nobody cancels ends at the stream's timeout.
//
// The span ends when the provider's own stream returns, which is when the
// library learns the request is over. A provider that obeys its context returns
// as soon as the stream is cancelled or its timeout passes. One that ignores its
// context keeps the span open until it returns, however long that is, though the
// reader has already been released: the span's duration is then the provider's,
// not the reader's wait. A reader that leaves a stream before its end, by
// breaking out of the loop or by calling Cancel, ends the request with status
// Error "canceled", since the answer was not delivered; filter on
// schemaflux.stream to tell those from failures of the provider.
//
// # Attributes
//
// The gen_ai.* and error.type keys follow the OpenTelemetry semantic
// conventions for generative AI as published in semantic conventions v1.41.0
// (go.opentelemetry.io/otel/semconv/v1.41.0, whose constants are used here).
// Upstream marks every gen_ai.* attribute as in development, so these names
// can change with a later version of the convention. The library's own facts
// are under schemaflux.* and nowhere else.
//
// On a request span, set when the span starts:
//
//	gen_ai.operation.name        string   always "chat"
//	gen_ai.provider.name         string   the provider the request was sent to
//	gen_ai.request.model         string   the model asked for
//	gen_ai.output.type           string   "json" or "text", when the operation declared one
//	schemaflux.operation         string   the operation the request serves, when it names itself
//	schemaflux.schema.name       string   the name of the schema asked for, when there is one
//	schemaflux.mode              string   the request's mode: strict, transform or creative
//	schemaflux.tier              string   the tier the request asked for: smart, fast or quick
//	schemaflux.request_id        string   the request id, at most 128 printable characters
//	schemaflux.correlation_id    string   the correlation id, bounded the same way
//	schemaflux.repair            bool     true on a repair; absent otherwise
//	schemaflux.stream            bool     true on a streamed answer; absent otherwise
//
// On a request span, set when it ends:
//
//	schemaflux.attempts                       int       provider calls made, retries included
//	schemaflux.provider_duration_ms           int64     time inside provider calls, when measured
//	gen_ai.response.model                     string    the model that answered
//	gen_ai.response.finish_reasons            []string  the provider's reason for stopping
//	gen_ai.usage.input_tokens                 int       input tokens, over every answered attempt
//	gen_ai.usage.output_tokens                int       output tokens, over every answered attempt
//	gen_ai.usage.cache_read.input_tokens      int       input tokens read from the provider's cache, when any
//	gen_ai.usage.cache_creation.input_tokens  int       input tokens written to it, when any
//	gen_ai.usage.reasoning.output_tokens      int       reasoning tokens, when any
//	schemaflux.response.provider              string    the provider that answered
//	schemaflux.cost.priced                    bool      whether a price for the model was known
//	schemaflux.cost.total                     float64   the cost of the answered attempts, when priced
//	schemaflux.cost.currency                  string    its currency, when priced
//	schemaflux.cache.hit                      bool      true when a response cache answered; absent otherwise
//	schemaflux.escalation.provider            string    the decision provider asked first, when one was
//	schemaflux.escalation.model               string    the model it was asked under
//	schemaflux.escalation.reason              string    why its answer was not used: "below the floor", "unavailable" or "unsupported"
//	error.type                                string    the library's error kind, on failure
//
// And on a request span, set when the second copy of a hedged request is sent:
//
//	schemaflux.hedged                         bool      true; absent on a request that was sent once
//
// The response, usage and cost attributes are set only when a provider
// answered; a request that failed before any answer has none of them, rather
// than zeros. schemaflux.cost.priced is absent when no price was worked out, and
// false when one was looked for and none was found. A request that was retried
// after an answer was refused (an empty one, a truncated one) reports the
// tokens and the cost of every attempt that was answered, since each was
// charged. A request whose decision provider was asked first and answered counts
// that call in schemaflux.attempts and in the usage and cost, and
// schemaflux.response.provider names the one that answered. A request a response
// cache answered has schemaflux.attempts 0 and token counts of 0, which say that
// nothing was bought, and no response provider, model or cost, since nothing
// answered. The convention's cache attributes describe a provider's own prompt
// cache, a different thing, so the library's own key marks the hit. A hedged
// request's schemaflux.attempts counts the calls the library made; the second
// copy is the middleware's, and the attribute and the event say it went out.
//
// On an operation span:
//
//	schemaflux.operation         string   the operation's name
//	schemaflux.requests          int      requests the call made to a provider, those of the operations it ran itself included
//	schemaflux.items.total       int      for a run over many items, how many it was given; absent on any other call
//	schemaflux.items.succeeded   int      how many of them succeeded
//	schemaflux.items.failed      int      how many failed; a run that fails as a whole and returns no item counts every item
//	schemaflux.items.cancelled   int      how many were cancelled before they ran or while they ran, by a policy that stops at the first failure
//	error.type                   string   the library's error kind, on failure
//
// The item counts are numbers and nothing else: no item's data, identifier or
// error is on any span.
//
// On a "retry" event: schemaflux.attempt (int, the attempt that failed),
// error.type (string, why) and schemaflux.backoff_ms (int64, the wait before
// the next). On a "hedge" event: schemaflux.attempt (int, the copy sent).
//
// error.type is the library's own name for the failure: the name of the Kind of
// the Error a caller of the operation would hold, spelled as the library spells
// its kinds, with spaces where a kind has several words. A cancelled call
// ("canceled") and an expired deadline ("timeout") are told apart from a failure
// of the provider. The complete list of values, on a span and on a retry event,
// is:
//
//	budget exceeded
//	busy
//	canceled
//	closed
//	configuration
//	internal
//	invalid answer
//	invalid request
//	policy
//	rate limited
//	refused
//	review required
//	timeout
//	too large
//	truncated
//	unauthorized
//	unavailable
//	unsupported
//
// "internal" is a call that ended in a panic, an error that reports an internal
// failure, a middleware's copy that panicked among them, and a failure that
// carries no kind of its own, which only code outside the library can produce: a
// custom provider's, or a host's own validator's. A failure the library itself
// raises always has one of the others. For every one of the eighteen kinds, the
// span, the event and the *Error the caller holds name the same kind, and a test
// holds that for each through a real operation: an internal failure is never
// reported as an answer that was invalid. A test keeps this list equal to the
// library's kinds.
//
// The values have spaces where a kind has several words, as the library spells
// its kinds. The OpenTelemetry convention asks that error.type be predictable
// and low in cardinality and that an instrumentation document the list it
// reports, which this list is; it does not ask for a spelling, and its examples
// include both "timeout" and "server_certificate_invalid". The strings are the
// ones the Kind of an *Error spells, so a filter written against the kind of an
// error a caller logs matches the span's.
//
// # Status
//
// A span that failed has status Error with the error kind as its description.
// A span that succeeded is left Unset, which is what OpenTelemetry asks of
// instrumentation: Ok is the application's to set. See the comment in
// Observer.Finished.
//
// A run over many items follows the call, not the items. When the call returns
// an error the run's span is Error. When it returns a result in which some item
// succeeded, the call succeeded, and the span is Unset with the counts to say
// how many items did not. When it returns a result in which every item failed,
// nothing was produced, and the span is Error with the kind of the first item's
// failure, though the call returned no error: an alert on the error rate of
// operations would otherwise never see a run that did nothing. The request spans
// of the items that failed at the provider are Error themselves.
//
// # What is never recorded
//
// No span name, attribute, event or status description holds a prompt, an
// input, an answer, a steering text, a credential, a URL, or a provider's free
// text, and no error is recorded on a span (RecordError is not called): an
// error's text can quote a provider's echo of the caller's data. A failure is
// its kind and nothing more.
//
// The strings that a caller or a provider supplies, and that reach a span, are
// the request and correlation ids the caller chose; the model the caller
// pinned, and the model, provider and finish reason the provider answered
// with; the provider's name; the name of the schema; and the name of a custom
// operation. Each is cut to a fixed length (128 characters for an id, a model
// and a schema name; 64 for a provider, a finish reason and an operation name)
// and anything unprintable in it is replaced by "?", in the span's name as well
// as in its attributes.
//
// The request id strategy requestid.StrategyTrace, which makes a request's id
// the trace id of the span on the caller's context, needs Install, or
// InstallIDSources, to have a trace to read.
//
// What a head sampler or a span processor may want to choose on, the operation,
// the provider and the model, is given to the tracer when the span is made, so
// the sampler sees gen_ai.operation.name, gen_ai.provider.name,
// gen_ai.request.model and schemaflux.operation. The rest is set after.
//
// When a sampler drops a span, nothing is computed for it beyond those
// attributes and nothing is set on it, and it is still ended, once.
package otel
