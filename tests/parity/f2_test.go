package parity

import (
	"context"
	"testing"

	oldroot "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// The text functions of version 2 beside Summarize: Rewrite, Translate and
// Expand, each with its Detail form, and Compress, Normalize, Format and Merge,
// each against the old root's function of the same request. A plain form is
// compared with the old root's plain function in every state, so that a recorder,
// an observer and a client with no floor never turn it into the Detail request;
// a Detail form is compared with the old root's Result form, which sends the
// Detail request in every state.

const (
	bufferedTextInput        = "hey team, the thing broke again lol"
	bufferedRewritePlain     = "Dear team, the system failed again."
	bufferedRewriteDetailRep = `{"text":"Dear team, the system failed again.","changes_made":["formal greeting"],"tone_achieved":"formal","confidence":0.9}`

	bufferedTranslateText      = "Good morning, team. The invoice is ready."
	bufferedTranslatePlain     = "Buenos dias, equipo. La factura esta lista."
	bufferedTranslateDetailRep = `{"text":"Buenos dias, equipo. La factura esta lista.","source_language_detected":"English","confidence":0.95,"alternatives":[{"phrase":"team","alternative":"grupo","context":"informal"}]}`

	bufferedExpandText      = "Revenue grew."
	bufferedExpandPlain     = "Revenue grew twelve percent over the previous quarter, driven by new subscriptions."
	bufferedExpandDetailRep = `{"text":"Revenue grew twelve percent over the previous quarter, driven by new subscriptions.","added_content":["the cause"],"confidence":0.8}`

	bufferedCompressRep      = `{"compressed":{"title":"Q3 report","body":"Revenue up 12%, costs flat."},"preserved_info":["growth"],"removed_info":["wording"]}`
	bufferedCompressSmallRep = `{"compressed":{"title":"Q3","body":"up"},"preserved_info":[],"removed_info":[]}`

	bufferedNormalizeRep = `{"normalized":{"name":"ada  lovelace","phone":"+1 555 0100"},"changes":[{"field":"phone","original":"5550100","normalized":"+1 555 0100","reason":"format","confidence":0.8}],"warnings":[]}`

	bufferedFormatRep = "| item | count |\n|---|---|\n| pens | 3 |"
	bufferedMergeRep  = `{"name":"Ada Lovelace","phone":"+1 555 0100"}`
)

type bufferedParityTextMemo struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type bufferedParityTextContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

var (
	parityTextMemoInput    = bufferedParityTextMemo{Title: "Q3 report", Body: "Revenue grew twelve percent while costs stayed flat across the quarter."}
	parityTextContactInput = bufferedParityTextContact{Name: "ada  lovelace", Phone: "5550100"}
)

// ---- Rewrite and RewriteDetail ----

type bufferedRewriteSetting struct {
	name string
	v2   schemaflux.RewriteOptions
	old  func(o *oldroot.RewriteOptions)
}

func bufferedRewriteSettings() []bufferedRewriteSetting {
	settings := []bufferedRewriteSetting{
		{"the default", schemaflux.RewriteOptions{}, func(*oldroot.RewriteOptions) {}},
		{"Tone", schemaflux.RewriteOptions{Tone: "formal"}, func(o *oldroot.RewriteOptions) { o.TargetTone = "formal" }},
		{"Formality 8", schemaflux.RewriteOptions{Formality: 8}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 8 }},
		{"Formality 5 is the default", schemaflux.RewriteOptions{Formality: 5}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 5 }},
		{"Formality 1", schemaflux.RewriteOptions{Formality: 1}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 1 }},
		{"Formality 10", schemaflux.RewriteOptions{Formality: 10}, func(o *oldroot.RewriteOptions) { o.FormalityLevel = 10 }},
		{"NoPreserveFacts", schemaflux.RewriteOptions{NoPreserveFacts: true}, func(o *oldroot.RewriteOptions) { o.PreserveFacts = false }},
		{"Audience", schemaflux.RewriteOptions{Audience: "executives"}, func(o *oldroot.RewriteOptions) { o.Audience = "executives" }},
		{"StyleGuide", schemaflux.RewriteOptions{StyleGuide: "AP"}, func(o *oldroot.RewriteOptions) { o.StyleGuide = "AP" }},
		{"Changes", schemaflux.RewriteOptions{Changes: []string{"shorten", "drop jargon"}}, func(o *oldroot.RewriteOptions) { o.Changes = []string{"shorten", "drop jargon"} }},
		{"AvoidWords", schemaflux.RewriteOptions{AvoidWords: []string{"synergy", "leverage"}}, func(o *oldroot.RewriteOptions) { o.AvoidWords = []string{"synergy", "leverage"} }},
		{"IncludeWords", schemaflux.RewriteOptions{IncludeWords: []string{"team"}}, func(o *oldroot.RewriteOptions) { o.IncludeWords = []string{"team"} }},
		{"every own setting",
			schemaflux.RewriteOptions{Tone: "formal", Formality: 8, NoPreserveFacts: true, Audience: "executives", StyleGuide: "AP", Changes: []string{"shorten"}, AvoidWords: []string{"synergy"}, IncludeWords: []string{"team"}},
			func(o *oldroot.RewriteOptions) {
				o.TargetTone, o.FormalityLevel, o.PreserveFacts, o.Audience, o.StyleGuide = "formal", 8, false, "executives", "AP"
				o.Changes, o.AvoidWords, o.IncludeWords = []string{"shorten"}, []string{"synergy"}, []string{"team"}
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, bufferedRewriteSetting{c.name, schemaflux.RewriteOptions{Options: c.v2}, func(o *oldroot.RewriteOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func bufferedRewriteCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedRewriteSettings() {
		cases = append(cases, parityCase{
			name: "Rewrite/" + s.name, op: "rewrite", replies: []string{bufferedRewritePlain},
			old: func(ctx context.Context) error {
				o := oldroot.NewRewriteOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.Rewrite(bufferedTextInput, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Rewrite(ctx, bufferedTextInput, s.v2)
				return err
			},
		})
	}
	return cases
}

func bufferedRewriteDetailCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedRewriteSettings() {
		cases = append(cases, parityCase{
			name: "RewriteDetail/" + s.name, op: "rewritedetail", replies: []string{bufferedRewriteDetailRep}, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				o := oldroot.NewRewriteOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.RewriteResult(bufferedTextInput, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.RewriteDetail(ctx, bufferedTextInput, s.v2)
				return err
			},
		})
	}
	return cases
}

// ---- Translate and TranslateDetail ----

type bufferedTranslateSetting struct {
	name string
	v2   schemaflux.TranslateOptions
	old  func(o *oldroot.TranslateOptions)
}

func bufferedTranslateSettings() []bufferedTranslateSetting {
	glossary := map[string]string{"invoice": "factura", "team": "equipo"}
	settings := []bufferedTranslateSetting{
		{"the default", schemaflux.TranslateOptions{}, func(*oldroot.TranslateOptions) {}},
		{"SourceLanguage", schemaflux.TranslateOptions{SourceLanguage: "English"}, func(o *oldroot.TranslateOptions) { o.SourceLanguage = "English" }},
		{"NoPreserveFormatting", schemaflux.TranslateOptions{NoPreserveFormatting: true}, func(o *oldroot.TranslateOptions) { o.PreserveFormatting = false }},
		{"CulturalAdaptation 8", schemaflux.TranslateOptions{CulturalAdaptation: 8}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 8 }},
		{"CulturalAdaptation 10", schemaflux.TranslateOptions{CulturalAdaptation: 10}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 10 }},
		{"CulturalAdaptation 1", schemaflux.TranslateOptions{CulturalAdaptation: 1}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 1 }},
		{"CulturalAdaptation 5 is the default", schemaflux.TranslateOptions{CulturalAdaptation: 5}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 5 }},
		// A negative value means a literal translation, which the old root says as zero.
		{"CulturalAdaptation negative is zero", schemaflux.TranslateOptions{CulturalAdaptation: -1}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 0 }},
		{"CulturalAdaptation very negative is zero", schemaflux.TranslateOptions{CulturalAdaptation: -1000}, func(o *oldroot.TranslateOptions) { o.CulturalAdaptation = 0 }},
		{"Formality", schemaflux.TranslateOptions{Formality: "formal"}, func(o *oldroot.TranslateOptions) { o.Formality = "formal" }},
		{"Glossary", schemaflux.TranslateOptions{Glossary: glossary}, func(o *oldroot.TranslateOptions) { o.Glossary = glossary }},
		{"Dialect", schemaflux.TranslateOptions{Dialect: "Mexican"}, func(o *oldroot.TranslateOptions) { o.Dialect = "Mexican" }},
		{"every own setting",
			schemaflux.TranslateOptions{SourceLanguage: "English", NoPreserveFormatting: true, CulturalAdaptation: 8, Formality: "formal", Glossary: glossary, Dialect: "Mexican"},
			func(o *oldroot.TranslateOptions) {
				o.SourceLanguage, o.PreserveFormatting, o.CulturalAdaptation, o.Formality, o.Glossary, o.Dialect = "English", false, 8, "formal", glossary, "Mexican"
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, bufferedTranslateSetting{c.name, schemaflux.TranslateOptions{Options: c.v2}, func(o *oldroot.TranslateOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func bufferedTranslateCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedTranslateSettings() {
		cases = append(cases, parityCase{
			name: "Translate/" + s.name, op: "translate", replies: []string{bufferedTranslatePlain},
			old: func(ctx context.Context) error {
				o := oldroot.NewTranslateOptions().WithTargetLanguage("Spanish")
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.Translate(bufferedTranslateText, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Translate(ctx, bufferedTranslateText, "Spanish", s.v2)
				return err
			},
		})
	}
	return cases
}

func bufferedTranslateDetailCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedTranslateSettings() {
		cases = append(cases, parityCase{
			name: "TranslateDetail/" + s.name, op: "translatedetail", replies: []string{bufferedTranslateDetailRep}, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				o := oldroot.NewTranslateOptions().WithTargetLanguage("Spanish")
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.TranslateResult(bufferedTranslateText, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.TranslateDetail(ctx, bufferedTranslateText, "Spanish", s.v2)
				return err
			},
		})
	}
	return cases
}

// ---- Expand and ExpandDetail ----

type bufferedExpandSetting struct {
	name string
	v2   schemaflux.ExpandOptions
	old  func(o *oldroot.ExpandOptions)
}

func bufferedExpandSettings() []bufferedExpandSetting {
	settings := []bufferedExpandSetting{
		{"the default", schemaflux.ExpandOptions{}, func(*oldroot.ExpandOptions) {}},
		{"Factor 3", schemaflux.ExpandOptions{Factor: 3}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 3 }},
		{"Factor 2.5", schemaflux.ExpandOptions{Factor: 2.5}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 2.5 }},
		{"Factor 2 is the default", schemaflux.ExpandOptions{Factor: 2}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 2 }},
		{"Factor 1", schemaflux.ExpandOptions{Factor: 1}, func(o *oldroot.ExpandOptions) { o.ExpansionFactor = 1 }},
		{"DetailLevel 9", schemaflux.ExpandOptions{DetailLevel: 9}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 9 }},
		{"DetailLevel 1", schemaflux.ExpandOptions{DetailLevel: 1}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 1 }},
		{"DetailLevel 10", schemaflux.ExpandOptions{DetailLevel: 10}, func(o *oldroot.ExpandOptions) { o.DetailLevel = 10 }},
		{"IncludeExamples", schemaflux.ExpandOptions{IncludeExamples: true}, func(o *oldroot.ExpandOptions) { o.IncludeExamples = true }},
		{"ElaborateOn", schemaflux.ExpandOptions{ElaborateOn: []string{"growth", "cost"}}, func(o *oldroot.ExpandOptions) { o.ElaborateOn = []string{"growth", "cost"} }},
		{"AddContext", schemaflux.ExpandOptions{AddContext: []string{"the market"}}, func(o *oldroot.ExpandOptions) { o.AddContext = []string{"the market"} }},
		{"Style", schemaflux.ExpandOptions{Style: "technical"}, func(o *oldroot.ExpandOptions) { o.ExpansionStyle = "technical" }},
		{"every own setting",
			schemaflux.ExpandOptions{Factor: 3, DetailLevel: 9, IncludeExamples: true, ElaborateOn: []string{"growth"}, AddContext: []string{"the market"}, Style: "narrative"},
			func(o *oldroot.ExpandOptions) {
				o.ExpansionFactor, o.DetailLevel, o.IncludeExamples, o.ElaborateOn, o.AddContext, o.ExpansionStyle = 3, 9, true, []string{"growth"}, []string{"the market"}, "narrative"
			}},
	}
	for _, c := range commonCases {
		settings = append(settings, bufferedExpandSetting{c.name, schemaflux.ExpandOptions{Options: c.v2}, func(o *oldroot.ExpandOptions) { c.flat(&o.CommonOptions, &o.OpOptions) }})
	}
	return settings
}

func bufferedExpandCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedExpandSettings() {
		cases = append(cases, parityCase{
			name: "Expand/" + s.name, op: "expand", replies: []string{bufferedExpandPlain},
			old: func(ctx context.Context) error {
				o := oldroot.NewExpandOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.Expand(bufferedExpandText, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Expand(ctx, bufferedExpandText, s.v2)
				return err
			},
		})
	}
	return cases
}

func bufferedExpandDetailCases() []parityCase {
	var cases []parityCase
	for _, s := range bufferedExpandSettings() {
		cases = append(cases, parityCase{
			name: "ExpandDetail/" + s.name, op: "expanddetail", replies: []string{bufferedExpandDetailRep}, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				o := oldroot.NewExpandOptions()
				s.old(&o)
				o.CommonOptions.Context = ctx
				_, err := oldroot.ExpandResult(bufferedExpandText, o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.ExpandDetail(ctx, bufferedExpandText, s.v2)
				return err
			},
		})
	}
	return cases
}

// ---- Compress ----

func bufferedCompressCase(name string, old oldroot.CompressOptions, v2 schemaflux.CompressOptions) parityCase {
	return bufferedCompressCaseAnswering(name, bufferedCompressRep, old, v2)
}

// bufferedCompressCaseAnswering is bufferedCompressCase with the reply the providers give.
func bufferedCompressCaseAnswering(name, reply string, old oldroot.CompressOptions, v2 schemaflux.CompressOptions) parityCase {
	return parityCase{
		name: "Compress/" + name, op: "compress", replies: []string{reply}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Compress(parityTextMemoInput, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Compress(ctx, parityTextMemoInput, v2)
			return err
		},
	}
}

