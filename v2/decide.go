package schemaflux

import (
	"context"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Judged is an answer together with the model's own confidence in it. The
// confidence is a claim, not a measurement: it is not calibrated and not
// comparable across models or prompts.
type Judged[T any] struct {
	// Answer is the answer, as the plain form of the decision returns it.
	Answer T

	// ModelConfidence is the model's own claim, from 0 to 1.
	ModelConfidence float64
}

// judgedOf is the answer of an internal decision as the root declares it. The two
// have the same fields, so the conversion copies nothing.
func judgedOf[T any](j ops.Judged[T]) Judged[T] { return Judged[T](j) }

// Is answers a yes-or-no question about a subject. The subject is any value that
// can be written as JSON; text is the usual one. An answer that is not a plain
// true or false is an error, never a "no". Default tier Fast.
//
//	spam, err := schemaflux.Is(ctx, message, "Is this message spam?")
//
// A blank question is an error of kind ErrInvalidRequest, returned before
// anything is sent. When the client's provider is a decision provider (put in
// front of a model with mw.Decisions) the question goes to it as a typed
// decision; otherwise the model is asked and its answer is read against a
// declared schema. The Record of the call is "is", delivered at
// ContractSchemaAndInvariantChecked either way, and with a Recorder whose
// Decisions is set it says what was asked and which provider answered.
func Is(ctx context.Context, subject any, question string, opts ...Options) (bool, error) {
	call, err := bind.Begin(ctx, "is")
	if err != nil {
		return false, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return false, err
	}
	if err := o.common().Check("is"); err != nil {
		return false, call.End(nil, err)
	}
	if call.Plain() {
		answer, err := ops.Is(call.Context(), subject, question, decisionOptions(o))
		return answer, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (bool, error) {
		return ops.Is(ctx, subject, question, decisionOptions(o))
	})
}

// IsDetail is Is, asking the model for its confidence as well. It is a
// different request from Is: the model is asked to report how sure it is, and
// the answer is held to the same checks. An answer without a confidence is an
// error of kind ErrInvalidAnswer, which includes one from a decision provider
// that reported no probability. The Record of the call is "isdetail".
//
//	j, err := schemaflux.IsDetail(ctx, message, "Is this message spam?")
//	if j.ModelConfidence < 0.6 { escalate() }
func IsDetail(ctx context.Context, subject any, question string, opts ...Options) (Judged[bool], error) {
	var zero Judged[bool]
	call, err := bind.Begin(ctx, "isdetail")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return zero, err
	}
	if err := o.common().Check("isdetail"); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("is")
	res, err := ops.IsResult(call.Context(), subject, question, decisionOptions(o))
	return judgedOf(res.Value), call.End(&res.Meta, err)
}

// Which answers a question by choosing one of the labels, and returns that
// label as it was given. The answer is always one of the labels: an answer that
// names none of them is an error of kind ErrInvalidAnswer, never the first
// label. Default tier Fast.
//
//	team, err := schemaflux.Which(ctx, message, "Which team should handle this?",
//		[]string{"billing", "technical", "shipping"})
//
// A blank question, no labels, one label (a choice needs two), and a label that
// is blank or the same as another once the space around it is trimmed are each
// an error of kind ErrInvalidRequest, returned before anything is sent. So many
// labels that one answer cannot be declared for them (a few hundred) is an error
// of kind ErrConfiguration. The labels are the caller's own and are not changed.
//
// When the client's provider is a decision provider (put in front of a model
// with mw.Decisions) the question goes to it as a typed choice among the
// labels; otherwise the model is asked and its answer is read against a
// declared schema. The Record of the call is "which", delivered at
// ContractSchemaAndInvariantChecked either way, and with a Recorder whose
// Decisions is set it says what was asked and which provider answered.
func Which(ctx context.Context, subject any, question string, labels []string, opts ...Options) (string, error) {
	call, err := bind.Begin(ctx, "which")
	if err != nil {
		return "", err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return "", err
	}
	if err := refuseChoice(&call, labels); err != nil {
		return "", err
	}
	if err := o.common().Check("which"); err != nil {
		return "", call.End(nil, err)
	}
	if call.Plain() {
		answer, err := ops.Which(call.Context(), subject, question, labels, decisionOptions(o))
		return answer, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (string, error) {
		return ops.Which(ctx, subject, question, labels, decisionOptions(o))
	})
}

