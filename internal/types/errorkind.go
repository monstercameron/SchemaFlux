package types

import (
	"errors"
	"fmt"
	"time"
)

// The failure taxonomy.
//
// A caller automating recovery needs to know what *kind* of thing went wrong,
// and until this existed the only way to find out was to match substrings
// against an English sentence. That is not a protocol: the sentence changes
// when someone improves the wording, and it changes per provider.
//
// The kinds are chosen by what a caller would *do* about them, which is why
// there are more than a handful. "Malformed output" and "schema violation" are
// both bad answers, but one is repaired by asking again with the parse error
// quoted and the other by regenerating from the source -- editing an answer that
// invented a field tends to produce an answer that invents it again.
//
// This is the register the specification's §10.1 describes. It is defined here,
// in the types package, because it has to be reachable from every layer that
// classifies a failure without any of them importing each other.
type ErrorKind uint16

const (
	// KindUnknown is the zero value: a failure nobody classified. It is not a
	// category, it is a gap, and treating it as retryable or as terminal would
	// both be guesses.
	KindUnknown ErrorKind = iota

	// Before the call.
	KindConfiguration // missing provider, no model for the provider, contradictory settings
	KindAuthentication
	KindPermission
	KindInvalidRequest // the provider rejected the shape of the request
	KindPolicyViolation
	KindBudgetExceeded
	KindAdmissionRejected // the scheduler refused to queue it

	// The call itself.
	KindRateLimited
	KindProviderUnavailable
	KindTimeout
	KindCanceled
	KindCircuitOpen
	KindShutdown

	// The answer.
	KindContextTooLarge
	KindOutputTruncated
	KindMalformedOutput        // not parseable
	KindSchemaViolation        // parseable, wrong shape
	KindBatchProtocolViolation // missing, duplicate, or unknown item IDs
	KindInvariantViolation     // parseable, right shape, wrong relationship to the input
	KindEvidenceViolation      // a claim with nothing supporting it

	// After recovery.
	KindUnsupportedCapability
	KindRepairExhausted
	KindReviewRequired
)

// String names the kind for logs and messages.
func (k ErrorKind) String() string {
	switch k {
	case KindConfiguration:
		return "configuration"
	case KindAuthentication:
		return "authentication"
	case KindPermission:
		return "permission"
	case KindInvalidRequest:
		return "invalid request"
	case KindPolicyViolation:
		return "policy violation"
	case KindBudgetExceeded:
		return "budget exceeded"
	case KindAdmissionRejected:
		return "admission rejected"
	case KindRateLimited:
		return "rate limited"
	case KindProviderUnavailable:
		return "provider unavailable"
	case KindTimeout:
		return "timeout"
	case KindCanceled:
		return "canceled"
	case KindCircuitOpen:
		return "circuit open"
	case KindShutdown:
		return "shutdown"
	case KindContextTooLarge:
		return "context too large"
	case KindOutputTruncated:
		return "output truncated"
	case KindMalformedOutput:
		return "malformed output"
	case KindSchemaViolation:
		return "schema violation"
	case KindBatchProtocolViolation:
		return "batch protocol violation"
	case KindInvariantViolation:
		return "invariant violation"
	case KindEvidenceViolation:
		return "evidence violation"
	case KindUnsupportedCapability:
		return "unsupported capability"
	case KindRepairExhausted:
		return "repair exhausted"
	case KindReviewRequired:
		return "review required"
	default:
		return "unknown"
	}
}

