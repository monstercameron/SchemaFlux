package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/telemetry"
)

// The library's rule is that a log line never holds what a caller or a model
// wrote: not an input value, not the value of an option, not a label, a
// category or a field name, not the model's answer. Every caller logs what the
// library logs, and what the library logs is read by people who were never
// shown the data.
//
// This test runs every operation of the catalogue once through a client whose
// logger writes every level, with a canary string in each place a caller or a
// model writes into a call:
//
//   - the input, and every part of it that is a string;
//   - the value of each option the operation takes (steering, labels,
//     categories, criteria, queries, field names, rules, schema hints);
//   - the name of each field of the Go types the call is made over, which a
//     caller chose and which reaches the library as a JSON tag;
//   - every string the scripted model answers with.
//
// The answer is valid for the operation, so that the call succeeds and the
// lines written on the success path, which are the "operation completed" lines,
// are the ones read. A row that cannot be made to succeed says why in refuses,
// and its failure path is read instead.
//
// What the logger saw is searched in the text handler's output and in the JSON
// handler's, and a failure names the operation and prints the line.
const (
	leakCanaryInput  = "CANARY_INPUT_5d2b"
	leakCanaryOption = "CANARY_OPTION_5d2b"
	leakCanaryAnswer = "CANARY_ANSWER_5d2b"
	leakCanaryField  = "CANARY_FIELD_5d2b"
)

var leakCanaries = []string{leakCanaryInput, leakCanaryOption, leakCanaryAnswer, leakCanaryField}

// logAtEveryLevel is a logger that writes every level, in both of the standard
// handlers, so that a leak in a text line and a leak in a JSON line are both
// seen.
type logAtEveryLevel struct {
	text, json bytes.Buffer
}

func (l *logAtEveryLevel) logger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	return slog.New(teeLogHandler{slog.NewTextHandler(&l.text, opts), slog.NewJSONHandler(&l.json, opts)})
}

// leaks returns every line, text or JSON, that holds a canary.
func (l *logAtEveryLevel) leaks() []string {
	var held []string
	for _, output := range []string{l.text.String(), l.json.String()} {
		for _, line := range strings.Split(output, "\n") {
			for _, canary := range leakCanaries {
				if strings.Contains(line, canary) {
					held = append(held, line)
					break
				}
			}
		}
	}
	return held
}

func (l *logAtEveryLevel) lineCount() int {
	return strings.Count(l.text.String(), "\n")
}

type teeLogHandler struct{ a, b slog.Handler }

func (t teeLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.a.Enabled(ctx, level)
}

func (t teeLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if err := t.a.Handle(ctx, r.Clone()); err != nil {
		return err
	}
	return t.b.Handle(ctx, r.Clone())
}

func (t teeLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeLogHandler{t.a.WithAttrs(attrs), t.b.WithAttrs(attrs)}
}

func (t teeLogHandler) WithGroup(name string) slog.Handler {
	return teeLogHandler{t.a.WithGroup(name), t.b.WithGroup(name)}
}

// leakRecord is the type the calls are made over. Its field names are the
// canary: a caller chose them, and they reach the library as JSON tags.
type leakRecord struct {
	Name  string `json:"CANARY_FIELD_5d2b_name"`
	Notes string `json:"CANARY_FIELD_5d2b_notes"`
}

// leakOutput is the type a call is made into.
type leakOutput struct {
	Headline string `json:"CANARY_FIELD_5d2b_headline"`
	Detail   string `json:"CANARY_FIELD_5d2b_detail"`
}

var (
	leakIn    = leakRecord{Name: leakCanaryInput, Notes: leakCanaryInput + " notes"}
	leakInAlt = leakRecord{Name: leakCanaryInput + " other", Notes: leakCanaryInput + " other notes"}
	leakItems = []leakRecord{leakIn, leakInAlt, {Name: leakCanaryInput + " third", Notes: leakCanaryInput + " third notes"}}
	leakText  = "Text holding " + leakCanaryInput + " for the call."
	leakAsk   = "Is this " + leakCanaryOption + "?"
)

