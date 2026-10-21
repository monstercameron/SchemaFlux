package schemaflux

import (
	"context"
	"math"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// AnnotateOptions are Annotate's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: AnnotateOptions{Mode: ModeStrict, Types: []string{"entities"}}.
type AnnotateOptions struct {
	Options

	// Types are the kinds of annotation wanted: "entities", "sentiment", "topics",
	// "keywords", "intent", "language", or a word of the caller's own. Nil means
	// entities and sentiment; a non-nil empty slice means none, for a call that
	// uses only CustomSchema. A call with no kind and no CustomSchema is a
	// configuration error.
	Types []string

	// CustomSchema describes kinds of the caller's own, keyed by name. Nil means
	// none.
	CustomSchema map[string]string

	// OmitConfidence leaves the per-annotation confidence out of the request.
	// False asks for it.
	OmitConfidence bool

	// MinConfidence drops an annotation the model is less sure of. Zero means 0.5;
	// a negative value keeps every annotation. A value above 1, or one that is not
	// a number, is a configuration error. It has no effect when OmitConfidence is
	// set.
	MinConfidence float64

	// Language is the language of the input. Empty means the model detects it.
	Language string

	// Domain names the subject area. Empty means none.
	Domain string
}

// AnnotateResult is a text with what was found in it.
type AnnotateResult struct {
	// Original is the input as it was annotated.
	Original string `json:"original"`

	// Annotations are the annotations kept, in the order the model gave them.
	Annotations []Annotation `json:"annotations"`

	// ByType is the same annotations keyed by Type.
	ByType map[string][]Annotation `json:"by_type"`

	// Summary is the model's own overall remarks (sentiment, topics, counts), as
	// it wrote them.
	Summary map[string]any `json:"summary,omitempty"`
}

// Annotation is one thing the model found in the input.
type Annotation struct {
	// Type is the kind of annotation.
	Type string `json:"type"`

	// Value is what was found: a name, a sentiment, a topic.
	Value string `json:"value"`

	// Text is the span of the input it refers to, when there is one.
	Text string `json:"text,omitempty"`

	// Start is the offset in characters of the input where the span starts. The
	// span is inside the input: an answer with one outside it is refused.
	Start int `json:"start,omitempty"`

	// End is the offset in characters of the input where the span ends, after its
	// last character.
	End int `json:"end,omitempty"`

	// ModelConfidence is the model's own claim, when it gave one. It is zero when
	// OmitConfidence was set and the model gave none. It is not a measurement.
	ModelConfidence float64 `json:"confidence,omitempty"`

	// Attributes is whatever else the model attached, as it wrote it. It is
	// encoded as "metadata", as the same member of the result of version 1 is.
	Attributes map[string]any `json:"metadata,omitempty"`
}

// mergeAnnotateOptions merges the values of a call (M0).
func mergeAnnotateOptions(opts []AnnotateOptions) AnnotateOptions {
	return mergeValues(opts, func(o, later AnnotateOptions) AnnotateOptions {
		o.Options = o.Options.merge(later.Options)
		laySlice(&o.Types, later.Types)
		layMap(&o.CustomSchema, later.CustomSchema)
		lay(&o.OmitConfidence, later.OmitConfidence)
		lay(&o.MinConfidence, later.MinConfidence)
		lay(&o.Language, later.Language)
		lay(&o.Domain, later.Domain)
		return o
	})
}

// checkAnnotateOptions returns the internal error of a setting no call can use,
// or nil. The number is checked on the caller's own value, before the meaning
// of its zero and of a negative is applied.
func checkAnnotateOptions(o AnnotateOptions) error {
	if err := o.common().Check("annotate"); err != nil {
		return err
	}
	return checkFloat("annotate", "MinConfidence", o.MinConfidence, math.Inf(-1), 1)
}

// annotateInternal builds the internal options of an Annotate call on ctx.
func annotateInternal(ctx context.Context, o AnnotateOptions) ops.AnnotateOptions {
	opts := ops.NewAnnotateOptions()
	laySlice(&opts.AnnotationTypes, o.Types)
	layMap(&opts.CustomSchema, o.CustomSchema)
	turnOff(&opts.IncludeConfidence, o.OmitConfidence)
	switch {
	case o.MinConfidence > 0:
		opts.MinConfidence = o.MinConfidence
	case o.MinConfidence < 0:
		opts.MinConfidence = 0
	}
	lay(&opts.Language, o.Language)
	lay(&opts.Domain, o.Domain)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// annotateResult is the result of the internal call as the root declares it.
// The annotations are converted once and the map by type is built from the
// converted ones. The zero result, which the internal call returns beside an
// error, converts to the zero result.
func annotateResult(res ops.AnnotateResult) AnnotateResult {
	out := AnnotateResult{Original: res.Original, Summary: res.Summary}
	if res.Annotations != nil {
		out.Annotations = make([]Annotation, len(res.Annotations))
		for i, a := range res.Annotations {
			out.Annotations[i] = Annotation{
				Type: a.Type, Value: a.Value, Text: a.Text, Start: a.Start, End: a.End,
				ModelConfidence: a.ModelConfidence, Attributes: a.Metadata,
			}
		}
	}
	if res.ByType != nil {
		out.ByType = make(map[string][]Annotation, len(res.ByType))
		for _, a := range out.Annotations {
			out.ByType[a.Type] = append(out.ByType[a.Type], a)
		}
	}
	return out
}

// Annotate marks up a value: entities, sentiment, topics, keywords, or kinds the
// caller describes. Default tier Fast, mode Transform.
//
//	notes, err := schemaflux.Annotate(ctx, article)
//	notes, err := schemaflux.Annotate(ctx, article, schemaflux.AnnotateOptions{Types: []string{"entities"}})
//	for _, a := range notes.ByType["entities"] { fmt.Println(a.Value) }
//
// An answer without the list of annotations, one that leaves out a member of an
// annotation, a span outside the input, and a confidence outside 0 to 1 when it
// was asked for are errors of kind ErrInvalidAnswer. An annotation the model is
// less sure of than MinConfidence is left out of the result, not refused.
//
// The Record of the call is "annotate", delivered at ContractJSONWellFormed: the
// answer is parsed and checked, but no schema is declared to the provider.
func Annotate[T any](ctx context.Context, input T, opts ...AnnotateOptions) (AnnotateResult, error) {
	var zero AnnotateResult
	call, err := bind.Begin(ctx, "annotate")
	if err != nil {
		return zero, err
	}
	o := mergeAnnotateOptions(opts)
	if err := checkAnnotateOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Annotate(input, annotateInternal(call.Context(), o))
		return annotateResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.AnnotateResult, error) {
		return ops.Annotate(input, annotateInternal(ctx, o))
	})
	return annotateResult(res), err
}

