package schemaflux

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// This file reads an error of the call path, or of a provider, as an *Error: the
// kind table between the internal kinds and the root's, the walk of a chain, and
// the facts a chain states. It is the one place that decides these, and every
// package that has to read an internal error as the root's reads it through
// internal/bind, which holds the functions registered from bind.go.

// kindFromInternal is the v2 kind of an internal kind, with the mark the
// internal error carries (types.NarrowingOf). The mark is read first: a refusal
// (ErrRefused) and a recovered panic (ErrInternal) are told apart from the other
// errors of the kind they were filed under. A policy violation with no mark is
// ErrPolicy: it is the client's own data policy refusing the route, and only an
// answer a model declined or a provider's filter withheld carries the refusal
// mark, which types.ClassifiedRefusal puts on it. An internal kind nobody
// classified (types.KindUnknown) has no v2 kind and reports false.
func kindFromInternal(kind types.ErrorKind, narrow types.Narrowing) (Kind, bool) {
	switch narrow {
	case types.NarrowRefusal:
		return ErrRefused, true
	case types.NarrowPanic:
		return ErrInternal, true
	}
	switch kind {
	case types.KindConfiguration:
		return ErrConfiguration, true
	case types.KindInvalidRequest:
		return ErrInvalidRequest, true
	case types.KindUnsupportedCapability:
		return ErrUnsupported, true
	case types.KindAuthentication, types.KindPermission:
		return ErrUnauthorized, true
	case types.KindPolicyViolation:
		return ErrPolicy, true
	case types.KindBudgetExceeded:
		return ErrBudgetExceeded, true
	case types.KindAdmissionRejected:
		return ErrBusy, true
	case types.KindRateLimited:
		return ErrRateLimited, true
	case types.KindProviderUnavailable, types.KindCircuitOpen:
		return ErrUnavailable, true
	case types.KindTimeout:
		return ErrTimeout, true
	case types.KindCanceled:
		return ErrCanceled, true
	case types.KindShutdown:
		return ErrClosed, true
	case types.KindContextTooLarge:
		return ErrTooLarge, true
	case types.KindOutputTruncated:
		return ErrTruncated, true
	case types.KindMalformedOutput, types.KindSchemaViolation, types.KindBatchProtocolViolation,
		types.KindInvariantViolation, types.KindEvidenceViolation, types.KindRepairExhausted:
		return ErrInvalidAnswer, true
	case types.KindReviewRequired:
		return ErrReviewRequired, true
	}
	return 0, false
}

// kindToInternal is the internal kind a v2 kind is carried as when an error
// that has one travels through the call path, and the mark that lets
// kindFromInternal bring it back as the same kind. It is the inverse of
// kindFromInternal for every kind: ErrRefused is a policy violation marked as a
// refusal, and ErrInternal is an invariant violation marked as a panic.
func kindToInternal(kind Kind) (types.ErrorKind, types.Narrowing) {
	switch kind {
	case ErrConfiguration:
		return types.KindConfiguration, types.NarrowNone
	case ErrInvalidRequest:
		return types.KindInvalidRequest, types.NarrowNone
	case ErrUnsupported:
		return types.KindUnsupportedCapability, types.NarrowNone
	case ErrUnauthorized:
		return types.KindAuthentication, types.NarrowNone
	case ErrPolicy:
		return types.KindPolicyViolation, types.NarrowNone
	case ErrBudgetExceeded:
		return types.KindBudgetExceeded, types.NarrowNone
	case ErrBusy:
		return types.KindAdmissionRejected, types.NarrowNone
	case ErrRateLimited:
		return types.KindRateLimited, types.NarrowNone
	case ErrUnavailable:
		return types.KindProviderUnavailable, types.NarrowNone
	case ErrTimeout:
		return types.KindTimeout, types.NarrowNone
	case ErrCanceled:
		return types.KindCanceled, types.NarrowNone
	case ErrClosed:
		return types.KindShutdown, types.NarrowNone
	case ErrTooLarge:
		return types.KindContextTooLarge, types.NarrowNone
	case ErrTruncated:
		return types.KindOutputTruncated, types.NarrowNone
	case ErrRefused:
		return types.KindPolicyViolation, types.NarrowRefusal
	case ErrInvalidAnswer:
		return types.KindMalformedOutput, types.NarrowNone
	case ErrReviewRequired:
		return types.KindReviewRequired, types.NarrowNone
	case ErrInternal:
		return types.KindInvariantViolation, types.NarrowPanic
	}
	return types.KindProviderUnavailable, types.NarrowNone
}