// scriptedModel answers every request it is sent. With no answer given it
// writes a value of the shape the request's schema declares, every string of it
// the answer canary, so that the answer is valid for any operation that
// declares what it reads. A request that declares no schema is answered with
// the answer canary as text.
type scriptedModel struct {
	mu     sync.Mutex
	answer func(req schemaflux.CompletionRequest) string
	sent   int
	lastSystem string // TEMPDEV
	lastFormat string // TEMPDEV
	lastSchema bool // TEMPDEV
}

func (m *scriptedModel) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	m.mu.Lock()
	m.sent++
	m.lastSystem = req.SystemPrompt // TEMPDEV
	m.lastFormat = req.ResponseFormat // TEMPDEV
	m.lastSchema = req.JSONSchema != nil // TEMPDEV
	m.mu.Unlock()
	content := ""
	if m.answer != nil {
		content = m.answer(req)
	}
	if content == "" {
		content = answerForRequest(req)
	}
	return schemaflux.CompletionResponse{
		Content: content, Model: req.Model, Provider: "local", FinishReason: "stop",
		Usage: schemaflux.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
	}, nil
}
func (*scriptedModel) Name() string                                      { return "local" }
func (*scriptedModel) EstimateCost(schemaflux.CompletionRequest) float64 { return 0 }
func (*scriptedModel) RetryPolicy() (int, time.Duration)                 { return 0, 0 }

func answerForRequest(req schemaflux.CompletionRequest) string {
	if req.JSONSchema == nil {
		if req.ResponseFormat == "json" {
			return `{"result":"` + leakCanaryAnswer + `"}`
		}
		return leakCanaryAnswer
	}
	raw, _ := json.Marshal(valueForSchema(req.JSONSchema))
	return string(raw)
}

// valueForSchema writes a value that satisfies a JSON schema, with the answer
// canary in every string.
func valueForSchema(schema map[string]any) any {
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	if value, ok := schema["const"]; ok {
		return value
	}
	if options, ok := schema["anyOf"].([]any); ok && len(options) > 0 {
		for _, option := range options {
			if inner, ok := option.(map[string]any); ok && inner["type"] != "null" {
				return valueForSchema(inner)
			}
		}
	}
	kind := schema["type"]
	if kinds, ok := kind.([]any); ok {
		kind = nil
		for _, candidate := range kinds {
			if candidate != "null" {
				kind = candidate
				break
			}
		}
	}
	switch kind {
	case "object":
		out := map[string]any{}
		properties, _ := schema["properties"].(map[string]any)
		for name, property := range properties {
			if inner, ok := property.(map[string]any); ok {
				out[name] = valueForSchema(inner)
			}
		}
		return out
	case "array":
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return []any{}
		}
		return []any{valueForSchema(items)}
	case "integer":
		return int(numberWithin(schema, 1))
	case "number":
		return numberWithin(schema, 0.5)
	case "boolean":
		return true
	case "null":
		return nil
	default:
		return leakCanaryAnswer
	}
}

func numberWithin(schema map[string]any, fallback float64) float64 {
	low, hasLow := schema["minimum"].(float64)
	high, hasHigh := schema["maximum"].(float64)
	switch {
	case hasLow && hasHigh:
		return math.Round((low + high) / 2)
	case hasLow:
		return low
	case hasHigh:
		return high
	}
	return fallback
}

// leakRow is one operation: the call, and the model's answer to it.
type leakRow struct {
	// call makes the call over the canaries. It returns what the call
	// returned, so that a refusal can be told from a success.
	call func(ctx context.Context) error
	// answer is the model's reply, when the schema of the request is not enough
	// to write one. It sees the request, so that an operation that asks the
	// model more than once can be answered each time.
	answer func(req schemaflux.CompletionRequest) string
	// refuses says why the call cannot be made to succeed with a scripted
	// answer. The call is then run for its failure path, and a row that says
	// this must in fact refuse.
	refuses string
}

// leakExempt is the catalogue's operations that are not driven, each with the
// reason. An operation is exempt when there is no model in it, and nothing for
// a model to answer, not when its answer is hard to script.
var leakExempt = map[string]string{
	"Chunk": "splits a slice by a size; asks no model and logs nothing",
}

