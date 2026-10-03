// Package otel turns what SchemaFlux does into OpenTelemetry spans on a
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
// From then on every operation of the library produces spans on that
// provider, as children of whatever span is on the context the operation was
// given. This package builds no exporter, reads no environment variable, picks
// no sampler, and never calls otel.SetTracerProvider or
// otel.SetTextMapPropagator: those are the host's, and a library that sets
// them cannot be embedded beside another one that does.
//
// # What Install changes, and how to undo it
//
// Install changes two things in the process, both inside this library:
//
//   - it adds an Observer to the library's set of observers
//     (telemetry.AddObserver), which is what produces the spans;
//   - it installs the readers that let the library report the trace and span
//     ID of the span a call ran in (InstallIDSources): they fill
//     ResultMetadata.TraceID and SpanID, and they are what a request ID
//     strategy of "trace" reads.
//
// The function Install returns undoes both and nothing else. Installing twice,
// with the same provider or with two, produces spans on each install; the
// restores may run in any order. A call that is in flight when a restore runs
// still ends the spans it began. New returns the Observer alone, for a host
// that wants to add it itself.
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
// answer. Every span of one call names the same operation.
//
// Not every operation has a span of its own yet. Extract in both its forms (a
// call refused before anything is sent included), the custom operations run
// through RunOp, the Result forms built on the shared envelope, and the engines
// that run many items or stages do. A request made by an operation that has none
// is a child of the caller's span directly, and carries no schemaflux.operation
// unless the operation names itself.
//
// A run over many items (RunOpMany, RunOpManyPartial, RunOpManyRecover) is one
// operation span named for the operation, whichever entry it came in by, and the
// requests of its items and chunks are its children. A staged plan
// (RunStagedPlan) is one span named for the plan, with the operation span of each
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
//	schemaflux.mode              string   the request's mode
//	schemaflux.intelligence      string   the tier the request asked for
//	schemaflux.request_id        string   the request ID, at most 128 printable characters
//	schemaflux.correlation_id    string   the correlation ID, bounded the same way
//	schemaflux.repair            bool     true on a repair; absent otherwise
//	schemaflux.stream            bool     true on a streamed answer; absent otherwise
//
// On a request span, set when it ends:
//
//	schemaflux.attempts                       int       provider calls made, retries included
//	schemaflux.provider_duration_ms           int64     time inside provider calls, when measured
//	gen_ai.response.model                     string    the model that answered
//	gen_ai.response.finish_reasons            []string  the provider's reason for stopping
//	gen_ai.usage.input_tokens                 int       prompt tokens, over every answered attempt
//	gen_ai.usage.output_tokens                int       completion tokens, over every answered attempt
//	gen_ai.usage.cache_read.input_tokens      int       prompt tokens read from the provider's cache, when any
//	gen_ai.usage.cache_creation.input_tokens  int       prompt tokens written to it, when any
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
// than zeros. A request that was retried after an answer was refused (an empty
// one, a truncated one) reports the tokens and the cost of every attempt that
// was answered, since each was charged. A request whose decision provider was
// asked first and answered counts that call in schemaflux.attempts and in the
// usage and cost, and schemaflux.response.provider names the one that answered.
// A request a response cache answered has schemaflux.attempts 0 and token counts
// of 0, which say that nothing was bought, and no response provider, model or
// cost, since nothing answered. The convention's cache attributes describe a
// provider's own prompt cache, a different thing, so the library's own key marks
// the hit. A hedged request's schemaflux.attempts counts the calls the library
// made; the second copy is the middleware's, and the attribute and the event
// say it went out.
//
// On an operation span:
//
//	schemaflux.operation         string   the operation's name
//	schemaflux.requests          int      requests the call made to a provider, those of the operations it ran itself included
//	schemaflux.items.total       int      for a run over many items, how many it was given; absent on any other call
//	schemaflux.items.succeeded   int      how many of them succeeded
//	schemaflux.items.failed      int      how many failed; a run that fails as a whole and returns no item, as RunOpMany does, counts every item
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
// error.type is the library's own name for the failure, spelled as the library
// spells its error kinds, with spaces where a kind has several words. A
// cancelled call ("canceled") and an expired deadline ("timeout") are told
// apart from a failure of the provider. The complete list of values, on a span
// and on a retry event, is:
//
//	admission rejected
//	authentication
//	batch protocol violation
//	budget exceeded
//	canceled
//	circuit open
//	configuration
//	context too large
//	evidence violation
//	internal
//	invalid request
//	invariant violation
//	malformed output
//	output truncated
//	permission
//	policy violation
//	provider unavailable
//	rate limited
//	refused
//	repair exhausted
//	review required
//	schema violation
//	shutdown
//	timeout
//	unknown
//	unsupported capability
//
// "internal" is a call that ended in a panic, and an error that carries the mark
// of a recovered panic: a middleware's copy that panicked, a step of a Gather
// that did, a provider that reports an internal failure. The library files those
// under "invariant violation", which is also what an answer that broke its own
// rules is; the span says "internal" for the first and "invariant violation" for
// the second, so an alert on a defect in code is not set off by a model's
// answer, and one on a model's answers is not set off by a defect. "refused" is
// an answer the model declined to give, or that the provider's content filter
// withheld: the library files it under "policy violation", which is also what a
// data policy that forbids a route is, and tells the two apart here. "unknown" is
// an error that carries no kind, which only code outside the library can
// produce: a custom provider's, or a host's own validator's. A failure the
// library itself raises always has one of the others. For every kind, the span,
// the event and the error the caller holds name the same one, and a test holds
// that for each kind through a real operation. A test keeps this list equal to
// the library's kinds.
//
// The values have spaces where a kind has several words, as the library spells
// its kinds. The OpenTelemetry convention asks that error.type be predictable
// and low in cardinality and that an instrumentation document the list it
// reports, which this list is; it does not ask for a spelling, and its examples
// include both "timeout" and "server_certificate_invalid". The strings are the
// ones the library's own errors carry, so a filter written against the kind of
// an error a caller logs matches the span's.
//
// # Status
//
// A span that failed has status Error with the error kind as its description.
// A span that succeeded is left Unset, which is what OpenTelemetry asks of
// instrumentation: Ok is the application's to set. See the comment in
// OperationFinished.
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
// the request and correlation IDs the caller chose; the model the caller
// pinned, and the model, provider and finish reason the provider answered
// with; the provider's name; the name of the schema; and the name of a custom
// operation. Each is cut to a fixed length (128 characters for an ID, a model
// and a schema name; 64 for a provider, a finish reason and an operation name)
// and anything unprintable in it is replaced by "?", in the span's name as well
// as in its attributes.
//
// The request ID strategy "trace", which makes a request's ID the trace ID of
// the span on the caller's context, is chosen by the library's own setting
// SCHEMAFLUX_REQUEST_ID_STRATEGY=trace and needs Install, or InstallIDSources,
// to have a trace to read.
//
// What a head sampler or a span processor may want to choose on, the operation,
// the provider and the model, is given to the tracer when the span is made, so
// the sampler sees gen_ai.operation.name, gen_ai.provider.name,
// gen_ai.request.model and schemaflux.operation. The rest is set after.
//
// When a sampler drops a span, nothing is computed for it beyond those
// attributes and nothing is set on it, and it is still ended, once.
package otel

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/telemetry"
)

