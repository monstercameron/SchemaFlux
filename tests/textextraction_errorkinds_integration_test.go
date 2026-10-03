package tests

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
	"github.com/monstercameron/schemaflux/internal/types"
)

type textExtractionDoc struct {
	Title string `json:"title"`
}

// textExtractionCalls is one call of each operation of the text and extraction
// files, made through the root's exported API with options that cannot be used,
// and what each is refused with. The messages are the ones the operations had
// before their refusals were given a kind.
func textExtractionCalls() []struct {
	name string
	call func() error
	text string
	kind types.ErrorKind
	is   error
} {
	type refusal = struct {
		name string
		call func() error
		text string
		kind types.ErrorKind
		is   error
	}
	config := schemaflux.ErrConfiguration

	return []refusal{
		{"Summarize with a threshold above one", func() error {
			opts := schemaflux.NewSummarizeOptions()
			opts.CommonOptions.Threshold = 2
			_, err := schemaflux.Summarize("text", opts)
			return err
		}, "invalid options: threshold must be between 0 and 1, got 2.000000", types.KindConfiguration, config},
		{"SummarizeWithMetadata with a length unit it does not know", func() error {
			opts := schemaflux.NewSummarizeOptions()
			opts.LengthUnit = "furlongs"
			_, err := schemaflux.SummarizeWithMetadata("text", opts)
			return err
		}, "invalid options: invalid length unit: furlongs", types.KindConfiguration, config},
		{"Rewrite with no formality", func() error {
			opts := schemaflux.NewRewriteOptions()
			opts.FormalityLevel = 0
			_, err := schemaflux.Rewrite("text", opts)
			return err
		}, "invalid options: formality level must be between 1 and 10, got 0", types.KindConfiguration, config},
		{"Translate with no target language", func() error {
			opts := schemaflux.NewTranslateOptions()
			_, err := schemaflux.Translate("text", opts)
			return err
		}, "invalid options: target language is required", types.KindConfiguration, config},
		{"Expand with a detail level of zero", func() error {
			opts := schemaflux.NewExpandOptions()
			opts.DetailLevel = 0
			_, err := schemaflux.Expand("text", opts)
			return err
		}, "invalid options: detail level must be between 1 and 10, got 0", types.KindConfiguration, config},
		{"ValidateDeterministically with free-text rules", func() error {
			opts := schemaflux.NewValidateOptions()
			opts.Rules = "looks plausible"
			opts.FieldRules = map[string]string{"email": "email"}
			_, err := schemaflux.ValidateDeterministically("a@b.c", opts)
			return err
		}, "validate deterministically: Rules is free text and requires model judgment; use ValidateHybrid", types.KindConfiguration, config},
		{"Merge with no sources", func() error {
			_, err := schemaflux.Merge([]string{}, "newest wins")
			return err
		}, "no sources to merge", types.KindConfiguration, config},
		{"Question with a blank question", func() error {
			_, err := schemaflux.Question[string, string]("data", schemaflux.NewQuestionOptions("  "))
			return err
		}, "invalid options: question cannot be empty", types.KindConfiguration, config},
		{"Infer of data JSON cannot write", func() error {
			_, err := schemaflux.Infer(make(chan int), schemaflux.NewInferOptions())
			return err
		}, "infer: invalid request: the partial data could not be written as JSON", types.KindInvalidRequest, schemaflux.ErrInvalidRequest},
		{"Parse with an empty custom delimiter", func() error {
			opts := schemaflux.NewParseOptions()
			opts.CustomDelimiters = []string{""}
			_, err := schemaflux.Parse[textExtractionDoc](`{"title":"a"}`, opts)
			return err
		}, "invalid options: parse: configuration: custom delimiter 1 is empty", types.KindConfiguration, config},
		{"Complete with a maximum length of zero", func() error {
			opts := schemaflux.NewCompleteOptions()
			opts.MaxLength = 0
			_, err := schemaflux.Complete("The", opts)
			return err
		}, "invalid options: MaxLength must be positive", types.KindConfiguration, config},
		{"CompleteField of a field the struct does not have", func() error {
			_, err := schemaflux.CompleteField(textExtractionDoc{Title: "a"}, schemaflux.NewCompleteFieldOptions("Missing"))
			return err
		}, `field "Missing" not found in struct`, types.KindConfiguration, config},
		{"Suggest with a top N of zero", func() error {
			opts := schemaflux.NewSuggestOptions()
			opts.TopN = 0
			_, err := schemaflux.Suggest[string]("state", opts)
			return err
		}, "invalid options: topN must be at least 1, got 0", types.KindConfiguration, config},
		{"Annotate with no annotation type", func() error {
			opts := schemaflux.NewAnnotateOptions()
			opts.AnnotationTypes = nil
			_, err := schemaflux.Annotate("Ada met Grace.", opts)
			return err
		}, "invalid options: at least one annotation type or custom schema is required", types.KindConfiguration, config},
		{"Explain for an audience it does not know", func() error {
			opts := schemaflux.NewExplainOptions()
			opts.Audience = "alien"
			_, err := schemaflux.Explain("data", opts)
			return err
		}, "invalid options: invalid audience: alien", types.KindConfiguration, config},
		{"Compress with a compression ratio of zero", func() error {
			opts := schemaflux.NewCompressOptions()
			opts.CompressionRatio = 0
			_, err := schemaflux.Compress("some text", opts)
			return err
		}, "invalid options: compression ratio must be between 0 and 1, got 0.000000", types.KindConfiguration, config},
		{"Normalize with a case it does not know", func() error {
			opts := schemaflux.NewNormalizeOptions()
			opts.NormalizeCase = "weird"
			_, err := schemaflux.Normalize("some  text", opts)
			return err
		}, "invalid options: invalid normalize case: weird", types.KindConfiguration, config},
		{"Redact with no category", func() error {
			opts := schemaflux.NewRedactOptions()
			opts.Categories = nil
			_, err := schemaflux.Redact("jane@example.com", opts)
			return err
		}, "invalid options: redact: configuration: at least one category must be specified", types.KindConfiguration, config},
		{"RedactLLM with a minimum mask of zero", func() error {
			opts := schemaflux.NewRedactLLMOptions()
			opts.MinMask = 0
			_, err := schemaflux.RedactLLM("call jane", opts)
			return err
		}, "redact_llm: configuration: invalid options: MinMask must be at least 1", types.KindConfiguration, config},
		{"Extract with strict schema and partial extraction both on", func() error {
			opts := schemaflux.NewExtractOptions()
			opts.StrictSchema = true
			opts.AllowPartial = true
			_, err := schemaflux.Extract[textExtractionDoc]("text", opts)
			return err
		}, "invalid options: cannot have both StrictSchema and AllowPartial", types.KindConfiguration, config},
		{"Transform with a merge strategy it does not know", func() error {
			opts := schemaflux.NewTransformOptions()
			opts.MergeStrategy = "sideways"
			_, err := schemaflux.Transform[textExtractionDoc, textExtractionDoc](textExtractionDoc{Title: "a"}, opts)
			return err
		}, "invalid options: invalid merge strategy: sideways", types.KindConfiguration, config},
		{"Generate with a count of zero", func() error {
			opts := schemaflux.NewGenerateOptions()
			opts.Count = 0
			_, err := schemaflux.Generate[textExtractionDoc]("a document", opts)
			return err
		}, "invalid options: count must be at least 1, got 0", types.KindConfiguration, config},
		{"Generate from an empty prompt", func() error {
			_, err := schemaflux.Generate[textExtractionDoc]("", schemaflux.NewGenerateOptions())
			return err
		}, "generation failed: prompt cannot be empty", types.KindInvalidRequest, schemaflux.ErrInvalidRequest},
	}
}