// logMustNotHold runs one row under a logger that writes every level and fails
// when a canary is in a line.
func logMustNotHold(t *testing.T, name string, row leakRow) {
	t.Helper()
	log := &logAtEveryLevel{}
	previous := telemetry.GetLogger()
	telemetry.SetLogger(telemetry.FromSlog(log.logger()))
	config.SetDebugMode(true)
	model := &scriptedModel{answer: row.answer}
	client := schemaflux.NewClient("").WithProviderInstance(model)
	t.Cleanup(func() {
		config.SetDebugMode(false)
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(nil)
		telemetry.SetLogger(previous)
	})

	err := row.call(client.Context(context.Background()))
	switch {
	case row.refuses == "" && err != nil:
		t.Errorf("%s: the scripted answer was refused, so the success path was not read: %v", name, err)
		t.Logf("TEMPDEV %s format=%s schema=%v system=%q", name, model.lastFormat, model.lastSchema, tailOf(model.lastSystem)) // TEMPDEV
	case row.refuses != "" && err == nil:
		t.Errorf("%s: the row says the call refuses (%s) and it succeeded; drop refuses", name, row.refuses)
	}
	if model.sent == 0 && row.refuses == "" {
		t.Logf("%s: the call asked no model", name)
	}
	if log.lineCount() == 0 {
		t.Logf("%s: the call wrote no log line", name)
	}
	for _, line := range log.leaks() {
		t.Errorf("%s: a log line holds what a caller or a model wrote:\n    %s", name, line)
	}
}

func TestEveryOperationKeepsWhatItWasGivenOutOfTheLog(t *testing.T) {
	rows := leakRows()

	for _, entry := range schemaflux.Catalog() {
		if entry.Category == schemaflux.CategoryInfrastructure {
			continue
		}
		_, driven := rows[entry.Name]
		reason, exempt := leakExempt[entry.Name]
		switch {
		case driven && exempt:
			t.Errorf("%s is both driven and exempt", entry.Name)
		case !driven && !exempt:
			t.Errorf("%s is in the catalogue and neither driven by leakRows nor exempt in leakExempt", entry.Name)
		case exempt && strings.TrimSpace(reason) == "":
			t.Errorf("%s is exempt without a reason", entry.Name)
		}
	}
	catalogued := map[string]bool{}
	for _, entry := range schemaflux.Catalog() {
		catalogued[entry.Name] = true
	}
	for name := range rows {
		if !catalogued[name] {
			t.Errorf("leakRows drives %s, which is not in the catalogue", name)
		}
	}
	for name := range leakExempt {
		if !catalogued[name] {
			t.Errorf("leakExempt names %s, which is not in the catalogue", name)
		}
	}

	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) { logMustNotHold(t, name, rows[name]) })
	}
}

func leakCommon(o *ops.CommonOptions) {
	o.Steering = leakCanaryOption
}

func leakRows() map[string]leakRow {
	rows := map[string]leakRow{}
	leakCoreRows(rows)
	leakTextRows(rows)
	leakGenerationRows(rows)
	leakClassificationRows(rows)
	leakSelectionRows(rows)
	leakTransformationRows(rows)
	leakReviewRows(rows)
	leakJudgmentRows(rows)
	return rows
}

// errOf is the error of a call, whatever else the call returned.
func errOf[T any](_ T, err error) error { return err }

var (
	leakHints = map[string]string{leakCanaryOption: leakCanaryOption}
	leakList  = []string{leakCanaryOption, leakCanaryOption + " two"}
)

func leakCoreRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	rows["Extract"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewExtractOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.SchemaHints = hints
		o.FieldRules = hints
		_, err := schemaflux.Extract[leakRecord](leakText, o)
		return err
	}}
	rows["ExtractResult"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewExtractOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.SchemaHints = hints
		o.FieldRules = hints
		_, err := schemaflux.ExtractResult[leakRecord](leakIn, o)
		return err
	}}
	rows["Transform"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewTransformOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.MappingRules = hints
		o.PreserveFields = list
		o.TransformLogic = leakCanaryOption
		_, err := schemaflux.Transform[leakRecord, leakOutput](leakIn, o)
		return err
	}}
	rows["Generate"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewGenerateOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.Template = leakCanaryOption
		o.Style = leakCanaryOption
		_, err := schemaflux.Generate[leakOutput](leakText, o)
		return err
	}}
	rows["GenerateResult"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewGenerateOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		_, err := schemaflux.GenerateResult[leakOutput](leakText, o)
		return err
	}}
	rows["Classify"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewClassifyOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.Categories = list
		o.CategoryDescriptions = hints
		_, err := schemaflux.Classify[leakRecord, string](leakIn, o)
		return err
	}}
	rows["Score"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewScoreOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = list
		_, err := schemaflux.Score(leakIn, o)
		return err
	}}
	rows["Summarize"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewSummarizeOptions()
		o.CommonOptions.Context = ctx
		o.CommonOptions.Steering = leakCanaryOption
		o.FocusAreas = list
		_, err := schemaflux.Summarize(leakText, o)
		return err
	}}
}

func leakTextRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	summarize := func() schemaflux.SummarizeOptions {
		o := schemaflux.NewSummarizeOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.FocusAreas = list
		o.PreserveInfo = list
		return o
	}
	rewrite := func() schemaflux.RewriteOptions {
		o := schemaflux.NewRewriteOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Audience = leakCanaryOption
		o.StyleGuide = leakCanaryOption
		o.Changes = list
		o.AvoidWords = list
		o.IncludeWords = list
		return o
	}
	translate := func() schemaflux.TranslateOptions {
		o := schemaflux.NewTranslateOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.TargetLanguage = leakCanaryOption
		o.SourceLanguage = leakCanaryOption + " source"
		o.Glossary = hints
		o.Dialect = leakCanaryOption
		return o
	}
	expand := func() schemaflux.ExpandOptions {
		o := schemaflux.NewExpandOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.ElaborateOn = list
		o.AddContext = list
		return o
	}
	rows["SummarizeWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.SummarizeWithMetadata(leakText, summarize()))
	}}
	rows["SummarizeResult"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.SummarizeResult(leakText, summarize()))
	}}
	rows["Rewrite"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Rewrite(leakText, rewrite())) }}
	rows["RewriteWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.RewriteWithMetadata(leakText, rewrite()))
	}}
	rows["RewriteResult"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.RewriteResult(leakText, rewrite()))
	}}
	rows["Translate"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Translate(leakText, translate())) }}
	rows["TranslateWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.TranslateWithMetadata(leakText, translate()))
	}}
	rows["TranslateResult"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.TranslateResult(leakText, translate()))
	}}
	rows["Expand"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Expand(leakText, expand())) }}
	rows["ExpandWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ExpandWithMetadata(leakText, expand()))
	}}
	rows["ExpandResult"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ExpandResult(leakText, expand()))
	}}
}

func leakGenerationRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	rows["Complete"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewCompleteOptions()
		o.Context = ctx
		o.Steering = leakCanaryOption
		o.Background = list
		o.StopSequences = list
		return errOf(schemaflux.Complete(leakText, o))
	}}
	rows["CompleteField"] = leakRow{call: func(ctx context.Context) error {
		o := schemaflux.NewCompleteFieldOptions("CANARY_FIELD_5d2b_Notes")
		o.Context = ctx
		o.Steering = leakCanaryOption
		o.Background = list
		return errOf(schemaflux.CompleteField(leakIn, o))
	}}
	rows["Suggest"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewSuggestOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Domain = leakCanaryOption
		o.Constraints = list
		o.Categories = list
		return errOf(schemaflux.Suggest[leakOutput](leakText, o))
	}}
	rows["Annotate"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewAnnotateOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.AnnotationTypes = list
		o.CustomSchema = hints
		o.Domain = leakCanaryOption
		return errOf(schemaflux.Annotate(leakText, o))
	}}
	rows["Explain"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewExplainOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.Background = leakCanaryOption
		o.Focus = leakCanaryOption
		o.Audience = leakCanaryOption
		return errOf(schemaflux.Explain(leakIn, o))
	}}
	rows["Question"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewQuestionOptions(leakAsk)
		o.CommonOptions.Steering = leakCanaryOption
		return errOf(schemaflux.Question[leakRecord, leakOutput](leakIn, o))
	}}
	rows["QuestionLegacy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.QuestionLegacy(leakIn, leakAsk, schemaflux.OpOptions{Steering: leakCanaryOption}))
	}}
	rows["Synthesize"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewSynthesizeOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.FocusAreas = list
		o.ExcludeAspects = list
		return errOf(schemaflux.Synthesize[leakOutput]([]any{leakIn, leakInAlt}, o))
	}}
	rows["Predict"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewPredictOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Horizon = leakCanaryOption
		o.Factors = list
		o.Assumptions = list
		return errOf(schemaflux.Predict[leakOutput](leakItems, o))
	}}
	rows["Assemble"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ComposeOptions{Steering: leakCanaryOption, Template: leakCanaryOption, MergeStrategy: leakCanaryOption}
		return errOf(schemaflux.Assemble[leakOutput]([]any{leakIn, leakInAlt}, o))
	}}
	rows["Derive"] = leakRow{call: func(context.Context) error {
		o := schemaflux.DeriveOptions{Steering: leakCanaryOption, Fields: list, Rules: hints}
		return errOf(schemaflux.Derive[leakRecord, leakOutput](leakIn, o))
	}}
	rows["Interpolate"] = leakRow{call: func(context.Context) error {
		o := schemaflux.InterpolateOptions{Steering: leakCanaryOption, Constraints: list, SequenceField: leakCanaryOption}
		return errOf(schemaflux.Interpolate(leakItems, o))
	}}
}

func leakClassificationRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	rows["Cluster"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewClusterOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.ClusterBy = leakCanaryOption
		return errOf(schemaflux.Cluster(leakItems, o))
	}}
	rows["Compare"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewCompareOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.ComparisonAspects = list
		return errOf(schemaflux.Compare(leakIn, leakInAlt, o))
	}}
	rows["Similar"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewSimilarOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.Aspects = list
		return errOf(schemaflux.Similar(leakIn, leakInAlt, o))
	}}
	rows["CheckingSimilarity"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.CheckingSimilarity(leakIn, leakInAlt).Aspects(leakCanaryOption).Run())
	}}
	rows["Diff"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewDiffOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.Background = leakCanaryOption
		o.IgnoreFields = list
		return errOf(schemaflux.Diff(leakIn, leakInAlt, o))
	}}
	rows["Deduplicate"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.Deduplicate(leakItems, 0.5, schemaflux.OpOptions{Steering: leakCanaryOption}))
	}}
	_ = hints
}

func leakSelectionRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	rows["Choose"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewChooseOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = list
		return errOf(schemaflux.Choose(leakItems, o))
	}}
	rows["ChooseBy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ChooseBy(leakItems, list...))
	}}
	rows["Filter"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewFilterOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = leakCanaryOption
		return errOf(schemaflux.Filter(leakItems, o))
	}}
	rows["FilterBy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.FilterBy(leakItems, leakCanaryOption))
	}}
	rows["Sort"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewSortOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = leakCanaryOption
		o.SecondaryCriteria = list
		return errOf(schemaflux.Sort(leakItems, o))
	}}
	rows["SortBy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.SortBy(leakItems, leakCanaryOption))
	}}
	rows["SortResult"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewSortOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = leakCanaryOption
		return errOf(schemaflux.SortResult(leakItems, o))
	}}
	rows["Rank"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewRankOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Query = leakCanaryOption
		o.RankingFactors = list
		return errOf(schemaflux.Rank(leakItems, o))
	}}
	rows["MatchOne"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewMatchOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.MatchCriteria = leakCanaryOption
		o.MatchFields = list
		return errOf(schemaflux.MatchOne(leakIn, leakItems, o))
	}}
	rows["SemanticMatch"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewMatchOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.MatchCriteria = leakCanaryOption
		o.MatchFields = list
		return errOf(schemaflux.SemanticMatch(leakItems[:2], leakItems, o))
	}}
	rows["Resolve"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ResolveOptions{Steering: leakCanaryOption, FieldPriorities: map[string]int{leakCanaryOption: 1}}
		return errOf(schemaflux.Resolve(leakItems, o))
	}}
	rows["Arbitrate"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ArbitrateOptions{Steering: leakCanaryOption, Rules: list, Tiebreaker: leakCanaryOption}
		return errOf(schemaflux.Arbitrate(leakItems, o))
	}}
	rows["Settle"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NegotiateOptions{Steering: leakCanaryOption, Constraints: list, Priorities: map[string]float64{leakCanaryOption: 1}}
		return errOf(schemaflux.Settle[leakOutput](leakItems, o))
	}}
	rows["Negotiate"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NegotiateOptions{Steering: leakCanaryOption, Constraints: list, Priorities: map[string]float64{leakCanaryOption: 1}}
		return errOf(schemaflux.Negotiate[leakOutput](leakItems, o))
	}}
	adversarial := func() schemaflux.AdversarialContext[leakOutput] {
		return schemaflux.AdversarialContext[leakOutput]{
			Ours:         schemaflux.AdversarialPosition[leakOutput]{Position: leakOutput{Headline: leakCanaryInput}, MustHaves: list},
			Theirs:       schemaflux.AdversarialPosition[leakOutput]{Position: leakOutput{Headline: leakCanaryInput + " theirs"}, MustHaves: list},
			OurLeverage:  "balanced",
			Relationship: "mixed",
		}
	}
	rows["SettleAdversarial"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.SettleAdversarial(adversarial(), schemaflux.AdversarialOptions{Steering: leakCanaryOption}))
	}}
	rows["NegotiateAdversarial"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.NegotiateAdversarial(adversarial(), schemaflux.AdversarialOptions{Steering: leakCanaryOption}))
	}}
	_ = hints
}