// ExplainOptions are Explain's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: ExplainOptions{Mode: ModeStrict, Audience: "executive", Depth: 1}.
type ExplainOptions struct {
	Options

	// Audience is "technical", "non-technical", "children", "executive",
	// "beginner" or "expert". Empty means "non-technical". Any other value is a
	// configuration error.
	Audience string

	// Depth is 1 (high level) to 4 (comprehensive). Zero means 2. A value outside
	// 1 to 4 is a configuration error.
	Depth int

	// Format is "paragraph", "bullet-points", "step-by-step", "qa" or
	// "structured". Empty means "paragraph". Any other value is a configuration
	// error.
	Format string

	// Focus is "overview", "usage", "implementation", "benefits", "limitations" or
	// "examples". Empty means "overview". Any other value is a configuration
	// error.
	Focus string

	// Background is prose the model should take into account. Empty means none.
	Background string
}

// ExplainResult is an explanation and what was worked out about the value it
// explains.
type ExplainResult struct {
	// Explanation is the explanation.
	Explanation string `json:"explanation"`

	// Summary is a short overview.
	Summary string `json:"summary"`

	// KeyPoints are the points to remember.
	KeyPoints []string `json:"key_points"`

	// DataType is the Go type of the value explained, worked out here.
	DataType string `json:"data_type"`

	// FieldCount is the value's number of fields (of a slice or a map, of items),
	// worked out here. It is 1 for anything else.
	FieldCount int `json:"field_count"`

	// DataComplexity is "low", "medium" or "high": from the field count for a
	// struct, "medium" for a slice or a map, and "low" for anything else. It is
	// worked out here.
	DataComplexity string `json:"data_complexity"`
}