// instrumentationName identifies this library as the source of the spans it
// produces, which is what lets a host filter them.
const instrumentationName = "github.com/monstercameron/schemaflux"

// The library's own attribute keys. Everything the conventions have a name for
// uses that name instead (see the package comment).
const (
	keyOperation        = attribute.Key("schemaflux.operation")
	keySchemaName       = attribute.Key("schemaflux.schema.name")
	keyMode             = attribute.Key("schemaflux.mode")
	keyIntelligence     = attribute.Key("schemaflux.intelligence")
	keyRequestID        = attribute.Key("schemaflux.request_id")
	keyCorrelationID    = attribute.Key("schemaflux.correlation_id")
	keyRepair           = attribute.Key("schemaflux.repair")
	keyStream           = attribute.Key("schemaflux.stream")
	keyAttempts         = attribute.Key("schemaflux.attempts")
	keyProviderDuration = attribute.Key("schemaflux.provider_duration_ms")
	keyResponseProvider = attribute.Key("schemaflux.response.provider")
	keyCostPriced       = attribute.Key("schemaflux.cost.priced")
	keyCostTotal        = attribute.Key("schemaflux.cost.total")
	keyCostCurrency     = attribute.Key("schemaflux.cost.currency")
	keyAttempt          = attribute.Key("schemaflux.attempt")
	keyBackoff          = attribute.Key("schemaflux.backoff_ms")

	keyCacheHit           = attribute.Key("schemaflux.cache.hit")
	keyHedged             = attribute.Key("schemaflux.hedged")
	keyEscalationProvider = attribute.Key("schemaflux.escalation.provider")
	keyEscalationModel    = attribute.Key("schemaflux.escalation.model")
	keyEscalationReason   = attribute.Key("schemaflux.escalation.reason")
	keyRequests           = attribute.Key("schemaflux.requests")
	keyItems              = attribute.Key("schemaflux.items.total")
	keyItemsSucceeded     = attribute.Key("schemaflux.items.succeeded")
	keyItemsFailed        = attribute.Key("schemaflux.items.failed")
	keyItemsCancelled     = attribute.Key("schemaflux.items.cancelled")
)