// leakAssessment is the questions of an Assess call. The questions and the
// labels are the caller's words, written in the type's tags.
type leakAssessment struct {
	Matches bool   `ask:"Is this CANARY_OPTION_5d2b?"`
	Team    string `ask:"Which CANARY_OPTION_5d2b team?" oneof:"CANARY_OPTION_5d2b_a,CANARY_OPTION_5d2b_b"`
	Level   int    `ask:"How CANARY_OPTION_5d2b is it?" scale:"1-5"`
}

func leakTransformationRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	rows["Parse"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewParseOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.AllowLLMFallback = true
		o.FormatHints = list
		o.CustomDelimiters = list
		return errOf(schemaflux.Parse[leakRecord](leakText, o))
	}}
	rows["Infer"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewInferOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.Background = leakCanaryOption
		return errOf(schemaflux.Infer(leakIn, o))
	}}
	normalize := func() schemaflux.NormalizeOptions {
		o := schemaflux.NewNormalizeOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Standard = leakCanaryOption
		o.Rules = hints
		o.CanonicalMappings = hints
		o.Fields = list
		o.Locale = leakCanaryOption
		return o
	}
	rows["Normalize"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Normalize(leakIn, normalize())) }}
	rows["NormalizeText"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.NormalizeText(leakText, normalize()))
	}}
	rows["NormalizeBatch"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.NormalizeBatch(leakItems, normalize()))
	}}
	rows["Redact"] = leakRow{call: func(context.Context) error {
		o := schemaflux.NewRedactOptions()
		o.OpOptions.Steering = leakCanaryOption
		o.Categories = list
		o.CustomPatterns = list
		o.MaskText = leakCanaryOption
		return errOf(schemaflux.Redact(leakIn, o))
	}}
	redactLLM := func(ctx context.Context) schemaflux.RedactLLMOptions {
		o := schemaflux.NewRedactLLMOptions()
		o.Context = ctx
		o.OpOptions.Steering = leakCanaryOption
		o.Categories = list
		return o
	}
	rows["RedactLLM"] = leakRow{call: func(ctx context.Context) error { return errOf(schemaflux.RedactLLM(leakText, redactLLM(ctx))) }}
	rows["RedactLLMWithMeta"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.RedactLLMWithMeta(leakText, redactLLM(ctx)))
	}}
	op := schemaflux.OpOptions{Steering: leakCanaryOption}
	rows["Format"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Format(leakIn, leakCanaryOption, op)) }}
	rows["FormatWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.FormatWithMetadata(leakIn, leakCanaryOption, op))
	}}
	rows["Merge"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Merge(leakItems, leakCanaryOption, op)) }}
	rows["MergeWithMetadata"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.MergeWithMetadata(leakItems, leakCanaryOption, op))
	}}
	compress := func() schemaflux.CompressOptions {
		o := schemaflux.NewCompressOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.RetainFields = list
		o.RemoveFields = list
		o.PreserveInfo = list
		o.Priority = leakCanaryOption
		return o
	}
	rows["Compress"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Compress(leakIn, compress())) }}
	rows["CompressText"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.CompressText(leakText, compress())) }}
	rows["Conform"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ConformOptions{Steering: leakCanaryOption, CustomRules: hints}
		return errOf(schemaflux.Conform(leakIn, leakCanaryOption, o))
	}}
	rows["Project"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ProjectOptions{Steering: leakCanaryOption, Mappings: hints, Exclude: list}
		return errOf(schemaflux.Project[leakRecord, leakOutput](leakIn, o))
	}}
	rows["Pivot"] = leakRow{call: func(context.Context) error {
		o := schemaflux.PivotOptions{Steering: leakCanaryOption, PivotOn: list, GroupBy: list, Aggregate: leakCanaryOption}
		return errOf(schemaflux.Pivot[leakRecord, leakOutput](leakIn, o))
	}}
	enrich := func() schemaflux.EnrichOptions {
		o := schemaflux.NewEnrichOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.DeriveFields = list
		o.DerivationRules = hints
		o.Context = map[string]any{leakCanaryOption: leakCanaryOption}
		o.ExcludeFields = list
		o.Domain = leakCanaryOption
		return o
	}
	rows["Enrich"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.Enrich[leakRecord, leakOutput](leakIn, enrich()))
	}}
	rows["EnrichInPlace"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.EnrichInPlace(leakIn, enrich())) }}
	decompose := func() schemaflux.DecomposeOptions {
		o := schemaflux.NewDecomposeOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.DecomposeBy = leakCanaryOption
		return o
	}
	rows["Decompose"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Decompose(leakIn, decompose())) }}
	rows["DecomposeToSlice"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.DecomposeToSlice[leakRecord, leakOutput](leakIn, decompose()))
	}}
}