// maxChainNodes bounds a walk of an error chain, so that an error that wraps
// itself ends the walk and does not hang the call that returned it.
const maxChainNodes = 256

// walkChain calls visit with err and then with each error beneath it, outermost
// first and, for an error that wraps several, in the order it lists them. It
// stops when visit returns false or after maxChainNodes errors.
func walkChain(err error, visit func(error) bool) {
	budget := maxChainNodes
	walkChainNode(err, visit, &budget)
}

func walkChainNode(err error, visit func(error) bool, budget *int) bool {
	if err == nil || *budget <= 0 {
		return true
	}
	*budget--
	if !visit(err) {
		return false
	}
	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		return walkChainNode(wrapper.Unwrap(), visit, budget)
	case interface{ Unwrap() []error }:
		for _, inner := range wrapper.Unwrap() {
			if !walkChainNode(inner, visit, budget) {
				return false
			}
		}
	}
	return true
}

// sourceOfKind says which kind of error decided a failure's kind.
type sourceOfKind uint8

const (
	// fromLibrary: an *Error or a bare Kind of this package.
	fromLibrary sourceOfKind = iota + 1
	// fromOperationError: an *types.OperationError.
	fromOperationError
	// fromCarrier: another internal error that carries a kind, such as an
	// error made by types.Classified or a review request.
	fromCarrier
)

// verdict is the first classified error of a chain and the kind it decides.
type verdict struct {
	kind   Kind
	source sourceOfKind
	node   error
}

// classifiedBy walks err from the outside and returns the first error that
// carries a kind: an *Error with a kind, a bare Kind, an internal
// *types.OperationError, or another internal error that carries a kind. The
// outermost classified error decides, so a kind given by a caller's own wrapper
// is not overridden by an older one beneath it.
func classifiedBy(err error) (verdict, bool) {
	var found verdict
	var ok bool
	walkChain(err, func(node error) bool {
		switch typed := node.(type) {
		case *Error:
			if typed != nil && typed.Kind.valid() {
				found, ok = verdict{kind: typed.Kind, source: fromLibrary, node: node}, true
				return false
			}
		case Kind:
			if typed.valid() {
				found, ok = verdict{kind: typed, source: fromLibrary, node: node}, true
				return false
			}
		case *types.OperationError:
			if typed != nil {
				if kind, known := kindFromInternal(typed.Kind, typed.Narrow); known {
					found, ok = verdict{kind: kind, source: fromOperationError, node: node}, true
					return false
				}
			}
		default:
			if carriesKind(node) {
				kind, _ := kindFromInternal(types.KindOf(node), narrowingOfCarrier(node))
				found, ok = verdict{kind: kind, source: fromCarrier, node: node}, true
				return false
			}
		}
		return true
	})
	return found, ok
}

// narrowingOfCarrier is the mark a carrier of a kind holds itself, for the one
// that has one (the error of an answer a provider's filter withheld), and none
// for every other. The mark of an error it wraps is not read: the carrier's
// own kind is the one that is being converted.
func narrowingOfCarrier(err error) types.Narrowing {
	if marked, ok := err.(interface{ Narrowing() types.Narrowing }); ok {
		return marked.Narrowing()
	}
	return types.NarrowNone
}

// carriesKind reports whether err itself, and not an error it wraps, carries
// an internal kind. types.KindOf reads the outermost kind in a chain, so an
// error carries one of its own when KindOf finds a kind in it that none of the
// errors it wraps has.
func carriesKind(err error) bool {
	kind := types.KindOf(err)
	if kind == types.KindUnknown {
		return false
	}
	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		return types.KindOf(wrapper.Unwrap()) != kind
	case interface{ Unwrap() []error }:
		for _, inner := range wrapper.Unwrap() {
			if types.KindOf(inner) == kind {
				return false
			}
		}
	}
	return true
}

// plainSentinels are the errors of the internal packages that name a failure
// without carrying a kind. An error that holds one and was classified by
// nothing else has the kind listed here. Those the call path classifies at the
// place it makes them are decided by that classification first.
var plainSentinels = []struct {
	err  error
	kind Kind
}{
	{ops.ErrNoProvider, ErrConfiguration},
	{config.ErrNoModelMapping, ErrConfiguration},
	{types.ErrEndpointNotAllowed, ErrConfiguration},
	{ops.ErrDataPolicyRefusal, ErrPolicy},
	{ops.ErrContractDegraded, ErrPolicy},
	{ops.ErrRouteRefused, ErrPolicy},
	{llm.ErrNoMessageOutput, ErrInvalidAnswer},
	{llm.ErrUnrequestedTool, ErrInvalidAnswer},
	{llm.ErrStreamIncomplete, ErrUnavailable},
	{llm.ErrCapabilityUnknown, ErrUnsupported},
	{llm.ErrCapabilityUnmet, ErrUnsupported},
	{ops.ErrTrustBoundaryViolated, ErrInternal},
	{ops.ErrUntrustedInSystemPrompt, ErrInternal},
}

