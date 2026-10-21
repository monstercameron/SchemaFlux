package telemetry

import (
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	wrapped "github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The longest a string that a caller or a provider chose may be in an Event, a
// Result or a Note, one limit for each kind of value. A string past its limit is
// cut, and a byte that is not printable ASCII is replaced by "?", because what
// an observer does with an event is write it to a log or a span, which are
// exports to places the library does not control.
const (
	maxID           = 128
	maxModel        = 128
	maxSchemaName   = 128
	maxProvider     = 64
	maxOperation    = 64
	maxFinishReason = 64
	maxFormat       = 64
	maxNoteName     = 64
	maxEscalation   = 64
)

// bound returns value fit to be written anywhere: at most limit bytes, each a
// printable ASCII character, with any other byte replaced by "?". A value that
// is already both is returned as it is without allocating, which is every id
// the library makes and every model name in use.
func bound(value string, limit int) string {
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

func scopeFrom(s wrapped.Scope) Scope {
	if s == wrapped.ScopeOperation {
		return ScopeOperation
	}
	return ScopeRequest
}

// eventFrom is the Event for the library's start event.
func eventFrom(e wrapped.OperationEvent) Event {
	return Event{
		Scope:          scopeFrom(e.Scope),
		Operation:      bound(e.Operation, maxOperation),
		Provider:       bound(e.Provider, maxProvider),
		Model:          bound(e.Model, maxModel),
		RequestID:      bound(e.RequestID, maxID),
		CorrelationID:  bound(e.CorrelationID, maxID),
		Mode:           modeFromName(e.Mode),
		Tier:           tierFromName(e.Intelligence),
		SchemaName:     bound(e.SchemaName, maxSchemaName),
		ResponseFormat: bound(e.ResponseFormat, maxFormat),
		Repair:         e.Repair,
		Stream:         e.Stream,
	}
}

// resultFrom is the Result for the library's finish event.
func resultFrom(r wrapped.OperationResult) Result {
	return Result{
		Scope:            scopeFrom(r.Scope),
		Operation:        bound(r.Operation, maxOperation),
		Duration:         r.Duration,
		ProviderDuration: r.ProviderDuration,
		Usage:            usageFrom(r.Usage),
		Cost:             costFrom(r.Cost),
		Attempts:         r.Attempts,
		Provider:         bound(r.Provider, maxProvider),
		Model:            bound(r.Model, maxModel),
		FinishReason:     bound(r.FinishReason, maxFinishReason),
		Cached:           r.Cached,
		AskedFirst:       bound(r.AskedFirst, maxProvider),
		AskedFirstModel:  bound(r.AskedFirstModel, maxModel),
		Escalated:        bound(r.Escalated, maxEscalation),
		Requests:         r.Requests,
		Items:            r.Items,
		Succeeded:        r.Succeeded,
		Failed:           r.Failed,
		Cancelled:        r.Cancelled,
		Kind:             kindFromName(r.ErrorKind),
	}
}

// noteFrom is the Note for the library's note.
func noteFrom(n wrapped.Note) Note {
	return Note{
		Name:    bound(n.Name, maxNoteName),
		Attempt: n.Attempt,
		Kind:    kindFromName(n.ErrorKind),
		Wait:    n.Wait,
	}
}

// modeFromName is the Mode for the name the library gives a mode. The library's
// own "unset", and anything it does not know, is the zero Mode.
func modeFromName(name string) schemaflux.Mode {
	switch name {
	case "strict":
		return schemaflux.ModeStrict
	case "transform":
		return schemaflux.ModeTransform
	case "creative":
		return schemaflux.ModeCreative
	}
	return 0
}

// tierFromName is the Tier for the name the library gives a tier.
func tierFromName(name string) schemaflux.Tier {
	switch name {
	case "smart":
		return schemaflux.TierSmart
	case "fast":
		return schemaflux.TierFast
	case "quick":
		return schemaflux.TierQuick
	}
	return 0
}

// usageFrom is the Usage for the internal token count. The count of a provider
// that reports only input and output tokens is read from those, and a total it
// left at zero beside either is their sum, which is what Total is defined to be.
func usageFrom(u types.TokenUsage) schemaflux.Usage {
	input, output := u.PromptTokens, u.CompletionTokens
	if input == 0 && output == 0 {
		input, output = u.InputTokens, u.OutputTokens
	}
	total := u.TotalTokens
	if total == 0 {
		total = input + output
	}
	return schemaflux.Usage{
		Input:      input,
		Output:     output,
		Total:      total,
		Cached:     u.CachedTokens,
		CacheWrite: u.CacheWriteTokens,
		Reasoning:  u.ReasoningTokens,
	}
}

// costFrom is the Cost for the internal cost. The price per token, the billing
// period, the organisation and the project are not carried: a price per token is
// the price table's to state, and the other three are not a call's.
func costFrom(c types.CostInfo) schemaflux.Cost {
	return schemaflux.Cost{
		Total:     c.TotalCost,
		Input:     c.PromptCost,
		Output:    c.CompletionCost,
		Cached:    c.CachedCost,
		Reasoning: c.ReasoningCost,
		Currency:  c.Currency,
		Priced:    c.Priced,
		Estimated: c.Quality == types.PricingEstimated,
		Source:    c.PricingSource,
	}
}

// kindByName is the Kind of each name the library gives a failure in an event.
// Every Kind in it is the one bind.KindOf gives, the conversion the root's own
// *Error is made with, so that an event, a span and the error a caller holds
// agree without this package keeping a table of its own. A name the library
// gives for a refusal or a recovered panic stands for the internal kind and the
// mark that KindOf reads them from.
var kindByName = func() map[string]schemaflux.Kind {
	kindOf := func(kind types.ErrorKind, mark types.Narrowing) schemaflux.Kind {
		return schemaflux.Kind(bind.KindOf(kind, mark))
	}
	names := map[string]schemaflux.Kind{
		wrapped.ErrorKindInternal: kindOf(types.KindInvariantViolation, types.NarrowPanic),
		wrapped.ErrorKindRefused:  kindOf(types.KindPolicyViolation, types.NarrowRefusal),
	}
	for kind := types.KindConfiguration; kind.String() != types.KindUnknown.String(); kind++ {
		names[kind.String()] = kindOf(kind, types.NarrowNone)
	}
	return names
}()

// kindFromName is the Kind of the failure an event names, and the zero Kind for
// no failure. A name that is not one the library gives, and the library's
// "unknown", which is a failure that no code classified, are ErrInternal, as
// they are in the Error a call returns: a failure is never reported as a
// success because its name was not recognised.
func kindFromName(name string) schemaflux.Kind {
	if name == "" {
		return 0
	}
	if kind, ok := kindByName[name]; ok {
		return kind
	}
	return schemaflux.ErrInternal
}