// Sentinels, one per kind, so `errors.Is` works without constructing a value.
//
// They carry no context on purpose: a sentinel is for asking "what kind of
// failure is this", and everything else belongs on the *OperationError that
// wraps it.
var (
	ErrConfiguration          = errors.New("configuration")
	ErrAuthentication         = errors.New("authentication")
	ErrPermission             = errors.New("permission")
	ErrInvalidRequest         = errors.New("invalid request")
	ErrPolicyViolation        = errors.New("policy violation")
	ErrBudgetExceeded         = errors.New("budget exceeded")
	ErrAdmissionRejected      = errors.New("admission rejected")
	ErrRateLimited            = errors.New("rate limited")
	ErrProviderUnavailable    = errors.New("provider unavailable")
	ErrTimeout                = errors.New("timeout")
	ErrCanceled               = errors.New("canceled")
	ErrCircuitOpen            = errors.New("circuit open")
	ErrShutdown               = errors.New("shutdown")
	ErrContextTooLarge        = errors.New("context too large")
	ErrOutputTruncated        = errors.New("output truncated")
	ErrMalformedOutput        = errors.New("malformed output")
	ErrSchemaViolation        = errors.New("schema violation")
	ErrBatchProtocolViolation = errors.New("batch protocol violation")
	ErrInvariantViolation     = errors.New("invariant violation")
	ErrEvidenceViolation      = errors.New("evidence violation")
	ErrUnsupportedCapability  = errors.New("unsupported capability")
	ErrRepairExhausted        = errors.New("repair exhausted")
	ErrReviewRequired         = errors.New("review required")
)

// sentinelFor maps a kind to its sentinel, so Is works from either direction.
func sentinelFor(kind ErrorKind) error {
	switch kind {
	case KindConfiguration:
		return ErrConfiguration
	case KindAuthentication:
		return ErrAuthentication
	case KindPermission:
		return ErrPermission
	case KindInvalidRequest:
		return ErrInvalidRequest
	case KindPolicyViolation:
		return ErrPolicyViolation
	case KindBudgetExceeded:
		return ErrBudgetExceeded
	case KindAdmissionRejected:
		return ErrAdmissionRejected
	case KindRateLimited:
		return ErrRateLimited
	case KindProviderUnavailable:
		return ErrProviderUnavailable
	case KindTimeout:
		return ErrTimeout
	case KindCanceled:
		return ErrCanceled
	case KindCircuitOpen:
		return ErrCircuitOpen
	case KindShutdown:
		return ErrShutdown
	case KindContextTooLarge:
		return ErrContextTooLarge
	case KindOutputTruncated:
		return ErrOutputTruncated
	case KindMalformedOutput:
		return ErrMalformedOutput
	case KindSchemaViolation:
		return ErrSchemaViolation
	case KindBatchProtocolViolation:
		return ErrBatchProtocolViolation
	case KindInvariantViolation:
		return ErrInvariantViolation
	case KindEvidenceViolation:
		return ErrEvidenceViolation
	case KindUnsupportedCapability:
		return ErrUnsupportedCapability
	case KindRepairExhausted:
		return ErrRepairExhausted
	case KindReviewRequired:
		return ErrReviewRequired
	default:
		return nil
	}
}

// OperationError is the failure a caller receives, carrying the structured
// context an automated recovery needs and nothing it does not.
//
// What it deliberately does not carry is the payload. Every caller logs this
// error, and the payload is their data -- the record being extracted, the note
// being redacted. Shapes and counts describe a failure adequately; values are
// somebody's invoice.
type OperationError struct {
	Kind ErrorKind

	// Op names the operation, so a log line says which of thirty calls failed.
	Op string

	Provider string
	Model    string

	RequestID     string
	CorrelationID string

	// AttemptID identifies the provider call within the logical request, so a
	// retry and its original are distinguishable.
	AttemptID string

	// ItemIDs names the items a batch failure applies to, which is what a
	// caller needs to know is missing.
	ItemIDs []string

	// RetryAfter is the wait the provider asked for, zero if it did not say.
	RetryAfter time.Duration

	// Ambiguous marks a failure that may have been served anyway -- a timeout
	// after the request was sent. The library performs no side effects, but its
	// callers do, and they cannot tell without being told.
	Ambiguous bool

	// Message is a sanitized description. No payload.
	Message string

	// Diagnostic points at a record a caller-configured DiagnosticSink
	// captured for this failure -- an ID and a content digest, never the
	// content. It is the zero DiagnosticRef whenever no sink was configured,
	// which is the default: A-011's Revised line asks for a second channel
	// for debugging content, not for errors to carry it directly, and this
	// field is the reference half of that channel.
	Diagnostic DiagnosticRef

	// Narrow says what the failure is within its kind, for a boundary that has
	// to tell apart what one kind holds (a refusal from the other policy
	// violations, a recovered panic from the other invariant violations). It
	// is zero for every error that has not been marked, it changes neither the
	// kind nor the sentinel errors.Is matches, and Error does not print it.
	Narrow Narrowing

	Cause error
}