// A caller of the old root who gives an operation an option it cannot use is
// refused with the text it has always been refused with, and can now tell what
// kind of failure it was without reading that text. The model is never asked.
func TestTextAndExtractionOperationsRefuseWithAKindThroughTheRoot(t *testing.T) {
	for _, tc := range textExtractionCalls() {
		t.Run(tc.name, func(t *testing.T) {
			provider := testfixtures.WithScriptedProvider(t, `{"title":"unused"}`, nil)

			err := tc.call()
			if err == nil {
				t.Fatal("the call was not refused")
			}
			if err.Error() != tc.text {
				t.Errorf("the text changed:\n got  %q\n want %q", err.Error(), tc.text)
			}
			if got := schemaflux.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
			if !errors.Is(err, tc.is) {
				t.Errorf("errors.Is(err, %v) is false", tc.is)
			}
			if requests := provider.Requests(); len(requests) != 0 {
				t.Errorf("a refused call reached the provider %d time(s)", len(requests))
			}
		})
	}
}

// A call of the old root writes its first line to the process logger, as it
// always has: with no client on the context there is nowhere else for it to go.
func TestTextAndExtractionOperationsLogToTheProcessLoggerThroughTheRoot(t *testing.T) {
	calls := []struct {
		name  string
		start string
		call  func()
	}{
		{"Summarize", "Starting summarize operation", func() {
			_, _ = schemaflux.Summarize("text", schemaflux.NewSummarizeOptions())
		}},
		{"Merge", "Starting merge operation", func() { _, _ = schemaflux.Merge([]string{"a", "b"}, "newest wins") }},
		{"Format", "Starting format operation", func() { _, _ = schemaflux.Format("data", "a table") }},
		{"Infer", "Starting infer operation", func() { _, _ = schemaflux.Infer(textExtractionDoc{Title: "a"}, schemaflux.NewInferOptions()) }},
		{"Parse", "Starting parse operation", func() {
			_, _ = schemaflux.Parse[textExtractionDoc](`{"title":"a"}`, schemaflux.NewParseOptions())
		}},
		{"Complete", "Starting complete operation", func() { _, _ = schemaflux.Complete("The", schemaflux.NewCompleteOptions()) }},
		{"Suggest", "Starting suggest operation", func() { _, _ = schemaflux.Suggest[string]("state", schemaflux.NewSuggestOptions()) }},
		{"Annotate", "Starting annotate operation", func() { _, _ = schemaflux.Annotate("Ada met Grace.", schemaflux.NewAnnotateOptions()) }},
		{"Explain", "Starting explain operation", func() { _, _ = schemaflux.Explain("data", schemaflux.NewExplainOptions()) }},
		{"Compress", "Starting compress operation", func() { _, _ = schemaflux.Compress("some text", schemaflux.NewCompressOptions()) }},
		{"Normalize", "Starting normalize operation", func() { _, _ = schemaflux.Normalize("some  text", schemaflux.NewNormalizeOptions()) }},
		{"Redact", "Starting redact operation", func() { _, _ = schemaflux.Redact("jane@example.com", schemaflux.NewRedactOptions()) }},
		{"RedactLLM", "Starting LLM redact operation", func() { _, _ = schemaflux.RedactLLM("call Jane Doe", schemaflux.NewRedactLLMOptions()) }},
		{"Extract", "Extract operation started", func() {
			_, _ = schemaflux.Extract[textExtractionDoc]("Title: a", schemaflux.NewExtractOptions())
		}},
		{"Transform", "Transform operation started", func() {
			_, _ = schemaflux.Transform[textExtractionDoc, textExtractionDoc](textExtractionDoc{Title: "a"}, schemaflux.NewTransformOptions())
		}},
		{"Generate", "Generate operation started", func() {
			_, _ = schemaflux.Generate[textExtractionDoc]("a document", schemaflux.NewGenerateOptions())
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			testfixtures.WithScriptedProvider(t, `{"title":"a"}`, nil)
			schemaflux.ConfigureLogging(telemetry.LoggerConfig{
				Level: telemetry.DebugLevel, Format: "text", DisableStderr: true, Capture: true, BufferSize: 1000,
			})
			config.SetDebugMode(true)
			t.Cleanup(func() {
				config.SetDebugMode(false)
				schemaflux.ConfigureLogging(telemetry.DefaultLoggerConfig())
			})
			schemaflux.ResetLogEntries()

			tc.call()

			entries := schemaflux.GetLogEntries()
			if len(entries) == 0 || entries[0].Message != tc.start {
				var messages []string
				for _, entry := range entries {
					messages = append(messages, entry.Message)
				}
				t.Errorf("the process logger's first line was not %q: %q", tc.start, messages)
			}
		})
	}
}
