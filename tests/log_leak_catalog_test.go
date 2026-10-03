package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// are the ones read. Each operation is then run twice more, with a model that
// answers in no shape the operation reads and with a provider that fails with a
// message quoting its input, because the lines about a refusal and about a
// failed call are written where the model's own words are closest to hand.
//
// What the logger saw is searched in the text handler's output and in the JSON
// handler's, and a failure names the operation and prints the line.
const (
	leakCanaryInput  = "CANARY_INPUT_5d2b"
	leakCanaryOption = "CANARY_OPTION_5d2b"
	leakCanaryAnswer = "CANARY_ANSWER_5d2b"
	leakCanaryField  = "CANARY_FIELD_5d2b"
	leakCanaryVendor = "CANARY_VENDOR_5d2b"
)

var leakCanaries = []string{leakCanaryInput, leakCanaryOption, leakCanaryAnswer, leakCanaryField, leakCanaryVendor}

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
	CANARY_FIELD_5d2b_Name  string `json:"CANARY_FIELD_5d2b_name"`
	CANARY_FIELD_5d2b_Notes string `json:"CANARY_FIELD_5d2b_notes"`
}

// leakOutput is the type a call is made into.
type leakOutput struct {
	CANARY_FIELD_5d2b_Headline string `json:"CANARY_FIELD_5d2b_headline"`
	CANARY_FIELD_5d2b_Detail   string `json:"CANARY_FIELD_5d2b_detail"`
}

var (
	leakIn    = leakRecord{leakCanaryInput, leakCanaryInput + " notes"}
	leakInAlt = leakRecord{leakCanaryInput + " other", leakCanaryInput + " other notes"}
	leakItems = []leakRecord{leakIn, leakInAlt, {leakCanaryInput + " third", leakCanaryInput + " third notes"}}
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
	fail   error // when set, every request fails with it
	sent   int
	// lastAsk is the end of the last system prompt it was sent, which is where
	// an operation says what shape it reads. It is kept for the message of a
	// scripted answer that was refused.
	lastAsk string
}

func (m *scriptedModel) Complete(_ context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	m.mu.Lock()
	m.sent++
	if m.fail != nil {
		m.mu.Unlock()
		return schemaflux.CompletionResponse{}, m.fail
	}
	m.lastAsk = req.SystemPrompt
	if len(m.lastAsk) > 600 {
		m.lastAsk = m.lastAsk[len(m.lastAsk)-600:]
	}
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
	// asksNoModel says the operation decides in Go and sends nothing to a
	// model, so the scripted answer is never read. A row that says this must in
	// fact ask none, and a row that does not must ask one: otherwise the call
	// succeeded without reaching the lines the answer is there to reach.
	asksNoModel bool
	// refusedOption makes the call again with the canary where the operation
	// reads an option that has to be one of a few words, and so refuses it.
	// The refusal is written before any model is asked. Nil for an operation
	// with no such option.
	refusedOption func(ctx context.Context) error
}

// leakExempt is the catalogue's operations that are not driven, each with the
// reason. An operation is exempt when there is no model in it, and nothing for
// a model to answer, not when its answer is hard to script.
var leakExempt = map[string]string{
	"Chunk": "splits a slice by a size; asks no model and logs nothing",
}

// leakPaths are the three ways a call can go, each of which writes lines of its
// own: the model answers as the row scripts it, the model answers with text no
// operation can read, and the provider fails with a message that quotes what it
// was sent, as a vendor's does. The fourth is the refusal of an option the
// caller wrote and the operation could not accept.
var leakPaths = []string{"answered", "garbled answer", "provider failure", "refused option"}

