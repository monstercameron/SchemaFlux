package ops

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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

	// ExpansionRatio is output length / input length, measured in runes. It is
	// zero when the input was empty, because there is no length to divide by.
	ExpansionRatio float64 `json:"expansion_ratio"`

	// AddedContent describes what was added
	AddedContent []string `json:"added_content,omitempty"`

	// ModelConfidence score for the expansion quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// The fixed system prompts of the plain text operations. The buffered forms
// (Summarize, Rewrite, Translate, Expand) and the streamed ones (StreamSummarize
// and the rest) send the same prompt, so each pair reads it from here and cannot
// drift; TestStreamingPromptsMatchTheBufferedOnes compares what reaches the
// provider.
const (
	summarizeSystemPrompt = `You are a text summarization expert. Create concise summaries that preserve key information.

Rules:
- Maintain the most important points
- Use clear, concise language
- Preserve critical details and context
- Keep the original tone when appropriate`

	rewriteSystemPrompt = `You are a text rewriting expert. Modify text while preserving its core meaning.

Rules:
- Maintain the original message and intent
- Improve clarity and readability
- Adapt style as requested
- Fix grammar and spelling errors`

	translateSystemPrompt = `You are a translation expert. Translate text accurately between languages.

Rules:
- Preserve meaning and nuance
- Maintain appropriate formality level
- Handle idioms and cultural references appropriately
- Keep technical terms accurate`

	expandSystemPrompt = `You are a content expansion expert. Elaborate on text with additional detail and context.

Rules:
- Add relevant details and examples
- Maintain consistency with the original
- Provide useful elaboration
- Keep the expanded content coherent`
)

