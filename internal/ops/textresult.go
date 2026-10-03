package ops

import (
	"github.com/monstercameron/schemaflux/internal/types"
)

// The text operations shipped as twins: Summarize returning a bare string and
// SummarizeWithMetadata returning a struct with a `Metadata map[string]any`
// bag beside it. OP-401 collapses that. The bag is the shape A-006 replaced --
// a map called metadata is where a model's claim and a measured token count
// end up indistinguishable, which is the whole failure this rebuild is about.
//
// So there are now two forms per operation and not three: the plain one that
// returns what the caller asked for, and the *Result one that returns the same
// answer inside types.Result, whose Meta separates what the runtime measured
// from what the model claimed. The WithMetadata twins are deprecated and still
// work.
//
// Each wrapper runs the same operation rather than a second implementation of
// it -- see extractresult.go for why. An envelope built by re-running the work
// down a parallel path is an envelope describing a different call.

// textResult is what the four *Result wrappers share: it records the call,
// checks the contract floor before and after it, and puts the value in an
// envelope. op is the operation's name in the envelope, and run is the
// deprecated *WithMetadata form it wraps; run reads the options through the
// pointers, so it sees the context that was recorded on.
func textResult[T any](op string, common *CommonOptions, embedded *types.OpOptions, run func() (T, error)) (_ types.Result[T], runErr error) {
	ctx, records := withCallRecording(WithOperationName(resolvedContext(*common, *embedded), op))
	// The call is one unit to the installed observers, ending with the kind of
	// whatever error this returns.
	ctx, observed := observeOperation(ctx, op)
	defer observed.end(&runErr)
	useContext(common, embedded, ctx)

	if floorErr := checkContractFloor(ctx, textResultContract); floorErr != nil {
		return types.Result[T]{Meta: types.Meta{Operation: op, RequestedContract: textResultContract}}, floorErr
	}

	value, err := run()
	meta := records.meta(op)
	meta.RequestedContract = textResultContract
	if err == nil {
		meta.DeliveredContract = textResultContract
	}

	if err != nil {
		return types.Result[T]{Meta: meta}, err
	}
	// The contract floor of the caller's policy, on the call or on the client:
	// an answer delivered below it is an error and not a quieter success.
	if policyErr := enforceContractPolicy(ctx, meta.DeliveredContract); policyErr != nil {
		return types.Result[T]{Meta: meta}, policyErr
	}
	return types.Result[T]{Value: value, Meta: meta}, nil
}

// SummarizeResult summarizes text and returns the record of how it ran: what
// the call cost, how many attempts it took, and what the model claimed about
// its own output, kept apart from the measurements.
func SummarizeResult(input string, opts SummarizeOptions) (types.Result[Summary], error) {
	return textResult("summarize", &opts.CommonOptions, &opts.OpOptions, func() (Summary, error) {
		return SummarizeWithMetadata(input, opts)
	})
}

// RewriteResult rewrites text and returns the record of how it ran.
func RewriteResult(input string, opts RewriteOptions) (types.Result[Rewritten], error) {
	return textResult("rewrite", &opts.CommonOptions, &opts.OpOptions, func() (Rewritten, error) {
		return RewriteWithMetadata(input, opts)
	})
}

// TranslateResult translates text and returns the record of how it ran.
func TranslateResult(input string, opts TranslateOptions) (types.Result[Translation], error) {
	return textResult("translate", &opts.CommonOptions, &opts.OpOptions, func() (Translation, error) {
		return TranslateWithMetadata(input, opts)
	})
}

// ExpandResult expands text and returns the record of how it ran.
func ExpandResult(input string, opts ExpandOptions) (types.Result[Expansion], error) {
	return textResult("expand", &opts.CommonOptions, &opts.OpOptions, func() (Expansion, error) {
		return ExpandWithMetadata(input, opts)
	})
}