// logMustNotHold runs one row down one of the paths under a logger that writes
// every level, and fails when a canary is in a line.
func logMustNotHold(t *testing.T, name, path string, row leakRow) {
	t.Helper()
	log := &logAtEveryLevel{}
	previous := telemetry.GetLogger()
	telemetry.SetLogger(telemetry.FromSlog(log.logger()))
	config.SetDebugMode(true)
	model := &scriptedModel{answer: row.answer}
	switch path {
	case "garbled answer":
		model.answer = replyText(leakCanaryAnswer + " is not an answer in any shape")
	case "provider failure":
		model.fail = errors.New("vendor " + leakCanaryVendor + " refused: " + leakCanaryInput + " is not allowed")
	}
	call := row.call
	if path == "refused option" {
		if row.refusedOption == nil {
			t.Skipf("%s has no option that has to be one of a few words", name)
		}
		call = row.refusedOption
	}
	client := schemaflux.NewClient("").WithProviderInstance(model)
	t.Cleanup(func() {
		config.SetDebugMode(false)
		ops.SetDefaultProvider(nil)
		schemaflux.SetDefaultClient(nil)
		telemetry.SetLogger(previous)
	})

	err := call(client.Context(context.Background()))
	switch {
	case path == "refused option" && err == nil:
		t.Errorf("%s: the option the row sets to a canary was accepted, so no refusal was read", name)
	case path == "answered" && err != nil:
		t.Errorf("%s: the scripted answer was refused, so the success path was not read: %v\n    the end of what it was asked: %q", name, err, model.lastAsk)
	case path == "answered" && model.sent == 0 && !row.asksNoModel:
		t.Errorf("%s: the call asked no model, so the answer it was scripted with was never read; say asksNoModel if that is right", name)
	case path == "answered" && model.sent > 0 && row.asksNoModel:
		t.Errorf("%s: the row says the call asks no model and it asked one; drop asksNoModel", name)
	}
	for _, line := range log.leaks() {
		t.Errorf("%s (%s): a log line holds what a caller, a model or a provider wrote:\n    %s", name, path, line)
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
		t.Run(name, func(t *testing.T) {
			for _, path := range leakPaths {
				t.Run(path, func(t *testing.T) { logMustNotHold(t, name, path, rows[name]) })
			}
		})
	}
}

// The test above is only as good as its eye: a canary in a text line and in a
// JSON line have to be found, a line without one must not be reported.
func TestTheLeakCatalogSeesACanaryInATextLineAndInAJSONLine(t *testing.T) {
	cases := []struct {
		name  string
		write func(*slog.Logger)
		want  int
	}{
		{"an attribute of a debug line", func(l *slog.Logger) { l.Debug("done", "category", leakCanaryOption) }, 2},
		{"the message", func(l *slog.Logger) { l.Info("saw " + leakCanaryInput) }, 2},
		{"an error attribute", func(l *slog.Logger) { l.Error("failed", "error", errors.New(leakCanaryVendor)) }, 2},
		{"a nested group", func(l *slog.Logger) { l.Warn("w", slog.Group("g", slog.String("k", leakCanaryAnswer))) }, 2},
		{"a field name used as a key", func(l *slog.Logger) { l.Debug("d", leakCanaryField, 1) }, 2},
		{"a counted line", func(l *slog.Logger) { l.Debug("completed", "count", 3, "position", 2) }, 0},
		{"a length", func(l *slog.Logger) { l.Debug("input", "inputLength", 120) }, 0},
		{"nothing at all", func(*slog.Logger) {}, 0},
		{"two lines, one clean", func(l *slog.Logger) { l.Debug("a", "k", 1); l.Debug("b", "k", leakCanaryOption) }, 2},
		{"the canary split over two attributes", func(l *slog.Logger) { l.Debug("a", "k", "CANARY_", "j", "OPTION_5d2b") }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &logAtEveryLevel{}
			tc.write(log.logger())
			if got := len(log.leaks()); got != tc.want {
				t.Errorf("%d leaking lines found (text and JSON together), want %d\ntext: %s\njson: %s", got, tc.want, log.text.String(), log.json.String())
			}
		})
	}
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
	// These decide in Go: a deterministic redaction, the rules of a validation
	// that name no free text, and the control flow that runs a step the caller
	// wrote. None sends anything to a model.
	for _, name := range []string{"Redact", "ValidateDeterministically", "Guard", "Approve", "Vote"} {
		row, ok := rows[name]
		if !ok {
			panic("leakRows has no row for " + name)
		}
		row.asksNoModel = true
		rows[name] = row
	}
	for name, refusal := range leakRefusedOptions() {
		row, ok := rows[name]
		if !ok {
			panic("leakRefusedOptions has " + name + ", which has no row")
		}
		row.refusedOption = refusal
		rows[name] = row
	}
	for name, answer := range leakAnswers() {
		row, ok := rows[name]
		if !ok {
			panic("leakAnswers scripts " + name + ", which has no row")
		}
		row.answer = answer
		rows[name] = row
	}
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
		o.AvoidWords = []string{leakCanaryOption + " avoid"}
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
		o.Focus = "overview"
		o.Audience = "technical"
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
		o := schemaflux.ComposeOptions{Steering: leakCanaryOption, Template: leakCanaryOption, MergeStrategy: "first", FillGaps: true}
		return errOf(schemaflux.Assemble[leakOutput]([]any{leakIn, leakInAlt}, o))
	}}
	rows["Derive"] = leakRow{call: func(context.Context) error {
		o := schemaflux.DeriveOptions{Steering: leakCanaryOption, Fields: []string{"CANARY_FIELD_5d2b_headline"}, Rules: hints}
		return errOf(schemaflux.Derive[leakRecord, leakOutput](leakIn, o))
	}}
	rows["Interpolate"] = leakRow{call: func(context.Context) error {
		o := schemaflux.InterpolateOptions{Steering: leakCanaryOption, Constraints: list, SequenceField: leakCanaryOption}
		o.GapIndices = []int{1}
		return errOf(schemaflux.Interpolate([]leakRecord{leakIn, {}, leakInAlt}, o))
	}}
}

