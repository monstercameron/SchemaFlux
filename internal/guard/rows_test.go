package guard_test

import (
	"context"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A row is one function that asks a model, with how to call it on a scripted
// provider. The table of rows is the one table every guard of this package runs
// over; the test in coverage_test.go fails for an exported function that is
// neither in it nor in the table of functions that ask no model.
//
// To add a row for a new function: write its row in rows_*_test.go with the Op
// of table A3, the level of internal/ops contracttable.go, its call path (A1),
// and a call that puts the input's parts where the function takes caller text.
type row struct {
	// pkg is the package under v2 ("" for the root) and name the function as the
	// coverage test sees it. variant names the instantiation of a generic
	// function the row uses, where one function has rows at different levels.
	pkg, name, variant string

	// op is the Record's Op string (A3). A function that adds a Record for a
	// decision made on its behalf (Whether, RouteBy) names that Record's Op.
	op string

	// level is the contract level the function delivers for this instantiation.
	// computed is true when the level is worked out per call, and then level is
	// the one this instantiation delivers.
	level    schemaflux.ContractLevel
	computed bool

	// floorAt, when not zero, is the level the other guards hold the row to in
	// the floor state instead of level. It is set only for a row that does not
	// deliver the level the table gives it, which TestEveryOperationUnderEveryFloor
	// and TestRecordsDeliverTheLevelOfTheTable report as a finding, so that the
	// guards of everything else still run on the row.
	floorAt schemaflux.ContractLevel

	// path is the call path of table A1: "twin", "recorded", "detail",
	// "decision", "decision-detail", "stream" or "composed".
	path string

	// detail names the Detail form that is this function's twin, or "".
	detail string

	// answer is the body the fake answers with when the call should succeed. It
	// may hold {{c}}, which is the canary in a canary run. Empty means an answer
	// shaped from the request (Provider.Shape). script, when set, replaces both.
	answer string
	script func(m *schemafluxtest.Provider, x in, withCanary bool)

	// call calls the function with x on the context and returns what it returned.
	// A function that returns a stream reads it to its end.
	call func(ctx context.Context, x in) (any, error)

	// na lists the failure scenarios that do not apply to the function, each with
	// the reason; scenarios are named as in failureScenarios.
	na map[string]string
}

// id names the row for a subtest.
func (r *row) id() string {
	name := "v2"
	if r.pkg != "" {
		name = "v2/" + r.pkg
	}
	name += "." + r.name
	if r.variant != "" {
		name += "[" + r.variant + "]"
	}
	return name
}

// key is the name the coverage test matches against: "pkg.Name".
func (r *row) key() string {
	if r.pkg == "" {
		return r.name
	}
	return r.pkg + "." + r.name
}

// textual says that the model's answer is text taken as it comes, so that a
// malformed answer is not a failure.
var textual = map[string]string{"malformed answer": "the answer is the model's text, which has no form to get wrong"}

// no is the function's own exemption from a failure scenario.
func no(scenario, why string) map[string]string { return map[string]string{scenario: why} }

// diffSummaryOnly exempts Diff from a scenario the provider causes: the model only
// writes the summary of a comparison Go makes, so its failure is reported in
// DiffResult.SummaryError and the call succeeds.
func diffSummaryOnly(scenario string) map[string]string {
	return no(scenario, "the model only writes Diff's summary: its failure is DiffResult.SummaryError and the call succeeds")
}

// merged joins exemptions.
func merged(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// note is the caller's own type in the rows that need one.
type note struct {
	Body string `json:"body"`
	N    int    `json:"n"`
}

// tagged is asked about field by field: the canary is in a struct tag, which a
// request holds and no Record or error may.
type tagged struct {
	Spam    bool   `ask:"Is this message ZZ-CANARY-7f3a91 spam?"`
	Team    string `ask:"Which team should handle it?" oneof:"billing,technical,shipping"`
	Urgency int    `ask:"How urgent is it?" scale:"1-5"`
}

const (
	noteAnswer   = `{"body":"{{c}}","n":1}`
	textAnswer   = `A short answer that mentions {{c}}.`
	yesAnswer    = `{"q1":true}`
	yesSure      = `{"q1":true,"confidence":0.8}`
	secondAnswer = `{"q1":2}`
	secondSure   = `{"q1":2,"confidence":0.8}`
)

func step(text string) schemaflux.Step[string] {
	return func(context.Context) (string, error) { return text, nil }
}

// nth returns a list of n distinct short items, the first of which is first.
func items(first string, rest ...string) []string {
	return append([]string{first}, rest...)
}

// stream reads a stream to its end and returns its text, which is what a caller
// does to make the stream's Record complete.
func stream(s *schemaflux.TextStream, err error) (any, error) {
	if err != nil {
		return "", err
	}
	text, err := s.Text()
	return text, err
}

// rootRows are the functions of the root package that ask a model.
func rootRows() []row {
	rows := []row{
		// The extraction family.
		{name: "Extract", op: "extract", level: schemaflux.ContractSchemaConstrained, computed: true, path: "twin", answer: noteAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Extract[note](ctx, x.Text, schemaflux.ExtractOptions{Options: x.opts()}))
			}},
		{name: "Transform", op: "transform", level: schemaflux.ContractSchemaConstrained, path: "recorded", answer: noteAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Transform[note](ctx, x.Text, schemaflux.TransformOptions{Options: x.opts()}))
			}},
		{name: "Generate", variant: "struct", op: "generate", level: schemaflux.ContractSchemaConstrained, computed: true, path: "twin", answer: noteAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Generate[note](ctx, "write a note about "+x.Text, schemaflux.GenerateOptions{Options: x.opts()}))
			}},
		{name: "Generate", variant: "string", op: "generate", level: schemaflux.ContractPromptOnly, computed: true, path: "twin", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Generate[string](ctx, "write about "+x.Text, schemaflux.GenerateOptions{Options: x.opts()}))
			}},
		{name: "Infer", op: "infer", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, withCanary bool) {
				m.Reply(`{"body":` + quote(x.Text) + `,"n":` + `1}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Infer(ctx, note{Body: x.Text}, schemaflux.InferOptions{Options: x.opts()}))
			}},
		{name: "Parse", op: "parse", level: schemaflux.ContractJSONWellFormed, path: "recorded", answer: noteAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Parse[note](ctx, x.Text, schemaflux.ParseOptions{Options: x.opts(), AllowLLMFallback: true}))
			}},
		{name: "Complete", op: "complete", level: schemaflux.ContractPromptOnly, path: "recorded", answer: ` and then {{c}}`, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Complete(ctx, x.Text, schemaflux.CompleteOptions{Options: x.opts()}))
			}},
		{name: "CompleteField", op: "completefield", level: schemaflux.ContractPromptOnly, path: "recorded", answer: `{{c}} filled in`, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.CompleteField(ctx, note{Body: x.Text}, "Body", schemaflux.CompleteOptions{Options: x.opts()}))
			}},
		{name: "Suggest", op: "suggest", level: schemaflux.ContractJSONWellFormed, path: "recorded", answer: `["Reply {{c}}","Offer a refund","Escalate"]`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Suggest[string](ctx, x.Text, schemaflux.SuggestOptions{Options: x.opts()}))
			}},
		{name: "Annotate", op: "annotate", level: schemaflux.ContractJSONWellFormed, path: "recorded", answer: `{"annotations":[],"summary":{}}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Annotate(ctx, x.Text, schemaflux.AnnotateOptions{Options: x.opts()}))
			}},
		{name: "Explain", op: "explain", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"explanation":"It reads {{c}}.","summary":"A read.","key_points":["one"]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Explain(ctx, map[string]string{x.Key: x.Text}, schemaflux.ExplainOptions{Options: x.opts()}))
			}},
		{name: "Question", op: "question", level: schemaflux.ContractSchemaConstrained, path: "recorded",
			answer: `{"answer":"{{c}}","confidence":0.9,"reasoning":"r","evidence":["e"]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Question[string](ctx, x.Text, x.Label+"?", schemaflux.QuestionOptions{Options: x.opts()}))
			}},

		// The text operations: a plain form, a Detail form and a stream each.
		{name: "Summarize", op: "summarize", level: schemaflux.ContractPromptOnly, path: "recorded", detail: "SummarizeDetail", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Summarize(ctx, x.Text, schemaflux.SummarizeOptions{Options: x.opts()}))
			}},
		{name: "SummarizeDetail", op: "summarizedetail", level: schemaflux.ContractJSONWellFormed, path: "detail",
			answer: `{"text":"Short {{c}}.","key_points":["k"],"confidence":0.85}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.SummarizeDetail(ctx, x.Text, schemaflux.SummarizeOptions{Options: x.opts()}))
			}},
		{name: "StreamSummarize", op: "streamsummarize", level: schemaflux.ContractPromptOnly, path: "stream", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return stream(schemaflux.StreamSummarize(ctx, x.Text, schemaflux.SummarizeOptions{Options: x.opts()}))
			}},
		{name: "Rewrite", op: "rewrite", level: schemaflux.ContractPromptOnly, path: "recorded", detail: "RewriteDetail", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Rewrite(ctx, x.Text, schemaflux.RewriteOptions{Options: x.opts()}))
			}},
		{name: "RewriteDetail", op: "rewritedetail", level: schemaflux.ContractJSONWellFormed, path: "detail",
			answer: `{"text":"Rewritten {{c}}.","changes_made":["formal"],"tone_achieved":"formal","confidence":0.9}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.RewriteDetail(ctx, x.Text, schemaflux.RewriteOptions{Options: x.opts()}))
			}},
		{name: "StreamRewrite", op: "streamrewrite", level: schemaflux.ContractPromptOnly, path: "stream", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return stream(schemaflux.StreamRewrite(ctx, x.Text, schemaflux.RewriteOptions{Options: x.opts()}))
			}},
		{name: "Translate", op: "translate", level: schemaflux.ContractPromptOnly, path: "recorded", detail: "TranslateDetail", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Translate(ctx, x.Text, "French", schemaflux.TranslateOptions{Options: x.opts()}))
			}},
		{name: "TranslateDetail", op: "translatedetail", level: schemaflux.ContractJSONWellFormed, path: "detail",
			answer: `{"text":"Traduit {{c}}.","source_language_detected":"English","confidence":0.9,"alternatives":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.TranslateDetail(ctx, x.Text, "French", schemaflux.TranslateOptions{Options: x.opts()}))
			}},
		{name: "StreamTranslate", op: "streamtranslate", level: schemaflux.ContractPromptOnly, path: "stream", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return stream(schemaflux.StreamTranslate(ctx, x.Text, "French", schemaflux.TranslateOptions{Options: x.opts()}))
			}},
		{name: "Expand", op: "expand", level: schemaflux.ContractPromptOnly, path: "recorded", detail: "ExpandDetail", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Expand(ctx, x.Text, schemaflux.ExpandOptions{Options: x.opts()}))
			}},
		{name: "ExpandDetail", op: "expanddetail", level: schemaflux.ContractJSONWellFormed, path: "detail",
			answer: `{"text":"Longer {{c}}.","added_content":["the cause"],"confidence":0.8}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.ExpandDetail(ctx, x.Text, schemaflux.ExpandOptions{Options: x.opts()}))
			}},
		{name: "StreamExpand", op: "streamexpand", level: schemaflux.ContractPromptOnly, path: "stream", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return stream(schemaflux.StreamExpand(ctx, x.Text, schemaflux.ExpandOptions{Options: x.opts()}))
			}},
		{name: "Compress", op: "compress", level: schemaflux.ContractSchemaConstrained, path: "recorded",
			answer: `{"compressed":"{{c}}","preserved_info":["a"],"removed_info":["b"]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Compress(ctx, x.Text, schemaflux.CompressOptions{Options: x.opts()}))
			}},
		{name: "Normalize", op: "normalize", level: schemaflux.ContractSchemaConstrained, path: "recorded",
			answer: `{"normalized":{"body":"{{c}}","n":1},"changes":[],"warnings":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Normalize(ctx, note{Body: x.Text}, schemaflux.NormalizeOptions{Options: x.opts()}))
			}},
		{name: "Format", op: "format", level: schemaflux.ContractPromptOnly, path: "recorded", answer: textAnswer, na: textual,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Format(ctx, map[string]string{x.Key: x.Text}, "a line about "+x.Label, x.opts()))
			}},
		{name: "Merge", op: "merge", level: schemaflux.ContractJSONWellFormed, path: "recorded", answer: noteAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Merge(ctx, []note{{Body: x.Text, N: 1}, {Body: "second", N: 2}}, x.Label, x.opts()))
			}},

		// Analysis.
		{name: "Classify", op: "classify", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, withCanary bool) {
				m.Reply(`{"category":` + quote(x.Label) + `,"confidence":0.9,"alternatives":[],"reasoning":"r"}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Classify(ctx, x.Text, []string{x.Label, "other"}, schemaflux.ClassifyOptions{Options: x.opts()}))
			}},
		{name: "Score", op: "score", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"value":4,"breakdown":{},"reasoning":"clear {{c}}","strengths":[],"weaknesses":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Score(ctx, x.Text, schemaflux.ScoreOptions{Options: x.opts()}))
			}},
		{name: "Compare", op: "compare", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"similarity_score":0.8,"similarities":[],"differences":[],"verdict":"alike {{c}}","aspect_scores":{}}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Compare(ctx, x.Text, "the other thing", schemaflux.CompareOptions{Options: x.opts()}))
			}},
		{name: "Similar", op: "similar", level: schemaflux.ContractSchemaConstrained, path: "recorded",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Similar(ctx, x.Text, "the other thing", schemaflux.SimilarOptions{Options: x.opts()}))
			}},
		{name: "Diff", op: "diff", level: schemaflux.ContractPromptOnly, path: "recorded", answer: textAnswer,
			na: merged(textual, diffSummaryOnly("provider unavailable"), diffSummaryOnly("provider rate limited"), diffSummaryOnly("provider unauthorized"),
				diffSummaryOnly("provider timeout"), diffSummaryOnly("provider too large"), diffSummaryOnly("provider unsupported"),
				diffSummaryOnly("provider bare kind"), diffSummaryOnly("provider raw error"), diffSummaryOnly("empty answer"), diffSummaryOnly("refusal"), diffSummaryOnly("client timeout")),
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Diff(ctx, note{Body: x.Text, N: 1}, note{Body: "changed", N: 2}, schemaflux.DiffOptions{Options: x.opts()}))
			}},
		{name: "Cluster", op: "cluster", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Cluster(ctx, []string{x.Text, "beta", "gamma"}, schemaflux.ClusterOptions{Options: x.opts()}))
			}},
		{name: "Deduplicate", op: "deduplicate", level: schemaflux.ContractSchemaAndInvariantChecked, path: "recorded", answer: `{"duplicates":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Deduplicate(ctx, []string{x.Text, "beta", "gamma"}, 0.8, x.opts()))
			}},

		// Collections.
		{name: "Choose", op: "choose", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Choose(ctx, []string{x.Text, "second", "third"}, schemaflux.ChooseOptions{Options: x.opts()}))
			}},
		{name: "Filter", op: "filter", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Filter(ctx, []string{x.Text, "second", "third"}, x.Label, schemaflux.FilterOptions{Options: x.opts()}))
			}},
		{name: "Sort", op: "sort", level: schemaflux.ContractJSONWellFormed, path: "twin",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Sort(ctx, []string{x.Text, "second", "third"}, x.Label, schemaflux.SortOptions{Options: x.opts()}))
			}},
		{name: "Rank", op: "rank", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"rankings":[{"index":0,"score":0.5},{"index":1,"score":0.9},{"index":2,"score":0.1}]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Rank(ctx, []string{x.Text, "second", "third"}, x.Label, schemaflux.RankOptions{Options: x.opts()}))
			}},
		{name: "SemanticMatch", op: "semanticmatch", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			answer: `{"matches":[{"source_index":0,"target_index":1,"score":0.9,"explanation":"same role","is_partial":false}],"unmatched_sources":[1],"unmatched_targets":[0]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.SemanticMatch(ctx, []string{x.Text, "second"}, []string{"third", "fourth"}, schemaflux.SemanticMatchOptions{Options: x.opts()}))
			}},

		// Judgments.
		{name: "Validate", op: "validate", level: schemaflux.ContractSchemaConstrained, path: "recorded",
			answer: `{"errors":[],"warnings":[],"info":[],"confidence":0.95,"summary":"fine {{c}}"}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Validate(ctx, note{Body: x.Text, N: 1}, schemaflux.ValidateOptions{Options: x.opts(), Rules: x.Label, FieldRules: map[string]string{x.Key: x.Label}}))
			}},
		{name: "Verify", op: "verify", level: schemaflux.ContractSchemaAndInvariantChecked, path: "recorded",
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Verify(ctx, x.Text, schemaflux.VerifyOptions{Options: x.opts()}))
			}},
		{name: "Audit", op: "audit", level: schemaflux.ContractSchemaAndInvariantChecked, path: "twin", answer: `{"findings":[]}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Audit(ctx, note{Body: x.Text, N: 1}, schemaflux.AuditOptions{Options: x.opts()}))
			}},
		{name: "Critique", op: "critique", level: schemaflux.ContractJSONWellFormed, path: "recorded",
			script: func(m *schemafluxtest.Provider, x in, _ bool) {
				m.Reply(`{"overall_score":0.7,"criteria_scores":{` + quote(x.Label) + `:0.8},"issues":[],"positives":[],"summary":"readable","top_priorities":["fix the intro"]}`)
			},
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Critique(ctx, x.Text, []string{x.Label}, schemaflux.CritiqueOptions{Options: x.opts()}))
			}},

		// The decisions.
		{name: "Is", op: "is", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision", detail: "IsDetail", answer: yesAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Is(ctx, map[string]string{x.Key: x.Text}, x.Label+"?", x.opts()))
			}},
		{name: "IsDetail", op: "isdetail", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision-detail", answer: yesSure,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.IsDetail(ctx, map[string]string{x.Key: x.Text}, x.Label+"?", x.opts()))
			}},
		{name: "Which", op: "which", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision", detail: "WhichDetail", answer: secondAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Which(ctx, x.Text, "which one?", []string{"first", x.Label}, x.opts()))
			}},
		{name: "WhichDetail", op: "whichdetail", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision-detail", answer: secondSure,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.WhichDetail(ctx, x.Text, "which one?", []string{"first", x.Label}, x.opts()))
			}},
		{name: "WhichOrNone", op: "whichornone", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision", detail: "WhichOrNoneDetail", answer: secondAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				v, ok, err := schemaflux.WhichOrNone(ctx, x.Text, "which one?", []string{"first", x.Label}, x.opts())
				return struct {
					V  string
					OK bool
				}{v, ok}, err
			}},
		{name: "WhichOrNoneDetail", op: "whichornonedetail", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision-detail", answer: secondSure,
			call: func(ctx context.Context, x in) (any, error) {
				v, ok, err := schemaflux.WhichOrNoneDetail(ctx, x.Text, "which one?", []string{"first", x.Label}, x.opts())
				return struct {
					V  schemaflux.Judged[string]
					OK bool
				}{v, ok}, err
			}},
		{name: "Rate", op: "rate", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision", detail: "RateDetail", answer: `{"q1":3}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Rate(ctx, x.Text, x.Label+"?", 1, 5, x.opts()))
			}},
		{name: "RateDetail", op: "ratedetail", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision-detail", answer: `{"q1":3,"confidence":0.8}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.RateDetail(ctx, x.Text, x.Label+"?", 1, 5, x.opts()))
			}},
		{name: "Assess", op: "assess", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision", detail: "AssessDetail", answer: `{"q1":true,"q2":2,"q3":4}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Assess[tagged](ctx, x.Text, x.opts()))
			}},
		{name: "AssessDetail", op: "assessdetail", level: schemaflux.ContractSchemaAndInvariantChecked, path: "decision-detail", answer: `{"q1":true,"q2":2,"q3":4,"confidence":0.7}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.AssessDetail[tagged](ctx, x.Text, x.opts()))
			}},

		// Combinators that make a decision: the decision's Record is the one.
		{name: "Whether", op: "is", level: schemaflux.ContractSchemaAndInvariantChecked, path: "composed", answer: yesAnswer,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.Whether(ctx, x.Text, x.Label+"?", step("then"), step("else"), x.opts()))
			}},
		{name: "RouteBy", op: "which", level: schemaflux.ContractSchemaAndInvariantChecked, path: "composed", answer: `{"q1":1}`,
			call: func(ctx context.Context, x in) (any, error) {
				return ret(schemaflux.RouteBy(ctx, x.Text, "which route?",
					[]schemaflux.Route[string]{schemaflux.To(x.Label, step("a")), schemaflux.To("beta", step("b"))}, x.opts()))
			}},
	}
	for i := range rows {
		rows[i].pkg = ""
	}
	return rows
}

// quote is s as a JSON string.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