func bufferedCompressCases() []parityCase {
	base := oldroot.NewCompressOptions
	cases := []parityCase{
		bufferedCompressCase("the default", base(), schemaflux.CompressOptions{}),
		bufferedCompressCase("Ratio 0.5", base().WithCompressionRatio(0.5), schemaflux.CompressOptions{Ratio: 0.5}),
		bufferedCompressCase("Ratio 1", base().WithCompressionRatio(1), schemaflux.CompressOptions{Ratio: 1}),
		bufferedCompressCase("Ratio 0.3 is the default", base().WithCompressionRatio(0.3), schemaflux.CompressOptions{Ratio: 0.3}),
		bufferedCompressCase("Ratio 0.05", base().WithCompressionRatio(0.05), schemaflux.CompressOptions{Ratio: 0.05}),
		bufferedCompressCase("RetainFields", base().WithRetainFields([]string{"title"}), schemaflux.CompressOptions{RetainFields: []string{"title"}}),
		bufferedCompressCase("RemoveFields", base().WithRemoveFields([]string{"missing"}), schemaflux.CompressOptions{RemoveFields: []string{"missing"}}),
		bufferedCompressCase("PreserveInfo", base().WithPreserveInfo([]string{"dates"}), schemaflux.CompressOptions{PreserveInfo: []string{"dates"}}),
		bufferedCompressCase("Strategy lossy", base().WithStrategy("lossy"), schemaflux.CompressOptions{Strategy: "lossy"}),
		bufferedCompressCase("Strategy lossless", base().WithStrategy("lossless"), schemaflux.CompressOptions{Strategy: "lossless"}),
		bufferedCompressCase("Priority actions", base().WithPriority("actions"), schemaflux.CompressOptions{Priority: "actions"}),
		bufferedCompressCase("OutputFormat key_points", base().WithOutputFormat("key_points"), schemaflux.CompressOptions{OutputFormat: "key_points"}),
		bufferedCompressCase("MaxSize above the target", base().WithMaxOutputSize(200), schemaflux.CompressOptions{MaxSize: 200}),
		// The ceiling caps the size the prompt aims at when it is below the ratio's.
		bufferedCompressCaseAnswering("MaxSize below the target", bufferedCompressSmallRep, base().WithMaxOutputSize(26), schemaflux.CompressOptions{MaxSize: 26}),
		bufferedCompressCase("every own setting",
			base().WithCompressionRatio(0.5).WithRetainFields([]string{"title"}).WithRemoveFields([]string{"missing"}).WithPreserveInfo([]string{"dates"}).
				WithStrategy("lossy").WithPriority("facts").WithOutputFormat("summary").WithMaxOutputSize(200),
			schemaflux.CompressOptions{Ratio: 0.5, RetainFields: []string{"title"}, RemoveFields: []string{"missing"}, PreserveInfo: []string{"dates"}, Strategy: "lossy", Priority: "facts", OutputFormat: "summary", MaxSize: 200}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, bufferedCompressCase(c.name, old, schemaflux.CompressOptions{Options: c.v2}))
	}
	return cases
}