// mergeExplainOptions merges the values of a call (M0).
func mergeExplainOptions(opts []ExplainOptions) ExplainOptions {
	return mergeValues(opts, func(o, later ExplainOptions) ExplainOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.Audience, later.Audience)
		lay(&o.Depth, later.Depth)
		lay(&o.Format, later.Format)
		lay(&o.Focus, later.Focus)
		lay(&o.Background, later.Background)
		return o
	})
}

// checkExplainOptions returns the internal error of a setting no call can use,
// or nil. The words are not repeated in the message: the error goes wherever the
// caller's logs go.
func checkExplainOptions(o ExplainOptions) error {
	if err := o.common().Check("explain"); err != nil {
		return err
	}
	if o.Depth < 0 || o.Depth > 4 {
		return types.Newf(types.KindConfiguration, "explain", "Depth must be from 1 to 4")
	}
	switch o.Audience {
	case "", "technical", "non-technical", "children", "executive", "beginner", "expert":
	default:
		return types.Newf(types.KindConfiguration, "explain", "Audience is not one of technical, non-technical, children, executive, beginner and expert")
	}
	switch o.Format {
	case "", "paragraph", "bullet-points", "step-by-step", "qa", "structured":
	default:
		return types.Newf(types.KindConfiguration, "explain", "Format is not one of paragraph, bullet-points, step-by-step, qa and structured")
	}
	switch o.Focus {
	case "", "overview", "usage", "implementation", "benefits", "limitations", "examples":
	default:
		return types.Newf(types.KindConfiguration, "explain", "Focus is not one of overview, usage, implementation, benefits, limitations and examples")
	}
	return nil
}

// explainInternal builds the internal options of an Explain call on ctx.
func explainInternal(ctx context.Context, o ExplainOptions) ops.ExplainOptions {
	opts := ops.NewExplainOptions()
	lay(&opts.Audience, o.Audience)
	lay(&opts.Depth, o.Depth)
	lay(&opts.Format, o.Format)
	lay(&opts.Focus, o.Focus)
	lay(&opts.Background, o.Background)
	o.common().Apply(&opts.OpOptions)
	opts.OpOptions.Context = ctx
	return opts
}

// explainResult is the result of the internal call as the root declares it. The
// three worked-out fields are read from the internal result's metadata. The zero
// result, which the internal call returns beside an error, converts to the zero
// result.
func explainResult(res ops.ExplainResult) ExplainResult {
	out := ExplainResult{Explanation: res.Explanation, Summary: res.Summary, KeyPoints: res.KeyPoints}
	if dataType, ok := res.Metadata["data_type"].(string); ok {
		out.DataType = dataType
	}
	if fieldCount, ok := res.Metadata["field_count"].(int); ok {
		out.FieldCount = fieldCount
	}
	if complexity, ok := res.Metadata["estimated_complexity"].(string); ok {
		out.DataComplexity = complexity
	}
	return out
}

// Explain writes an explanation of a value for a reader. Default tier Fast, mode
// Transform.
//
//	ex, err := schemaflux.Explain(ctx, queryPlan)
//	ex, err := schemaflux.Explain(ctx, queryPlan, schemaflux.ExplainOptions{Audience: "executive", Depth: 1})
//	fmt.Println(ex.Explanation)
//
// A nil value, and a value JSON cannot write, are errors returned before
// anything is sent. An answer that is not JSON, or that leaves out the
// explanation, the summary or the key points, or whose explanation is blank, is
// an error of kind ErrInvalidAnswer.
//
// The Record of the call is "explain", delivered at ContractJSONWellFormed: the
// answer is parsed and checked, but no schema is declared to the provider.
func Explain(ctx context.Context, data any, opts ...ExplainOptions) (ExplainResult, error) {
	var zero ExplainResult
	call, err := bind.Begin(ctx, "explain")
	if err != nil {
		return zero, err
	}
	o := mergeExplainOptions(opts)
	if err := checkExplainOptions(o); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Explain(data, explainInternal(call.Context(), o))
		return explainResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.ExplainResult, error) {
		return ops.Explain(data, explainInternal(ctx, o))
	})
	return explainResult(res), err
}

