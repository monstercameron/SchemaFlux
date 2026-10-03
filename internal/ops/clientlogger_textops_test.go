package ops

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loggingProbeProvider answers every request with text that is not JSON, so an
// operation that reads JSON refuses it and one that reads text takes it. It is
// the client's provider in the logging tables: what matters there is where the
// operation's lines go, not what it concludes.
type loggingProbeProvider struct{}

func (loggingProbeProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "this is not JSON", Model: req.Model, FinishReason: "stop"}, nil
}
func (loggingProbeProvider) Name() string                               { return "local" }
func (loggingProbeProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (loggingProbeProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// loggedOperation is one operation, run once under the context it is given.
type loggedOperation struct {
	name string
	// starts is the first line the operation writes, which every table checks
	// is where it should be.
	starts string
	run    func(ctx context.Context)
}

// clientCaptureLogger is a logger that keeps what it is given, as a client's would,
// and is not the process logger.
func clientCaptureLogger() *logger.Logger {
	return telemetry.NewLoggerWithConfig(telemetry.LoggerConfig{
		Level: telemetry.DebugLevel, Format: "text", DisableStderr: true, Capture: true, BufferSize: 10000,
	})
}

// loggedMessagesOf lists what a logger captured, one "LEVEL message" to a line and
// without attributes, which hold request ids and error text.
func loggedMessagesOf(log *logger.Logger) []string {
	var lines []string
	for _, entry := range log.Entries() {
		lines = append(lines, string(entry.Level)+" "+entry.Message)
	}
	return lines
}

// processLoggedLines swaps in a capturing process logger for one test and returns a
// function that lists what reached it.
func processLoggedLines(t *testing.T) func() []string {
	t.Helper()
	previous := logger.GetLogger()
	process := logger.ConfigureLogger(logger.LoggerConfig{
		Level: logger.DebugLevel, Format: "text", DisableStderr: true, Capture: true, BufferSize: 10000,
	})
	config.SetDebugMode(true)
	t.Cleanup(func() {
		config.SetDebugMode(false)
		logger.SetLogger(previous)
	})
	return func() []string { return withoutCallPathLines(loggedMessagesOf(process)) }
}

// callPathLines are written by the call path and not by the operations, so they
// are left out of what these tables count as an operation's own lines: the cost
// tracker's note that a model has no price and the note that a repeated prefix
// is not being cached, which are written where no context is in reach and stay
// on the process logger, and the repair loop's note, which is the call path's
// to route.
var callPathLines = map[string]bool{
	"WARN No pricing information available; cost reported as unpriced":                  true,
	"WARN prompt cache is reporting no cached tokens for a prefix that keeps repeating": true,
	// The repair loop's note, written by the call path's runner.
	"DEBUG repairing an unusable answer": true,
}

func withoutCallPathLines(lines []string) []string {
	var kept []string
	for _, line := range lines {
		if !callPathLines[line] {
			kept = append(kept, line)
		}
	}
	return kept
}

// textAndExtractionOperations is every operation of the files this table covers
// that writes a line where a context is in reach, each with options that carry
// ctx. Redact is not here: it makes no call and has no context to carry.
func textAndExtractionOperations() []loggedOperation {
	op := func(ctx context.Context) types.OpOptions { return types.OpOptions{Context: ctx} }

	summarize := func(ctx context.Context) SummarizeOptions {
		o := NewSummarizeOptions()
		o.CommonOptions.Context = ctx
		return o
	}
	rewrite := func(ctx context.Context) RewriteOptions {
		o := NewRewriteOptions()
		o.CommonOptions.Context = ctx
		return o
	}
	translate := func(ctx context.Context) TranslateOptions {
		o := NewTranslateOptions()
		o.TargetLanguage = "French"
		o.CommonOptions.Context = ctx
		return o
	}
	expand := func(ctx context.Context) ExpandOptions {
		o := NewExpandOptions()
		o.CommonOptions.Context = ctx
		return o
	}
	validate := func(ctx context.Context) ValidateOptions {
		o := NewValidateOptions()
		o.Rules = "the data should look plausible"
		o.CommonOptions.Context = ctx
		return o
	}

	return []loggedOperation{
		{"Summarize", "Starting summarize operation", func(ctx context.Context) { _, _ = Summarize("text", summarize(ctx)) }},
		{"SummarizeWithMetadata", "Starting summarize with metadata operation", func(ctx context.Context) { _, _ = SummarizeWithMetadata("text", summarize(ctx)) }},
		{"Rewrite", "Starting rewrite operation", func(ctx context.Context) { _, _ = Rewrite("text", rewrite(ctx)) }},
		{"RewriteWithMetadata", "Starting rewrite with metadata operation", func(ctx context.Context) { _, _ = RewriteWithMetadata("text", rewrite(ctx)) }},
		{"Translate", "Starting translate operation", func(ctx context.Context) { _, _ = Translate("text", translate(ctx)) }},
		{"TranslateWithMetadata", "Starting translate with metadata operation", func(ctx context.Context) { _, _ = TranslateWithMetadata("text", translate(ctx)) }},
		{"Expand", "Starting expand operation", func(ctx context.Context) { _, _ = Expand("text", expand(ctx)) }},
		{"ExpandWithMetadata", "Starting expand with metadata operation", func(ctx context.Context) { _, _ = ExpandWithMetadata("text", expand(ctx)) }},
		{"Validate", "Starting validate operation", func(ctx context.Context) { _, _ = Validate("data", validate(ctx)) }},
		{"ValidateHybrid", "Starting validate operation", func(ctx context.Context) { _, _ = ValidateHybrid("data", validate(ctx)) }},
		{"ValidateLegacy", "Starting legacy validate operation", func(ctx context.Context) { _, _ = ValidateLegacy("data", "rules", op(ctx)) }},
		{"Format", "Starting format operation", func(ctx context.Context) { _, _ = Format("data", "a table", op(ctx)) }},
		{"FormatWithMetadata", "Starting format with metadata operation", func(ctx context.Context) { _, _ = FormatWithMetadata("data", "a table", op(ctx)) }},
		{"Merge", "Starting merge operation", func(ctx context.Context) { _, _ = Merge([]string{"a", "b"}, "newest", op(ctx)) }},
		{"MergeWithMetadata", "Starting merge with metadata operation", func(ctx context.Context) { _, _ = MergeWithMetadata([]string{"a", "b"}, "newest", op(ctx)) }},
		{"Question", "Starting question operation", func(ctx context.Context) {
			opts := NewQuestionOptions("what is it?")
			opts.CommonOptions.Context = ctx
			_, _ = Question[string, string]("data", opts)
		}},
		{"QuestionLegacy", "Starting legacy question operation", func(ctx context.Context) { _, _ = QuestionLegacy("data", "what is it?", op(ctx)) }},
		{"Deduplicate", "Starting deduplicate operation", func(ctx context.Context) { _, _ = Deduplicate([]string{"a", "b"}, 0.5, op(ctx)) }},
		{"Infer", "Starting infer operation", func(ctx context.Context) {
			opts := NewInferOptions()
			opts.OpOptions.Context = ctx
			_, _ = Infer(refusalPerson{Name: "Ada"}, opts)
		}},
		{"Parse", "Starting parse operation", func(ctx context.Context) {
			opts := NewParseOptions()
			opts.OpOptions.Context = ctx
			opts.AllowLLMFallback = true
			_, _ = Parse[refusalPerson]("not any format", opts)
		}},
		{"Complete", "Starting complete operation", func(ctx context.Context) { _, _ = Complete(ctx, "The", NewCompleteOptions()) }},
		{"CompleteField", "Starting complete field operation", func(ctx context.Context) {
			_, _ = CompleteField(ctx, completeTarget{Body: "The"}, NewCompleteFieldOptions("Body"))
		}},
		{"Suggest", "Starting suggest operation", func(ctx context.Context) {
			opts := NewSuggestOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Suggest[string]("state", opts)
		}},
		{"Explain", "Starting explain operation", func(ctx context.Context) {
			opts := NewExplainOptions()
			opts.OpOptions.Context = ctx
			_, _ = Explain("data", opts)
		}},
		{"Compress", "Starting compress operation", func(ctx context.Context) {
			opts := NewCompressOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Compress("some text to compress", opts)
		}},
		{"Normalize", "Starting normalize operation", func(ctx context.Context) {
			opts := NewNormalizeOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Normalize("some  text", opts)
		}},
		{"NormalizeBatch", "Starting normalize batch operation", func(ctx context.Context) {
			opts := NewNormalizeOptions()
			opts.CommonOptions.Context = ctx
			_, _ = NormalizeBatch([]string{"some  text"}, opts)
		}},
		{"RedactLLM", "Starting LLM redact operation", func(ctx context.Context) {
			_, _ = RedactLLM(ctx, "call Jane Doe", NewRedactLLMOptions())
		}},
		{"Annotate", "Starting annotate operation", func(ctx context.Context) {
			opts := NewAnnotateOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Annotate("Ada met Grace in London.", opts)
		}},
		{"Extract", "Extract operation started", func(ctx context.Context) {
			opts := NewExtractOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Extract[refusalTarget]("Title: a report", opts)
		}},
		{"Transform", "Transform operation started", func(ctx context.Context) {
			opts := NewTransformOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Transform[refusalSource, refusalTarget](refusalSource{Name: "Ada"}, opts)
		}},
		{"Generate", "Generate operation started", func(ctx context.Context) {
			opts := NewGenerateOptions()
			opts.CommonOptions.Context = ctx
			_, _ = Generate[refusalTarget]("a report", opts)
		}},
	}
}

// A line an operation writes where its call's context is in reach goes to the
// logger of the client that context carries. The process logger, which is a
// second capture here, receives none of them.
func TestTextAndExtractionLinesGoToTheSnapshotsLogger(t *testing.T) {
	for _, tc := range textAndExtractionOperations() {
		t.Run(tc.name, func(t *testing.T) {
			process := processLoggedLines(t)
			client := clientCaptureLogger()
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: loggingProbeProvider{}, Logger: client})

			tc.run(ctx)

			lines := loggedMessagesOf(client)
			if len(lines) == 0 || lines[0] != "DEBUG "+tc.starts {
				t.Errorf("the client's logger did not receive %q first; it received %q", tc.starts, lines)
			}
			if got := process(); len(got) != 0 {
				t.Errorf("the process logger received lines meant for the client: %q", got)
			}
		})
	}
}