// ---- Normalize ----

func bufferedNormalizeCase(name string, old oldroot.NormalizeOptions, v2 schemaflux.NormalizeOptions) parityCase {
	return parityCase{
		name: "Normalize/" + name, op: "normalize", replies: []string{bufferedNormalizeRep}, floor: schemaflux.ContractSchemaConstrained,
		old: func(ctx context.Context) error {
			old.CommonOptions.Context = ctx
			_, err := oldroot.Normalize(parityTextContactInput, old)
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Normalize(ctx, parityTextContactInput, v2)
			return err
		},
	}
}

func bufferedNormalizeCases() []parityCase {
	base := oldroot.NewNormalizeOptions
	rules := map[string]string{"phone": "E.164", "name": "title case"}
	mappings := map[string]string{"USA": "United States"}
	cases := []parityCase{
		bufferedNormalizeCase("the default", base(), schemaflux.NormalizeOptions{}),
		bufferedNormalizeCase("Standard", base().WithStandard("ISO_8601"), schemaflux.NormalizeOptions{Standard: "ISO_8601"}),
		bufferedNormalizeCase("Rules", base().WithRules(rules), schemaflux.NormalizeOptions{Rules: rules}),
		bufferedNormalizeCase("CanonicalMappings", base().WithCanonicalMappings(mappings), schemaflux.NormalizeOptions{CanonicalMappings: mappings}),
		bufferedNormalizeCase("Case", base().WithNormalizeCase("title"), schemaflux.NormalizeOptions{Case: "title"}),
		bufferedNormalizeCase("Case lower", base().WithNormalizeCase("lower"), schemaflux.NormalizeOptions{Case: "lower"}),
		bufferedNormalizeCase("NoFixTypos", base().WithFixTypos(false), schemaflux.NormalizeOptions{NoFixTypos: true}),
		bufferedNormalizeCase("NoNormalizeWhitespace", base().WithNormalizeWhitespace(false), schemaflux.NormalizeOptions{NoNormalizeWhitespace: true}),
		bufferedNormalizeCase("both negated at once", base().WithFixTypos(false).WithNormalizeWhitespace(false), schemaflux.NormalizeOptions{NoFixTypos: true, NoNormalizeWhitespace: true}),
		bufferedNormalizeCase("Fields", base().WithFields([]string{"name", "phone"}), schemaflux.NormalizeOptions{Fields: []string{"name", "phone"}}),
		bufferedNormalizeCase("SkipFields", base().WithSkipFields([]string{"name"}), schemaflux.NormalizeOptions{SkipFields: []string{"name"}}),
		bufferedNormalizeCase("Locale", base().WithLocale("de-DE"), schemaflux.NormalizeOptions{Locale: "de-DE"}),
		bufferedNormalizeCase("Cautious", base().WithStrict(true), schemaflux.NormalizeOptions{Cautious: true}),
		bufferedNormalizeCase("every own setting",
			base().WithStandard("ISO_8601").WithRules(rules).WithCanonicalMappings(mappings).WithNormalizeCase("title").WithFixTypos(false).WithNormalizeWhitespace(false).
				WithFields([]string{"name", "phone"}).WithSkipFields([]string{"id"}).WithLocale("de-DE").WithStrict(true),
			schemaflux.NormalizeOptions{Standard: "ISO_8601", Rules: rules, CanonicalMappings: mappings, Case: "title", NoFixTypos: true, NoNormalizeWhitespace: true,
				Fields: []string{"name", "phone"}, SkipFields: []string{"id"}, Locale: "de-DE", Cautious: true}),
	}
	for _, c := range commonCases {
		old := base()
		c.flat(&old.CommonOptions, &old.OpOptions)
		cases = append(cases, bufferedNormalizeCase(c.name, old, schemaflux.NormalizeOptions{Options: c.v2}))
	}
	return cases
}