func leakReviewRows(rows map[string]leakRow) {
	hints, list := leakHints, leakList

	validate := func() schemaflux.ValidateOptions {
		o := schemaflux.NewValidateOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Rules = leakCanaryOption
		o.SchemaHints = hints
		return o
	}
	rows["Validate"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Validate(leakIn, validate())) }}
	rows["ValidateDeterministically"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ValidateDeterministically(leakIn, validate()))
	}}
	rows["ValidateHybrid"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.ValidateHybrid(leakIn, validate())) }}
	rows["ValidateLegacy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ValidateLegacy(leakIn, leakCanaryOption, schemaflux.OpOptions{Steering: leakCanaryOption}))
	}}
	verify := func() schemaflux.VerifyOptions {
		o := schemaflux.NewVerifyOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Sources = []any{leakCanaryOption}
		o.TrustedSources = list
		o.Domain = leakCanaryOption
		return o
	}
	rows["Verify"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Verify(leakText, verify())) }}
	rows["VerifyClaim"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.VerifyClaim(leakText, verify())) }}
	rows["VerifyWithModel"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.VerifyWithModel(leakText, verify())) }}
	audit := func() schemaflux.AuditOptions {
		return schemaflux.AuditOptions{Steering: leakCanaryOption, Policies: list, Categories: list}
	}
	rows["Audit"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Audit(leakIn, audit())) }}
	rows["AuditWithModel"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.AuditWithModel(leakIn, audit())) }}
	rows["AuditWithMeta"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.AuditWithMeta(leakIn, audit())) }}
	critique := func() schemaflux.CritiqueOptions {
		o := schemaflux.NewCritiqueOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Criteria = list
		o.Rubric = hints
		o.Domain = leakCanaryOption
		o.Audience = leakCanaryOption
		o.Style = leakCanaryOption
		return o
	}
	rows["Critique"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Critique(leakIn, critique())) }}
	rows["CritiqueWithModel"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.CritiqueWithModel(leakIn, critique()))
	}}
	rows["Guard"] = leakRow{call: func(ctx context.Context) error {
		schemaflux.Guard(ctx, leakIn,
			func(leakRecord) (bool, string) { return false, leakCanaryOption + " check failed" },
			func(leakRecord) (bool, string) { return true, "" })
		return nil
	}}
	rows["Approve"] = leakRow{call: func(ctx context.Context) error {
		step := func(context.Context) (string, error) { return leakCanaryAnswer, nil }
		gate := func(context.Context, string) (schemaflux.ApprovalOutcome, error) {
			return schemaflux.ApprovalOutcome{Approved: true, Evidence: list}, nil
		}
		return errOf(schemaflux.Approve(ctx, step, gate, list))
	}}
	rows["Vote"] = leakRow{call: func(ctx context.Context) error {
		step := func(context.Context) (string, error) { return leakCanaryAnswer, nil }
		_, _, err := schemaflux.Vote(ctx, 3, step, schemaflux.ExactAgreement[string](2), schemaflux.VoteOptions{})
		return err
	}}
}

