package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/monstercameron/schemaflux/internal/types"
)

// CompressOptions configures the Compress operation
type CompressOptions struct {
	CommonOptions
	types.OpOptions

	// Target compression ratio (0.0-1.0, e.g., 0.3 = 30% of original)
	CompressionRatio float64

	// Fields to retain (for structured data). An entry that is a field of the
	// input object has to still be in the compressed object, or the answer is
	// refused. An entry that is not a field of the input is free text, which
	// nothing can look for, and is a request only.
	RetainFields []string

	// Fields to remove (for structured data). A field named here that is still
	// in the compressed object makes the answer refused.
	RemoveFields []string

	// Preserve these specific pieces of information. This is free text: it goes
	// to the model as a request and is not checked.
	PreserveInfo []string

	// Compression strategy ("lossy", "lossless", "semantic")
	Strategy string

	// Priority for what to keep ("facts", "actions", "decisions", "all")
	Priority string

	// Output format ("same", "summary", "key_points", "structured")
	OutputFormat string

	// MaxOutputSize is a hard ceiling on the compressed output, in characters.
	//
	// The unit is characters because that is what the prompt states ("Target
	// size: approximately %d characters") and what CompressedSize measures. This
	// field used to be documented as "tokens/words", which agreed with neither,
	// and was enforced against nothing at all -- so the disagreement never
	// surfaced. CompressionRatio remains a target the model aims at; this is the
	// line it may not cross.
	MaxOutputSize int
}

// NewCompressOptions creates CompressOptions with defaults
func NewCompressOptions() CompressOptions {
	return CompressOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		CompressionRatio: 0.3,
		Strategy:         "semantic",
		Priority:         "all",
		OutputFormat:     "same",
	}
}

// Validate validates CompressOptions
func (c CompressOptions) Validate() error {
	if err := c.CommonOptions.Validate(); err != nil {
		return err
	}
	// Written as the range that is allowed, so a NaN is refused.
	if !(c.CompressionRatio > 0 && c.CompressionRatio <= 1) {
		return fmt.Errorf("compression ratio must be between 0 and 1, got %f", c.CompressionRatio)
	}
	validStrategies := map[string]bool{"lossy": true, "lossless": true, "semantic": true}
	if c.Strategy != "" && !validStrategies[c.Strategy] {
		return fmt.Errorf("invalid strategy: %s", c.Strategy)
	}
	validPriorities := map[string]bool{"facts": true, "actions": true, "decisions": true, "all": true}
	if c.Priority != "" && !validPriorities[c.Priority] {
		return fmt.Errorf("invalid priority: %s", c.Priority)
	}
	validFormats := map[string]bool{"same": true, "summary": true, "key_points": true, "structured": true}
	if c.OutputFormat != "" && !validFormats[c.OutputFormat] {
		return fmt.Errorf("invalid output format: %s", c.OutputFormat)
	}
	return nil
}

// WithCompressionRatio sets the target compression ratio
func (c CompressOptions) WithCompressionRatio(ratio float64) CompressOptions {
	c.CompressionRatio = ratio
	return c
}

// WithRetainFields sets fields to retain
func (c CompressOptions) WithRetainFields(fields []string) CompressOptions {
	c.RetainFields = fields
	return c
}

// WithRemoveFields sets fields to remove
func (c CompressOptions) WithRemoveFields(fields []string) CompressOptions {
	c.RemoveFields = fields
	return c
}

// WithPreserveInfo sets information to preserve
func (c CompressOptions) WithPreserveInfo(info []string) CompressOptions {
	c.PreserveInfo = info
	return c
}

// WithStrategy sets the compression strategy
func (c CompressOptions) WithStrategy(strategy string) CompressOptions {
	c.Strategy = strategy
	return c
}

// WithPriority sets the priority for what to keep
func (c CompressOptions) WithPriority(priority string) CompressOptions {
	c.Priority = priority
	return c
}

// WithOutputFormat sets the output format
func (c CompressOptions) WithOutputFormat(format string) CompressOptions {
	c.OutputFormat = format
	return c
}

// WithMaxOutputSize sets the maximum output size
func (c CompressOptions) WithMaxOutputSize(size int) CompressOptions {
	c.MaxOutputSize = size
	return c
}