// The longest a string that comes from a caller or a provider may be on a span,
// one limit for each kind of value. A span is exported to a backend the host
// chose, its name is what a trace view lists, and none of these strings is the
// library's own: a request or correlation ID is whatever the caller chose, a
// model is the one the caller pinned and the one the provider says answered, a
// provider is what a registered provider calls itself and what it answered
// with, a finish reason is the provider's word, and a schema or an operation
// is named by the caller's own type or custom operation. Each is cut to its
// limit and made printable by bounded.
const (
	maxIDLength           = 128
	maxModelLength        = 128
	maxProviderLength     = 64
	maxFinishReasonLength = 64
	maxSchemaNameLength   = 128
	maxOperationLength    = 64
	maxEscalationLength   = 64
)

// Observer implements telemetry.Observer and telemetry.NoteObserver by opening
// spans on a tracer taken from the host's provider: one for each call of an
// operation and one for each request to a model. The package comment describes
// the spans and their attributes.
//
// An Observer ends the spans its own OperationStarted opened and no other. The
// span on the context at that point may be the host's own, and ending it early,
// or giving it this library's outcome, would corrupt the host's trace. So each
// span it opens is recorded in the context it returns, and a finish ends only a
// span recorded there by the same Observer. A zero Observer, and a nil one,
// open nothing and finish nothing.
type Observer struct {
	tracer oteltrace.Tracer
}

// openedSpanKey is the context key of the innermost span an Observer of this
// package opened.
type openedSpanKey struct{}

// openedSpan is a span an Observer opened.
type openedSpan struct {
	owner *Observer
	scope telemetry.Scope
	span  oteltrace.Span
	// before is the span that was current on the context the span was started
	// from, and outer the span opened before this one on the same context, by
	// this Observer or another.
	before oteltrace.Span
	outer  *openedSpan
	// ended makes End happen once, whether or not the span is recording.
	ended atomic.Bool
}

// New returns an Observer that makes spans on the provider the host supplies.
// It installs nothing: pass the result to telemetry.AddObserver, or call
// Install, which does that and installs the ID sources.
//
// A nil provider is refused rather than replaced by the global one: silently
// falling back to otel.GetTracerProvider() is how a library ends up writing
// into a stack nobody asked it to write into.
func New(provider oteltrace.TracerProvider) (*Observer, error) {
	if provider == nil {
		return nil, fmt.Errorf("otel: no TracerProvider; pass the one your application configured")
	}
	return &Observer{tracer: provider.Tracer(instrumentationName,
		oteltrace.WithInstrumentationVersion(types.Version),
		oteltrace.WithSchemaURL(semconv.SchemaURL),
	)}, nil
}

// Install sets tracing up whole in one call: it adds an Observer on provider
// to the library's observers, so operations produce spans, and it installs the
// trace and span ID sources (InstallIDSources), so results and request IDs can
// carry them. It returns the function that undoes exactly that.
//
// It changes nothing outside this library: not the global TracerProvider, not
// the global propagator, and it starts nothing that needs shutting down. The
// provider stays the host's to flush and shut down.
//
// Install may be called more than once, and the restores may run in any order
// and more than once. Spans begun before a restore are still ended after it.
func Install(provider oteltrace.TracerProvider) (restore func(), err error) {
	observer, err := New(provider)
	if err != nil {
		return nil, err
	}
	removeObserver := telemetry.AddObserver(observer)
	removeIDSources := InstallIDSources()

	return func() {
		removeIDSources()
		removeObserver()
	}, nil
}

