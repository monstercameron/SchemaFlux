package flow

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// matchOp is the operation of Match: the Op of its Record and of its errors.
const matchOp = "flow.match"

// matchStack is how many cases Match converts without allocating.
const matchStack = 8

// Match runs the action of the first case whose condition holds and reports
// whether any case ran. A condition that is a string is judged by a model; a
// reflect.Type, an error value or any other value is compared by type in Go and
// asks no model. The string "_", "otherwise" or "default" is the default case and
// is not judged; Otherwise builds one. The default case is evaluated last
// wherever it is written, and only the first runs. A case with a nil Action is
// skipped, and so is a case whose condition is the empty string.
//
//	matched, err := flow.Match(ctx, ticket,
//		flow.When("a billing question", handleBilling),
//		flow.Otherwise(handleGeneral))
//
// The model is asked once for each string condition that is reached, on the Quick
// tier in Transform mode, and its answer is a text: true or false, in either
// case, yes or no, with or without quotes and a full stop. Anything else is an
// error of kind ErrInvalidAnswer. A failure to judge a condition is returned and
// nothing runs, not the cases after it and not the default: it is never read as
// "did not match". The input is written to the model as JSON, so a value JSON
// cannot hold is an error of kind ErrInvalidRequest when a string condition is
// reached. Match takes no options: the tier is not a caller's choice.
//
// A context that is over (a nil one is an error of kind ErrInvalidRequest) ends
// the call before anything is judged or run, with the error of kind ErrCanceled
// or ErrTimeout for it.
//
// The Record of the call is "flow.match", delivered at ContractPromptOnly: the
// model's answer is a text that Go reads, with no schema. A Record is added for
// every call, with no requests when no string condition was reached. A client
// held to a floor above ContractPromptOnly refuses every call, whatever its
// conditions are, with ErrPolicy before anything is sent. The Record has no Asked:
// Match builds no decision record.
//
// The action runs on the caller's goroutine inside the call, after the
// conditions that decided it, so the Record's Elapsed includes it, and a panic in
// it is the caller's: no Record is added for a call that panics. A call the
// action makes of its own is a call of its own, with a Record of its own.
// Match starts no goroutine.
func Match(ctx context.Context, input any, cases ...Case) (bool, error) {
	call, err := bind.Begin(ctx, matchOp)
	if err != nil {
		return false, err
	}
	// A call whose context is over starts nothing: not a model's request, and not
	// an action either.
	if err := ctx.Err(); err != nil {
		return false, call.End(nil, err)
	}
	if call.Plain() {
		var stack [matchStack]types.Case
		matched, err := ops.Match(call.Context(), input, matchCases(&stack, cases)...)
		return matched, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (bool, error) {
		return ops.Match(ctx, input, matchCases(nil, cases)...)
	})
}

// matchCases is the internal form of the cases, which have the same two fields.
// It is written into stack when they fit, so that the plain path allocates
// nothing for the list, and into a slice of its own when they do not or when no
// stack is given. A change to a field of Case that the internal case does not
// have is a compile error here and not a field that goes missing.
func matchCases(stack *[matchStack]types.Case, cases []Case) []types.Case {
	var internal []types.Case
	if stack != nil && len(cases) <= matchStack {
		internal = stack[:len(cases)]
	} else {
		internal = make([]types.Case, len(cases))
	}
	for i, c := range cases {
		internal[i] = types.Case(c)
	}
	return internal
}