// WithSteering sets the steering prompt
func (c CompressOptions) WithSteering(steering string) CompressOptions {
	c.CommonOptions = c.CommonOptions.WithSteering(steering)
	return c
}

// WithMode sets the mode
func (c CompressOptions) WithMode(mode types.Mode) CompressOptions {
	c.CommonOptions = c.CommonOptions.WithMode(mode)
	return c
}

// WithIntelligence sets the intelligence level
func (c CompressOptions) WithIntelligence(intelligence types.Speed) CompressOptions {
	c.CommonOptions = c.CommonOptions.WithIntelligence(intelligence)
	return c
}

func (c CompressOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(c.CommonOptions, c.OpOptions)
}

func (c CompressOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return c.CommonOptions, c.OpOptions
}

// CompressResult contains the results of compression
type CompressResult[T any] struct {
	Compressed     T              `json:"compressed"`
	OriginalSize   int            `json:"original_size"`
	CompressedSize int            `json:"compressed_size"`
	ActualRatio    float64        `json:"actual_ratio"`
	PreservedInfo  []string       `json:"preserved_info,omitempty"`
	RemovedInfo    []string       `json:"removed_info,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// Compress performs semantic compression on data, preserving essential meaning.
// Unlike summarization, Compress works on both text and structured data,
// and focuses on information density rather than narrative flow.
//
// Type parameter T specifies the type of data to compress.
//
// Examples:
//
//	// Compress text to 30% of original
//	result, err := Compress(longText, NewCompressOptions().
//	    WithCompressionRatio(0.3))
//
//	// Compress structured data, keeping specific fields
//	result, err := Compress(data, NewCompressOptions().
//	    WithRetainFields([]string{"key_facts", "decisions"}).
//	    WithStrategy("semantic"))
//
//	// Compress for LLM context window optimization
//	result, err := Compress(conversationHistory, NewCompressOptions().
//	    WithPriority("facts").
//	    WithMaxOutputSize(2000))
func Compress[T any](input T, opts CompressOptions) (CompressResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting compress operation")

	// Every return that carries an error carries the zero result with it.

	// Validate options
	if err := opts.Validate(); err != nil {
		return CompressResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert input to string for size calculation
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Compress operation failed: input normalization error", "error", err)
		return CompressResult[T]{}, fmt.Errorf("failed to normalize input: %w", err)
	}
	// Sizes are counted in characters, as MaxOutputSize is documented and as the
	// prompt states the target: len() counted bytes, so three bytes stood for
	// each Japanese character and a 1,000-character answer failed a
	// 2,000-character ceiling.
	originalSize := runeLen(inputStr)

	// Calculate target size
	targetSize := int(float64(originalSize) * opts.CompressionRatio)
	if opts.MaxOutputSize > 0 && targetSize > opts.MaxOutputSize {
		targetSize = opts.MaxOutputSize
	}

	// Build field instructions. The entries are the caller's text and are
	// written as JSON lists.
	fieldInstructions := ""
	if len(opts.RetainFields) > 0 {
		quoted, _ := promptJSON(opts.RetainFields)
		fieldInstructions += fmt.Sprintf("\nMust retain these fields/information: %s", quoted)
	}
	if len(opts.RemoveFields) > 0 {
		quoted, _ := promptJSON(opts.RemoveFields)
		fieldInstructions += fmt.Sprintf("\nRemove these fields/information: %s", quoted)
	}
	if len(opts.PreserveInfo) > 0 {
		quoted, _ := promptJSON(opts.PreserveInfo)
		fieldInstructions += fmt.Sprintf("\nPreserve this specific information: %s", quoted)
	}

	strategyDesc := ""
	switch opts.Strategy {
	case "lossy":
		strategyDesc = "Aggressive compression, some information loss is acceptable for better compression."
	case "lossless":
		strategyDesc = "Preserve all information, only remove redundancy and verbose expressions."
	case "semantic":
		strategyDesc = "Preserve meaning and essential information, can restructure and rephrase."
	}

	priorityDesc := ""
	switch opts.Priority {
	case "facts":
		priorityDesc = "Prioritize keeping factual information and data."
	case "actions":
		priorityDesc = "Prioritize keeping action items and to-dos."
	case "decisions":
		priorityDesc = "Prioritize keeping decisions and conclusions."
	case "all":
		priorityDesc = "Balance all types of information."
	}

	outputDesc := ""
	switch opts.OutputFormat {
	case "same":
		outputDesc = "Maintain the same format as input (text stays text, JSON stays JSON)."
	case "summary":
		outputDesc = "Output as a flowing summary paragraph."
	case "key_points":
		outputDesc = "Output as bullet points of key information."
	case "structured":
		outputDesc = "Output as structured JSON with labeled sections."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at semantic data compression. Compress the input while preserving essential meaning.

Target size: approximately %d characters (%.0f%% of original)
Strategy: %s
Priority: %s
Output format: %s%s

For text: Remove redundancy, verbose expressions, and less important details.
For structured data: Remove or simplify fields while keeping essential information.

Return a JSON object with:
{
  "compressed": {},
  "preserved_info": ["list of key information preserved"],
  "removed_info": ["list of information removed or simplified"]
}`, targetSize, opts.CompressionRatio*100, strategyDesc, priorityDesc, outputDesc, fieldInstructions)

	userPrompt := fmt.Sprintf("Compress this:\n\n%s", inputStr)

	// the third instance of one defect: the prose template hard-codes
	// `"compressed": {}` while that field's type is the caller's T, so
	// `Compress[string]` is shown an object where only a string will parse.
	// Predict and Decompose had the same line in a different envelope. Declaring
	// the shape from T is what the prose cannot do, because the prose is one
	// fixed string and T varies per call.
	var compressedZero T
	// The type's schema is the plan's own: it is put into the envelope below as it
	// is and nothing changes it, so it is not copied.
	if compressedSchema := typePlanFor(reflect.TypeOf(compressedZero)).sharedSchema(); compressedSchema != nil {
		// an open object with one required field is refused by the
		// live API in strict mode, so this operation could not complete a
		// call. strictEnvelope closes it and declares the two lists as
		// unions with null.
		stringList := listOf(map[string]any{"type": "string"})
		if schema := strictEnvelope(map[string]any{
			"compressed":     compressedSchema,
			"preserved_info": stringList,
			"removed_info":   stringList,
		}, "compressed"); schema != nil {
			opt.JSONSchema = schema
			opt.SchemaName = "compression"
		}
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Compress operation LLM call failed", "error", err)
		return CompressResult[T]{}, fmt.Errorf("compression failed: %w", err)
	}

	// Parse the response. The declared schema requires all three members, and lets
	// the two lists be null, which is read as no entries. A missing one is
	// refused by name.
	var envelope struct {
		Compressed    json.RawMessage `json:"compressed"`
		PreservedInfo []string        `json:"preserved_info"`
		RemovedInfo   []string        `json:"removed_info"`
	}

	if err := decodeAnswer("compress", response, &envelope); err != nil {
		log.Error("Compress operation failed: parse error", "error", err)
		return CompressResult[T]{}, fmt.Errorf("failed to parse compressed result: %w", err)
	}

	// The compressed value is the answer. A null for it decoded into T's zero
	// value without an error, so Compress[string] returned "" with a size of
	// zero, which passes any maximum. A RawMessage can hold the null, so the
	// reader lets it through.
	if isJSONNull(envelope.Compressed) {
		log.Error("Compress operation failed: the compressed value is null")
		return CompressResult[T]{}, types.Newf(types.KindSchemaViolation, "compress",
			"compressed is null, so there is nothing to return")
	}

	// Decoded into a local, so that an answer that fails halfway leaves nothing
	// in the result.
	var compressed T
	if err := decodeCallerAnswer("compress", string(envelope.Compressed), &compressed); err != nil {
		var compressedString string
		if err2 := json.Unmarshal(envelope.Compressed, &compressedString); err2 != nil {
			log.Error("Compress operation failed: compressed payload parse error", "error", err)
			return CompressResult[T]{}, fmt.Errorf("failed to parse compressed payload: %w", err)
		}
		var reset T
		compressed = reset
		if err2 := decodeCallerAnswer("compress", compressedString, &compressed); err2 != nil {
			log.Error("Compress operation failed: compressed string parse error", "error", err2)
			return CompressResult[T]{}, fmt.Errorf("failed to parse compressed payload string: %w", err2)
		}
	}

	// Calculate compressed size, in characters. A value that cannot be
	// measured is an error: dropping it left the size at zero, and a size of
	// zero cannot exceed MaxOutputSize.
	compressedStr, err := NormalizeInput(compressed)
	if err != nil {
		log.Error("Compress operation failed: the compressed value cannot be measured", "error", err)
		return CompressResult[T]{}, types.Newf(types.KindSchemaViolation, "compress",
			"the compressed value has no text to measure, so its size cannot be checked")
	}
	result := CompressResult[T]{
		Compressed:     compressed,
		OriginalSize:   originalSize,
		CompressedSize: runeLen(compressedStr),
		PreservedInfo:  envelope.PreservedInfo,
		RemovedInfo:    envelope.RemovedInfo,
		Metadata:       make(map[string]any),
	}
	if result.OriginalSize > 0 {
		result.ActualRatio = float64(result.CompressedSize) / float64(result.OriginalSize)
	}

	// MaxOutputSize is documented as a maximum and was compared against nothing,
	// even though this function had already measured the output two lines above.
	// A caller sizing a context window by it got no signal when the answer did
	// not fit.
	//
	// CompressionRatio is deliberately NOT enforced. The prompt states it as
	// "approximately", it is a target rather than a bound, and a compression
	// that beat the target is a better answer, not a violation.
	if opts.MaxOutputSize > 0 && result.CompressedSize > opts.MaxOutputSize {
		log.Error("Compress produced output above the requested maximum",
			"compressedSize", result.CompressedSize, "maxOutputSize", opts.MaxOutputSize)
		return CompressResult[T]{}, types.Newf(types.KindInvariantViolation, "compress",
			"the compressed output is %d characters, above the requested maximum of %d",
			result.CompressedSize, opts.MaxOutputSize)
	}

	// RetainFields is stated in the prompt as "Must retain these
	// fields/information" -- "must", not "prefer" -- and a field named there
	// that is missing from the compressed output is the one outcome the option
	// exists to prevent. Only structured output is checked: when the compressed
	// value is not a JSON object there are no named fields to look for, and the
	// option is documented as being for structured data.
	//
	// The check runs against the model's own JSON, not against
	// result.Compressed. Decoding into T fills every field the Go type
	// declares, so a struct always looks like it carries "ssn" -- with an
	// empty value. The wire object is the only place a dropped field is
	// still visible as dropped.
	if wire := compressedObject(envelope.Compressed); wire != nil {
		// An entry is looked for only when it is a field of the input. The
		// prompt calls the entries "fields/information", so one that is free
		// text would be refused as a missing key on a correct answer.
		source := jsonObjectOf(input)
		for _, field := range opts.RetainFields {
			if _, isField := source[field]; !isField {
				continue
			}
			if _, present := wire[field]; !present {
				log.Error("Compress dropped a field named in RetainFields", "field", field)
				return CompressResult[T]{}, types.Newf(types.KindInvariantViolation, "compress",
					"field %q is named in RetainFields and is missing from the compressed output", field)
			}
		}
		// RemoveFields is the opposite and equally exact: the key is there or
		// it is not.
		for _, field := range opts.RemoveFields {
			if _, present := wire[field]; present {
				log.Error("Compress kept a field named in RemoveFields", "field", field)
				return CompressResult[T]{}, types.Newf(types.KindInvariantViolation, "compress",
					"field %q is named in RemoveFields and is still in the compressed output", field)
			}
		}
	}

	log.Debug("Compress operation succeeded",
		"originalSize", result.OriginalSize,
		"compressedSize", result.CompressedSize,
		"actualRatio", result.ActualRatio)
	return result, nil
}

// CompressText is a convenience function for compressing plain text
func CompressText(input string, opts CompressOptions) (string, error) {
	result, err := Compress(input, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result.Compressed), nil
}

// compressedObject renders the model's "compressed" value as a generic JSON
// object, or nil when it is not one.
//
// It handles the double-encoded case Compress already tolerates above: some
// answers put the whole object in a JSON string rather than returning it
// directly, and a retained field is just as dropped either way.
func compressedObject(raw json.RawMessage) map[string]any {
	if object := jsonObjectOf(raw); object != nil {
		return object
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(encoded), &object); err != nil {
		return nil
	}
	return object
}