// OperationStarted opens a span for the unit, as a child of the span on ctx,
// and returns the context carrying it.
func (o *Observer) OperationStarted(ctx context.Context, event telemetry.OperationEvent) context.Context {
	if o == nil || o.tracer == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	outer, _ := ctx.Value(openedSpanKey{}).(*openedSpan)
	before := oteltrace.SpanFromContext(ctx)

	name, kind := spanName(event), oteltrace.SpanKindInternal
	if event.Scope == telemetry.ScopeRequest {
		kind = oteltrace.SpanKindClient
	}

	// Started on the caller's context, never on a fresh one. That is what makes
	// a host's already-open span the parent: Tracer.Start reads the span out of
	// the context and links to it, so a library span nests inside whatever the
	// host opened around the call. A fresh context would produce a
	// correct-looking span in a trace of its own.
	//
	// What is already known and what a sampler or a span processor would choose
	// on, the operation, the provider and the model, goes in with the start:
	// attributes set afterwards are invisible to a head sampler.
	ctx, span := o.tracer.Start(o.parentContext(ctx, outer, before), name,
		oteltrace.WithSpanKind(kind), oteltrace.WithAttributes(creationAttributes(event)...))

	// A sampler that dropped the span gets nothing computed for it. Names,
	// identifiers and settings only, in any case: an attribute is exported to
	// whatever backend the host configured, which is the least controlled place
	// this library could put somebody's data.
	if span.IsRecording() {
		setStartAttributes(span, event)
	}
	return context.WithValue(ctx, openedSpanKey{}, &openedSpan{
		owner: o, scope: event.Scope, span: span, before: before, outer: outer,
	})
}

// spanName names the span of a unit. Both forms are low in cardinality: an
// operation's name is one the library or a custom operation declares, and a
// model is a configured value. Neither is the caller's data, and each is
// bounded because neither is the library's to vouch for.
func spanName(event telemetry.OperationEvent) string {
	if event.Scope == telemetry.ScopeRequest {
		// The convention's name for a model call: the operation, which for a
		// text or JSON completion is "chat", and the model asked for.
		if event.Model == "" {
			return "chat"
		}
		return "chat " + bounded(event.Model, maxModelLength)
	}
	if event.Operation == "" {
		// The library starts no operation without a name; an event from
		// elsewhere that has none still gets a name that reads as one.
		return "schemaflux.operation"
	}
	return "schemaflux." + bounded(event.Operation, maxOperationLength)
}

// parentContext returns the context to start a span from: ctx, unless the span
// current on it was opened by a different Observer of this package.
//
// With two Observers installed, the second is started on the context the first
// returned, where the first one's span is current. That span lives on another
// provider and would be a parent the second one's backend never receives. So
// the second steps back to the span that was current before it, and so on
// outwards until it reaches a span of its own or one the library did not open.
// With one Observer installed the walk stops at once.
func (o *Observer) parentContext(ctx context.Context, outer *openedSpan, current oteltrace.Span) context.Context {
	stepped := false
	for opened := outer; opened != nil && opened.owner != o; opened = opened.outer {
		if opened.span.SpanContext().Equal(current.SpanContext()) {
			current, stepped = opened.before, true
		}
	}
	if !stepped {
		return ctx
	}
	return oteltrace.ContextWithSpan(ctx, current)
}

// creationAttributes are the attributes of a unit that are given to the tracer
// when the span is made, so that a sampler and a span processor can see them:
// for a request the convention's operation name, the provider and the model it
// was sent to, and the operation it serves; for an operation, its name. All of
// them are strings the span's name already holds, so a span that is dropped has
// cost one small slice.
func creationAttributes(event telemetry.OperationEvent) []attribute.KeyValue {
	if event.Scope == telemetry.ScopeOperation {
		return []attribute.KeyValue{keyOperation.String(bounded(event.Operation, maxOperationLength))}
	}
	attributes := make([]attribute.KeyValue, 0, 4)
	attributes = append(attributes, semconv.GenAIOperationNameChat)
	if event.Provider != "" {
		attributes = append(attributes, semconv.GenAIProviderNameKey.String(bounded(event.Provider, maxProviderLength)))
	}
	if event.Model != "" {
		attributes = append(attributes, semconv.GenAIRequestModel(bounded(event.Model, maxModelLength)))
	}
	if event.Operation != "" {
		attributes = append(attributes, keyOperation.String(bounded(event.Operation, maxOperationLength)))
	}
	return attributes
}

