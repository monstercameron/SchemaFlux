// Package requestid lets a program that has its own request id, from an
// incoming HTTP request, a job or a trace, carry it through the library's
// calls, so that the library's records, errors and logs for one piece of work
// can be joined with the program's own.
//
// # Putting an id on a context and reading it back
//
// An id travels on the context.Context that is passed to a call, never as an
// option of the call:
//
//	ctx = requestid.With(ctx, "order-7-4f2a")
//	ctx = requestid.WithCorrelation(ctx, "nightly-sync")
//	answer, err := schemaflux.Summarize(ctx, text)
//	ids := requestid.From(ctx) // {Request: "order-7-4f2a", Correlation: "nightly-sync"}
//
// The request id names one request, job or trace. The correlation id groups
// several of them, for example every request made for one user action. The
// Record that describes the call carries the ids the call ran under, in its
// RequestID and CorrelationID fields, and the Error the call returns carries the
// request id in its RequestID field.
//
// Ids nest: the id nearest the call, the one set last on the context the call
// receives, is the one it uses. Setting an id on a context changes only that
// context and the contexts derived from it.
//
// # Bounds on an id
//
// An id may arrive from another process, and it is copied into records, error
// values and the headers of outgoing requests, so With, WithCorrelation and
// Extract store an id only if, after leading and trailing white space is
// trimmed, it is 1 to 128 bytes of printable ASCII (the bytes 0x21 to 0x7e,
// which leaves out the space). An id that fails the rule is not stored and is
// not shortened or repaired, because an id changed on the way in would join
// nothing: With returns the context it was given, and Extract acts as if the
// header were absent. Inject applies the same rule to what it writes. The rule
// is the one internal/requesttracking applies to an id read from a carrier,
// which is the code the library's calls run on; a test of this package holds
// the two together.
//
// An id is a label and not a secret. It is written to headers and shown in
// records and errors, so it must not be derived from anything that should stay
// private, and nothing in this package logs it.
//
// # HTTP
//
// Extract takes the ids from the headers of an incoming request and returns a
// context that carries them. Inject writes the ids a context carries onto the
// headers of an outgoing request. They use the header names X-Request-ID and
// X-Correlation-ID unless Configure sets others, and they match names without
// regard to case:
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		ctx := requestid.Extract(r.Context(), r.Header)
//		...
//		out, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
//		requestid.Inject(ctx, out.Header)
//	}
//
// If a header holds several values, Extract takes the first, as
// http.Header.Get does. If the first is refused, the header gives no id; a later
// value is not tried in its place. Inject makes no id: a context that carries
// none writes none.
//
// # When the caller supplies no id
//
// A call whose context carries no request id gets one made for it. Config
// chooses how:
//
//   - StrategyAuto, the default, makes "req_" and sixteen hexadecimal digits.
//   - StrategyUUID makes a random version 4 UUID.
//   - StrategyTimestamp makes "req_", the time in nanoseconds, the process id
//     and a counter, joined by underscores.
//   - StrategyTrace uses the trace id of the span on the context, when the
//     program has set up tracing through the telemetry/otel subpackage, and
//     otherwise makes the id StrategyAuto would.
//   - StrategyNone makes no request id.
//
// A call whose context carries no correlation id gets one by the Correlation
// setting: CorrelationInherit, the default, uses the request id,
// CorrelationGenerate makes "corr_" and sixteen hexadecimal digits, and
// CorrelationNone makes none. With no request id to use, the first makes the id
// CorrelationGenerate would. An id the caller put on the context is never
// replaced by one of these, and Config.Disabled turns the making of ids off
// altogether while leaving the caller's ids in place.
//
// # Configure changes the process
//
// Configure sets the header names and the strategies for every call, every
// client and every goroutine of the process, not for a context, a client or the
// calling goroutine (the configuration is process-wide). It is meant to be
// called once, while the program starts, and by tests. It returns a function
// that undoes that one change:
//
//	restore, err := requestid.Configure(requestid.Config{Strategy: requestid.StrategyUUID})
//	if err != nil {
//		return err
//	}
//	defer restore()
//
// The restore undoes that call: it puts back what was in force when the call
// was made, unless something has been set since, in which case that later
// setting stays in force until it is restored too, and then what is in force is
// the newest setting not yet restored, or the original if there is none. It does
// nothing when called a second time, may be called in any order relative to the
// restore functions of other calls, and may be called from several goroutines.
//
// Configure returns an *Error of kind ErrConfiguration, with the operation
// "requestid.configure", and a nil restore, if the Config cannot work: a header
// name is not a valid HTTP header name, both ids are given the same header name,
// or a Strategy or Correlation is not one of the constants. The configuration is
// then left as it was, and the restore functions of earlier calls still work.
// This package does not import the root package, so the error is made by the
// call path's own converter: it is the root's *Error in any program that has
// linked the root, as every program that makes a call has.
//
// # The environment configures nothing
//
// Version 1 of the library read the variables SCHEMAFLUX_REQUEST_TRACKING,
// SCHEMAFLUX_REQUEST_ID_STRATEGY, SCHEMAFLUX_CORRELATION_ID_STRATEGY,
// SCHEMAFLUX_REQUEST_ID_HEADER and SCHEMAFLUX_CORRELATION_ID_HEADER. They are
// removed. This package does not read the environment, and linking it fixes the
// configuration to the default, as set out above, until Configure changes it.
// A program that wants what a variable used to give calls Configure with it.
package requestid