// WhichDetail is Which, asking the model for its confidence as well. It is a
// different request from Which: the model is asked to report how sure it is,
// and the answer is held to the same checks. An answer without a confidence is
// an error of kind ErrInvalidAnswer, which includes one from a decision provider
// that reported no probability. The Record of the call is "whichdetail".
//
//	j, err := schemaflux.WhichDetail(ctx, message, "Which team should handle this?", teams)
//	if j.ModelConfidence < 0.6 { escalate() }
func WhichDetail(ctx context.Context, subject any, question string, labels []string, opts ...Options) (Judged[string], error) {
	var zero Judged[string]
	call, err := bind.Begin(ctx, "whichdetail")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return zero, err
	}
	if err := refuseChoice(&call, labels); err != nil {
		return zero, err
	}
	if err := o.common().Check("whichdetail"); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("which")
	res, err := ops.WhichResult(call.Context(), subject, question, labels, decisionOptions(o))
	return judgedOf(res.Value), call.End(&res.Meta, err)
}

// WhichOrNone is Which with "none of these" as an answer the model can give. It
// returns the label as it was given and true, or "" and false. With no labels
// the answer is none and nothing is asked. A label of "none of these", in any
// letter case, is an error of kind ErrConfiguration: the model would be shown
// two options that read alike. Default tier Fast.
//
//	ticket, found, err := schemaflux.WhichOrNone(ctx, message,
//		"Which open ticket is this a follow-up to?", summaries)
//	if !found { openNew() }
//
// A blank question, and a label that is blank or the same as another once the
// space around it is trimmed, are each an error of kind ErrInvalidRequest,
// returned before anything is sent; so is a blank question when there are no
// labels. The most labels one answer can be declared for is one fewer than Which
// takes, for the option that is added; more is an error of kind
// ErrConfiguration. A call with no labels still adds its Record, with no request
// counted. The Record of the call is "whichornone", and otherwise it is Which's.
func WhichOrNone(ctx context.Context, subject any, question string, labels []string, opts ...Options) (string, bool, error) {
	call, err := bind.Begin(ctx, "whichornone")
	if err != nil {
		return "", false, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return "", false, err
	}
	if err := refuseUnusableLabels(&call, labels); err != nil {
		return "", false, err
	}
	if err := o.common().Check("whichornone"); err != nil {
		return "", false, call.End(nil, err)
	}
	if call.Plain() {
		label, found, err := ops.WhichOrNone(call.Context(), subject, question, labels, decisionOptions(o))
		return label, found, call.End(nil, err)
	}
	var found bool
	label, err := bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (string, error) {
		label, picked, err := ops.WhichOrNone(ctx, subject, question, labels, decisionOptions(o))
		found = picked
		return label, err
	})
	return label, found, err
}

// WhichOrNoneDetail is WhichOrNone, asking the model for its confidence as well.
// The label is the Judged's Answer; the bool is whether one was found. With no
// labels nothing is asked and the confidence is zero. It is a different request
// from WhichOrNone, with the same checks, and an answer without a confidence is
// an error of kind ErrInvalidAnswer. The Record of the call is
// "whichornonedetail".
//
//	j, found, err := schemaflux.WhichOrNoneDetail(ctx, message, "Which open ticket is this a follow-up to?", summaries)
func WhichOrNoneDetail(ctx context.Context, subject any, question string, labels []string, opts ...Options) (Judged[string], bool, error) {
	var zero Judged[string]
	call, err := bind.Begin(ctx, "whichornonedetail")
	if err != nil {
		return zero, false, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return zero, false, err
	}
	if err := refuseUnusableLabels(&call, labels); err != nil {
		return zero, false, err
	}
	if err := o.common().Check("whichornonedetail"); err != nil {
		return zero, false, call.End(nil, err)
	}
	call.Form("which")
	res, err := ops.WhichOrNoneResult(call.Context(), subject, question, labels, decisionOptions(o))
	return Judged[string]{Answer: res.Value.Answer.Label, ModelConfidence: res.Value.ModelConfidence}, res.Value.Answer.Found, call.End(&res.Meta, err)
}