// setStartAttributes writes the rest of what is known of a unit when it starts,
// after creationAttributes. An attribute is set only when there is something to
// say: an empty attribute is a key a backend indexes and a reader has to
// interpret, and "no schema was named" and "the schema is called nothing" are
// not the same fact.
func setStartAttributes(span oteltrace.Span, event telemetry.OperationEvent) {
	if event.Scope == telemetry.ScopeOperation {
		return
	}

	attributes := make([]attribute.KeyValue, 0, 8)
	switch event.ResponseFormat {
	case "json":
		attributes = append(attributes, semconv.GenAIOutputTypeJSON)
	case "text":
		attributes = append(attributes, semconv.GenAIOutputTypeText)
	}
	if event.SchemaName != "" {
		attributes = append(attributes, keySchemaName.String(bounded(event.SchemaName, maxSchemaNameLength)))
	}
	if event.Mode != "" {
		attributes = append(attributes, keyMode.String(event.Mode))
	}
	if event.Intelligence != "" {
		attributes = append(attributes, keyIntelligence.String(event.Intelligence))
	}
	if event.RequestID != "" {
		attributes = append(attributes, keyRequestID.String(bounded(event.RequestID, maxIDLength)))
	}
	if event.CorrelationID != "" {
		attributes = append(attributes, keyCorrelationID.String(bounded(event.CorrelationID, maxIDLength)))
	}
	if event.Repair {
		attributes = append(attributes, keyRepair.Bool(true))
	}
	if event.Stream {
		attributes = append(attributes, keyStream.Bool(true))
	}
	if len(attributes) > 0 {
		span.SetAttributes(attributes...)
	}
}

// bounded returns a string fit for a span name or an attribute: at most limit
// bytes, each a printable ASCII character. A byte that is not is replaced by
// "?", so a value that held a control character or a newline cannot forge a
// line in whatever prints the span. A value that is already both, which is
// every ID the library generates and every model name in use, is returned as it
// is without allocating.
func bounded(value string, limit int) string {
	clean := len(value) <= limit
	for index := 0; clean && index < len(value); index++ {
		clean = value[index] >= 0x20 && value[index] <= 0x7e
	}
	if clean {
		return value
	}

	if len(value) > limit {
		value = value[:max(limit, 0)]
	}
	var cleaned strings.Builder
	cleaned.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x20 && value[index] <= 0x7e {
			cleaned.WriteByte(value[index])
		} else {
			cleaned.WriteByte('?')
		}
	}
	return cleaned.String()
}

// opened returns the innermost span on ctx that this Observer opened for a
// unit of the scope, or nil.
func (o *Observer) opened(ctx context.Context, scope telemetry.Scope) *openedSpan {
	if o == nil || ctx == nil {
		return nil
	}
	opened, _ := ctx.Value(openedSpanKey{}).(*openedSpan)
	for opened != nil && (opened.owner != o || opened.scope != scope) {
		opened = opened.outer
	}
	return opened
}

// OperationFinished records the outcome on the span this Observer opened for
// the unit and ends it. A context that carries no such span is left alone, and
// a span is ended once however often its finish arrives.
func (o *Observer) OperationFinished(ctx context.Context, result telemetry.OperationResult) {
	opened := o.opened(ctx, result.Scope)
	if opened == nil || !opened.ended.CompareAndSwap(false, true) {
		return
	}
	span := opened.span
	// Ended whether or not it is recording. The SDK's unsampled span ignores
	// End, but ending every span that was started is the API's contract, and a
	// provider that pools or wraps spans depends on it.
	defer span.End()

	if !span.IsRecording() {
		return
	}
	if result.Scope == telemetry.ScopeRequest {
		setFinishAttributes(span, result)
	} else {
		setOperationFinishAttributes(span, result)
	}

	if result.ErrorKind != "" {
		// The kind, not the message: a provider's error message can quote the
		// caller's input back, and a span is an export. For the same reason
		// RecordError is not called: it would put the error's text in an event.
		span.SetAttributes(semconv.ErrorTypeKey.String(result.ErrorKind))
		span.SetStatus(codes.Error, result.ErrorKind)
		return
	}

	// On success the status is left Unset. The trace API documents codes.Ok
	// as: "Ok indicates operation has been validated by an Application
	// developers or Operator to have completed successfully, or contain no
	// error." (go.opentelemetry.io/otel/codes), and Span.SetStatus as setting
	// the status "provided the status hasn't already been set to a higher
	// value before (OK > Error > Unset)". So Ok is a verdict for the
	// application to give, and one given here could not be overridden by a
	// host that knows the call's answer was wrong for its purposes. A request
	// that got an answer is not thereby a success of the operation it served;
	// the operation span says how that went.
}

