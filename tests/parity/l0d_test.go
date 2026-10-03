package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The first six functions of version 2: Extract, Classify, Summarize,
// SummarizeDetail, Is and IsDetail, each against the old root's function of the
// same request. Each case is one pair; the common settings are one table that
// every function runs.

// commonCase is one of the seven common settings, as version 2 sets it and as the
// old root sets the same thing.
type commonCase struct {
	name string
	v2   schemaflux.Options

	// flat lays the old root's setting on an options type that has the two halves
	// (CommonOptions and the embedded OpOptions). The setters of the old root write
	// the CommonOptions half, except for the two settings only the embedded half
	// has.
	flat func(common *ops.CommonOptions, embedded *types.OpOptions)

	// decision lays it on the options of a decision, which are an OpOptions alone.
	decision func(o *types.OpOptions)
}

var commonCases = []commonCase{
	{"Model", schemaflux.Options{Model: pinnedModel},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Model = pinnedModel },
		func(o *types.OpOptions) { o.Model = pinnedModel }},
	{"TierSmart", schemaflux.Options{Tier: schemaflux.TierSmart},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Intelligence = types.Smart },
		func(o *types.OpOptions) { o.Intelligence = types.Smart }},
	{"TierQuick", schemaflux.Options{Tier: schemaflux.TierQuick},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Intelligence = types.Quick },
		func(o *types.OpOptions) { o.Intelligence = types.Quick }},
	{"Effort", schemaflux.Options{Effort: schemaflux.EffortLow},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Effort = types.Effort("low") },
		func(o *types.OpOptions) { o.Effort = types.Effort("low") }},
	{"ModeStrict", schemaflux.Options{Mode: schemaflux.ModeStrict},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Mode = types.Strict },
		func(o *types.OpOptions) { o.Mode = types.Strict }},
	{"ModeCreative", schemaflux.Options{Mode: schemaflux.ModeCreative},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Mode = types.Creative },
		func(o *types.OpOptions) { o.Mode = types.Creative }},
	{"Steering", schemaflux.Options{Steering: "answer plainly"},
		func(c *ops.CommonOptions, _ *types.OpOptions) { c.Steering = "answer plainly" },
		func(o *types.OpOptions) { o.Steering = "answer plainly" }},
	{"MaxOutputTokens", schemaflux.Options{MaxOutputTokens: 123},
		func(_ *ops.CommonOptions, e *types.OpOptions) { e.MaxOutputTokens = 123 },
		func(o *types.OpOptions) { o.MaxOutputTokens = 123 }},
	{"WebSearch", schemaflux.Options{WebSearch: true},
		func(_ *ops.CommonOptions, e *types.OpOptions) { e.WebSearch = true },
		func(o *types.OpOptions) { o.WebSearch = true }},
	{"every setting at once",
		schemaflux.Options{Model: pinnedModel, Tier: schemaflux.TierSmart, Effort: schemaflux.EffortHigh, Mode: schemaflux.ModeStrict, Steering: "answer plainly", MaxOutputTokens: 123, WebSearch: true},
		func(c *ops.CommonOptions, e *types.OpOptions) {
			c.Model, c.Intelligence, c.Effort, c.Mode, c.Steering = pinnedModel, types.Smart, types.Effort("high"), types.Strict, "answer plainly"
			e.MaxOutputTokens, e.WebSearch = 123, true
		},
		func(o *types.OpOptions) {
			o.Model, o.Intelligence, o.Effort, o.Mode, o.Steering = pinnedModel, types.Smart, types.Effort("high"), types.Strict, "answer plainly"
			o.MaxOutputTokens, o.WebSearch = 123, true
		}},
}

// ---- Extract ----

type parityInvoice struct {
	Number string  `json:"number"`
	Total  float64 `json:"total"`
}

const (
	extractInput  = "Invoice A-17 for 12.50, due on the first."
	extractAnswer = `{"number":"A-17","total":12.5}`
)