// Rate answers a question with a whole number from low to high, both included.
// An answer off the scale is an error of kind ErrInvalidAnswer, never the nearest
// end. A scale has at most 100 levels. Default tier Fast.
//
//	urgency, err := schemaflux.Rate(ctx, message, "How urgent is this?", 1, 5)
//
// A blank question, and a scale with nothing to choose (low not below high) or
// with more than 100 levels, are each an error of kind ErrInvalidRequest,
// returned before anything is sent. When the client's provider is a decision
// provider the question goes to it as a typed level on the scale. The Record of
// the call is "rate", delivered at ContractSchemaAndInvariantChecked.
func Rate(ctx context.Context, subject any, question string, low, high int, opts ...Options) (int, error) {
	call, err := bind.Begin(ctx, "rate")
	if err != nil {
		return 0, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return 0, err
	}
	if err := refuseScale(&call, low, high); err != nil {
		return 0, err
	}
	if err := o.common().Check("rate"); err != nil {
		return 0, call.End(nil, err)
	}
	if call.Plain() {
		answer, err := ops.Rate(call.Context(), subject, question, low, high, decisionOptions(o))
		return answer, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (int, error) {
		return ops.Rate(ctx, subject, question, low, high, decisionOptions(o))
	})
}

// RateDetail is Rate, asking the model for its confidence as well. It is a
// different request from Rate, with the same checks, and an answer without a
// confidence is an error of kind ErrInvalidAnswer, which includes one from a
// decision provider that reported no probability. The Record of the call is
// "ratedetail".
//
//	j, err := schemaflux.RateDetail(ctx, message, "How urgent is this?", 1, 5)
func RateDetail(ctx context.Context, subject any, question string, low, high int, opts ...Options) (Judged[int], error) {
	var zero Judged[int]
	call, err := bind.Begin(ctx, "ratedetail")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return zero, err
	}
	if err := refuseScale(&call, low, high); err != nil {
		return zero, err
	}
	if err := o.common().Check("ratedetail"); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("rate")
	res, err := ops.RateResult(call.Context(), subject, question, low, high, decisionOptions(o))
	return judgedOf(res.Value), call.End(&res.Meta, err)
}

// Assess answers several questions about one subject in one call and returns
// them as a T, which has to be a struct. Each exported field of T asks a question
// in its `ask` tag, and the field's type says what kind of answer it takes: a
// bool is a yes or no, a string with a `oneof` tag chooses among the labels in
// it, separated by commas, and an integer with a `scale` tag ("1-5") is a rating.
// Default tier Fast.
//
//	type Triage struct {
//		Spam    bool   `ask:"Is this message spam?"`
//		Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
//		Urgency int    `ask:"How urgent is it?" scale:"1-5"`
//	}
//	triage, err := schemaflux.Assess[Triage](ctx, message)
//
// It is one request where Is, Which and Rate would be three. Every question has
// to be answered: an answer that leaves one out, or gives one of the wrong kind
// or off its labels or scale, is an error of kind ErrInvalidAnswer, and no field
// is filled: the T returned beside an error is the zero value, never a partly
// filled one. A field tagged `ask:"-"` is left alone, and a struct embedded by
// value asks its own questions as if they were written in T.
//
// A T that cannot be asked about (not a struct; a field with no question, or
// with a `oneof` or `scale` its type cannot take; no question at all) is an
// error of kind ErrConfiguration, returned before anything is sent. The Record
// of the call is "assess", delivered at ContractSchemaAndInvariantChecked.
func Assess[T any](ctx context.Context, subject any, opts ...Options) (T, error) {
	var zero T
	call, err := bind.Begin(ctx, "assess")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := o.common().Check("assess"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		answer, err := ops.Assess[T](call.Context(), subject, decisionOptions(o))
		return answer, call.End(nil, err)
	}
	return bind.Recorded(&call, types.ContractSchemaAndInvariantChecked, func(ctx context.Context) (T, error) {
		return ops.Assess[T](ctx, subject, decisionOptions(o))
	})
}

