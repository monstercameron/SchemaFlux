package schemaflux

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// An Error is the error every operation returns when it fails. Its Kind says
// what went wrong in terms of what a caller can do about it, and the other
// fields say where.
//
// Test for a kind with errors.Is, and reach the fields with errors.As:
//
//	if errors.Is(err, schemaflux.ErrBudgetExceeded) { ... }
//
//	var e *schemaflux.Error
//	if errors.As(err, &e) && e.Retryable() {
//		time.Sleep(e.RetryAfter)
//	}
//
// Its message is lower case, names the operation, and carries none of the
// caller's data and none of the model's or the provider's text: an Error is
// safe to log. What the provider wrote is kept for Detail.
//
// A Provider or a middleware reports a failure the library can act on by
// returning an *Error with Kind set; the library fills in the fields it was
// left without. The zero Error has no kind and is not a failure the library
// returns.
type Error struct {
	// Kind is what went wrong. It is never zero in an error the library
	// returns. The zero value is a kind the library does not know, and a
	// Provider that returns an Error with it is taken to have failed without
	// saying how.
	Kind Kind

	// Op is the operation that failed, in lower case: "extract", "classify".
	// It is the Op of the call's Record. Empty means the library fills it in
	// with the operation the error passes through.
	Op string

	// Provider and Model are the route the failure came from. They are empty
	// when nothing was sent.
	Provider string
	Model    string

	// RequestID is the id of the request that failed, as in the call's Record
	// and the library's logs. Empty when no request was made.
	RequestID string

	// Status is the HTTP status the provider answered with, or zero when the
	// failure was not an HTTP answer.
	Status int

	// Code is the provider's own code for the failure
	// ("context_length_exceeded"), when it gave one. It is one of the
	// provider's fixed words, never free text. Empty when there was none.
	Code string

	// RetryAfter is how long the provider asked the caller to wait, or zero
	// when it did not say.
	RetryAfter time.Duration

	// Message is this library's own account of the failure, printed by Error
	// after the kind. It must never be given the caller's data or a model's or
	// a provider's text. A Provider or a middleware written outside this
	// module sets Kind and Message. Empty means the kind says it all.
	Message string

	// Err is the cause: the provider's error, a context error, or another
	// error from this package. Unwrap returns it. Nil means there is none.
	Err error
}

// Error returns "<op>: <kind>", followed by the library's own account of the
// failure when it has one, and by the route, the status, the provider's code
// and the request id when they are known. It does not print Err.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Kind.Error())
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}

	var where []string
	switch {
	case e.Provider != "" && e.Model != "":
		where = append(where, e.Provider+"/"+e.Model)
	case e.Provider != "":
		where = append(where, e.Provider)
	case e.Model != "":
		where = append(where, e.Model)
	}
	if e.Status != 0 {
		where = append(where, "status "+strconv.Itoa(e.Status))
	}
	if e.Code != "" {
		where = append(where, e.Code)
	}
	if e.RequestID != "" {
		where = append(where, "request "+e.RequestID)
	}
	if len(where) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(where, ", "))
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap returns the cause, so that errors.Is(err, context.Canceled) and
// errors.Is(err, context.DeadlineExceeded) hold for a call that ended with its
// context.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target is this error's Kind. It is what makes
// errors.Is(err, schemaflux.ErrRateLimited) work through any wrapping. An error
// of kind ErrUnsupported also matches errors.ErrUnsupported. errors.Is also
// matches a kind further down the chain; e.Kind is the one answer to what kind
// this is.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	if kind, ok := target.(Kind); ok {
		return kind == e.Kind
	}
	return e.Kind == ErrUnsupported && target == errors.ErrUnsupported
}

// Retryable reports whether making the same call again later could succeed:
// the kind is ErrRateLimited, ErrUnavailable, ErrTimeout or ErrBusy. The
// library has already retried what it could within the client's Retries before
// returning such an error. It says nothing about an answer that failed its
// checks: that was repaired, within the client's Repairs, before it became
// ErrInvalidAnswer. A timeout of the caller's own context is not worth
// repeating under the same context.
func (e *Error) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ErrRateLimited, ErrUnavailable, ErrTimeout, ErrBusy:
		return true
	}
	return false
}