// setOperationFinishAttributes writes what a call of an operation is known to
// have done once it has ended: how many requests it made, and for a run over
// many items how many it was given and what became of them. They are counts and
// nothing else; no item's data, identifier or error reaches a span.
func setOperationFinishAttributes(span oteltrace.Span, result telemetry.OperationResult) {
	attributes := make([]attribute.KeyValue, 0, 5)
	attributes = append(attributes, keyRequests.Int(result.Requests))
	if result.Items > 0 {
		attributes = append(attributes,
			keyItems.Int(result.Items),
			keyItemsSucceeded.Int(result.Succeeded),
			keyItemsFailed.Int(result.Failed),
			keyItemsCancelled.Int(result.Cancelled))
	}
	span.SetAttributes(attributes...)
}

// setFinishAttributes writes what a request is known to have done once it has
// ended.
func setFinishAttributes(span oteltrace.Span, result telemetry.OperationResult) {
	attributes := make([]attribute.KeyValue, 0, 18)
	attributes = append(attributes, keyAttempts.Int(result.Attempts))
	if result.ProviderDuration > 0 {
		// Beside the span's own duration rather than instead of it: the
		// difference is the library's retry backoff, and a reader looking at a
		// slow request has to be able to tell a slow provider from a request
		// that waited before asking again.
		attributes = append(attributes, keyProviderDuration.Int64(result.ProviderDuration.Milliseconds()))
	}
	if result.FinishReason != "" {
		// A provider enum, "stop" or "length", not generated text. It is the
		// only thing that separates a complete answer from a truncated one.
		attributes = append(attributes, semconv.GenAIResponseFinishReasons(bounded(result.FinishReason, maxFinishReasonLength)))
	}
	answered := result.Provider != "" || result.Model != ""
	if result.AskedFirst != "" {
		attributes = append(attributes, keyEscalationProvider.String(bounded(result.AskedFirst, maxProviderLength)))
	}
	if result.AskedFirstModel != "" {
		attributes = append(attributes, keyEscalationModel.String(bounded(result.AskedFirstModel, maxModelLength)))
	}
	if result.Escalated != "" {
		attributes = append(attributes, keyEscalationReason.String(bounded(result.Escalated, maxEscalationLength)))
	}

	if result.Cached {
		// A response cache answered. The usage says outright that nothing was
		// bought, which an absent figure would not: it would read as one nobody
		// measured. The convention's cache attributes are the provider's own
		// prompt cache, a different thing, so the library's own key says it.
		attributes = append(attributes,
			keyCacheHit.Bool(true),
			semconv.GenAIUsageInputTokens(0),
			semconv.GenAIUsageOutputTokens(0))
	} else if answered || result.AskedFirst != "" {
		// An answer's usage, or the usage of the call that was asked first and
		// paid for even when the one that answered then failed.
		attributes = appendUsage(attributes, result.Usage)
	}

	// What follows describes an answer. With no answer there is nothing to
	// describe, and a zero would claim a fact nobody established: a cost of
	// nothing, or a model that answered.
	if answered {
		if result.Provider != "" {
			attributes = append(attributes, keyResponseProvider.String(bounded(result.Provider, maxProviderLength)))
		}
		if result.Model != "" {
			attributes = append(attributes, semconv.GenAIResponseModel(bounded(result.Model, maxModelLength)))
		}

		// A cost is a figure only when the model was priced. An unpriced call
		// reporting a cost of zero is indistinguishable from a free one, so it
		// says it was not priced and gives no figure. A result whose cost was
		// never worked out, which is one with no pricing quality, says nothing.
		switch {
		case result.Cost.Priced:
			attributes = append(attributes,
				keyCostPriced.Bool(true),
				keyCostTotal.Float64(result.Cost.TotalCost))
			if result.Cost.Currency != "" {
				attributes = append(attributes, keyCostCurrency.String(result.Cost.Currency))
			}
		case result.Cost.Quality != "":
			attributes = append(attributes, keyCostPriced.Bool(false))
		}
	}
	span.SetAttributes(attributes...)
}

