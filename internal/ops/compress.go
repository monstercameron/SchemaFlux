// package ops - Compress operation for semantic compression preserving meaning
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// CompressOptions configures the Compress operation
type CompressOptions struct {
	CommonOptions
	types.OpOptions

	// Target compression ratio (0.0-1.0, e.g., 0.3 = 30% of original)
	CompressionRatio float64

	// Fields to retain (for structured data)
	RetainFields []string

	// Fields to remove (for structured data)
	RemoveFields []string

	// Preserve these specific pieces of information
	PreserveInfo []string

	// Compression strategy ("lossy", "lossless", "semantic")
	Strategy string

	// Priority for what to keep ("facts", "actions", "decisions", "all")
	Priority string

	// Output format ("same", "summary", "key_points", "structured")
	OutputFormat string

	// Maximum output tokens/words
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
	if c.CompressionRatio <= 0 || c.CompressionRatio > 1 {
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
	return c.CommonOptions.toOpOptions()
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
	log := logger.GetLogger()
	log.Debug("Starting compress operation")

	var result CompressResult[T]
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
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
		return result, fmt.Errorf("failed to normalize input: %w", err)
	}
	result.OriginalSize = len(inputStr)

	// Calculate target size
	targetSize := int(float64(result.OriginalSize) * opts.CompressionRatio)
	if opts.MaxOutputSize > 0 && targetSize > opts.MaxOutputSize {
		targetSize = opts.MaxOutputSize
	}

	// Build field instructions
	fieldInstructions := ""
	if len(opts.RetainFields) > 0 {
		fieldInstructions += fmt.Sprintf("\nMust retain these fields/information: %s", strings.Join(opts.RetainFields, ", "))
	}
	if len(opts.RemoveFields) > 0 {
		fieldInstructions += fmt.Sprintf("\nRemove these fields/information: %s", strings.Join(opts.RemoveFields, ", "))
	}
	if len(opts.PreserveInfo) > 0 {
		fieldInstructions += fmt.Sprintf("\nPreserve this specific information: %s", strings.Join(opts.PreserveInfo, ", "))
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

	// CI-008, the third instance of one defect: the prose template hard-codes
	// `"compressed": {}` while that field's type is the caller's T, so
	// `Compress[string]` is shown an object where only a string will parse.
	// Predict and Decompose had the same line in a different envelope. Declaring
	// the shape from T is what the prose cannot do, because the prose is one
	// fixed string and T varies per call.
	var compressedZero T
	if compressedSchema := GenerateJSONSchema(reflect.TypeOf(compressedZero)); compressedSchema != nil {
		opt.JSONSchema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"compressed":     compressedSchema,
				"preserved_info": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"removed_info":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"compressed"},
		}
		opt.SchemaName = "compression"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Compress operation LLM call failed", "error", err)
		return result, fmt.Errorf("compression failed: %w", err)
	}

	// Parse the response
	var envelope struct {
		Compressed    json.RawMessage `json:"compressed"`
		PreservedInfo []string        `json:"preserved_info"`
		RemovedInfo   []string        `json:"removed_info"`
	}

	if err := ParseJSONStrict(response, &envelope); err != nil {
		log.Error("Compress operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse compressed result: %w", err)
	}

	if err := json.Unmarshal(envelope.Compressed, &result.Compressed); err != nil {
		var compressedString string
		if err2 := json.Unmarshal(envelope.Compressed, &compressedString); err2 != nil {
			log.Error("Compress operation failed: compressed payload parse error", "error", err)
			return result, fmt.Errorf("failed to parse compressed payload: %w", err)
		}
		if err2 := ParseJSONStrict(compressedString, &result.Compressed); err2 != nil {
			log.Error("Compress operation failed: compressed string parse error", "error", err2)
			return result, fmt.Errorf("failed to parse compressed payload string: %w", err2)
		}
	}
	result.PreservedInfo = envelope.PreservedInfo
	result.RemovedInfo = envelope.RemovedInfo

	// Calculate compressed size
	compressedStr, _ := NormalizeInput(result.Compressed)
	result.CompressedSize = len(compressedStr)
	if result.OriginalSize > 0 {
		result.ActualRatio = float64(result.CompressedSize) / float64(result.OriginalSize)
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