func (e *OperationError) Error() string {
	parts := e.Kind.String()
	if e.Op != "" {
		parts = e.Op + ": " + parts
	}
	if e.Message != "" {
		parts += ": " + e.Message
	}
	if e.Ambiguous {
		parts += " (the request may have been served)"
	}
	if e.Cause != nil && e.Message == "" {
		parts += ": " + e.Cause.Error()
	}
	if !e.Diagnostic.IsZero() {
		// Safe to include: DiagnosticRef.String() is an ID and a digest, not
		// the captured content.
		parts += " (" + e.Diagnostic.String() + ")"
	}
	return parts
}

func (e *OperationError) Unwrap() error { return e.Cause }

// Is reports true for this error's own kind sentinel and for no other target, so
// a caller can write errors.Is(err, types.ErrRateLimited) without knowing which
// layer produced it. It does not match another *OperationError of the same kind:
// two errors with different operations and messages are different errors, and a
// target that merely wraps the sentinel is not the sentinel. Equality of kind is
// what KindOf is for.
//
// errors.Is walks the chain, so an OperationError wrapping another one also
// matches the inner one's sentinel. KindOf reports the outermost kind alone.
func (e *OperationError) Is(target error) bool {
	sentinel := sentinelFor(e.Kind)
	return sentinel != nil && target == sentinel
}

// Retryable reports whether repeating the same request could succeed.
//
// It is about the *transport*, not about the answer: a malformed body is not
// retryable here because sending the identical request again asks the same
// question of the same model. Fixing a bad answer is repair, which is a
// different mechanism with a different budget.
func (e *OperationError) Retryable() bool {
	switch e.Kind {
	case KindRateLimited, KindProviderUnavailable, KindTimeout, KindCircuitOpen:
		return true
	default:
		return false
	}
}

// Repairable reports whether asking again with the problem named could succeed.
func (e *OperationError) Repairable() bool {
	switch e.Kind {
	case KindMalformedOutput, KindSchemaViolation, KindOutputTruncated,
		KindBatchProtocolViolation, KindInvariantViolation, KindEvidenceViolation:
		return true
	default:
		return false
	}
}

// Terminal reports whether nothing this library can do will help, so a caller
// knows to stop rather than to wait.
func (e *OperationError) Terminal() bool {
	switch e.Kind {
	case KindConfiguration, KindAuthentication, KindPermission, KindInvalidRequest,
		KindPolicyViolation, KindBudgetExceeded, KindUnsupportedCapability,
		KindCanceled, KindShutdown, KindRepairExhausted, KindReviewRequired:
		return true
	default:
		return false
	}
}