// isTrustGuard reports whether err holds one of the two errors of the library's
// own guard against untrusted text in a system prompt. Those are a defect of the
// library, whatever kind the call path filed them under.
func isTrustGuard(err error) bool {
	return errors.Is(err, ops.ErrTrustBoundaryViolated) || errors.Is(err, ops.ErrUntrustedInSystemPrompt)
}

// decidedKind returns the kind of any error and the node that decided it, and
// whether anything did. The first classified error of the chain decides. An error
// nothing classified is read by the internal classifier (a context error, an HTTP
// status, a network failure, the library's own phrases) and by the sentinels of
// plainSentinels; for what none of them knows it reports false.
func decidedKind(err error) (Kind, verdict, bool) {
	if decided, ok := classifiedBy(err); ok {
		if decided.source != fromLibrary && isTrustGuard(err) {
			return ErrInternal, decided, true
		}
		return decided.kind, decided, true
	}
	for _, sentinel := range plainSentinels {
		if errors.Is(err, sentinel.err) {
			return sentinel.kind, verdict{}, true
		}
	}
	if kind, known := kindFromInternal(llm.Classify(err), types.NarrowNone); known {
		return kind, verdict{}, true
	}
	return 0, verdict{}, false
}

// kindOfError returns the kind of any error, and the node that decided it when
// one did. An error nothing knows is ErrInternal: one the call path made and did
// not classify is a defect of the library.
func kindOfError(err error) (Kind, verdict) {
	kind, decided, known := decidedKind(err)
	if !known {
		return ErrInternal, verdict{}
	}
	return kind, decided
}

// fixedWord returns code if it is a provider's fixed word, a short run of
// letters, digits and the separators such words use, and the empty string
// otherwise. A provider chooses its codes, and one that is free text is a
// message by another name.
func fixedWord(code string) string {
	if code == "" || utf8.RuneCountInString(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == ':') {
			return ""
		}
	}
	return code
}

// facts are what a chain says about where and how a failure happened. Each
// field is taken from the outermost error in the chain that gives it.
type facts struct {
	provider, model, requestID string
	status                     int
	code                       string
	retryAfter                 time.Duration
	message                    string
}

// factsOf reads facts from the errors of a chain. An *Error and an
// *types.OperationError give the route, the id, the wait and the message they
// hold. An HTTP error of the llm package gives the status and the provider's
// code, and a rate-limit error the wait. The message of an internal error that
// the call path made before it sent anything (a configuration or an invalid
// request, classified by types.Classified) is the library's own words and is
// kept; the text of any other error is not, since it can be a provider's.
func factsOf(err error, decided verdict) facts {
	var f facts
	walkChain(err, func(node error) bool {
		switch typed := node.(type) {
		case *Error:
			if typed == nil {
				return true
			}
			setIfEmpty(&f.provider, typed.Provider)
			setIfEmpty(&f.model, typed.Model)
			setIfEmpty(&f.requestID, typed.RequestID)
			setIfEmpty(&f.message, typed.Message)
			setIfEmpty(&f.code, fixedWord(typed.Code))
			if f.status == 0 {
				f.status = typed.Status
			}
			if f.retryAfter == 0 {
				f.retryAfter = typed.RetryAfter
			}
		case *types.OperationError:
			if typed == nil {
				return true
			}
			setIfEmpty(&f.provider, typed.Provider)
			setIfEmpty(&f.model, typed.Model)
			setIfEmpty(&f.requestID, typed.RequestID)
			setIfEmpty(&f.message, typed.Message)
			if f.retryAfter == 0 {
				f.retryAfter = typed.RetryAfter
			}
		case *llm.RateLimitError:
			if typed == nil {
				return true
			}
			if f.retryAfter == 0 {
				f.retryAfter = typed.RetryAfter
			}
		case *llm.APIError:
			if typed == nil {
				return true
			}
			if f.status == 0 {
				f.status = typed.StatusCode
			}
			setIfEmpty(&f.code, fixedWord(typed.Code))
			setIfEmpty(&f.provider, typed.Provider)
			setIfEmpty(&f.model, typed.Model)
		}
		return true
	})
	if provider, model, requestID, retryAfter, ok := types.CallOf(err); ok {
		setIfEmpty(&f.provider, provider)
		setIfEmpty(&f.model, model)
		setIfEmpty(&f.requestID, requestID)
		if f.retryAfter == 0 {
			f.retryAfter = retryAfter
		}
	} else if decided.source == fromCarrier && (decided.kind == ErrConfiguration || decided.kind == ErrInvalidRequest) {
		setIfEmpty(&f.message, decided.node.Error())
	}
	if f.message != "" {
		if errors.Is(err, config.ErrNoModelMapping) {
			f.message = noModelMessage(f.message)
		}
		f.message = inVersion2Words(f.message)
	}
	return f
}

