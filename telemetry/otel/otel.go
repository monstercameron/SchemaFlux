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

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/telemetry"
)

// instrumentationName identifies this library as the source of the spans it
// produces, which is what lets a host filter them.
const instrumentationName = "github.com/monstercameron/schemaflux/v2"

// The library's own attribute keys. Everything the conventions have a name for
// uses that name instead (see the package comment).
const (
	keyOperation        = attribute.Key("schemaflux.operation")
	keySchemaName       = attribute.Key("schemaflux.schema.name")
	keyMode             = attribute.Key("schemaflux.mode")
	keyTier             = attribute.Key("schemaflux.tier")
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
// library's own: a request or correlation id is whatever the caller chose, a
// model is the one the caller pinned and the one the provider says answered, a
// provider is what a registered provider calls itself and what it answered
// with, a finish reason is the provider's word, and a schema or an operation
// is named by the caller's own type or custom operation. The events this package
// is given have already been cut to the same limits, so these hold for an
// Observer that is called with an event made by hand as well. Each is cut to its
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

// An Observer opens spans on a tracer taken from the host's TracerProvider: one
// for each call of an operation and one for each request to a model. It is a
// telemetry.Observer and a telemetry.NoteObserver. The package comment describes
// the spans and their attributes.
//
// An Observer ends the spans its own Started opened and no other. The span on
// the context at that point may be the host's own, and ending it early, or
// giving it this library's outcome, would corrupt the host's trace. So each span
// it opens is recorded in the context it returns, and a finish ends only a span
// recorded there by the same Observer. A nil Observer and the zero Observer open
// nothing and finish nothing.
type Observer struct {
	tracer oteltrace.Tracer
}

var (
	_ telemetry.Observer     = (*Observer)(nil)
	_ telemetry.NoteObserver = (*Observer)(nil)
)

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
// It registers nothing: pass the result to telemetry.RegisterObserver, or call
// Install, which does that and installs the id readers.
//
// A nil provider is refused rather than replaced by the global one: silently
// falling back to otel.GetTracerProvider() is how a library ends up writing
// into a stack nobody asked it to write into.
func New(provider oteltrace.TracerProvider) (*Observer, error) {
	if provider == nil {
		return nil, fmt.Errorf("otel: no TracerProvider; pass the one your application configured")
	}
	return &Observer{tracer: provider.Tracer(instrumentationName,
		oteltrace.WithInstrumentationVersion(schemaflux.Version),
		oteltrace.WithSchemaURL(semconv.SchemaURL),
	)}, nil
}

// Install sets tracing up whole in one call: it registers an Observer on
// provider with the library's observers, so operations produce spans, and it
// installs the trace and span id readers (InstallIDSources), so telemetry.TraceID
// and requestid.StrategyTrace can read them. It returns the function that undoes
// exactly that.
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
	removeObserver := telemetry.RegisterObserver(observer)
	removeIDSources := InstallIDSources()

	return func() {
		removeIDSources()
		removeObserver()
	}, nil
}