// A client whose logger discards writes nothing anywhere: the process logger
// stays as quiet as the client's.
func TestTextAndExtractionLinesStayOffTheProcessLoggerWhenTheClientDiscards(t *testing.T) {
	for _, tc := range textAndExtractionOperations() {
		t.Run(tc.name, func(t *testing.T) {
			process := processLoggedLines(t)
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: loggingProbeProvider{}, Logger: telemetry.Discard()})

			tc.run(ctx)

			if got := process(); len(got) != 0 {
				t.Errorf("the process logger received lines although the client discards: %q", got)
			}
		})
	}
}

// linesWithNoClient is what each operation wrote to the process logger in the
// tree before the logger sites changed, for a call made with no client on its
// context: the old root's output, which has to stay as it is.
var linesWithNoClient = map[string][]string{
	"Summarize": {
		"DEBUG Starting summarize operation",
		"DEBUG Summarize operation succeeded",
	},
	"SummarizeWithMetadata": {
		"DEBUG Starting summarize with metadata operation",
		"ERROR SummarizeWithMetadata failed: parse error",
	},
	"Rewrite": {
		"DEBUG Starting rewrite operation",
		"DEBUG Rewrite operation succeeded",
	},
	"RewriteWithMetadata": {
		"DEBUG Starting rewrite with metadata operation",
		"ERROR RewriteWithMetadata failed: parse error",
	},
	"Translate": {
		"DEBUG Starting translate operation",
		"DEBUG Translate operation succeeded",
	},
	"TranslateWithMetadata": {
		"DEBUG Starting translate with metadata operation",
		"ERROR TranslateWithMetadata failed: parse error",
	},
	"Expand": {
		"DEBUG Starting expand operation",
		"DEBUG Expand operation succeeded",
	},
	"ExpandWithMetadata": {
		"DEBUG Starting expand with metadata operation",
		"ERROR ExpandWithMetadata failed: parse error",
	},
	"Validate": {
		"DEBUG Starting validate operation",
		"ERROR Validate operation failed: the answer was refused",
	},
	"ValidateHybrid": {
		"DEBUG Starting validate operation",
		"ERROR Validate operation failed: the answer was refused",
	},
	"ValidateLegacy": {
		"DEBUG Starting legacy validate operation",
		"ERROR ValidateLegacy operation failed: the answer was refused",
	},
	"Format": {
		"DEBUG Starting format operation",
		"DEBUG Format operation succeeded",
	},
	"FormatWithMetadata": {
		"DEBUG Starting format with metadata operation",
		"ERROR FormatWithMetadata could not parse the response",
	},
	"Merge": {
		"DEBUG Starting merge operation",
		"ERROR Merge operation failed: unmarshal error",
	},
	"MergeWithMetadata": {
		"DEBUG Starting merge with metadata operation",
		"ERROR MergeWithMetadata operation failed: unmarshal error",
	},
	"Question": {
		"DEBUG Starting question operation",
		"ERROR Question operation failed: parse error",
	},
	"QuestionLegacy": {
		"DEBUG Starting legacy question operation",
		"DEBUG Question operation succeeded",
	},
	"Deduplicate": {
		"DEBUG Starting deduplicate operation",
	},
	"Infer": {
		"DEBUG Starting infer operation",
		"ERROR Infer operation refused the answer",
	},
	"Parse": {
		"DEBUG Starting parse operation",
		"ERROR Parse operation LLM fallback failed",
	},
	"Complete": {
		"DEBUG Starting complete operation",
		"DEBUG Complete operation succeeded",
	},
	"CompleteField": {
		"DEBUG Starting complete field operation",
		"DEBUG Starting complete operation",
		"DEBUG Complete operation succeeded",
		"DEBUG Complete field operation succeeded",
	},
	"Suggest": {
		"DEBUG Starting suggest operation",
		"ERROR Suggest operation parse failed",
	},
	"Explain": {
		"DEBUG Starting explain operation",
		"ERROR Explain operation explanation generation failed",
	},
	"Compress": {
		"DEBUG Starting compress operation",
		"ERROR Compress operation failed: parse error",
	},
	"Normalize": {
		"DEBUG Starting normalize operation",
		"ERROR Normalize operation refused the answer",
	},
	"NormalizeBatch": {
		"DEBUG Starting normalize batch operation",
		"DEBUG Starting normalize operation",
		"ERROR Normalize operation refused the answer",
		"ERROR NormalizeBatch failed for item",
	},
	"RedactLLM": {
		"DEBUG Starting LLM redact operation",
		"ERROR RedactLLM parse failed",
	},
	"Annotate": {
		"DEBUG Starting annotate operation",
		"ERROR Annotate operation failed: parse error",
	},
	"Extract": {
		"DEBUG Extract operation started",
		"DEBUG Extract input normalized",
		"ERROR Extract failed",
	},
	"Transform": {
		"DEBUG Transform operation started",
		"DEBUG Transform schemas",
		"ERROR Transform failed: parsing error",
	},
	"Generate": {
		"DEBUG Generate operation started",
		"DEBUG Generate schema",
		"ERROR Generate failed: parsing error",
	},
}