// Detail returns the error with everything the provider said about it: its
// message and the response body. Those can quote the request back, so Detail
// is for a developer reading a failure on data they own, and not for a log. An
// error with nothing of the provider's in it returns what Error returns.
func (e *Error) Detail() string {
	if e == nil {
		return "<nil>"
	}
	text := e.Error()
	var detail string
	walkChain(e.Err, func(err error) bool {
		if _, own := err.(*Error); own {
			return true
		}
		if provider, ok := err.(interface{ Detail() string }); ok {
			detail = provider.Detail()
			return false
		}
		return true
	})
	if detail == "" {
		return text
	}
	return text + "\n" + detail
}

// LogValue is what log/slog logs for an Error: the kind's name, the operation,
// the route, the request id, the status, the code, the wait and the message,
// under the names MarshalJSON writes them under, and the wait as a string such
// as "1.5s" in both. It never includes Err, so logging an Error cannot write a
// provider's text or a caller's data to a log.
func (e *Error) LogValue() slog.Value {
	if e == nil {
		return slog.Value{}
	}
	attrs := make([]slog.Attr, 0, 9)
	attrs = append(attrs, slog.String("kind", e.Kind.Error()))
	if e.Op != "" {
		attrs = append(attrs, slog.String("op", e.Op))
	}
	if e.Provider != "" {
		attrs = append(attrs, slog.String("provider", e.Provider))
	}
	if e.Model != "" {
		attrs = append(attrs, slog.String("model", e.Model))
	}
	if e.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", e.RequestID))
	}
	if e.Status != 0 {
		attrs = append(attrs, slog.Int("status", e.Status))
	}
	if e.Code != "" {
		attrs = append(attrs, slog.String("code", e.Code))
	}
	if e.RetryAfter > 0 {
		attrs = append(attrs, slog.String("retry_after", e.RetryAfter.String()))
	}
	if e.Message != "" {
		attrs = append(attrs, slog.String("message", e.Message))
	}
	return slog.GroupValue(attrs...)
}

// MarshalJSON encodes the fields LogValue logs, with the kind as its name. It
// never encodes Err, so marshalling an Error cannot write a provider's text or
// a caller's data. The wait is written as a duration string such as "1.5s".
func (e *Error) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	view := struct {
		Kind       Kind   `json:"kind"`
		Op         string `json:"op,omitempty"`
		Provider   string `json:"provider,omitempty"`
		Model      string `json:"model,omitempty"`
		RequestID  string `json:"request_id,omitempty"`
		Status     int    `json:"status,omitempty"`
		Code       string `json:"code,omitempty"`
		RetryAfter string `json:"retry_after,omitempty"`
		Message    string `json:"message,omitempty"`
	}{
		Kind:      e.Kind,
		Op:        e.Op,
		Provider:  e.Provider,
		Model:     e.Model,
		RequestID: e.RequestID,
		Status:    e.Status,
		Code:      e.Code,
		Message:   e.Message,
	}
	if e.RetryAfter > 0 {
		view.RetryAfter = e.RetryAfter.String()
	}
	return json.Marshal(view)
}

// A Kind is what went wrong with a call, by what a caller can do about it. The
// kinds are themselves errors, so that one name serves both as the value of
// Error.Kind and as the target of errors.Is.
//
// The zero Kind and a value out of range print "unknown". An operation never
// returns a bare Kind: compare e.Kind, or use errors.Is; err ==
// schemaflux.ErrRateLimited is never true.
type Kind uint8

// Error returns the kind's name in lower case: "rate limited".
func (k Kind) Error() string {
	if k.valid() {
		return kindNames[k]
	}
	return "unknown"
}

// MarshalText returns the kind's name, so that a Kind is written as its name by
// encoding/json and by any other encoder that honours encoding.TextMarshaler.
func (k Kind) MarshalText() ([]byte, error) {
	return []byte(k.Error()), nil
}

