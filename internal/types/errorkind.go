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
	return parts
}

func (e *OperationError) Unwrap() error { return e.Cause }

// Is reports true for this error's own kind sentinel, so a caller can write
// errors.Is(err, types.ErrRateLimited) without knowing which layer produced it.
func (e *OperationError) Is(target error) bool {
	if sentinel := sentinelFor(e.Kind); sentinel != nil && errors.Is(target, sentinel) {
		return true
	}
	return false
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

// KindOf reports the classified kind of an error, or KindUnknown.
//
// KindUnknown is deliberately not a guess. A caller branching on it should treat
// it as "nobody classified this", which is a gap to close rather than a
// category to handle.
func KindOf(err error) ErrorKind {
	var operationErr *OperationError
	if errors.As(err, &operationErr) {
		return operationErr.Kind
	}
	return KindUnknown
}