// QuestionOptions are Question's settings. The zero value is the default.
//
// The common settings of Options come first and are set without naming the
// embedded type: QuestionOptions{Mode: ModeStrict, OmitReasoning: true}.
type QuestionOptions struct {
	Options

	// OmitEvidence leaves the supporting evidence out of the request. False asks
	// for it.
	OmitEvidence bool

	// OmitConfidence leaves the model's confidence out of the request. False asks
	// for it.
	OmitConfidence bool

	// OmitReasoning leaves the reasoning out of the request. False asks for it.
	OmitReasoning bool
}

// QuestionResult is the answer to a question about a value.
type QuestionResult[A any] struct {
	// Answer is the answer.
	Answer A `json:"answer"`

	// ModelConfidence is the model's own claim, from 0 to 1. It is not a
	// measurement. It is zero when OmitConfidence was set.
	ModelConfidence float64 `json:"confidence,omitempty"`

	// Reasoning is how the model says it arrived at the answer. It is empty when
	// OmitReasoning was set.
	Reasoning string `json:"reasoning,omitempty"`

	// Evidence is what in the value supports the answer. It is nil when
	// OmitEvidence was set.
	Evidence []string `json:"evidence,omitempty"`
}

// mergeQuestionOptions merges the values of a call (M0).
func mergeQuestionOptions(opts []QuestionOptions) QuestionOptions {
	return mergeValues(opts, func(o, later QuestionOptions) QuestionOptions {
		o.Options = o.Options.merge(later.Options)
		lay(&o.OmitEvidence, later.OmitEvidence)
		lay(&o.OmitConfidence, later.OmitConfidence)
		lay(&o.OmitReasoning, later.OmitReasoning)
		return o
	})
}

// questionInternal builds the internal options of a Question call on ctx.
func questionInternal(ctx context.Context, question string, o QuestionOptions) ops.QuestionOptions {
	opts := ops.NewQuestionOptions(question)
	turnOff(&opts.IncludeEvidence, o.OmitEvidence)
	turnOff(&opts.IncludeConfidence, o.OmitConfidence)
	turnOff(&opts.IncludeReasoning, o.OmitReasoning)
	settleHalves(ctx, o.common(), &opts.CommonOptions, &opts.OpOptions)
	return opts
}

// questionResult is the result of the internal call as the root declares it. The
// zero result, which the internal call returns beside an error, converts to the
// zero result.
func questionResult[A any](res ops.QuestionResult[A]) QuestionResult[A] {
	return QuestionResult[A]{Answer: res.Answer, ModelConfidence: res.ModelConfidence, Reasoning: res.Reasoning, Evidence: res.Evidence}
}

// Question answers a question about a value, as an A: a string, a number, a
// struct. A is the type asked for; the value's type is inferred. Default tier
// Fast, mode Transform.
//
//	res, err := schemaflux.Question[string](ctx, report, "What is the main finding?")
//	total, err := schemaflux.Question[float64](ctx, invoice, "What is the total with tax?")
//	fmt.Println(res.Answer, res.Evidence)
//
// A blank question is an error of kind ErrInvalidRequest. An answer that is
// missing, null or empty, that does not fit A, that leaves out a member that
// was asked for, or whose confidence is outside 0 to 1, is an error of kind
// ErrInvalidAnswer.
//
// The Record of the call is "question". It is delivered at
// ContractSchemaConstrained when a schema can be built for A, which is declared
// to the provider, and at ContractJSONWellFormed when it cannot (a map, a
// recursive type, an interface, a type nested deeper than six levels): no schema
// is sent then and the answer is only read as JSON. A floor above the level of A
// refuses the call before anything is sent.
func Question[A any, T any](ctx context.Context, data T, question string, opts ...QuestionOptions) (QuestionResult[A], error) {
	var zero QuestionResult[A]
	call, err := bind.Begin(ctx, "question")
	if err != nil {
		return zero, err
	}
	o := mergeQuestionOptions(opts)
	if err := refuseBlank(&call, "question", question); err != nil {
		return zero, err
	}
	if err := o.common().Check("question"); err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Question[T, A](data, questionInternal(call.Context(), question, o))
		return questionResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, ops.ContractLevelFor[A]("question"), func(ctx context.Context) (ops.QuestionResult[A], error) {
		return ops.Question[T, A](data, questionInternal(ctx, question, o))
	})
	return questionResult(res), err
}