// Summarize creates a concise summary of the input text.
// For metadata including key points and confidence, use SummarizeWithMetadata.
//
// TargetLength is a request and not a guarantee. It is written into the prompt
// and this form returns the model's text as it came: it measures nothing, so a
// caller who asked for 50 words can be given 400 and is told nothing. The
// result form, SummarizeResult, is the one that checks. It refuses a summary
// more than 20% over the target and reports the length that came back, whether
// that is within the target, and the tolerance, in the Metadata of its Value:
// "length_measured", "length_unit", "length_target", "length_within_target" and
// "length_tolerance". They are present only when a TargetLength was set.
func Summarize(input string, opts SummarizeOptions) (_ string, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "summarize").end(&runErr)
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting summarize operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Summarize operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build summarization instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), summarizeInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	systemPrompt := summarizeSystemPrompt

	userPrompt := "Summarize this text:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Summarize operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.SummarizeError{
			InputShape: types.DescribeValue(input),
			Length:     len(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result, err := plainTextAnswer("summarize", response)
	if err != nil {
		log.Error("Summarize operation returned no text", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	log.Debug("Summarize operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// SummarizeWithMetadata creates a summary with additional metadata including
// compression ratio, key points extracted, and confidence score.
//
// When a TargetLength was set, the summary is measured in Go and the figures
// are in Metadata: "length_measured" (counted in LengthUnit), "length_unit",
// "length_target", "length_within_target" (measured is at most the target) and
// "length_tolerance" (0.2). A summary more than the tolerance over the target
// is an error, so a summary that is returned and is not within the target is
// over it by no more than that. These are measurements made here, not claims
// of the model.
//
// An answer without a text, with an empty text, or without a confidence on the
// 0 to 1 scale is an error. When MaxCompression is set, Metadata also carries
// "compression_max" and "compression_within_max" (CompressionRatio is at most
// the maximum). That is a report and not a refusal: the prompt does not state
// MaxCompression, so a model that was never told the ceiling is not refused for
// exceeding it.
//
// Deprecated: use SummarizeResult, which returns the same Summary inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func SummarizeWithMetadata(input string, opts SummarizeOptions) (Summary, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting summarize with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("SummarizeWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Summary{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build summarization instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), summarizeInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
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

	userPrompt := "Summarize this text and provide metadata:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("SummarizeWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Summary{}, types.SummarizeError{
			InputShape: types.DescribeValue(input),
			Length:     len(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the JSON response. The prompt asks for all three members, so an
	// answer that leaves one out is refused by name, and a confidence of zero is
	// the model saying zero. An empty list of key points is how it says there
	// were none.
	var parsed struct {
		Text            string   `json:"text"`
		KeyPoints       []string `json:"key_points"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := decodeAnswer("summarize", response, &parsed); err != nil {
		// An answer that does not parse is an error. It is never returned as the
		// summary with a made-up confidence.
		log.Error("SummarizeWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Summary{}, fmt.Errorf("summarize: could not parse the summary response: %w", err)
	}

	text, confidence, err := readTextAnswer("summarize", parsed.Text, parsed.ModelConfidence)
	if err != nil {
		log.Error("SummarizeWithMetadata failed: the answer lacks what was asked for", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Summary{}, err
	}

	// TargetLength was requested in the prompt and never checked, so an
	// operation documented as producing at most N sentences produced whatever
	// the model produced. The tolerance is deliberate: a summary asked for
	// three sentences that comes back as four has done the job, and failing the
	// call would be a worse answer than the one it gave.
	if err := WithinLength(text, opts.TargetLength, LengthUnit(opts.LengthUnit), summaryLengthTolerance); err != nil {
		log.Error("SummarizeWithMetadata result is longer than requested",
			"requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Summary{}, types.Wrap(types.KindInvariantViolation, "summarize", err)
	}

	// Runes, not bytes. A summary of Japanese text measured in bytes reports a
	// compression ratio three times smaller than the one a reader would count,
	// and the ratio is the number a caller tunes MaxCompression against.
	compressionRatio := 0.0
	if inputRunes := len([]rune(input)); inputRunes > 0 {
		compressionRatio = float64(len([]rune(text))) / float64(inputRunes)
	}

	metadata := dx12SummaryLength(text, opts)
	if opts.MaxCompression > 0 && len(input) > 0 {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["compression_max"] = opts.MaxCompression
		metadata["compression_within_max"] = compressionRatio <= opts.MaxCompression
	}

	result := Summary{
		Text:             text,
		CompressionRatio: compressionRatio,
		KeyPoints:        parsed.KeyPoints,
		ModelConfidence:  confidence,
		Metadata:         metadata,
	}

	log.Debug("SummarizeWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "keyPoints", len(result.KeyPoints))

	return result, nil
}

// Rewrite transforms text according to specified parameters.
// For metadata including changes made and confidence, use RewriteWithMetadata.
func Rewrite(input string, opts RewriteOptions) (_ string, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "rewrite").end(&runErr)
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting rewrite operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Rewrite operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build rewrite instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), rewriteInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	systemPrompt := rewriteSystemPrompt

	userPrompt := "Rewrite this text:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Rewrite operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.RewriteError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result, err := plainTextAnswer("rewrite", response)
	if err != nil {
		log.Error("Rewrite operation returned no text", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	if err := checkRewriteWordConstraints(result, opts); err != nil {
		log.Error("Rewrite operation failed a word constraint", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	log.Debug("Rewrite operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// RewriteWithMetadata rewrites text with additional metadata including
// what changes were made, the achieved tone, and confidence score.
//
// An answer without a text, with an empty text, or without a confidence on the
// 0 to 1 scale is an error.
//
// Deprecated: use RewriteResult, which returns the same Rewritten inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func RewriteWithMetadata(input string, opts RewriteOptions) (Rewritten, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting rewrite with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("RewriteWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Rewritten{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build rewrite instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), rewriteInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
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

	userPrompt := "Rewrite this text and provide metadata about the changes:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("RewriteWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Rewritten{}, types.RewriteError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the JSON response. The prompt asks for all four members, so an answer
	// that leaves one out is refused by name.
	var parsed struct {
		Text            string   `json:"text"`
		ChangesMade     []string `json:"changes_made"`
		ToneAchieved    string   `json:"tone_achieved"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := decodeAnswer("rewrite", response, &parsed); err != nil {
		log.Error("RewriteWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Rewritten{}, fmt.Errorf("rewrite: could not parse the rewrite response: %w", err)
	}

	text, confidence, err := readTextAnswer("rewrite", parsed.Text, parsed.ModelConfidence)
	if err != nil {
		log.Error("RewriteWithMetadata failed: the answer lacks what was asked for", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Rewritten{}, err
	}

	result := Rewritten{
		Text:            text,
		ChangesMade:     parsed.ChangesMade,
		ToneAchieved:    parsed.ToneAchieved,
		ModelConfidence: confidence,
	}

	if err := checkRewriteWordConstraints(result.Text, opts); err != nil {
		log.Error("RewriteWithMetadata failed a word constraint", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Rewritten{}, err
	}

	log.Debug("RewriteWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "changesMade", len(result.ChangesMade))

	return result, nil
}

// Translate converts text to a target language.
// For metadata including detected source language and alternatives, use TranslateWithMetadata.
//
// A reply with no text is an error. When a Glossary was given, a term that
// occurs in the input has to appear in the translation as the translation the
// glossary names for it, or the call is an error.
func Translate(input string, opts TranslateOptions) (_ string, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "translate").end(&runErr)
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting translate operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Translate operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build translation instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), translateInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	systemPrompt := translateSystemPrompt

	userPrompt := "Translate this text:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Translate operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.TranslateError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result, err := plainTextAnswer("translate", response)
	if err != nil {
		log.Error("Translate operation returned no text", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	if err := checkGlossaryApplied(input, result, opts.Glossary); err != nil {
		log.Error("Translate operation ignored the glossary", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	log.Debug("Translate operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// TranslateWithMetadata translates text with additional metadata including
// detected source language, confidence, and alternative translations.
//
// An answer without a text, with an empty text, or without a confidence on the
// 0 to 1 scale is an error. When a Glossary was given, a term that occurs in
// the input has to appear in the translation as the translation the glossary
// names for it, or the call is an error.
//
// Deprecated: use TranslateResult, which returns the same Translation inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func TranslateWithMetadata(input string, opts TranslateOptions) (Translation, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting translate with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("TranslateWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Translation{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build translation instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), translateInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
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

	userPrompt := "Translate this text and provide metadata:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("TranslateWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Translation{}, types.TranslateError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the JSON response. The prompt asks for the text, the detected language
	// and the confidence every time, so an answer that leaves one out is refused
	// by name. The alternatives are the one member the prompt says may be left
	// out ("optional, can be empty array").
	var parsed struct {
		Text                   string                   `json:"text"`
		SourceLanguageDetected string                   `json:"source_language_detected"`
		ModelConfidence        float64                  `json:"confidence"`
		Alternatives           []TranslationAlternative `json:"alternatives,omitempty"` // optional in the prompt
	}
	if err := decodeAnswer("translate", response, &parsed); err != nil {
		log.Error("TranslateWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Translation{}, fmt.Errorf("translate: could not parse the translation response: %w", err)
	}

	text, confidence, err := readTextAnswer("translate", parsed.Text, parsed.ModelConfidence)
	if err != nil {
		log.Error("TranslateWithMetadata failed: the answer lacks what was asked for", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Translation{}, err
	}
	if err := checkGlossaryApplied(input, text, opts.Glossary); err != nil {
		log.Error("TranslateWithMetadata ignored the glossary", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Translation{}, err
	}

	result := Translation{
		Text:                   text,
		SourceLanguageDetected: parsed.SourceLanguageDetected,
		ModelConfidence:        confidence,
		Alternatives:           parsed.Alternatives,
	}

	log.Debug("TranslateWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "detectedLanguage", result.SourceLanguageDetected != "")

	return result, nil
}

// Expand elaborates on text with additional detail.
// For metadata including expansion ratio and what was added, use ExpandWithMetadata.
//
// ExpansionFactor is a request and this form measures nothing: it returns the
// model's text as it came. ExpandWithMetadata checks it.
func Expand(input string, opts ExpandOptions) (_ string, runErr error) {
	defer observedPlain(&opts.CommonOptions, &opts.OpOptions, "expand").end(&runErr)
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting expand operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input), "expansionFactor", opts.ExpansionFactor)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Expand operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build expansion instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), expandInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	systemPrompt := expandSystemPrompt

	userPrompt := "Expand on this text:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Expand operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", types.ExpandError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	result, err := plainTextAnswer("expand", response)
	if err != nil {
		log.Error("Expand operation returned no text", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return "", err
	}
	log.Debug("Expand operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result))

	return result, nil
}

// ExpandWithMetadata expands text with additional metadata including
// expansion ratio, what content was added, and confidence score.
//
// The ratio is the length of the expansion over the length of the input, both
// counted in runes, and is zero for an empty input, which has nothing to be
// measured against. An answer without a text, with an empty text, or without a
// confidence on the 0 to 1 scale is an error. When an ExpansionFactor other
// than the default was asked for, an expansion more than 20% short of it is an
// error, and "expansion_factor", "expansion_reached_factor" and
// "expansion_tolerance" are in Metadata.
//
// Deprecated: use ExpandResult, which returns the same Expansion inside a
// types.Result. The envelope keeps what the runtime measured apart from
// what the model claimed, which the Metadata map on this shape cannot.
// See OP-401.
func ExpandWithMetadata(input string, opts ExpandOptions) (Expansion, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting expand with metadata operation", "requestID", opts.CommonOptions.RequestID, "inputLength", len(input))

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("ExpandWithMetadata operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Expansion{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Build expansion instructions
	opt := textOperationOptions(opts.toOpOptions(), effectiveSteering(opts.CommonOptions, opts.OpOptions), expandInstructions(opts))

	// From opt, not resolved a second time: that derived a second deadline and
	// left the one inside opt running its timer unused.
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
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

	userPrompt := "Expand on this text and provide metadata about what you added:\n" + quotedText(input)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("ExpandWithMetadata operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Expansion{}, types.ExpandError{
			InputShape: types.DescribeValue(input),
			Reason:     err.Error(),
			Err:        err,
		}
	}

	// Parse the JSON response. The prompt asks for all three members, so an answer
	// that leaves one out is refused by name.
	var parsed struct {
		Text            string   `json:"text"`
		AddedContent    []string `json:"added_content"`
		ModelConfidence float64  `json:"confidence"`
	}
	if err := decodeAnswer("expand", response, &parsed); err != nil {
		log.Error("ExpandWithMetadata failed: parse error", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Expansion{}, fmt.Errorf("expand: could not parse the expansion response: %w", err)
	}

	text, confidence, err := readTextAnswer("expand", parsed.Text, parsed.ModelConfidence)
	if err != nil {
		log.Error("ExpandWithMetadata failed: the answer lacks what was asked for", "requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Expansion{}, err
	}

	// Runes, as Summarize counts them, and zero for an empty input: there is
	// nothing to measure an expansion against, and dividing by zero gave +Inf,
	// which json.Marshal then refuses to write.
	expansionRatio := 0.0
	inputRunes := len([]rune(input))
	if inputRunes > 0 {
		expansionRatio = float64(len([]rune(text))) / float64(inputRunes)
	}

	metadata, err := expansionFactorMetadata(opts, expansionRatio, inputRunes > 0)
	if err != nil {
		log.Error("ExpandWithMetadata result is shorter than the factor asked for",
			"requestID", opts.CommonOptions.RequestID, "error", loggedError(err))
		return Expansion{}, err
	}

	result := Expansion{
		Text:            text,
		ExpansionRatio:  expansionRatio,
		AddedContent:    parsed.AddedContent,
		ModelConfidence: confidence,
		Metadata:        metadata,
	}

	log.Debug("ExpandWithMetadata operation succeeded", "requestID", opts.CommonOptions.RequestID, "outputLength", len(result.Text), "expansionRatio", result.ExpansionRatio)

	return result, nil
}

// The defaults of the options that the instruction builders leave out of a
// prompt when they are unchanged. options.go states the same numbers in the
// New*Options constructors; they are named here so that the prompt and the
// constructor are not two separate guesses at one value.
const (
	defaultFormalityLevel     = 5
	defaultCulturalAdaptation = 5
	defaultTranslateFormality = "neutral"
	defaultExpansionFactor    = 2.0
)

// expansionTolerance is how far under ExpansionFactor an expansion may land
// before it is refused. It mirrors summaryLengthTolerance: the factor is a
// target, and a model that expands by 2.5 when asked for 3 has done the job.
const expansionTolerance = 0.2

// plainTextAnswer trims the reply of a text operation that returns the text
// itself and refuses an empty one. A reply with nothing in it was returned as a
// successful empty summary, translation or rewrite.
func plainTextAnswer(op, response string) (string, error) {
	text := strings.TrimSpace(response)
	if text == "" {
		return "", types.Newf(types.KindMalformedOutput, op, "the model returned no text")
	}
	return text, nil
}

// readTextAnswer checks what the reader cannot see in the two fields every text
// answer in JSON form carries: the reader has already refused an answer that
// leaves either out, so what is left is the meaning. A text with nothing in it
// is no summary, translation, rewrite or expansion, and the confidence is the
// model's own claim and must be on the 0 to 1 scale it was asked for.
func readTextAnswer(op string, text string, confidence float64) (string, float64, error) {
	if strings.TrimSpace(text) == "" {
		return "", 0, types.Newf(types.KindSchemaViolation, op, "text is empty")
	}
	if err := onScale(op, "the confidence", confidence); err != nil {
		return "", 0, err
	}
	return text, confidence, nil
}

// checkGlossaryApplied holds a translation to the glossary it was given. A term
// that occurs in the input has to come out as the translation the caller chose
// for it; the model was asked to use the glossary and nothing looked.
//
// A term that is not in the input is not required, and the term is found as a
// whole word while the translation is found as a substring: the target
// language may inflect it or write it without spaces. The comparison is
// case-insensitive. The term named in the error is the caller's own option
// value, not the text that was translated.
func checkGlossaryApplied(input, translated string, glossary map[string]string) error {
	lowerOut := strings.ToLower(translated)
	for _, term := range sortedKeys(glossary) {
		want := strings.TrimSpace(glossary[term])
		if strings.TrimSpace(term) == "" || want == "" {
			continue
		}
		if !containsWholeWord(input, term) {
			continue
		}
		if !strings.Contains(lowerOut, strings.ToLower(want)) {
			return types.Newf(types.KindInvariantViolation, "translate",
				"the translation does not use the glossary translation for %q, which is in the input", term)
		}
	}
	return nil
}

// expansionFactorStated reports whether ExpandOptions.ExpansionFactor reached
// the prompt, which is when it differs from the default.
func expansionFactorStated(opts ExpandOptions) bool {
	return math.Abs(opts.ExpansionFactor-defaultExpansionFactor) > 1e-9
}

// expansionFactorMetadata measures the expansion against ExpansionFactor and
// refuses one that falls short of it by more than expansionTolerance. The
// figures go in Metadata, as Summarize's length figures do: "expansion_factor",
// "expansion_reached_factor" (the measured ratio is at least the factor) and
// "expansion_tolerance". They are measurements made here, not claims of the
// model.
//
// It returns nothing when the factor was not stated in the prompt, because the
// model was not asked for one, and when the input was empty, because there is
// no ratio to compare.
func expansionFactorMetadata(opts ExpandOptions, ratio float64, measurable bool) (map[string]any, error) {
	if !expansionFactorStated(opts) || !measurable {
		return nil, nil
	}
	if ratio < opts.ExpansionFactor*(1-expansionTolerance)-1e-9 {
		return nil, types.Newf(types.KindInvariantViolation, "expand",
			"the expansion is %.2fx the input, below the %.1fx that was asked for (tolerance %.0f%%)",
			ratio, opts.ExpansionFactor, expansionTolerance*100)
	}
	return map[string]any{
		"expansion_factor":         opts.ExpansionFactor,
		"expansion_reached_factor": ratio >= opts.ExpansionFactor,
		"expansion_tolerance":      expansionTolerance,
	}, nil
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

	if opts.FormalityLevel != defaultFormalityLevel {
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

	if opts.Formality != defaultTranslateFormality {
		instructions = append(instructions, fmt.Sprintf("Formality: %s", opts.Formality))
	}

	if opts.CulturalAdaptation != defaultCulturalAdaptation {
		instructions = append(instructions, fmt.Sprintf("Cultural adaptation level: %d/10", opts.CulturalAdaptation))
	}

	if opts.PreserveFormatting {
		instructions = append(instructions, "Preserve formatting")
	}

	if len(opts.Glossary) > 0 {
		// Sorted: ranging over the map gave a differently ordered clause on
		// every call, so no prefix cache could hit and no run was reproducible.
		glossary := "Use glossary: "
		for _, term := range sortedKeys(opts.Glossary) {
			glossary += fmt.Sprintf("%s=%s, ", term, opts.Glossary[term])
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

	if expansionFactorStated(opts) {
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
			return types.Newf(types.KindInvariantViolation, "rewrite",
				"the rewritten text contains %q, which AvoidWords ruled out", word)
		}
	}
	for _, word := range opts.IncludeWords {
		if !containsWholeWord(text, word) {
			return types.Newf(types.KindInvariantViolation, "rewrite",
				"the rewritten text omits %q, which IncludeWords required", word)
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