// Newf builds an OperationError with a formatted, payload-free message.
func Newf(kind ErrorKind, op string, format string, args ...any) *OperationError {
	return &OperationError{
		Kind:    kind,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap classifies an existing error without losing it.
func Wrap(kind ErrorKind, op string, cause error) *OperationError {
	return &OperationError{Kind: kind, Op: op, Cause: cause}
}

// Narrowing says what a failure is within its kind, for a boundary that tells
// apart what one kind holds. NarrowNone, the zero value, is every error that
// has not been marked.
type Narrowing uint8

const (
	// NarrowNone is an error nobody marked.
	NarrowNone Narrowing = iota

	// NarrowRefusal is read with KindPolicyViolation: the model declined, or
	// the provider's filter withheld the answer.
	NarrowRefusal

	// NarrowPanic is a recovered panic or a step that never returned.
	NarrowPanic
)

// classifiedError carries a kind for an error whose message has to stay as it
// is. It prints err's text, unwraps to err, and matches its kind's sentinel
// beside whatever err already matches. It is one unexported type so that
// KindOf, which reads it as a kindCarrier, and errors.Is cannot disagree.
type classifiedError struct {
	kind ErrorKind
	err  error

	// narrow is set by ClassifiedRefusal and is NarrowNone for every other
	// classified error.
	narrow Narrowing

	// routed is set by ClassifiedCall: the four fields below say where the
	// request went, and are the zero value otherwise.
	routed     bool
	provider   string
	model      string
	requestID  string
	retryAfter time.Duration
}

func (e *classifiedError) Error() string { return e.err.Error() }

func (e *classifiedError) Unwrap() error { return e.err }

func (e *classifiedError) errorKind() ErrorKind { return e.kind }

// Narrowing is the mark this error carries itself, and not the one of an error
// it wraps. It is how a boundary that reads a kind from a carrier (the v2
// converter) reads the mark that goes with that kind.
func (e *classifiedError) Narrowing() Narrowing { return e.narrow }

// Is reports true for the sentinel of this error's own kind and for no other
// target, as (*OperationError).Is does: another classified error of the same
// kind is a different error.
func (e *classifiedError) Is(target error) bool {
	sentinel := sentinelFor(e.kind)
	return sentinel != nil && target == sentinel
}

// Classified returns err carrying kind: KindOf reports kind, errors.Is matches
// the kind's sentinel, and its text and its Unwrap chain are err's own. It is
// for an error whose message must stay as it is, because a caller may already
// match on it; an error built to be classified is better made with Newf.
//
// A nil err stays nil. A kind that has no sentinel (KindUnknown, or a value
// past the last kind) returns err as it was, since there is nothing to say
// about it. Classifying an error that already has a kind puts the new kind
// outermost, so KindOf reports it, and the inner kind's sentinel still matches
// through errors.Is.
func Classified(kind ErrorKind, err error) error {
	if err == nil || sentinelFor(kind) == nil {
		return err
	}
	return &classifiedError{kind: kind, err: err}
}

// ClassifiedRefusal is Classified for an answer the model declined to give, or
// that the provider's filter withheld: it carries KindPolicyViolation and
// NarrowRefusal, so that a boundary which tells a refusal from the other policy
// violations (NarrowingOf) can. Its text and its Unwrap chain are err's own, as
// Classified's are. A nil err stays nil.
func ClassifiedRefusal(err error) error {
	if err == nil {
		return nil
	}
	return &classifiedError{kind: KindPolicyViolation, err: err, narrow: NarrowRefusal}
}

// ClassifiedCall is Classified for a failure of a request to a provider, with
// the route it failed on: the provider and model it went to, the request's id,
// and how long the provider asked the caller to wait (zero when it did not
// say). CallOf reads the route back. None of the four is the caller's data.
func ClassifiedCall(kind ErrorKind, err error, provider, model, requestID string, retryAfter time.Duration) error {
	if err == nil || sentinelFor(kind) == nil {
		return err
	}
	return &classifiedError{
		kind:       kind,
		err:        err,
		routed:     true,
		provider:   provider,
		model:      model,
		requestID:  requestID,
		retryAfter: retryAfter,
	}
}

// CallOf returns the route the outermost ClassifiedCall error in err's chain
// was made on, and whether there is one. An error that went through Classified
// alone, an *OperationError and a plain error have no route here; a caller
// that wants an *OperationError's own Provider and Model reads them from it.
func CallOf(err error) (provider, model, requestID string, retryAfter time.Duration, ok bool) {
	if routed := routeOf(err); routed != nil {
		return routed.provider, routed.model, routed.requestID, routed.retryAfter, true
	}
	return "", "", "", 0, false
}

// routeOf finds the first routed classifiedError of the chain, outermost
// first, descending into joined errors in order.
func routeOf(err error) *classifiedError {
	for err != nil {
		if typed, isClassified := err.(*classifiedError); isClassified && typed.routed {
			return typed
		}
		switch wrapper := err.(type) {
		case interface{ Unwrap() error }:
			err = wrapper.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range wrapper.Unwrap() {
				if found := routeOf(inner); found != nil {
					return found
				}
			}
			return nil
		default:
			return nil
		}
	}
	return nil
}

// NarrowingOf returns the mark of the outermost error in err's chain that has
// one to give: an *OperationError, whatever its Narrow is, or an error made by
// ClassifiedRefusal. It is NarrowNone when there is none. An unmarked outer
// *OperationError hides the mark of one it wraps: the outermost error is the
// one the caller was given. An error made by Classified or ClassifiedCall has
// no mark and is looked through.
func NarrowingOf(err error) Narrowing {
	mark, _ := narrowingIn(err)
	return mark
}

// narrowingIn finds the mark NarrowingOf reports, and whether there was a node
// to read it from. It looks at err and then at what it wraps, in order, and
// into every error of a joined one.
func narrowingIn(err error) (Narrowing, bool) {
	for err != nil {
		switch typed := err.(type) {
		case *OperationError:
			if typed == nil {
				return NarrowNone, true
			}
			return typed.Narrow, true
		case *classifiedError:
			if typed.narrow != NarrowNone {
				return typed.narrow, true
			}
		}
		switch wrapper := err.(type) {
		case interface{ Unwrap() error }:
			err = wrapper.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range wrapper.Unwrap() {
				if mark, found := narrowingIn(inner); found {
					return mark, true
				}
			}
			return NarrowNone, false
		default:
			return NarrowNone, false
		}
	}
	return NarrowNone, false
}

// lastKind is the highest kind there is. A kind added after it has to move it,
// and the test over every kind fails until it does.
const lastKind = KindReviewRequired

// kindOfSentinel reports the kind a sentinel stands for. A sentinel is the kind
// itself, so KindOf of the bare sentinel is its kind, as errors.Is of it
// against the sentinel always was.
func kindOfSentinel(err error) (ErrorKind, bool) {
	for kind := KindConfiguration; kind <= lastKind; kind++ {
		if err == sentinelFor(kind) {
			return kind, true
		}
	}
	return KindUnknown, false
}

// kindCarrier is implemented by an error type that carries a kind without being
// an *OperationError, so that KindOf and errors.Is agree about it. It is
// unexported: an error type that is not in this package says what it is by
// wrapping an *OperationError.
type kindCarrier interface {
	errorKind() ErrorKind
}

// KindOf reports the classified kind of an error, or KindUnknown. It is the kind
// of the outermost *OperationError, or of the outermost error that carries a kind
// of its own, in the chain; for every error type this package defines, it is the
// kind whose sentinel errors.Is matches.
//
// KindUnknown is deliberately not a guess. A caller branching on it should treat
// it as "nobody classified this", which is a gap to close rather than a
// category to handle.
func KindOf(err error) ErrorKind {
	if err == nil {
		return KindUnknown
	}
	if kind, isSentinel := kindOfSentinel(err); isSentinel {
		return kind
	}
	switch typed := err.(type) {
	case *OperationError:
		if typed == nil {
			return KindUnknown
		}
		return typed.Kind
	case kindCarrier:
		return typed.errorKind()
	}

	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		return KindOf(wrapper.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range wrapper.Unwrap() {
			if kind := KindOf(inner); kind != KindUnknown {
				return kind
			}
		}
	}
	return KindUnknown
}