// extractCase is one Extract pair: the old root's options and v2's.
func extractCase(name string, old oldroot.ExtractOptions, v2 schemaflux.ExtractOptions) parityCase {
	return parityCase{
		name: "Extract/" + name, op: "extract", replies: []string{extractAnswer}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Extract[parityInvoice](extractInput, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Extract[parityInvoice](ctx, extractInput, v2)
			return err
		},
	}
}

func extractCases() []parityCase {
	hints := map[string]string{"total": "in euros", "number": "the invoice number"}
	rules := map[string]string{"total": "two decimals"}
	examples := []any{parityInvoice{Number: "Z-9", Total: 1}}
	base := oldroot.NewExtractOptions
	cases := []parityCase{
		extractCase("the default", base(), schemaflux.ExtractOptions{}),
		extractCase("SchemaHints", base().WithSchemaHints(hints), schemaflux.ExtractOptions{SchemaHints: hints}),
		extractCase("FieldRules", base().WithFieldRules(rules), schemaflux.ExtractOptions{FieldRules: rules}),
		extractCase("Examples", base().WithExamples(examples...), schemaflux.ExtractOptions{Examples: examples}),
		extractCase("StrictSchema", base().WithStrictSchema(true).WithAllowPartial(false), schemaflux.ExtractOptions{StrictSchema: true}),
		extractCase("StrictSchema with hints", base().WithStrictSchema(true).WithAllowPartial(false).WithSchemaHints(hints),
			schemaflux.ExtractOptions{StrictSchema: true, SchemaHints: hints}),
		extractCase("NoPartial with hints", base().WithAllowPartial(false).WithSchemaHints(hints),
			schemaflux.ExtractOptions{NoPartial: true, SchemaHints: hints}),
		extractCase("NoPartial alone", base().WithAllowPartial(false), schemaflux.ExtractOptions{NoPartial: true}),
		extractCase("a hint without NoPartial keeps the partial sentence", base().WithSchemaHints(hints), schemaflux.ExtractOptions{SchemaHints: hints}),
		extractCase("ExactFields", base().WithExactFields(), schemaflux.ExtractOptions{ExactFields: true}),
		extractCase("CompleteFields", base().WithCompleteFields(), schemaflux.ExtractOptions{CompleteFields: true}),
		extractCase("every own setting",
			base().WithSchemaHints(hints).WithFieldRules(rules).WithExamples(examples...).WithStrictSchema(true).WithAllowPartial(false).WithExactFields().WithCompleteFields(),
			schemaflux.ExtractOptions{SchemaHints: hints, FieldRules: rules, Examples: examples, StrictSchema: true, NoPartial: true, ExactFields: true, CompleteFields: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, extractCase(c.name, old, schemaflux.ExtractOptions{Options: c.v2}))
	}
	return cases
}

// ---- Classify ----

const classifyAnswer = `{"category":"billing","confidence":0.9,"alternatives":[{"category":"technical","confidence":0.3}],"reasoning":"a charge"}`

func classifyCase(name string, old oldroot.ClassifyOptions, v2 schemaflux.ClassifyOptions) parityCase {
	categories := []string{"billing", "technical"}
	return parityCase{
		name: "Classify/" + name, op: "classify", replies: []string{classifyAnswer}, floor: schemaflux.ContractJSONWellFormed,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Classify[string, string]("my card was charged twice", old.WithCategories(categories))
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Classify(ctx, "my card was charged twice", categories, v2)
			return err
		},
	}
}