func leakClassificationRows(rows map[string]leakRow) {
	list := leakList

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
}

func leakSelectionRows(rows map[string]leakRow) {
	list := leakList

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
		o := schemaflux.ArbitrateOptions{Steering: leakCanaryOption, Rules: list, Tiebreaker: "first"}
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
			Ours:         schemaflux.AdversarialPosition[leakOutput]{Position: leakOutput{CANARY_FIELD_5d2b_Headline: leakCanaryInput}, MustHaves: list},
			Theirs:       schemaflux.AdversarialPosition[leakOutput]{Position: leakOutput{CANARY_FIELD_5d2b_Headline: leakCanaryInput + " theirs"}, MustHaves: list},
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
}

// leakRefusedOptions are the calls made with the canary in an option that has to
// be one of a few words. The operation refuses the call, and what it writes
// about the refusal is read.
func leakRefusedOptions() map[string]func(ctx context.Context) error {
	o := leakCanaryOption
	list := leakList
	critique := func() schemaflux.CritiqueOptions {
		opts := schemaflux.NewCritiqueOptions()
		opts.Criteria = list
		opts.Style = o
		return opts
	}
	compress := func() schemaflux.CompressOptions {
		opts := schemaflux.NewCompressOptions()
		opts.Strategy = o
		return opts
	}
	decompose := func() schemaflux.DecomposeOptions {
		opts := schemaflux.NewDecomposeOptions()
		opts.Strategy = o
		return opts
	}
	enrich := func() schemaflux.EnrichOptions {
		opts := schemaflux.NewEnrichOptions()
		opts.DeriveFields = list
		opts.Depth = o
		return opts
	}
	match := func() schemaflux.MatchOptions {
		opts := schemaflux.NewMatchOptions()
		opts.Strategy = o
		return opts
	}
	normalize := func() schemaflux.NormalizeOptions {
		opts := schemaflux.NewNormalizeOptions()
		opts.NormalizeCase = o
		return opts
	}
	summarize := func() schemaflux.SummarizeOptions {
		opts := schemaflux.NewSummarizeOptions()
		opts.LengthUnit = o
		return opts
	}
	verify := func() schemaflux.VerifyOptions {
		opts := schemaflux.NewVerifyOptions()
		opts.Strictness = o
		return opts
	}
	return map[string]func(context.Context) error{
		"Annotate": func(context.Context) error {
			opts := schemaflux.NewAnnotateOptions()
			opts.Format = o
			return errOf(schemaflux.Annotate(leakText, opts))
		},
		"Cluster": func(context.Context) error {
			opts := schemaflux.NewClusterOptions()
			opts.NamingStrategy = o
			return errOf(schemaflux.Cluster(leakItems, opts))
		},
		"Compress":          func(context.Context) error { return errOf(schemaflux.Compress(leakIn, compress())) },
		"CompressText":      func(context.Context) error { return errOf(schemaflux.CompressText(leakText, compress())) },
		"Critique":          func(context.Context) error { return errOf(schemaflux.Critique(leakIn, critique())) },
		"CritiqueWithModel": func(context.Context) error { return errOf(schemaflux.CritiqueWithModel(leakIn, critique())) },
		"Decompose":         func(context.Context) error { return errOf(schemaflux.Decompose(leakIn, decompose())) },
		"DecomposeToSlice": func(context.Context) error {
			return errOf(schemaflux.DecomposeToSlice[leakRecord, leakOutput](leakIn, decompose()))
		},
		"Enrich": func(context.Context) error {
			return errOf(schemaflux.Enrich[leakRecord, leakOutput](leakIn, enrich()))
		},
		"EnrichInPlace": func(context.Context) error { return errOf(schemaflux.EnrichInPlace(leakIn, enrich())) },
		"Explain": func(context.Context) error {
			opts := schemaflux.NewExplainOptions()
			opts.Focus = o
			return errOf(schemaflux.Explain(leakIn, opts))
		},
		"MatchOne": func(context.Context) error { return errOf(schemaflux.MatchOne(leakIn, leakItems, match())) },
		"SemanticMatch": func(context.Context) error {
			return errOf(schemaflux.SemanticMatch(leakItems[:2], leakItems, match()))
		},
		"Normalize":      func(context.Context) error { return errOf(schemaflux.Normalize(leakIn, normalize())) },
		"NormalizeText":  func(context.Context) error { return errOf(schemaflux.NormalizeText(leakText, normalize())) },
		"NormalizeBatch": func(context.Context) error { return errOf(schemaflux.NormalizeBatch(leakItems, normalize())) },
		"Transform": func(context.Context) error {
			opts := schemaflux.NewTransformOptions()
			opts.MergeStrategy = o
			return errOf(schemaflux.Transform[leakRecord, leakOutput](leakIn, opts))
		},
		"Summarize":             func(context.Context) error { return errOf(schemaflux.Summarize(leakText, summarize())) },
		"SummarizeWithMetadata": func(context.Context) error { return errOf(schemaflux.SummarizeWithMetadata(leakText, summarize())) },
		"SummarizeResult":       func(context.Context) error { return errOf(schemaflux.SummarizeResult(leakText, summarize())) },
		"Compare": func(context.Context) error {
			opts := schemaflux.NewCompareOptions()
			opts.FocusOn = o
			return errOf(schemaflux.Compare(leakIn, leakInAlt, opts))
		},
		"Choose": func(context.Context) error {
			opts := schemaflux.NewChooseOptions()
			opts.Criteria = list
			opts.Strategy = o
			return errOf(schemaflux.Choose(leakItems, opts))
		},
		"SortResult": func(context.Context) error {
			opts := schemaflux.NewSortOptions()
			opts.Criteria = o
			opts.Direction = o
			return errOf(schemaflux.SortResult(leakItems, opts))
		},
		"Predict": func(context.Context) error {
			opts := schemaflux.NewPredictOptions()
			opts.Method = o
			return errOf(schemaflux.Predict[leakOutput](leakItems, opts))
		},
		"Suggest": func(context.Context) error {
			opts := schemaflux.NewSuggestOptions()
			opts.Strategy = schemaflux.SuggestStrategy(o)
			return errOf(schemaflux.Suggest[leakOutput](leakText, opts))
		},
		"Synthesize": func(context.Context) error {
			opts := schemaflux.NewSynthesizeOptions()
			opts.Strategy = o
			return errOf(schemaflux.Synthesize[leakOutput]([]any{leakIn, leakInAlt}, opts))
		},
		"Verify":          func(context.Context) error { return errOf(schemaflux.Verify(leakText, verify())) },
		"VerifyClaim":     func(context.Context) error { return errOf(schemaflux.VerifyClaim(leakText, verify())) },
		"VerifyWithModel": func(context.Context) error { return errOf(schemaflux.VerifyWithModel(leakText, verify())) },
	}
}

