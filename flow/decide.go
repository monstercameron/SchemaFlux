package flow

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// decideOp is the operation of Decide: the Op of its Record and of its errors.
const decideOp = "flow.decide"

// decideStack is how many decisions Decide converts without allocating.
const decideStack = 8

// fallbackStrategy is the Strategy of the Record of a Decide that took its
// fallback.
const fallbackStrategy = "fallback"

// Decision is one way a decision can go.
type Decision[T any] struct {
	// Value is what Decide returns when this decision is taken.
	Value T

	// Description says when it applies, in plain language, for the model. It is
	// the only part of a decision a model is shown. An empty Description gives
	// the model nothing to choose this decision by.
	Description string

	// Condition, when it is not nil and returns true for the situation, takes
	// this decision without asking a model. Nil means the model decides. The
	// conditions are called in the order of the decisions, on the caller's
	// goroutine, until one holds.
	Condition func(situation any) bool

	// Fallback marks the decision to take when the model could not be reached or
	// its answer could not be used: a failure of kind ErrUnavailable,
	// ErrRateLimited, ErrTimeout or ErrInvalidAnswer, while the caller's context
	// is still live. Any other failure (a budget, a policy, a cancellation, a
	// configuration error) is returned. At most one decision may be marked; with
	// none marked every failure is returned. False, the zero value, marks
	// nothing.
	Fallback bool
}

