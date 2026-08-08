package ops

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Summary contains the summary with metadata
type Summary struct {
	// Text is the summarized content
	Text string `json:"text"`

	// CompressionRatio is output length / input length, measured in runes.
	//
	// Bytes would make the same summary of the same text report a different
	// number depending on the alphabet: three times smaller for Japanese, and
	// the ratio is what a caller tunes MaxCompression against.
	CompressionRatio float64 `json:"compression_ratio"`

	// KeyPoints are the main points extracted
	KeyPoints []string `json:"key_points,omitempty"`

	// ModelConfidence score for the summary quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Rewritten contains the rewritten text with metadata
type Rewritten struct {
	// Text is the rewritten content
	Text string `json:"text"`

	// ChangesMade describes what was changed
	ChangesMade []string `json:"changes_made,omitempty"`

	// ModelConfidence score for the rewrite quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// ToneAchieved describes the tone of the output
	ToneAchieved string `json:"tone_achieved,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Translation contains the translation with metadata
type Translation struct {
	// Text is the translated content
	Text string `json:"text"`

	// SourceLanguageDetected is the detected source language (if not specified)
	SourceLanguageDetected string `json:"source_language_detected,omitempty"`

	// ModelConfidence score for the translation quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Alternatives are alternative translations for ambiguous phrases
	Alternatives []TranslationAlternative `json:"alternatives,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TranslationAlternative represents an alternative translation
type TranslationAlternative struct {
	Phrase      string `json:"phrase"`
	Alternative string `json:"alternative"`
	Context     string `json:"context,omitempty"`
}

// Expansion contains the expanded text with metadata
type Expansion struct {
	// Text is the expanded content
	Text string `json:"text"`

	// ExpansionRatio is output length / input length
	ExpansionRatio float64 `json:"expansion_ratio"`

	// AddedContent describes what was added
	AddedContent []string `json:"added_content,omitempty"`

	// ModelConfidence score for the expansion quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Summarize creates a concise summary of the input text.
// For metadata including key points and confidence, use SummarizeWithMetadata.
func Summarize(input string, opts SummarizeOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting summarize operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Summarize operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", fmt.Errorf("invalid options: %w", err)
	}

	// Build summarization instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), summarizeInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a text summarization expert. Create concise summaries that preserve key information.

Rules:
- Maintain the most important points
- Use clear, concise language
- Preserve critical details and context
- Keep the original tone when appropriate`

	userPrompt := fmt.Sprintf("Summarize this text:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Summarize operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", types.SummarizeError{
			InputShape: types.DescribeValue(input),
			Length:     len(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result := strings.TrimSpace(response)
	log.Debug("Summarize operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// SummarizeWithMetadata creates a summary with additional metadata including
// compression ratio, key points extracted, and confidence score.
// Deprecated: use SummarizeResult, which returns the same Summary inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func SummarizeWithMetadata(input string, opts SummarizeOptions) (Summary, error) {
	log := logger.GetLogger()
	log.Debug("Starting summarize with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("SummarizeWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Summary{}, fmt.Errorf("invalid options: %w", err)
	}

	// Build summarization instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), summarizeInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a text summarization expert. Create concise summaries that preserve key information.

Respond ONLY with valid JSON in this exact format:
{
  "text": "The summarized text here",
  "key_points": ["Main point 1", "Main point 2", "Main point 3"],
  "confidence": 0.85
}

Rules:
- "text": The complete summary
- "key_points": 3-7 main points extracted from the text
- "confidence": A value from 0.0 to 1.0 indicating summary quality (1.0 = excellent)`

	userPrompt := fmt.Sprintf("Summarize this text and provide metadata:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("SummarizeWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Summary{}, types.SummarizeError{
			InputShape: types.DescribeValue(input),
			Length:     len(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse JSON response
	var parsed struct {
		Text            string   `json:"text"`
		KeyPoints       []string `json:"key_points"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		// No fallback verdict. The previous branch returned the raw response as
		// the summary with a literal ModelConfidence of 0.7 and no KeyPoints, and a
		// nil error -- so a caller reading the documented metadata silently got
		// an invented number and an empty list. It also used json.Unmarshal
		// directly, without the shared fence stripping, so any fenced response
		// took this path.
		log.Error("SummarizeWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Summary{}, fmt.Errorf("summarize: could not parse the summary response: %w", err)
	}

	// TargetLength was requested in the prompt and never checked, so an
	// operation documented as producing at most N sentences produced whatever
	// the model produced. The tolerance is deliberate: a summary asked for
	// three sentences that comes back as four has done the job, and failing the
	// call would be a worse answer than the one it gave.
	if err := WithinLength(parsed.Text, opts.TargetLength, LengthUnit(opts.LengthUnit), summaryLengthTolerance); err != nil {
		log.Error("SummarizeWithMetadata result is longer than requested",
			"requestID", opts.CommonOptions.RequestID, "error", err)
		return Summary{}, fmt.Errorf("summarize: %w", err)
	}

	// Runes, not bytes. A summary of Japanese text measured in bytes reports a
	// compression ratio three times smaller than the one a reader would count,
	// and the ratio is the number a caller tunes MaxCompression against.
	compressionRatio := 0.0
	if inputRunes := len([]rune(input)); inputRunes > 0 {
		compressionRatio = float64(len([]rune(parsed.Text))) / float64(inputRunes)
	}

	result := Summary{
		Text:             parsed.Text,
		CompressionRatio: compressionRatio,
		KeyPoints:        parsed.KeyPoints,
		ModelConfidence:  parsed.ModelConfidence,
	}

	log.Debug("SummarizeWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "keyPoints", len(result.KeyPoints))

	return result, nil
}

// Rewrite transforms text according to specified parameters.
// For metadata including changes made and confidence, use RewriteWithMetadata.
func Rewrite(input string, opts RewriteOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting rewrite operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Rewrite operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", fmt.Errorf("invalid options: %w", err)
	}

	// Build rewrite instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), rewriteInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a text rewriting expert. Modify text while preserving its core meaning.

Rules:
- Maintain the original message and intent
- Improve clarity and readability
- Adapt style as requested
- Fix grammar and spelling errors`

	userPrompt := fmt.Sprintf("Rewrite this text:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Rewrite operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", types.RewriteError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result := strings.TrimSpace(response)
	if err := checkRewriteWordConstraints(result, opts); err != nil {
		log.Error("Rewrite operation failed a word constraint", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", err
	}
	log.Debug("Rewrite operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// RewriteWithMetadata rewrites text with additional metadata including
// what changes were made, the achieved tone, and confidence score.
// Deprecated: use RewriteResult, which returns the same Rewritten inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func RewriteWithMetadata(input string, opts RewriteOptions) (Rewritten, error) {
	log := logger.GetLogger()
	log.Debug("Starting rewrite with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("RewriteWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Rewritten{}, fmt.Errorf("invalid options: %w", err)
	}

	// Build rewrite instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), rewriteInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a text rewriting expert. Modify text while preserving its core meaning.

Respond ONLY with valid JSON in this exact format:
{
  "text": "The rewritten text here",
  "changes_made": ["Changed tone to professional", "Simplified complex sentences"],
  "tone_achieved": "professional",
  "confidence": 0.9
}

Rules:
- "text": The complete rewritten text
- "changes_made": List of specific changes made to the original
- "tone_achieved": The resulting tone of the rewritten text
- "confidence": A value from 0.0 to 1.0 indicating rewrite quality`

	userPrompt := fmt.Sprintf("Rewrite this text and provide metadata about the changes:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("RewriteWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Rewritten{}, types.RewriteError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse JSON response
	var parsed struct {
		Text            string   `json:"text"`
		ChangesMade     []string `json:"changes_made"`
		ToneAchieved    string   `json:"tone_achieved"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("RewriteWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Rewritten{}, fmt.Errorf("rewrite: could not parse the rewrite response: %w", err)
	}

	result := Rewritten{
		Text:            parsed.Text,
		ChangesMade:     parsed.ChangesMade,
		ToneAchieved:    parsed.ToneAchieved,
		ModelConfidence: parsed.ModelConfidence,
	}

	if err := checkRewriteWordConstraints(result.Text, opts); err != nil {
		log.Error("RewriteWithMetadata failed a word constraint", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Rewritten{}, err
	}

	log.Debug("RewriteWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "changesMade", len(result.ChangesMade))

	return result, nil
}

// Translate converts text to a target language.
// For metadata including detected source language and alternatives, use TranslateWithMetadata.
func Translate(input string, opts TranslateOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting translate operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input), "targetLang", opts.TargetLanguage)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Translate operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", fmt.Errorf("invalid options: %w", err)
	}

	// Build translation instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), translateInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a translation expert. Translate text accurately between languages.

Rules:
- Preserve meaning and nuance
- Maintain appropriate formality level
- Handle idioms and cultural references appropriately
- Keep technical terms accurate`

	userPrompt := fmt.Sprintf("Translate this text:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Translate operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", types.TranslateError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result := strings.TrimSpace(response)
	log.Debug("Translate operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// TranslateWithMetadata translates text with additional metadata including
// detected source language, confidence, and alternative translations.
// Deprecated: use TranslateResult, which returns the same Translation inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func TranslateWithMetadata(input string, opts TranslateOptions) (Translation, error) {
	log := logger.GetLogger()
	log.Debug("Starting translate with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input), "targetLang", opts.TargetLanguage)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("TranslateWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Translation{}, fmt.Errorf("invalid options: %w", err)
	}

	// Build translation instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), translateInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a translation expert. Translate text accurately between languages.

Respond ONLY with valid JSON in this exact format:
{
  "text": "The translated text here",
  "source_language_detected": "English",
  "confidence": 0.95,
  "alternatives": [
    {"phrase": "original phrase", "alternative": "alternate translation", "context": "when to use"}
  ]
}

Rules:
- "text": The complete translation
- "source_language_detected": The detected source language
- "confidence": A value from 0.0 to 1.0 indicating translation accuracy
- "alternatives": Alternate translations for ambiguous phrases (optional, can be empty array)`

	userPrompt := fmt.Sprintf("Translate this text and provide metadata:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("TranslateWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Translation{}, types.TranslateError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse JSON response
	var parsed struct {
		Text                   string                   `json:"text"`
		SourceLanguageDetected string                   `json:"source_language_detected"`
		ModelConfidence        float64                  `json:"confidence"`
		Alternatives           []TranslationAlternative `json:"alternatives"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("TranslateWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Translation{}, fmt.Errorf("translate: could not parse the translation response: %w", err)
	}

	result := Translation{
		Text:                   parsed.Text,
		SourceLanguageDetected: parsed.SourceLanguageDetected,
		ModelConfidence:        parsed.ModelConfidence,
		Alternatives:           parsed.Alternatives,
	}

	log.Debug("TranslateWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "detectedLang", result.SourceLanguageDetected)

	return result, nil
}

// Expand elaborates on text with additional detail.
// For metadata including expansion ratio and what was added, use ExpandWithMetadata.
func Expand(input string, opts ExpandOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting expand operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input), "expansionFactor", opts.ExpansionFactor)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Expand operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", fmt.Errorf("invalid options: %w", err)
	}

	// Build expansion instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), expandInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a content expansion expert. Elaborate on text with additional detail and context.

Rules:
- Add relevant details and examples
- Maintain consistency with the original
- Provide useful elaboration
- Keep the expanded content coherent`

	userPrompt := fmt.Sprintf("Expand on this text:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Expand operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return "", types.ExpandError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result := strings.TrimSpace(response)
	log.Debug("Expand operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// ExpandWithMetadata expands text with additional metadata including
// expansion ratio, what content was added, and confidence score.
// Deprecated: use ExpandResult, which returns the same Expansion inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func ExpandWithMetadata(input string, opts ExpandOptions) (Expansion, error) {
	log := logger.GetLogger()
	log.Debug("Starting expand with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("ExpandWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Expansion{}, fmt.Errorf("invalid options: %w", err)
	}

	// Build expansion instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), expandInstructions(opts))

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	systemPrompt := `You are a content expansion expert. Elaborate on text with additional detail and context.

Respond ONLY with valid JSON in this exact format:
{
  "text": "The expanded text here",
  "added_content": ["Added background context", "Included example of X", "Elaborated on Y"],
  "confidence": 0.9
}

Rules:
- "text": The complete expanded text
- "added_content": List of what was added or elaborated upon
- "confidence": A value from 0.0 to 1.0 indicating expansion quality`

	userPrompt := fmt.Sprintf("Expand on this text and provide metadata about what you added:\n%s", input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("ExpandWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Expansion{}, types.ExpandError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse JSON response
	var parsed struct {
		Text            string   `json:"text"`
		AddedContent    []string `json:"added_content"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("ExpandWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", err)
		return Expansion{}, fmt.Errorf("expand: could not parse the expansion response: %w", err)
	}

	expansionRatio := float64(len(parsed.Text)) / float64(len(input))

	result := Expansion{
		Text:            parsed.Text,
		ExpansionRatio:  expansionRatio,
		AddedContent:    parsed.AddedContent,
		ModelConfidence: parsed.ModelConfidence,
	}

	log.Debug("ExpandWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "expansionRatio", result.ExpansionRatio)

	return result, nil
}

// summaryLengthTolerance is how far past TargetLength a summary may land before
// the result is refused.
//
// Twenty percent, because the prompt says "target" and a model that returns
// four sentences for three has done what was asked. A caller who needs a hard
// ceiling wants MaxLength on a different operation, not a stricter reading of
// this one.
const summaryLengthTolerance = 0.2

// textOperationOptions folds an operation's own instruction clauses into the
// caller's steering.
//
// The caller's text comes first, because it is the more specific instruction and
// the model reads in order.
func textOperationOptions(opt types.OpOptions, callerSteering string, instructions []string) types.OpOptions {
	if len(instructions) == 0 {
		return opt
	}

	steering := strings.Join(instructions, ". ")
	if callerSteering != "" {
		steering = callerSteering + ". " + steering
	}
	opt.Steering = steering
	return opt
}

// summarizeInstructions builds the steering clauses for a summarize request.
//
// It exists once rather than twice. The block was duplicated verbatim between
// Summarize and SummarizeWithMetadata, so a rule added to one silently did not apply
// to the other -- which is what T-01 describes, and the reason two functions
// that differ only in their return type should not differ anywhere else.
func summarizeInstructions(opts SummarizeOptions) []string {
	var instructions []string

	if opts.TargetLength > 0 {
		instructions = append(instructions, fmt.Sprintf("Target length: %d %s", opts.TargetLength, opts.LengthUnit))
	}

	if opts.BulletPoints {
		instructions = append(instructions, "Format as bullet points")
	} else if opts.Style != "" {
		instructions = append(instructions, fmt.Sprintf("Style: %s", opts.Style))
	}

	if len(opts.FocusAreas) > 0 {
		instructions = append(instructions, fmt.Sprintf("Focus on: %s", strings.Join(opts.FocusAreas, ", ")))
	}

	if len(opts.PreserveInfo) > 0 {
		instructions = append(instructions, fmt.Sprintf("Must preserve: %s", strings.Join(opts.PreserveInfo, ", ")))
	}

	return instructions
}

// rewriteInstructions builds the steering clauses for a rewrite request.
//
// It exists once rather than twice. The block was duplicated verbatim between
// Rewrite and RewriteWithMetadata, so a rule added to one silently did not apply
// to the other -- which is what T-01 describes, and the reason two functions
// that differ only in their return type should not differ anywhere else.
func rewriteInstructions(opts RewriteOptions) []string {
	var instructions []string

	if opts.TargetTone != "" {
		instructions = append(instructions, fmt.Sprintf("Target tone: %s", opts.TargetTone))
	}

	if opts.FormalityLevel != 5 {
		instructions = append(instructions, fmt.Sprintf("Formality level: %d/10", opts.FormalityLevel))
	}

	if opts.Audience != "" {
		instructions = append(instructions, fmt.Sprintf("Target audience: %s", opts.Audience))
	}

	if opts.StyleGuide != "" {
		instructions = append(instructions, fmt.Sprintf("Follow style: %s", opts.StyleGuide))
	}

	if len(opts.Changes) > 0 {
		instructions = append(instructions, fmt.Sprintf("Make these changes: %s", strings.Join(opts.Changes, ", ")))
	}

	if len(opts.AvoidWords) > 0 {
		instructions = append(instructions, fmt.Sprintf("Avoid: %s", strings.Join(opts.AvoidWords, ", ")))
	}

	if len(opts.IncludeWords) > 0 {
		instructions = append(instructions, fmt.Sprintf("Include: %s", strings.Join(opts.IncludeWords, ", ")))
	}

	if opts.PreserveFacts {
		instructions = append(instructions, "Preserve all factual information")
	}

	return instructions
}

// translateInstructions builds the steering clauses for a translate request.
//
// It exists once rather than twice. The block was duplicated verbatim between
// Translate and TranslateWithMetadata, so a rule added to one silently did not apply
// to the other -- which is what T-01 describes, and the reason two functions
// that differ only in their return type should not differ anywhere else.
func translateInstructions(opts TranslateOptions) []string {
	var instructions []string

	instructions = append(instructions, fmt.Sprintf("Translate to %s", opts.TargetLanguage))

	if opts.SourceLanguage != "" {
		instructions = append(instructions, fmt.Sprintf("From %s", opts.SourceLanguage))
	}

	if opts.Dialect != "" {
		instructions = append(instructions, fmt.Sprintf("Use %s dialect", opts.Dialect))
	}

	if opts.Formality != "neutral" {
		instructions = append(instructions, fmt.Sprintf("Formality: %s", opts.Formality))
	}

	if opts.CulturalAdaptation != 5 {
		instructions = append(instructions, fmt.Sprintf("Cultural adaptation level: %d/10", opts.CulturalAdaptation))
	}

	if opts.PreserveFormatting {
		instructions = append(instructions, "Preserve formatting")
	}

	if len(opts.Glossary) > 0 {
		glossary := "Use glossary: "
		for term, translation := range opts.Glossary {
			glossary += fmt.Sprintf("%s=%s, ", term, translation)
		}
		instructions = append(instructions, strings.TrimSuffix(glossary, ", "))
	}

	return instructions
}

// expandInstructions builds the steering clauses for a expand request.
//
// It exists once rather than twice. The block was duplicated verbatim between
// Expand and ExpandWithMetadata, so a rule added to one silently did not apply
// to the other -- which is what T-01 describes, and the reason two functions
// that differ only in their return type should not differ anywhere else.
func expandInstructions(opts ExpandOptions) []string {
	var instructions []string

	if opts.ExpansionFactor != 2.0 {
		instructions = append(instructions, fmt.Sprintf("Expand by %.1fx", opts.ExpansionFactor))
	}

	instructions = append(instructions, fmt.Sprintf("Detail level: %d/10", opts.DetailLevel))

	if opts.ExpansionStyle != "" {
		instructions = append(instructions, fmt.Sprintf("Style: %s", opts.ExpansionStyle))
	}

	if opts.IncludeExamples {
		instructions = append(instructions, "Include relevant examples")
	}

	if len(opts.ElaborateOn) > 0 {
		instructions = append(instructions, fmt.Sprintf("Elaborate on: %s", strings.Join(opts.ElaborateOn, ", ")))
	}

	if len(opts.AddContext) > 0 {
		instructions = append(instructions, fmt.Sprintf("Add context about: %s", strings.Join(opts.AddContext, ", ")))
	}

	return instructions
}

// checkRewriteWordConstraints holds the rewritten text to AvoidWords and
// IncludeWords, which reached the prompt as "Avoid: ..." / "Include: ..." and
// were checked nowhere.
//
// Both are decidable against the answer without asking the model anything: the
// word is either in the text or it is not. ExcludesValues already existed in
// invariants.go for the "must not contain" half and was never called from here.
//
// The match is on word boundaries, case-insensitively. Substring matching would
// make AvoidWords("cat") refuse the word "category", which is not what a caller
// banning a word means; and a caller who writes "Bob" does not want "bob" to
// slip through. A multi-word phrase is matched as a phrase.
//
// The offending word IS named in the error. It is the caller's own option
// value, not the caller's payload -- the distinction ExcludesValues's counting
// exists to protect, which does not apply to a list the caller wrote.
func checkRewriteWordConstraints(text string, opts RewriteOptions) error {
	for _, word := range opts.AvoidWords {
		if containsWholeWord(text, word) {
			return fmt.Errorf("rewrite: the rewritten text contains %q, which AvoidWords ruled out", word)
		}
	}
	for _, word := range opts.IncludeWords {
		if !containsWholeWord(text, word) {
			return fmt.Errorf("rewrite: the rewritten text omits %q, which IncludeWords required", word)
		}
	}
	return nil
}

// containsWholeWord reports whether word appears in text delimited by something
// other than a letter or digit, case-insensitively.
//
// It does not use a regexp: the words come from the caller, and a caller who
// happens to ban "C++" or "$5" should get a literal match rather than a regexp
// compile error or an accidental metacharacter.
func containsWholeWord(text, word string) bool {
	if word == "" {
		return false
	}

	lowerText := []rune(strings.ToLower(text))
	lowerWord := []rune(strings.ToLower(word))

	for start := 0; start+len(lowerWord) <= len(lowerText); start++ {
		if !runesEqualAt(lowerText, lowerWord, start) {
			continue
		}
		// A boundary is anything that is not a letter or a digit, including the
		// ends of the text. This treats the word's own leading/trailing
		// punctuation as part of the word, which is what a caller banning "C++"
		// expects.
		if start > 0 && isWordRune(lowerText[start-1]) && isWordRune(lowerWord[0]) {
			continue
		}
		end := start + len(lowerWord)
		if end < len(lowerText) && isWordRune(lowerText[end]) && isWordRune(lowerWord[len(lowerWord)-1]) {
			continue
		}
		return true
	}
	return false
}

func runesEqualAt(text, word []rune, start int) bool {
	for i, r := range word {
		if text[start+i] != r {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