func classifyCases() []parityCase {
	descriptions := map[string]string{"billing": "anything about money"}
	examples := map[string][]string{"technical": {"it crashed", "it froze"}}
	base := oldroot.NewClassifyOptions
	cases := []parityCase{
		classifyCase("the default", base(), schemaflux.ClassifyOptions{}),
		classifyCase("MinConfidence 0.8", base().WithMinConfidence(0.8), schemaflux.ClassifyOptions{MinConfidence: 0.8}),
		classifyCase("MinConfidence 0.2", base().WithMinConfidence(0.2), schemaflux.ClassifyOptions{MinConfidence: 0.2}),
		classifyCase("MinConfidence 0.9", base().WithMinConfidence(0.9), schemaflux.ClassifyOptions{MinConfidence: 0.9}),
		// A negative value means no floor, which the old root says as a floor of zero.
		classifyCase("MinConfidence negative is a floor of zero", base().WithMinConfidence(0), schemaflux.ClassifyOptions{MinConfidence: -1}),
		classifyCase("MinConfidence very negative", base().WithMinConfidence(0), schemaflux.ClassifyOptions{MinConfidence: -0.001}),
		classifyCase("Descriptions", withDescriptions(base(), descriptions), schemaflux.ClassifyOptions{Descriptions: descriptions}),
		classifyCase("Examples", withExamples(base(), examples), schemaflux.ClassifyOptions{Examples: examples}),
		classifyCase("every own setting", withExamples(withDescriptions(base().WithMinConfidence(0.7), descriptions), examples),
			schemaflux.ClassifyOptions{MinConfidence: 0.7, Descriptions: descriptions, Examples: examples}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, classifyCase(c.name, old, schemaflux.ClassifyOptions{Options: c.v2}))
	}
	return cases
}

func withDescriptions(o oldroot.ClassifyOptions, d map[string]string) oldroot.ClassifyOptions {
	o.CategoryDescriptions = d
	return o
}

func withExamples(o oldroot.ClassifyOptions, e map[string][]string) oldroot.ClassifyOptions {
	o.CategoryExamples = e
	return o
}

// ---- Summarize and SummarizeDetail ----

const (
	summarizeInput  = "The quarterly report shows revenue grew twelve percent while costs stayed flat."
	summarizeAnswer = "Revenue grew."
	summaryAnswer   = `{"text":"Revenue grew twelve.","key_points":["revenue up 12%"],"confidence":0.8}`
)

// summarizeSetting is one setting of SummarizeOptions, as version 2 sets it and
// as the old root sets it.
type summarizeSetting struct {
	name string
	v2   schemaflux.SummarizeOptions
	old  func(o *oldroot.SummarizeOptions)
}

