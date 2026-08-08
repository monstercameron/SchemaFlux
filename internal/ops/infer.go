// package ops - Infer operation for smart missing data inference
package ops

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// InferOptions configures the Infer operation
type InferOptions struct {
	types.OpOptions
	// Background is prose the model should take into account: known facts,
	// surrounding detail. It was called Context and collided with the embedded
	// context.Context, which is a deadline rather than prompt material. X-06.
	Background string
}

// NewInferOptions creates InferOptions with defaults
func NewInferOptions() InferOptions {
	return InferOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
	}
}

// WithBackground sets prose the model should take into account. Prompt
// material, not a context.Context -- see InferOptions.Background.
func (opts InferOptions) WithBackground(background string) InferOptions {
	opts.Background = background
	return opts
}

// WithIntelligence sets the intelligence level
func (opts InferOptions) WithIntelligence(intelligence types.Speed) InferOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate validates InferOptions
func (opts InferOptions) Validate() error {
	return nil // No specific validation needed
}

// toOpOptions converts InferOptions to types.OpOptions
func (opts InferOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Infer smartly fills in missing fields in partial data using LLM inference
//
// Examples:
//
//	// Infer missing fields in a person record
//	complete, err := Infer[Person](Person{Name: "John", Age: 30},
//	    NewInferOptions().WithBackground("This person works in tech"))
//
//	// Infer product details from partial information
//	product, err := Infer[Product](Product{Name: "iPhone 15"},
//	    NewInferOptions().WithBackground("Latest Apple smartphone released in 2023"))
func Infer[T any](partialData T, opts InferOptions) (T, error) {
	return inferImpl(partialData, opts)
}

func inferImpl[T any](partialData T, opts InferOptions) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting infer operation", "requestID", opts.RequestID, "dataType", fmt.Sprintf("%T", partialData))

	var result T

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Infer operation validation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	// Get type information
	targetType := reflect.TypeOf(result)
	typeSchema := GenerateTypeSchema(targetType)

	// Marshal partial data to JSON
	partialJSON, err := json.Marshal(partialData)
	if err != nil {
		log.Error("Infer operation marshal failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to marshal partial data: %w", err)
	}

	// Build system prompt
	systemPrompt := fmt.Sprintf(`You are a data inference expert. Given partial data and context, intelligently infer missing fields.

Target schema:
%s

Rules:
- Infer missing fields based on available data and logical reasoning
- Use reasonable defaults when inference is uncertain
- Maintain data consistency and coherence
- Return ONLY valid JSON matching the complete schema`, typeSchema)

	// Build user prompt
	userPrompt := fmt.Sprintf("Complete this partial data by inferring missing fields:\n%s", string(partialJSON))

	if opts.Background != "" {
		userPrompt += fmt.Sprintf("\n\nAdditional context: %s", opts.Background)
	}

	// Call LLM for inference
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Infer operation LLM call failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("inference failed: %w", err)
	}

	// Parse inferred data
	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("Infer operation parse failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to parse inferred result: %w", err)
	}

	log.Debug("Infer operation succeeded", "requestID", opts.RequestID)

	return result, nil
}
