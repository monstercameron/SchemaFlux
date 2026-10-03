package schemaflux

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RewriteOptions are the settings of Rewrite and RewriteDetail. The zero value
// is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: RewriteOptions{Model: "gpt-6-luna", Tone: "formal"}.
type RewriteOptions struct {
	Options

	// Tone is the tone to write in: "formal", "casual", "friendly". It is not
	// checked: it is a request, and any word is passed to the model. Empty means
	// unchanged.
	Tone string

	// Formality is 1 (loose) to 10 (formal). Zero means 5. A value outside 1 to
	// 10 is a configuration error.
	Formality int

	// NoPreserveFacts withholds the instruction to keep every fact. False keeps
	// it.
	NoPreserveFacts bool

	// Audience is who the text is for. Empty means unstated.
	Audience string

	// StyleGuide is a style to follow. Empty means none.
	StyleGuide string

	// Changes are specific changes to make. Nil means none.
	Changes []string

	// AvoidWords must not appear in the result. A word is found as a whole word,
	// without regard to case, and a phrase as a phrase; a result that has one is
	// refused. Nil means none.
	AvoidWords []string

	// IncludeWords must appear in the result, found the same way; a result that
	// lacks one is refused. Nil means none.
	IncludeWords []string
}

// Rewritten is a rewrite with what the model reported about it.
type Rewritten struct {
	// Text is the rewritten text.
	Text string `json:"text"`

	// ChangesMade is what the model says it changed. Nil when it listed none.
	ChangesMade []string `json:"changes_made,omitempty"`

	// ToneAchieved is the tone the model says the result has.
	ToneAchieved string `json:"tone_achieved,omitempty"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement.
	ModelConfidence float64 `json:"confidence"`
}

// mergeRewriteOptions merges the values of a call (M0).
func mergeRewriteOptions(opts []RewriteOptions) RewriteOptions {
	return mergeValues(opts, func(o, later RewriteOptions) RewriteOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Tone, later.Tone)
		lay(&o.Formality, later.Formality)
		lay(&o.NoPreserveFacts, later.NoPreserveFacts)
		lay(&o.Audience, later.Audience)
		lay(&o.StyleGuide, later.StyleGuide)
		laySlice(&o.Changes, later.Changes)
		laySlice(&o.AvoidWords, later.AvoidWords)
		laySlice(&o.IncludeWords, later.IncludeWords)
		return o
	})
}

// checkRewriteOptions returns the internal error of a setting no rewrite can
// use, or nil. op is the operation the error names: the function that was called.
func checkRewriteOptions(op string, o RewriteOptions) error {
	if err := o.common().Check(op); err != nil {
		return err
	}
	return checkWholeRange(op, "Formality", o.Formality, 1, 10)
}

// rewriteInternal builds the internal options of a rewrite on ctx: the
// constructor's value, then each setting that is not its zero value.
func rewriteInternal(ctx context.Context, o RewriteOptions) ops.RewriteOptions {
	opts := ops.NewRewriteOptions()
	lay(&opts.TargetTone, o.Tone)
	lay(&opts.FormalityLevel, o.Formality)
	turnOff(&opts.PreserveFacts, o.NoPreserveFacts)
	lay(&opts.Audience, o.Audience)
	lay(&opts.StyleGuide, o.StyleGuide)
	laySlice(&opts.Changes, o.Changes)
	laySlice(&opts.AvoidWords, o.AvoidWords)
	laySlice(&opts.IncludeWords, o.IncludeWords)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// rewrittenOf is the rewrite of the internal call as the root declares it. The
// internal metadata map is never written for a rewrite and is not carried over.
// The zero rewrite, which the internal call returns beside an error, converts to
// the zero rewrite.
func rewrittenOf(r ops.Rewritten) Rewritten {
	return Rewritten{Text: r.Text, ChangesMade: r.ChangesMade, ToneAchieved: r.ToneAchieved, ModelConfidence: r.ModelConfidence}
}

// Rewrite returns a text rewritten as the options say. A word in AvoidWords that
// appears, or one in IncludeWords that does not, is an error of kind
// ErrInvalidAnswer. The model's text is returned trimmed, and an empty answer is
// an error. Default tier Fast, mode Transform.
//
//	formal, err := schemaflux.Rewrite(ctx, note, schemaflux.RewriteOptions{Tone: "formal"})
//
// The Record of the call is "rewrite". The model's text is not a declared shape,
// so the answer is delivered at ContractPromptOnly: a client whose DataPolicy
// sets any floor refuses the call with ErrPolicy before anything is sent.
// RewriteDetail asks for more and is held to more.
func Rewrite(ctx context.Context, text string, opts ...RewriteOptions) (string, error) {
	call, err := bind.Begin(ctx, "rewrite")
	if err != nil {
		return "", err
	}
	o := mergeRewriteOptions(opts)
	if err := checkRewriteOptions("rewrite", o); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		rewritten, err := ops.Rewrite(text, rewriteInternal(call.Context(), o))
		return rewritten, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractPromptOnly, func(ctx context.Context) (string, error) {
		return ops.Rewrite(text, rewriteInternal(ctx, o))
	})
}

// RewriteDetail asks for the rewrite together with the changes made, the tone
// achieved and the model's confidence. It is a different request from Rewrite,
// and the model is sent the same one in every state of the call. The word lists
// are checked as Rewrite checks them.
//
//	r, err := schemaflux.RewriteDetail(ctx, note, schemaflux.RewriteOptions{AvoidWords: []string{"synergy"}})
//
// The Record of the call is "rewritedetail", delivered at ContractJSONWellFormed.
func RewriteDetail(ctx context.Context, text string, opts ...RewriteOptions) (Rewritten, error) {
	var zero Rewritten
	call, err := bind.Begin(ctx, "rewritedetail")
	if err != nil {
		return zero, err
	}
	o := mergeRewriteOptions(opts)
	if err := checkRewriteOptions("rewritedetail", o); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("rewrite")
	res, err := ops.RewriteResult(text, rewriteInternal(call.Context(), o))
	return rewrittenOf(res.Value), call.End(&res.Meta, err)
}