func leakJudgmentRows(rows map[string]leakRow) {
	list := leakList
	op := schemaflux.OpOptions{Steering: leakCanaryOption}

	rows["Is"] = leakRow{call: func(ctx context.Context) error { return errOf(schemaflux.Is(ctx, leakIn, leakAsk, op)) }}
	rows["IsResult"] = leakRow{call: func(ctx context.Context) error { return errOf(schemaflux.IsResult(ctx, leakIn, leakAsk, op)) }}
	rows["Which"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.Which(ctx, leakIn, leakAsk, list, op))
	}}
	rows["WhichResult"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.WhichResult(ctx, leakIn, leakAsk, list, op))
	}}
	rows["WhichOrNone"] = leakRow{call: func(ctx context.Context) error {
		_, _, err := schemaflux.WhichOrNone(ctx, leakIn, leakAsk, list, op)
		return err
	}}
	rows["WhichOrNoneResult"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.WhichOrNoneResult(ctx, leakIn, leakAsk, list, op))
	}}
	rows["Rate"] = leakRow{call: func(ctx context.Context) error { return errOf(schemaflux.Rate(ctx, leakIn, leakAsk, 1, 5, op)) }}
	rows["RateResult"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.RateResult(ctx, leakIn, leakAsk, 1, 5, op))
	}}
	rows["Assess"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.Assess[leakAssessment](ctx, leakIn, op))
	}}
	rows["AssessResult"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.AssessResult[leakAssessment](ctx, leakIn, op))
	}}
	step := func(context.Context) (string, error) { return leakCanaryAnswer, nil }
	rows["Whether"] = leakRow{call: func(ctx context.Context) error {
		return errOf(schemaflux.Whether(ctx, leakIn, leakAsk, step, step, op))
	}}
	rows["RouteBy"] = leakRow{call: func(ctx context.Context) error {
		routes := []schemaflux.Route[string]{schemaflux.To(leakCanaryOption, step), schemaflux.To(leakCanaryOption+" two", step)}
		return errOf(schemaflux.RouteBy(ctx, leakIn, leakAsk, routes, op))
	}}
	decisions := []schemaflux.Decision[string]{
		{Value: leakCanaryOption + " first", Description: leakCanaryOption + " first"},
		{Value: leakCanaryOption + " second", Description: leakCanaryOption + " second"},
	}
	rows["Decide"] = leakRow{call: func(ctx context.Context) error {
		_, _, err := schemaflux.Decide(ctx, leakIn, decisions, schemaflux.DecideOptions{OpOptions: op})
		return err
	}}
	rows["Match"] = leakRow{call: func(ctx context.Context) error {
		_, err := schemaflux.Match(ctx, leakIn,
			schemaflux.Like(leakCanaryOption, func() {}),
			schemaflux.Otherwise(func() {}))
		return err
	}}
}

var _ = fmt.Sprint

func tailOf(s string) string { // TEMPDEV
	if i := strings.Index(s, "After reasoning about the task"); i >= 0 { // TEMPDEV
		if j := strings.Index(s[i:], "Every field must be populated"); j >= 0 { // TEMPDEV
			s = s[i+j+100:] // TEMPDEV
		} // TEMPDEV
	} // TEMPDEV
	if len(s) > 900 { // TEMPDEV
		s = s[len(s)-900:] // TEMPDEV
	} // TEMPDEV
	return s // TEMPDEV
} // TEMPDEV