func summarizeSettings() []summarizeSetting {
	settings := []summarizeSetting{
		{"the default", schemaflux.SummarizeOptions{}, func(*oldroot.SummarizeOptions) {}},
		{"TargetLength with the default unit", schemaflux.SummarizeOptions{TargetLength: 3}, func(o *oldroot.SummarizeOptions) { o.TargetLength = 3 }},
		{"TargetLength in words", schemaflux.SummarizeOptions{TargetLength: 3, LengthUnit: "words"}, func(o *oldroot.SummarizeOptions) { o.TargetLength, o.LengthUnit = 3, "words" }},
		{"TargetLength in paragraphs", schemaflux.SummarizeOptions{TargetLength: 1, LengthUnit: "paragraphs"}, func(o *oldroot.SummarizeOptions) { o.TargetLength, o.LengthUnit = 1, "paragraphs" }},
		{"LengthUnit with no target", schemaflux.SummarizeOptions{LengthUnit: "words"}, func(o *oldroot.SummarizeOptions) { o.LengthUnit = "words" }},
		{"Style", schemaflux.SummarizeOptions{Style: "executive"}, func(o *oldroot.SummarizeOptions) { o.Style = "executive" }},
		{"BulletPoints", schemaflux.SummarizeOptions{BulletPoints: true}, func(o *oldroot.SummarizeOptions) { o.BulletPoints = true }},
		{"BulletPoints with a style", schemaflux.SummarizeOptions{BulletPoints: true, Style: "executive"}, func(o *oldroot.SummarizeOptions) { o.BulletPoints, o.Style = true, "executive" }},
		{"FocusAreas", schemaflux.SummarizeOptions{FocusAreas: []string{"revenue", "costs"}}, func(o *oldroot.SummarizeOptions) { o.FocusAreas = []string{"revenue", "costs"} }},
		{"PreserveInfo", schemaflux.SummarizeOptions{PreserveInfo: []string{"dates", "names"}}, func(o *oldroot.SummarizeOptions) { o.PreserveInfo = []string{"dates", "names"} }},
		{"every own setting",
			schemaflux.SummarizeOptions{TargetLength: 3, LengthUnit: "words", Style: "executive", BulletPoints: true, FocusAreas: []string{"revenue"}, PreserveInfo: []string{"dates"}},
			func(o *oldroot.SummarizeOptions) {
				o.TargetLength, o.LengthUnit, o.Style, o.BulletPoints = 3, "words", "executive", true
				o.FocusAreas, o.PreserveInfo = []string{"revenue"}, []string{"dates"}
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, summarizeSetting{c.name, schemaflux.SummarizeOptions{Options: c.v2}, func(o *oldroot.SummarizeOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func summarizeCases() []parityCase {
	var cases []parityCase
	for _, s := range summarizeSettings() {
		cases = append(cases, parityCase{
			name: "Summarize/" + s.name, op: "summarize", replies: []string{summarizeAnswer},
			old: func(ctx context.Context) error {
				o := oldroot.NewSummarizeOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.Summarize(summarizeInput, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Summarize(ctx, summarizeInput, s.v2)
				return err
			},
		})
	}
	return cases
}

func summarizeDetailCases() []parityCase {
	var cases []parityCase
	for _, s := range summarizeSettings() {
		cases = append(cases, parityCase{
			name: "SummarizeDetail/" + s.name, op: "summarizedetail", replies: []string{summaryAnswer}, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				o := oldroot.NewSummarizeOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.SummarizeResult(summarizeInput, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.SummarizeDetail(ctx, summarizeInput, s.v2)
				return err
			},
		})
	}
	return cases
}

// ---- Is and IsDetail ----

const (
	isSubject  = "Please refund my order."
	isQuestion = "Is the customer asking for a refund?"
)

type isSubjectStruct struct {
	Body string `json:"body"`
}

// isSetting is one setting of the decisions' options, as version 2 sets it and as
// the old root sets it.
type isSetting struct {
	name string
	v2   schemaflux.Options
	old  func(o *types.OpOptions)
}

func isSettings() []isSetting {
	settings := []isSetting{{"the default", schemaflux.Options{}, func(*types.OpOptions) {}}}
	for _, c := range commonCases {
		settings = append(settings, isSetting{c.name, c.v2, c.decision})
	}
	return settings
}

func isCases() []parityCase {
	var cases []parityCase
	for _, s := range isSettings() {
		cases = append(cases, parityCase{
			name: "Is/" + s.name, op: "is", replies: []string{`{"q1":true}`}, floor: schemaflux.ContractSchemaAndInvariantChecked,
			old: func(ctx context.Context) error {
				var o types.OpOptions
				s.old(&o)
				_, err := oldroot.Is(ctx, isSubject, isQuestion, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Is(ctx, isSubject, isQuestion, s.v2)
				return err
			},
		})
	}
	cases = append(cases, parityCase{
		name: "Is/a struct for a subject", op: "is", replies: []string{`{"q1":false}`}, floor: schemaflux.ContractSchemaAndInvariantChecked,
		old: func(ctx context.Context) error {
			_, err := oldroot.Is(ctx, isSubjectStruct{Body: isSubject}, isQuestion)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Is(ctx, isSubjectStruct{Body: isSubject}, isQuestion)
			return err
		},
	})
	return cases
}

func isDetailCases() []parityCase {
	var cases []parityCase
	for _, s := range isSettings() {
		cases = append(cases, parityCase{
			name: "IsDetail/" + s.name, op: "isdetail", replies: []string{`{"q1":true,"confidence":0.8}`}, floor: schemaflux.ContractSchemaAndInvariantChecked,
			old: func(ctx context.Context) error {
				var o types.OpOptions
				s.old(&o)
				_, err := oldroot.IsResult(ctx, isSubject, isQuestion, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.IsDetail(ctx, isSubject, isQuestion, s.v2)
				return err
			},
		})
	}
	return cases
}

// The tests are not parallel: runParity installs an observer, which is process
// state, while it runs its last group.

func TestExtractSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, extractCases()) }

func TestClassifySendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, classifyCases()) }

func TestSummarizeSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, summarizeCases()) }

func TestSummarizeDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, summarizeDetailCases())
}

func TestIsSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, isCases()) }

func TestIsDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, isDetailCases())
}