// With no client on the context every line goes where it went before: to the
// process logger, starting with the operation's own first line.
func TestTextAndExtractionLinesGoToTheProcessLoggerWithNoClient(t *testing.T) {
	for _, tc := range textAndExtractionOperations() {
		t.Run(tc.name, func(t *testing.T) {
			process := processLoggedLines(t)
			// The provider arrives by the hook the old root's tests use, so no
			// snapshot is on the context.
			swapLLMResponse(t, "this is not JSON")

			tc.run(context.Background())

			lines := process()
			if len(lines) == 0 || lines[0] != "DEBUG "+tc.starts {
				t.Errorf("the process logger did not receive %q first; it received %q", tc.starts, lines)
			}
			if want := linesWithNoClient[tc.name]; !reflect.DeepEqual(lines, want) {
				t.Errorf("the process logger received\n got  %q\n want %q", lines, want)
			}
		})
	}
}

// Redact makes no call and carries no context, so its lines stay on the
// process logger whatever client is around.
func TestRedactKeepsTheProcessLogger(t *testing.T) {
	process := processLoggedLines(t)
	client := clientCaptureLogger()
	_ = WithExecConfig(context.Background(), &ExecConfig{Logger: client})

	if _, err := Redact("jane@example.com", NewRedactOptions()); err != nil {
		t.Fatal(err)
	}

	lines := process()
	if len(lines) == 0 || lines[0] != "DEBUG Starting redact operation" {
		t.Errorf("the process logger received %q", lines)
	}
	if got := loggedMessagesOf(client); len(got) != 0 {
		t.Errorf("the client's logger received %q", got)
	}
}