// replyWith is a model that answers every request with the same JSON.
func replyWith(value any) func(schemaflux.CompletionRequest) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return func(schemaflux.CompletionRequest) string { return string(raw) }
}

// replyText is a model that answers every request with the same text.
func replyText(text string) func(schemaflux.CompletionRequest) string {
	return func(schemaflux.CompletionRequest) string { return text }
}

type leakObject = map[string]any

// leakAnswers are the replies the operations whose answer the request's schema
// does not describe are scripted with. Each is what the operation's prompt asks
// the model to return, with the answer canary in every string a model writes and
// the option canary where the answer has to name something the caller wrote (a
// category, an aspect, a constraint).
func leakAnswers() map[string]func(schemaflux.CompletionRequest) string {
	a, o := leakCanaryAnswer, leakCanaryOption
	record := leakObject{"CANARY_FIELD_5d2b_name": a, "CANARY_FIELD_5d2b_notes": a}
	output := leakObject{"CANARY_FIELD_5d2b_headline": a, "CANARY_FIELD_5d2b_detail": a}
	span := strings.Index(leakText, leakCanaryInput)
	matches := func(sources int) any {
		var found []leakObject
		for i := 0; i < sources; i++ {
			found = append(found, leakObject{"source_index": i, "target_index": i, "score": 0.9, "explanation": a, "is_partial": false})
		}
		unmatched := []int{}
		for i := sources; i < 3; i++ {
			unmatched = append(unmatched, i)
		}
		return leakObject{"matches": found, "unmatched_sources": []int{}, "unmatched_targets": unmatched}
	}
	rewritten := o + " " + o + " two " + a
	rewrite := leakObject{"text": rewritten, "changes_made": []string{a}, "tone_achieved": a, "confidence": 0.8}
	summary := leakObject{"text": a, "key_points": []string{a}, "confidence": 0.8}
	translation := leakObject{"text": a, "source_language_detected": a, "confidence": 0.9,
		"alternatives": []leakObject{{"phrase": a, "alternative": a, "context": a}}}
	expansion := leakObject{"text": a, "added_content": []string{a}, "confidence": 0.9}
	negotiated := leakObject{"solution": output, "satisfaction": leakObject{o: 0.9, o + " two": 0.8}, "overall_satisfaction": 0.9,
		"tradeoffs":    []leakObject{{"sacrificed": a, "gained": a, "impact": "low", "reasoning": a}},
		"alternatives": []any{output}, "reasoning": a, "confidence": 0.8}
	deal := leakObject{"deal": output, "deal_reached": true,
		"term_movements":    []leakObject{{"term": "CANARY_FIELD_5d2b_headline", "our_ask": a, "their_offer": a, "final_value": a, "movement": "split"}},
		"who_conceded_more": "equal", "our_satisfaction": 0.8, "their_satisfaction": 0.8, "confidence": 0.8}
	normalized := func(value any) any {
		return leakObject{"normalized": value,
			"changes":  []leakObject{{"field": "CANARY_FIELD_5d2b_name", "original": a, "normalized": a, "reason": a, "confidence": 0.9}},
			"warnings": []string{a}}
	}
	critique := leakObject{"overall_score": 0.7, "criteria_scores": leakObject{o: 0.7, o + " two": 0.6}, "summary": a,
		"top_priorities": []string{a},
		"issues":         []leakObject{{"criterion": o, "severity": "major", "description": a, "location": a, "suggestion": a, "fix": a, "impact": a}},
		"positives":      []leakObject{{"criterion": o, "description": a, "strength": a}}}
	validation := leakObject{
		"errors":    []leakObject{{"field": "CANARY_FIELD_5d2b_name", "message": a, "suggestion": a, "explanation": a}},
		"warnings":  []leakObject{{"field": "CANARY_FIELD_5d2b_notes", "message": a, "suggestion": a, "explanation": a}},
		"info":      []leakObject{{"field": nil, "message": a, "suggestion": a, "explanation": a}},
		"corrected": nil, "confidence": 0.8, "summary": a}

	return map[string]func(schemaflux.CompletionRequest) string{
		"Annotate": replyWith(leakObject{
			"annotations": []leakObject{{"type": o, "value": a, "text": leakCanaryInput, "start": span, "end": span + len(leakCanaryInput), "confidence": 0.9, "metadata": leakObject{}}},
			"summary":     leakObject{"overall_sentiment": a, "main_topics": []string{a}, "entity_count": 1}}),
		"Choose":   replyWith(leakObject{"id": "i-000001"}),
		"ChooseBy": replyWith(leakObject{"id": "i-000001"}),
		"Classify": replyWith(leakObject{"category": o, "confidence": 0.9, "reasoning": a,
			"alternatives": []leakObject{{"category": o + " two", "confidence": 0.4}}}),
		"Cluster": replyWith(leakObject{"clusters": []leakObject{{"name": a, "description": a, "indices": []int{0, 1, 2}, "keywords": []string{a}}},
			"outlier_indices": []int{}, "quality": 0.8}),
		"Compare": replyWith(leakObject{"similarity_score": 0.5, "verdict": a,
			"similarities":  []leakObject{{"aspect": o, "description": a}},
			"differences":   []leakObject{{"aspect": o, "description": a, "severity": "minor"}},
			"aspect_scores": leakObject{o: 0.5, o + " two": 0.5}}),
		"Assemble": replyWith(leakObject{"composed": output, "conflicts_resolved": 0, "gaps_filled": []string{a},
			"unused_parts": []int{}, "completeness": 0.9,
			"field_sources": []leakObject{{"field": "CANARY_FIELD_5d2b_headline", "sources": []int{0}, "method": "single", "conflicts": false, "resolution": a}}}),
		"Compress": replyWith(leakObject{"compressed": leakObject{"CANARY_FIELD_5d2b_name": a, "CANARY_FIELD_5d2b_notes": ""},
			"preserved_info": []string{a}, "removed_info": []string{a}}),
		"CompressText":      replyWith(leakObject{"compressed": a, "preserved_info": []string{a}, "removed_info": []string{a}}),
		"Critique":          replyWith(critique),
		"CritiqueWithModel": replyWith(critique),
		"Pivot": replyWith(leakObject{"pivoted": output, "data_loss": []string{a},
			"stats":    leakObject{"source_fields": 2, "target_fields": 2, "expansions": 0, "compressions": 0, "depth_change": 0},
			"mappings": []leakObject{{"source_path": "CANARY_FIELD_5d2b_name", "target_path": "CANARY_FIELD_5d2b_headline", "transformation": a, "aggregation": a}}}),
		"Project": replyWith(leakObject{"projected": leakObject{"CANARY_FIELD_5d2b_headline": leakCanaryInput, "CANARY_FIELD_5d2b_detail": a},
			"lost": []string{"CANARY_FIELD_5d2b_notes"}, "inferred": []string{}, "confidence": 0.8,
			"mappings": []leakObject{{"source_field": "CANARY_FIELD_5d2b_name", "target_field": "CANARY_FIELD_5d2b_headline", "method": "rename", "transformation": a}}}),
		"Conform": replyWith(leakObject{"conformed": record, "violations": []string{a}, "compliance": 0.9,
			"adjustments": []leakObject{{"field": "CANARY_FIELD_5d2b_name", "original_value": a, "conformed_value": a, "rule": a, "description": a}}}),
		"Decompose": replyWith(leakObject{"root_parts": []string{"p1"}, "parts": []leakObject{{
			"id": "p1", "name": a, "description": a, "content": record, "parent_id": "", "dependencies": []string{},
			"depth": 0, "order": 1, "estimate": a}}}),
		"DecomposeToSlice": replyWith([]any{record}),
		"Deduplicate":      replyWith(leakObject{"duplicates": []leakObject{{"index": 1, "duplicate_of": 0, "similarity": 0.9}}}),
		"Derive": replyWith(leakObject{"derived": output, "overall_confidence": 0.8,
			"field_confidence": leakObject{"CANARY_FIELD_5d2b_headline": 0.8},
			"derivations": []leakObject{{"field": "CANARY_FIELD_5d2b_headline", "source_fields": []string{"CANARY_FIELD_5d2b_name"},
				"method": a, "reasoning": a, "confidence": 0.8}}}),
		"Enrich": replyWith(leakObject{"enriched": output, "added_fields": []string{"CANARY_FIELD_5d2b_headline"},
			"confidence": leakObject{"CANARY_FIELD_5d2b_headline": 0.9}, "derivations": leakObject{"CANARY_FIELD_5d2b_headline": a}}),
		"EnrichInPlace":      replyWith(leakObject{"CANARY_FIELD_5d2b_name": leakCanaryInput, "CANARY_FIELD_5d2b_notes": a}),
		"Expand":             replyText(a),
		"ExpandResult":       replyWith(expansion),
		"ExpandWithMetadata": replyWith(expansion),
		"Explain":            replyWith(leakObject{"explanation": a, "summary": a, "key_points": []string{a}}),
		"Filter":             replyWith(leakObject{"ids": []string{"i-000001"}}),
		"FilterBy":           replyWith(leakObject{"ids": []string{"i-000001"}}),
		"FormatWithMetadata": replyWith(leakObject{"text": a, "format_applied": a, "transformation_notes": []string{a}, "confidence": 0.9}),
		"Infer":              replyWith(leakObject{"CANARY_FIELD_5d2b_name": leakCanaryInput, "CANARY_FIELD_5d2b_notes": a}),
		"Interpolate": replyWith(leakObject{"complete": []any{leakIn, record, leakInAlt}, "gap_count": 1, "method": "semantic",
			"average_confidence": 0.8,
			"filled":             []leakObject{{"index": 1, "method": "semantic", "based_on": []int{0, 2}, "reasoning": a, "confidence": 0.8}}}),
		"Match":                replyText("true"),
		"MatchOne":             replyWith(matches(1)),
		"SemanticMatch":        replyWith(matches(2)),
		"Merge":                replyWith(record),
		"MergeWithMetadata":    replyWith(record),
		"Negotiate":            replyWith(negotiated),
		"Settle":               replyWith(negotiated),
		"NegotiateAdversarial": replyWith(deal),
		"SettleAdversarial":    replyWith(deal),
		"Normalize":            replyWith(normalized(record)),
		"NormalizeBatch":       replyWith(normalized(record)),
		"NormalizeText":        replyWith(normalized(a)),
		"Parse":                replyWith(record),
		"Predict": replyWith(leakObject{"prediction": output, "confidence": 0.7, "reasoning": a,
			"interval":    leakObject{"lower": 0, "upper": 1, "confidence_level": 0.8},
			"scenarios":   []leakObject{{"name": a, "description": a, "prediction": output, "probability": 0.3, "conditions": []string{a}}},
			"factors":     []leakObject{{"name": a, "impact": "positive", "weight": 0.3, "reasoning": a}},
			"assumptions": []string{a}, "risks": []string{a}}),
		"Rank": replyWith(leakObject{"rankings": []leakObject{
			{"index": 0, "score": 0.9, "explanation": a}, {"index": 1, "score": 0.8, "explanation": a}, {"index": 2, "score": 0.7, "explanation": a}}}),
		"RedactLLM":         replyWith(leakObject{"spans": []leakObject{{"text": leakCanaryInput, "category": o}}}),
		"RedactLLMWithMeta": replyWith(leakObject{"spans": []leakObject{{"text": leakCanaryInput, "category": o}}}),
		"Resolve": replyWith(leakObject{"resolved": record, "confidence": 0.8,
			"conflicts": []leakObject{{"field": "CANARY_FIELD_5d2b_name", "values": leakObject{"0": a, "1": a}, "resolution": a,
				"chosen_source": 0, "chosen_value": a, "reasoning": a}},
			"source_contributions": leakObject{"0": []string{"CANARY_FIELD_5d2b_name"}, "1": []string{"CANARY_FIELD_5d2b_notes"}}}),
		"Rewrite":             replyText(rewritten),
		"RewriteResult":       replyWith(rewrite),
		"RewriteWithMetadata": replyWith(rewrite),
		"Score": replyWith(leakObject{"value": 7.5, "reasoning": a, "strengths": []string{a}, "weaknesses": []string{a},
			"breakdown": leakObject{o: 7, o + " two": 8}}),
		"Sort":                  replyWith(leakObject{"ids": []string{"i-000003", "i-000001", "i-000002"}}),
		"SortBy":                replyWith(leakObject{"ids": []string{"i-000003", "i-000001", "i-000002"}}),
		"SortResult":            replyWith(leakObject{"ids": []string{"i-000003", "i-000001", "i-000002"}}),
		"Suggest":               replyWith([]any{output}),
		"SummarizeResult":       replyWith(summary),
		"SummarizeWithMetadata": replyWith(summary),
		"Synthesize": replyWith(leakObject{"synthesized": output,
			"facts":           []leakObject{{"fact": a, "sources": []int{0, 1}, "conflicts": []string{a}}},
			"insights":        []leakObject{{"insight": a, "supporting_sources": []int{0}, "type": "pattern"}},
			"conflicts":       []leakObject{{"topic": a, "positions": leakObject{"0": a, "1": a}, "resolution": a, "chosen_source": 0}},
			"source_coverage": leakObject{"0": 0.8, "1": 0.9}}),
		"TranslateResult":       replyWith(translation),
		"TranslateWithMetadata": replyWith(translation),
		"Validate":              replyWith(validation),
		"ValidateHybrid":        replyWith(validation),
		"ValidateLegacy":        replyWith(leakObject{"valid": false, "issues": []string{a}, "confidence": 0.8, "suggestions": []string{a}}),
	}
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
		return errOf(schemaflux.Infer(leakRecord{CANARY_FIELD_5d2b_Name: leakCanaryInput}, o))
	}}
	normalize := func() schemaflux.NormalizeOptions {
		o := schemaflux.NewNormalizeOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Standard = leakCanaryOption
		o.Rules = hints
		o.CanonicalMappings = hints
		o.Fields = []string{"CANARY_FIELD_5d2b_name", "CANARY_FIELD_5d2b_notes"}
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
		o.Categories = []string{"pii"}
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
		o.Priority = "facts"
		return o
	}
	rows["Compress"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.Compress(leakIn, compress())) }}
	rows["CompressText"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.CompressText(leakText, compress())) }}
	rows["Conform"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ConformOptions{Steering: leakCanaryOption, CustomRules: hints}
		return errOf(schemaflux.Conform(leakIn, leakCanaryOption, o))
	}}
	rows["Project"] = leakRow{call: func(context.Context) error {
		o := schemaflux.ProjectOptions{Steering: leakCanaryOption,
			Mappings: map[string]string{"CANARY_FIELD_5d2b_name": "CANARY_FIELD_5d2b_headline"},
			Exclude:  []string{"CANARY_FIELD_5d2b_notes"}}
		return errOf(schemaflux.Project[leakRecord, leakOutput](leakIn, o))
	}}
	rows["Pivot"] = leakRow{call: func(context.Context) error {
		fields := []string{"CANARY_FIELD_5d2b_name"}
		o := schemaflux.PivotOptions{Steering: leakCanaryOption, PivotOn: fields, GroupBy: fields, Aggregate: "first"}
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
	rows["EnrichInPlace"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.EnrichInPlace(leakRecord{CANARY_FIELD_5d2b_Name: leakCanaryInput}, enrich()))
	}}
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
		// Only rules Go decides: free text and schema hints would go to a model.
		o := schemaflux.NewValidateOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.FieldRules = map[string]string{"CANARY_FIELD_5d2b_name": "email", "CANARY_FIELD_5d2b_notes": "required"}
		return errOf(schemaflux.ValidateDeterministically(leakIn, o))
	}}
	rows["ValidateHybrid"] = leakRow{call: func(context.Context) error { return errOf(schemaflux.ValidateHybrid(leakIn, validate())) }}
	rows["ValidateLegacy"] = leakRow{call: func(context.Context) error {
		return errOf(schemaflux.ValidateLegacy(leakIn, leakCanaryOption, schemaflux.OpOptions{Steering: leakCanaryOption}))
	}}
	verify := func() schemaflux.VerifyOptions {
		o := schemaflux.NewVerifyOptions()
		o.CommonOptions.Steering = leakCanaryOption
		o.Sources = []any{leakCanaryOption, leakCanaryOption + " two"}
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
		o.Style = "balanced"
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