// noModelMessage is the message of a call that named no model and whose provider
// has none to give, in the words of version 2. The internal message is written for
// the old root: it names its client methods and the variables that set a model for
// every provider, and lists the providers it has a built-in mapping for, which are
// not the ones this package offers. What it says of the fault is kept, which is
// everything before its first semicolon.
func noModelMessage(message string) string {
	fault, _, _ := strings.Cut(message, "; ")
	return fault + "; name one with Client.Model, or with Model in the call's options"
}

// version1Words are phrases of messages the internal packages write that name a
// setting of the old root, and what version 2 calls it. A message is a sentence
// the library wrote, not a provider's, and these are the ones that reach a caller
// of version 2 through a classified error.
var version1Words = []struct{ old, new string }{
	{"raise MaxTokens", "raise MaxOutputTokens"},
	{"set SCHEMAFLUX_MODEL to a model", "set Model to a model"},
}

// inVersion2Words returns message with the names of the old root's settings
// replaced by version 2's.
func inVersion2Words(message string) string {
	for _, words := range version1Words {
		message = strings.ReplaceAll(message, words.old, words.new)
	}
	return message
}

func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}

// convertError is the *Error a caller is given for any error of the call path
// of an operation called op, and nil for a nil error. It is registered with the
// call path and is called by every operation on its way out.
//
// An *Error that already has a kind is returned as it is, with Op, Provider,
// Model, RequestID, Status, Code and RetryAfter filled in where it left them
// empty. Anything else becomes a new *Error with the chain it arrived as in Err.
// The kind is that of the first classified error of the chain, from the outside
// in, and the route, the id, the status, the code and the wait are read from
// the errors in it. An error nothing classified is read by the internal
// classifier and is ErrInternal when that does not know it.
//
// A caller's own error, the one a step of a combinator returned, is not passed
// here: it is returned unchanged by the combinator.
func convertError(op string, err error) error {
	return convertWith(op, err, ErrInternal)
}

// fromProviderError is the *Error for an error a built-in provider returned. It
// reads the chain as convertError does, and an error nothing knows is the
// provider being unavailable and not a defect of the library: a failure that says
// nothing about its kind is a provider that could not be reached. Its Op is
// empty, for the call that makes use of it to fill in.
func fromProviderError(err error) error {
	return convertWith("", err, ErrUnavailable)
}

// convertWith is convertError with the kind an error nothing knows is given.
func convertWith(op string, err error, unknown Kind) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok && e != nil && e.Kind.valid() {
		return completedError(e, op)
	}
	kind, decided, known := decidedKind(err)
	if !known {
		kind = unknown
	}
	f := factsOf(err, decided)
	return &Error{
		Kind:       kind,
		Op:         op,
		Provider:   f.provider,
		Model:      f.model,
		RequestID:  f.requestID,
		Status:     f.status,
		Code:       f.code,
		RetryAfter: f.retryAfter,
		Message:    f.message,
		Err:        err,
	}
}

// completedError returns e with the fields it left empty filled in from op and
// from the chain beneath it. It returns e itself when there is nothing to fill,
// and a copy that wraps e otherwise: an error a provider returned may be
// shared, and is not written to, and errors.Is still finds the original.
func completedError(e *Error, op string) *Error {
	f := factsOf(e.Err, verdict{})
	filled := *e
	filled.Err = e
	setIfEmpty(&filled.Op, op)
	setIfEmpty(&filled.Provider, f.provider)
	setIfEmpty(&filled.Model, f.model)
	setIfEmpty(&filled.RequestID, f.requestID)
	setIfEmpty(&filled.Code, f.code)
	if filled.Status == 0 {
		filled.Status = f.status
	}
	if filled.RetryAfter == 0 {
		filled.RetryAfter = f.retryAfter
	}
	if filled.Op == e.Op && filled.Provider == e.Provider && filled.Model == e.Model &&
		filled.RequestID == e.RequestID && filled.Code == e.Code &&
		filled.Status == e.Status && filled.RetryAfter == e.RetryAfter {
		return e
	}
	return &filled
}
