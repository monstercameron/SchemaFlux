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

// SummarizeResult summarizes text and returns the record of how it ran: what
// the call cost, how many attempts it took, and what the model claimed about
// its own output, kept apart from the measurements.
func SummarizeResult(input string, opts SummarizeOptions) (types.Result[Summary], error) {
	ctx, records := withCallRecording(opts.OpOptions.Context)
	opts.OpOptions.Context = ctx

	value, err := SummarizeWithMetadata(input, opts)
	meta := envelopeFrom(records.collect(), "summarize")

	if err != nil {
		return types.Result[Summary]{Meta: meta}, err
	}
	return types.Result[Summary]{Value: value, Meta: meta}, nil
}

// RewriteResult rewrites text and returns the record of how it ran.
func RewriteResult(input string, opts RewriteOptions) (types.Result[Rewritten], error) {
	ctx, records := withCallRecording(opts.OpOptions.Context)
	opts.OpOptions.Context = ctx

	value, err := RewriteWithMetadata(input, opts)
	meta := envelopeFrom(records.collect(), "rewrite")

	if err != nil {
		return types.Result[Rewritten]{Meta: meta}, err
	}
	return types.Result[Rewritten]{Value: value, Meta: meta}, nil
}

// TranslateResult translates text and returns the record of how it ran.
func TranslateResult(input string, opts TranslateOptions) (types.Result[Translation], error) {
	ctx, records := withCallRecording(opts.OpOptions.Context)
	opts.OpOptions.Context = ctx

	value, err := TranslateWithMetadata(input, opts)
	meta := envelopeFrom(records.collect(), "translate")

	if err != nil {
		return types.Result[Translation]{Meta: meta}, err
	}
	return types.Result[Translation]{Value: value, Meta: meta}, nil
}

// ExpandResult expands text and returns the record of how it ran.
func ExpandResult(input string, opts ExpandOptions) (types.Result[Expansion], error) {
	ctx, records := withCallRecording(opts.OpOptions.Context)
	opts.OpOptions.Context = ctx

	value, err := ExpandWithMetadata(input, opts)
	meta := envelopeFrom(records.collect(), "expand")

	if err != nil {
		return types.Result[Expansion]{Meta: meta}, err
	}
	return types.Result[Expansion]{Value: value, Meta: meta}, nil
}