// DecideResult is the decision taken and how it was arrived at.
type DecideResult[T any] struct {
	// Value is the Value of the decision taken. It is the zero value beside an
	// error.
	Value T `json:"value"`

	// Index is the position of the decision taken in the decisions given.
	Index int `json:"index"`

	// Explanation says why: the model's own words, or "Condition met for: " and
	// the decision's Description when ByCondition, or the library's own words
	// when Fallback.
	Explanation string `json:"explanation"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement and not comparable between models or prompts. Zero means no
	// model's answer was used: Fallback or ByCondition.
	ModelConfidence float64 `json:"confidence"`

	// Alternatives are the positions of the decisions the model also considered,
	// best first. Nil when it named none, and when Fallback or ByCondition.
	Alternatives []int `json:"alternatives,omitempty"`

	// Fallback reports that the decision marked Fallback was taken because the
	// model could not be reached or its answer could not be used. False means a
	// model chose it, or a condition did, or it is not the fallback: a model that
	// chooses the marked decision on its own leaves Fallback false.
	Fallback bool `json:"fallback"`

	// ByCondition reports that the decision's own Condition held and no model
	// was asked.
	ByCondition bool `json:"by_condition"`
}

// Decide takes one of the decisions: the first whose Condition holds, else the
// one a model chooses for the situation, which is written to it as JSON beside
// the Descriptions of the decisions. Default tier Smart, mode Transform.
//
//	d, err := flow.Decide(ctx, ticket, []flow.Decision[Dept]{
//		{Value: Billing, Description: "a question about an invoice or a payment"},
//		{Value: General, Description: "anything else", Fallback: true},
//	})
//
// An answer that selects nothing (no number, a number that is none of the
// decisions, a list of alternatives that names the selection or a decision twice)
// is an error of kind ErrInvalidAnswer unless a decision is marked Fallback: it
// is never read as the first decision. A call with no decisions is an error of
// kind ErrInvalidRequest, and so is a situation that cannot be written as JSON
// when no condition held; two decisions marked Fallback, more decisions than one
// answer can be declared for (a few hundred) and a setting no call can use are
// errors of kind ErrConfiguration. Each is returned before anything is sent.
//
// The marked decision is taken, with Fallback true and a nil error, when the
// model's call fails with the kind ErrUnavailable, ErrRateLimited, ErrTimeout or
// ErrInvalidAnswer, which includes a single request that timed out, and not
// when the caller's own context has ended: that is the caller's decision to
// stop, and the error for it is returned. Every other failure is returned too: a
// budget, a policy refusal, a refusal by the model, a configuration error and a
// defect are never hidden behind a decision that looks made. The result is the
// zero value beside every error.
//
// The Record of the call is "flow.decide", delivered at
// ContractSchemaAndInvariantChecked: the answer is declared as a schema and the
// selection and every alternative are held to positions inside the list. A call
// that took the fallback adds a Record that says what happened: its Err is the
// failure, its Strategy is "fallback" and its Delivered is ContractPromptOnly,
// because no answer of the model was delivered; the call itself returns no
// error. The Record has no Asked: Decide builds no decision record. A client
// held to a floor above ContractSchemaAndInvariantChecked refuses the call with
// ErrPolicy before anything is sent.
//
// The conditions run on the caller's goroutine and a panic in one is the
// caller's: no Record is added for a call that panics. Decide starts no
// goroutine.
func Decide[T any](ctx context.Context, situation any, decisions []Decision[T], opts ...schemaflux.Options) (DecideResult[T], error) {
	var zero DecideResult[T]
	call, err := bind.Begin(ctx, decideOp)
	if err != nil {
		return zero, err
	}
	// A call whose context is over starts nothing: not a model's request, and not
	// a condition either.
	if err := ctx.Err(); err != nil {
		return zero, call.End(nil, err)
	}
	if len(decisions) == 0 {
		return zero, call.Refuse(types.KindInvalidRequest, "decisions is empty")
	}
	common, err := decideCommon(opts)
	if err != nil {
		return zero, call.End(nil, err)
	}
	marked := -1
	for i := range decisions {
		if !decisions[i].Fallback {
			continue
		}
		if marked >= 0 {
			return zero, call.Refuse(types.KindConfiguration, "at most one decision may be marked Fallback")
		}
		marked = i
	}
	if len(decisions) > ops.MaxDecisions {
		return zero, call.Refuse(types.KindConfiguration,
			"%d decisions is more than one choice can declare an answer for (at most %d); choose among them in smaller groups",
			len(decisions), ops.MaxDecisions)
	}

	if call.Plain() {
		var stack [decideStack]ops.Decision[T]
		value, answer, err := decideAsk(call.Context(), situation, decisions, common, &stack)
		if err != nil {
			return decideFailed(&call, nil, decisions, marked, err)
		}
		return decideResultOf(value, answer), call.End(nil, nil)
	}
	// The recorded path is ops.Recorded under the call, which is what bind.Recorded
	// is, because a call that takes the fallback ends its Record with the failure
	// and a Strategy, and bind.Recorded ends the call before it can.
	run := func(ctx context.Context) (decideAnswer[T], error) {
		value, answer, err := decideAsk(ctx, situation, decisions, common, nil)
		return decideAnswer[T]{value: value, answer: answer}, err
	}
	res, err := ops.Recorded(call.Context(), decideOp, types.ContractSchemaAndInvariantChecked, run)
	meta := res.Meta
	if err != nil {
		return decideFailed(&call, &meta, decisions, marked, err)
	}
	return decideResultOf(res.Value.value, res.Value.answer), call.End(&meta, nil)
}

// decideAnswer is what the internal Decide returns, in one value for the call
// that runs it under ops.Recorded.
type decideAnswer[T any] struct {
	value  T
	answer ops.DecisionResult
}

// decideAsk is the internal Decide with the decisions converted and the common
// settings laid on. It is given no fallback index: whether a failure is one the
// marked decision stands in for is decided here, by the kind a caller sees.
func decideAsk[T any](ctx context.Context, situation any, decisions []Decision[T], common bind.Common, stack *[decideStack]ops.Decision[T]) (T, ops.DecisionResult, error) {
	var internal []ops.Decision[T]
	if stack != nil && len(decisions) <= decideStack {
		internal = stack[:len(decisions)]
	} else {
		internal = make([]ops.Decision[T], len(decisions))
	}
	for i := range decisions {
		d := &decisions[i]
		internal[i] = ops.Decision[T]{Value: d.Value, Condition: d.Condition, Description: d.Description}
	}
	var in ops.DecideOptions
	common.Apply(&in.OpOptions)
	return ops.Decide(ctx, situation, internal, in)
}

// decideResultOf is the result a caller is given for an answer of the internal
// Decide.
func decideResultOf[T any](value T, answer ops.DecisionResult) DecideResult[T] {
	return DecideResult[T]{
		Value:           value,
		Index:           answer.SelectedIndex,
		Explanation:     answer.Explanation,
		ModelConfidence: answer.ModelConfidence,
		Alternatives:    answer.Alternatives,
		Fallback:        answer.Fallback,
		ByCondition:     answer.ByCondition,
	}
}

// decideFailed ends a call whose internal Decide returned err: with the marked
// decision when there is one, the caller's context is live and the failure is of
// a kind the fallback stands in for, and with the error otherwise. meta is the
// record of how the call ran, nil on the plain path.
func decideFailed[T any](call *bind.Call, meta *types.Meta, decisions []Decision[T], marked int, err error) (DecideResult[T], error) {
	failure := bind.ConvertError(decideOp, err)
	if marked < 0 || call.Context().Err() != nil {
		var zero DecideResult[T]
		return zero, call.End(meta, failure)
	}
	words, covered := decideFallbackWords(failure)
	if !covered {
		var zero DecideResult[T]
		return zero, call.End(meta, failure)
	}
	if meta != nil {
		meta.Strategy = fallbackStrategy
		meta.DeliveredContract = types.ContractPromptOnly
	}
	// The Record says the call failed and the fallback was used. What the call
	// returns is the decision, so the error End hands back is not.
	_ = call.End(meta, failure)
	return DecideResult[T]{Value: decisions[marked].Value, Index: marked, Explanation: words, Fallback: true}, nil
}

// decideFallbackWords says, in the library's own words and nothing of the
// failure's, why the fallback was taken, and reports whether err is a failure
// the fallback stands in for: the model could not be reached (unavailable, rate
// limited, timeout) or its answer could not be used (invalid answer).
func decideFallbackWords(err error) (string, bool) {
	// bind.ConvertError gives an *Error, so a type assertion reads what it needs
	// and errors.As would allocate its target.
	e, ok := err.(*schemaflux.Error)
	if !ok || e == nil {
		return "", false
	}
	switch e.Kind {
	case schemaflux.ErrUnavailable, schemaflux.ErrRateLimited, schemaflux.ErrTimeout:
		return "fallback selection: the model could not be reached", true
	case schemaflux.ErrInvalidAnswer:
		return "fallback selection: the model's answer could not be used", true
	}
	return "", false
}

// decideCommon is the common settings of the values of a call, merged left to
// right, and the configuration error of one no call can use. No value is the
// zero settings, and one value is read in place, so that a call with no more
// than one allocates nothing for them.
func decideCommon(opts []schemaflux.Options) (bind.Common, error) {
	switch len(opts) {
	case 0:
		return bind.Common{}, nil
	case 1:
		return bind.CommonOf(&opts[0])
	}
	merged := mergeDecideOptions(opts)
	return bind.CommonOf(&merged)
}

// mergeDecideOptions lays each later value over the earlier: a field that is not
// its zero value replaces the earlier one, a field at its zero leaves it alone,
// and a WebSearch that is true stays on. It is Options.merge of the root, which
// a subpackage cannot call; a test holds it to every field of Options.
func mergeDecideOptions(opts []schemaflux.Options) schemaflux.Options {
	merged := opts[0]
	for _, later := range opts[1:] {
		if later.Model != "" {
			merged.Model = later.Model
		}
		if later.Tier != 0 {
			merged.Tier = later.Tier
		}
		if later.Effort != "" {
			merged.Effort = later.Effort
		}
		if later.Mode != 0 {
			merged.Mode = later.Mode
		}
		if later.Steering != "" {
			merged.Steering = later.Steering
		}
		if later.MaxOutputTokens != 0 {
			merged.MaxOutputTokens = later.MaxOutputTokens
		}
		if later.WebSearch {
			merged.WebSearch = true
		}
	}
	return merged
}