// appendUsage adds the token counts a request used. Each is written only when
// it is positive: a provider that reported none has no count to give, which is
// not a count of zero.
func appendUsage(attributes []attribute.KeyValue, usage types.TokenUsage) []attribute.KeyValue {
	input, output := usage.PromptTokens, usage.CompletionTokens
	if input == 0 && output == 0 {
		input, output = usage.InputTokens, usage.OutputTokens
	}
	if input > 0 || output > 0 {
		attributes = append(attributes,
			semconv.GenAIUsageInputTokens(input),
			semconv.GenAIUsageOutputTokens(output))
	}
	if usage.CachedTokens > 0 {
		attributes = append(attributes, semconv.GenAIUsageCacheReadInputTokens(usage.CachedTokens))
	}
	if usage.CacheWriteTokens > 0 {
		attributes = append(attributes, semconv.GenAIUsageCacheCreationInputTokens(usage.CacheWriteTokens))
	}
	if usage.ReasoningTokens > 0 {
		attributes = append(attributes, semconv.GenAIUsageReasoningOutputTokens(usage.ReasoningTokens))
	}
	return attributes
}

// OperationNoted adds a span event to the request span this Observer opened:
// "retry" or "hedge", with the attempt, the kind of the failure that caused a
// retry and the wait before the next attempt. It never carries an error's
// text.
func (o *Observer) OperationNoted(ctx context.Context, note telemetry.Note) {
	opened := o.opened(ctx, telemetry.ScopeRequest)
	if opened == nil || opened.ended.Load() || !opened.span.IsRecording() {
		return
	}

	attributes := make([]attribute.KeyValue, 0, 3)
	if note.Attempt > 0 {
		attributes = append(attributes, keyAttempt.Int(note.Attempt))
	}
	if note.ErrorKind != "" {
		attributes = append(attributes, semconv.ErrorTypeKey.String(note.ErrorKind))
	}
	if note.Name == telemetry.NoteRetry {
		attributes = append(attributes, keyBackoff.Int64(note.Wait.Milliseconds()))
	}
	if note.Name == telemetry.NoteHedge {
		// The event says when the second copy went out; the attribute says, to a
		// query over requests, that the request had two. Its attempts stay the
		// calls the library made: the copy is the middleware's.
		opened.span.SetAttributes(keyHedged.Bool(true))
	}
	opened.span.AddEvent(note.Name, oteltrace.WithAttributes(attributes...))
}

// InstallIDSources lets the library read the trace and span IDs of the span on
// a context without importing OpenTelemetry itself, and returns the function
// that removes this install. Install calls it; a host that adds the Observer
// itself calls this beside it.
//
// It installs two readers, both inside this library. One fills
// ResultMetadata.TraceID and SpanID with the IDs of the request span the call
// ran in. The other is what the request ID strategy "trace" reads: with that
// strategy a request ID is the trace ID of the span on the caller's context.
// With no span on a context both answer "", never a zero ID.
//
// The readers in effect are those of the newest install that has not been
// removed. Removals may run in any order, and calling one again does nothing.
func InstallIDSources() (restore func()) {
	removeTracking := requesttracking.SetTraceIDSource(traceIDFromContext)
	removeTelemetry := telemetry.SetSpanIDSources(traceIDFromContext, spanIDFromContext)

	return func() {
		removeTelemetry()
		removeTracking()
	}
}

// traceIDFromContext returns the trace ID of the span on ctx, or "" when there
// is none. SpanFromContext never returns nil: with no span it returns one whose
// SpanContext is the zero value, which would otherwise read as the ID
// "00000000000000000000000000000000" and correlate nothing.
func traceIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// spanIDFromContext returns the span ID of the span on ctx, or "" when there
// is none.
func spanIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