// Started opens a span for the unit, as a child of the span on ctx, and returns
// the context carrying it.
func (o *Observer) Started(ctx context.Context, event telemetry.Event) context.Context {
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
func spanName(event telemetry.Event) string {
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
// With two Observers registered, the second is started on the context the first
// returned, where the first one's span is current. That span lives on another
// provider and would be a parent the second one's backend never receives. So
// the second steps back to the span that was current before it, and so on
// outwards until it reaches a span of its own or one the library did not open.
// With one Observer registered the walk stops at once.
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
func creationAttributes(event telemetry.Event) []attribute.KeyValue {
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
func setStartAttributes(span oteltrace.Span, event telemetry.Event) {
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
	if mode := modeName(event.Mode); mode != "" {
		attributes = append(attributes, keyMode.String(mode))
	}
	if tier := tierName(event.Tier); tier != "" {
		attributes = append(attributes, keyTier.String(tier))
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

// modeName is the name a mode has on a span, and the empty string for no mode,
// or one the library does not have. A test holds the names equal to the ones the
// library's own events use.
func modeName(m schemaflux.Mode) string {
	switch m {
	case schemaflux.ModeStrict:
		return "strict"
	case schemaflux.ModeTransform:
		return "transform"
	case schemaflux.ModeCreative:
		return "creative"
	}
	return ""
}

// tierName is the name a tier has on a span, and the empty string for no tier,
// or one the library does not have.
func tierName(t schemaflux.Tier) string {
	switch t {
	case schemaflux.TierSmart:
		return "smart"
	case schemaflux.TierFast:
		return "fast"
	case schemaflux.TierQuick:
		return "quick"
	}
	return ""
}

// bounded returns a string fit for a span name or an attribute: at most limit
// bytes, each a printable ASCII character. A byte that is not is replaced by
// "?", so a value that held a control character or a newline cannot forge a
// line in whatever prints the span. A value that is already both, which is
// every id the library generates and every model name in use, is returned as it
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

// Finished records the outcome on the span this Observer opened for the unit
// and ends it. A context that carries no such span is left alone, and a span is
// ended once however often its finish arrives.
func (o *Observer) Finished(ctx context.Context, result telemetry.Result) {
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

	if result.Kind != 0 {
		// The kind, not the message: a provider's error message can quote the
		// caller's input back, and a span is an export. For the same reason
		// RecordError is not called: it would put the error's text in an event.
		kind := result.Kind.Error()
		span.SetAttributes(semconv.ErrorTypeKey.String(kind))
		span.SetStatus(codes.Error, kind)
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
func setOperationFinishAttributes(span oteltrace.Span, result telemetry.Result) {
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
func setFinishAttributes(span oteltrace.Span, result telemetry.Result) {
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
		// never worked out, which is the zero Cost, says nothing.
		switch {
		case result.Cost.Priced:
			attributes = append(attributes,
				keyCostPriced.Bool(true),
				keyCostTotal.Float64(result.Cost.Total))
			if result.Cost.Currency != "" {
				attributes = append(attributes, keyCostCurrency.String(result.Cost.Currency))
			}
		case result.Cost != (schemaflux.Cost{}):
			attributes = append(attributes, keyCostPriced.Bool(false))
		}
	}
	span.SetAttributes(attributes...)
}

// appendUsage adds the token counts a request used. Each is written only when
// it is positive: a provider that reported none has no count to give, which is
// not a count of zero.
func appendUsage(attributes []attribute.KeyValue, usage schemaflux.Usage) []attribute.KeyValue {
	if usage.Input > 0 || usage.Output > 0 {
		attributes = append(attributes,
			semconv.GenAIUsageInputTokens(usage.Input),
			semconv.GenAIUsageOutputTokens(usage.Output))
	}
	if usage.Cached > 0 {
		attributes = append(attributes, semconv.GenAIUsageCacheReadInputTokens(usage.Cached))
	}
	if usage.CacheWrite > 0 {
		attributes = append(attributes, semconv.GenAIUsageCacheCreationInputTokens(usage.CacheWrite))
	}
	if usage.Reasoning > 0 {
		attributes = append(attributes, semconv.GenAIUsageReasoningOutputTokens(usage.Reasoning))
	}
	return attributes
}

// Noted adds a span event to the request span this Observer opened: "retry" or
// "hedge", with the attempt, the kind of the failure that caused a retry and the
// wait before the next attempt. It never carries an error's text.
func (o *Observer) Noted(ctx context.Context, note telemetry.Note) {
	opened := o.opened(ctx, telemetry.ScopeRequest)
	if opened == nil || opened.ended.Load() || !opened.span.IsRecording() {
		return
	}

	attributes := make([]attribute.KeyValue, 0, 3)
	if note.Attempt > 0 {
		attributes = append(attributes, keyAttempt.Int(note.Attempt))
	}
	if note.Kind != 0 {
		attributes = append(attributes, semconv.ErrorTypeKey.String(note.Kind.Error()))
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

// InstallIDSources lets the library read the trace and span ids of the span on
// a context without importing OpenTelemetry itself, and returns the function
// that removes this install. Install calls it; a host that registers the
// Observer itself calls this beside it.
//
// It installs two readers, both inside this library. One is what
// telemetry.TraceID and telemetry.SpanID return: the ids of the span a call ran
// in. The other is what the request id strategy requestid.StrategyTrace reads:
// with that strategy a request's id is the trace id of the span on the caller's
// context. With no span on a context both answer "", never a zero id.
//
// The readers in effect are those of the newest install that has not been
// removed. Removals may run in any order and more than once, and one that runs
// out of order never brings back a reader whose own removal has already run.
func InstallIDSources() (restore func()) {
	removeTracking := requesttracking.SetTraceIDSource(traceIDFromContext)
	removeTelemetry := telemetry.SetSpanIDSources(traceIDFromContext, spanIDFromContext)

	return func() {
		removeTelemetry()
		removeTracking()
	}
}

// traceIDFromContext returns the trace id of the span on ctx, or "" when there
// is none. SpanFromContext never returns nil: with no span it returns one whose
// SpanContext is the zero value, which would otherwise read as the id
// "00000000000000000000000000000000" and correlate nothing.
func traceIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

// spanIDFromContext returns the span id of the span on ctx, or "" when there is
// none.
func spanIDFromContext(ctx context.Context) string {
	spanContext := oteltrace.SpanFromContext(ctx).SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}