const (
	// ErrConfiguration: the program is set up wrong. No provider or model can
	// be resolved, an option holds a value it cannot take, or a setting
	// contradicts another. Nothing was sent. It is the caller's code to fix.
	ErrConfiguration Kind = iota + 1

	// ErrInvalidRequest: this call's input cannot be used, or the provider
	// rejected the request as malformed (HTTP 400, 404, 422). The same call
	// will fail the same way.
	ErrInvalidRequest

	// ErrUnsupported: the provider or model cannot do what the call asked:
	// stream, take this effort, search the web. It also matches
	// errors.ErrUnsupported.
	ErrUnsupported

	// ErrUnauthorized: the provider refused the credential, or the credential
	// may not use this model (HTTP 401, 403).
	ErrUnauthorized

	// ErrPolicy: the client's DataPolicy does not allow the route the call
	// would take, or took, or the answer could not be delivered with the
	// guarantee the policy requires.
	ErrPolicy

	// ErrBudgetExceeded: a spend ceiling was reached: the client's Budget, the
	// process ledger of the pricing package, or a budget middleware. Nothing
	// was sent.
	ErrBudgetExceeded

	// ErrBusy: the client's own limits refused the call: its Scheduler's queue
	// was full or could not start the call before its deadline, or a bulkhead
	// middleware was full. Nothing was sent. A wait in the queue that outlasts
	// the deadline is this kind, and errors.Is(err, context.DeadlineExceeded)
	// also holds.
	ErrBusy

	// ErrRateLimited: the provider, or a rate-limit middleware, said to slow
	// down (HTTP 429). RetryAfter holds the wait when one was stated.
	ErrRateLimited

	// ErrUnavailable: the provider failed or could not be reached (HTTP 5xx, a
	// network failure, a stream that broke), or a circuit breaker is open for
	// it. A failure from a Provider that says nothing about its kind is this
	// one.
	ErrUnavailable

	// ErrTimeout: the call's deadline passed, or the provider gave up waiting
	// (HTTP 408, 504). The request may have been served.
	ErrTimeout

	// ErrCanceled: the call's context was canceled.
	ErrCanceled

	// ErrClosed: the client's Scheduler was closed while the call waited or
	// ran.
	ErrClosed

	// ErrTooLarge: the input is more than the model's context can hold (HTTP
	// 413, or the library's own size guard).
	ErrTooLarge

	// ErrTruncated: the answer was cut off at the output ceiling before it was
	// complete.
	ErrTruncated

	// ErrRefused: the model declined to answer, or the provider's content
	// filter withheld the answer.
	ErrRefused

	// ErrInvalidAnswer: the model answered and the answer failed the library's
	// checks: it did not parse, did not fit the type, broke a rule of the
	// operation (a subset that is not a subset), named evidence that is not in
	// the source, or did not cover the batch it was given; and the repairs the
	// client allows did not fix it.
	ErrInvalidAnswer

	// ErrReviewRequired: the call stopped to ask for a person: an approval gate
	// held the answer, or samples did not agree. It is an outcome, not a
	// breakdown; flow.ReviewError carries what is to be reviewed.
	ErrReviewRequired

	// ErrInternal: a defect: the library, a provider or a middleware panicked
	// or broke one of its own rules. It is reported, not handled.
	ErrInternal
)

// kindNames are the names the kinds print, indexed by kind. A renumbering or a
// rename changes what stored logs say, so a test pins every entry.
var kindNames = [ErrInternal + 1]string{
	ErrConfiguration:  "configuration",
	ErrInvalidRequest: "invalid request",
	ErrUnsupported:    "unsupported",
	ErrUnauthorized:   "unauthorized",
	ErrPolicy:         "policy",
	ErrBudgetExceeded: "budget exceeded",
	ErrBusy:           "busy",
	ErrRateLimited:    "rate limited",
	ErrUnavailable:    "unavailable",
	ErrTimeout:        "timeout",
	ErrCanceled:       "canceled",
	ErrClosed:         "closed",
	ErrTooLarge:       "too large",
	ErrTruncated:      "truncated",
	ErrRefused:        "refused",
	ErrInvalidAnswer:  "invalid answer",
	ErrReviewRequired: "review required",
	ErrInternal:       "internal",
}

// valid reports whether k is one of the eighteen kinds.
func (k Kind) valid() bool { return k >= ErrConfiguration && k <= ErrInternal }