// AssessDetail is Assess, asking the model for its confidence in its answers
// taken together as well. It is a different request from Assess, with the same
// checks, and an answer without a confidence is an error of kind
// ErrInvalidAnswer. The Record of the call is "assessdetail".
//
//	j, err := schemaflux.AssessDetail[Triage](ctx, message)
func AssessDetail[T any](ctx context.Context, subject any, opts ...Options) (Judged[T], error) {
	var zero Judged[T]
	call, err := bind.Begin(ctx, "assessdetail")
	if err != nil {
		return zero, err
	}
	o := mergeOptions(opts)
	if err := o.common().Check("assessdetail"); err != nil {
		return zero, call.End(nil, err)
	}
	call.Form("assess")
	res, err := ops.AssessResult[T](call.Context(), subject, decisionOptions(o))
	return judgedOf(res.Value), call.End(&res.Meta, err)
}

// widestScale is the most levels a scale of Rate may have, which is what the
// declared answer of a decision can hold for a rating.
const widestScale = 100

// largestLabelListCompared is how many labels refuseUnusableLabels compares
// pairwise, without a map: a list of that length is checked in no more than a
// few thousand comparisons, and a shorter one allocates nothing.
const largestLabelListCompared = 32

// refuseChoice ends the call with an error of kind ErrInvalidRequest when labels
// cannot be chosen between: none, one, or a label that is blank or repeated. It
// returns nil otherwise.
func refuseChoice(call *bind.Call, labels []string) error {
	if err := refuseEmpty(call, "labels", len(labels)); err != nil {
		return err
	}
	if len(labels) == 1 {
		return call.Refuse(types.KindInvalidRequest, "labels has one label, and a choice needs two")
	}
	return refuseUnusableLabels(call, labels)
}

// refuseUnusableLabels ends the call with an error of kind ErrInvalidRequest when
// a label is blank or repeats an earlier one (labels are compared without the
// space around them, as the internal check compares them), and returns nil
// otherwise. The messages give the position of the label and never the label.
func refuseUnusableLabels(call *bind.Call, labels []string) error {
	if len(labels) > largestLabelListCompared {
		seen := make(map[string]bool, len(labels))
		for i, label := range labels {
			trimmed := strings.TrimSpace(label)
			switch {
			case trimmed == "":
				return call.Refuse(types.KindInvalidRequest, "label %d is blank", i+1)
			case seen[trimmed]:
				return call.Refuse(types.KindInvalidRequest, "label %d repeats an earlier one", i+1)
			}
			seen[trimmed] = true
		}
		return nil
	}
	for i, label := range labels {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" {
			return call.Refuse(types.KindInvalidRequest, "label %d is blank", i+1)
		}
		for _, earlier := range labels[:i] {
			if strings.TrimSpace(earlier) == trimmed {
				return call.Refuse(types.KindInvalidRequest, "label %d repeats an earlier one", i+1)
			}
		}
	}
	return nil
}

// refuseScale ends the call with an error of kind ErrInvalidRequest when the
// scale of a rating leaves nothing to choose or has more levels than a rating
// may. The width is worked out unsigned: high minus low wraps for a scale as wide
// as the integers, and a wrapped width would pass for a narrow one.
func refuseScale(call *bind.Call, low, high int) error {
	if high <= low {
		return call.Refuse(types.KindInvalidRequest, "the scale runs from %d to %d, which leaves nothing to choose", low, high)
	}
	if uint64(high)-uint64(low) >= widestScale {
		return call.Refuse(types.KindInvalidRequest, "the scale runs from %d to %d, which is more than the %d levels a scale may have", low, high, widestScale)
	}
	return nil
}