// ---- Format and Merge ----

type bufferedParityTextRow struct {
	Item  string `json:"item"`
	Count int    `json:"count"`
}

func bufferedFormatCases() []parityCase {
	rows := []bufferedParityTextRow{{"pens", 3}, {"inks", 1}}
	var cases []parityCase
	for _, s := range isSettings() {
		cases = append(cases, parityCase{
			name: "Format/" + s.name, op: "format", replies: []string{bufferedFormatRep},
			old: func(ctx context.Context) error {
				var o types.OpOptions
				s.old(&o)
				o.Context = ctx
				_, err := oldroot.Format(rows, "markdown table", o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Format(ctx, rows, "markdown table", s.v2)
				return err
			},
		})
	}
	cases = append(cases, parityCase{
		name: "Format/a string for data", op: "format", replies: []string{bufferedFormatRep},
		old: func(ctx context.Context) error {
			_, err := oldroot.Format("say \"hi\" <now>", "one line", types.OpOptions{Context: ctx})
			return err
		},
		v2: func(ctx context.Context) error {
			_, err := schemaflux.Format(ctx, "say \"hi\" <now>", "one line")
			return err
		},
	})
	return cases
}

func bufferedMergeCases() []parityCase {
	sources := []bufferedParityTextContact{{Name: "Ada L.", Phone: "5550100"}, {Name: "Ada Lovelace", Phone: ""}, {Name: "A. Lovelace", Phone: "5550101"}}
	var cases []parityCase
	for _, s := range isSettings() {
		cases = append(cases, parityCase{
			name: "Merge/" + s.name, op: "merge", replies: []string{bufferedMergeRep}, floor: schemaflux.ContractJSONWellFormed,
			old: func(ctx context.Context) error {
				var o types.OpOptions
				s.old(&o)
				o.Context = ctx
				_, err := oldroot.Merge(sources, "prefer the newest", o)
				return err
			},
			v2: func(ctx context.Context) error {
				_, err := schemaflux.Merge(ctx, sources, "prefer the newest", s.v2)
				return err
			},
		})
	}
	return cases
}

// The tests are not parallel: runParity installs an observer, which is process
// state, while it runs its last group.

func TestRewriteSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, bufferedRewriteCases()) }

func TestRewriteDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, bufferedRewriteDetailCases())
}

func TestTranslateSendsTheRequestTheOldRootSends(t *testing.T) {
	runParity(t, bufferedTranslateCases())
}

func TestTranslateDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, bufferedTranslateDetailCases())
}

func TestExpandSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, bufferedExpandCases()) }

func TestExpandDetailSendsTheRequestTheOldRootsResultFormSends(t *testing.T) {
	runParity(t, bufferedExpandDetailCases())
}

func TestCompressSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, bufferedCompressCases()) }

func TestNormalizeSendsTheRequestTheOldRootSends(t *testing.T) {
	runParity(t, bufferedNormalizeCases())
}

func TestFormatSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, bufferedFormatCases()) }

func TestMergeSendsTheRequestTheOldRootSends(t *testing.T) { runParity(t, bufferedMergeCases()) }
