package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// CompleteResult contains the completion result and metadata
type CompleteResult struct {
	Text       string         `json:"text"`       // The completed text
	Original   string         `json:"original"`   // The original partial text
	Length     int            `json:"length"`     // Length of completion
	Confidence float64        `json:"confidence"` // Confidence score (0.0-1.0)
	Metadata   map[string]any `json:"metadata"`   // Additional metadata
}

// CompleteOptions configures the Complete operation
type CompleteOptions struct {
	types.OpOptions
	Context       []string // Previous context messages/text
	MaxLength     int      // Maximum length of completion
	StopSequences []string // Sequences that stop generation
	Temperature   float32  // Creativity level (0.0-2.0)
	TopP          float32  // Nucleus sampling (0.0-1.0)
	TopK          int      // Top-k sampling
}

// NewCompleteOptions creates CompleteOptions with defaults
func NewCompleteOptions() CompleteOptions {
	return CompleteOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Creative,
			Intelligence: types.Smart,
		},
		MaxLength:   100,
		Temperature: 0.7,
		TopP:        0.9,
		TopK:        50,
	}
}

// WithContext provides previous messages/text for context
func (opts CompleteOptions) WithContext(context []string) CompleteOptions {
	opts.Context = context
	return opts
}

// WithMaxLength sets the maximum completion length
func (opts CompleteOptions) WithMaxLength(maxLength int) CompleteOptions {
	opts.MaxLength = maxLength
	return opts
}

// WithStopSequences sets sequences that stop generation
func (opts CompleteOptions) WithStopSequences(sequences []string) CompleteOptions {
	opts.StopSequences = sequences
	return opts
}

// WithTemperature sets the creativity level
func (opts CompleteOptions) WithTemperature(temperature float32) CompleteOptions {
	opts.Temperature = temperature
	return opts
}

// WithTopP sets nucleus sampling parameter
func (opts CompleteOptions) WithTopP(topP float32) CompleteOptions {
	opts.TopP = topP
	return opts
}

// WithTopK sets top-k sampling parameter
func (opts CompleteOptions) WithTopK(topK int) CompleteOptions {
	opts.TopK = topK
	return opts
}

// WithIntelligence sets the intelligence level
func (opts CompleteOptions) WithIntelligence(intelligence types.Speed) CompleteOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// WithMode sets the reasoning mode
func (opts CompleteOptions) WithMode(mode types.Mode) CompleteOptions {
	opts.OpOptions.Mode = mode
	return opts
}

// Validate validates CompleteOptions
func (opts CompleteOptions) Validate() error {
	if opts.MaxLength <= 0 {
		return fmt.Errorf("MaxLength must be positive")
	}
	if opts.Temperature < 0 || opts.Temperature > 2.0 {
		return fmt.Errorf("Temperature must be between 0.0 and 2.0")
	}
	if opts.TopP <= 0 || opts.TopP > 1.0 {
		return fmt.Errorf("TopP must be between 0.0 and 1.0")
	}
	if opts.TopK <= 0 {
		return fmt.Errorf("TopK must be positive")
	}
	return nil
}

// toOpOptions converts CompleteOptions to types.OpOptions
func (opts CompleteOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Complete intelligently completes partial text using LLM intelligence.
// It uses context from previous messages/text to generate coherent completions.
// Note: This function requires a provider to be passed.
func Complete(ctx context.Context, provider llm.Provider, partialText string, opts CompleteOptions) (CompleteResult, error) {
	return completeImpl(ctx, provider, partialText, opts)
}

func completeImpl(ctx context.Context, provider llm.Provider, partialText string, opts CompleteOptions) (CompleteResult, error) {
	logger := telemetry.GetLogger()
	logger.Debug("Starting complete operation", "requestID", opts.RequestID, "partialLength", len(partialText))

	result := CompleteResult{
		Original: partialText,
		Metadata: make(map[string]any),
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		logger.Error("Complete operation validation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("invalid options: %w", err)
	}

	// Validate input
	if strings.TrimSpace(partialText) == "" {
		logger.Error("Complete operation failed: empty input", "requestID", opts.RequestID)
		return result, fmt.Errorf("partial text cannot be empty")
	}

	// Use provided context or create one with timeout
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
	}

	// Build prompt
	systemPrompt := buildCompleteSystemPrompt(opts)
	userPrompt := buildCompleteUserPrompt(partialText, opts)

	// Call LLM - use default provider if none provided
	opOpts := opts.toOpOptions()
	var response string
	var err error
	if provider != nil {
		response, err = CallLLM(ctx, provider, systemPrompt, userPrompt, opOpts)
	} else {
		response, err = callLLM(ctx, systemPrompt, userPrompt, opOpts)
	}
	if err != nil {
		logger.Error("Complete operation LLM call failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("completion failed: %w", err)
	}

	// Process response
	completedText := processCompletionResponse(response, partialText, opts)

	result.Text = completedText
	result.Length = len(completedText) - len(partialText)

	// Add metadata
	result.Metadata["model"] = "llm-completion"
	result.Metadata["temperature"] = opts.Temperature
	result.Metadata["max_length"] = opts.MaxLength
	result.Metadata["context_messages"] = len(opts.Context)

	logger.Debug("Complete operation succeeded", "requestID", opts.RequestID, "completionLength", result.Length)

	return result, nil
}

// buildCompleteSystemPrompt creates the system prompt for completion
func buildCompleteSystemPrompt(opts CompleteOptions) string {
	prompt := "You are an intelligent text completion assistant. Complete the given partial text naturally and coherently.\n\n"

	switch opts.OpOptions.Mode {
	case types.Strict:
		prompt += "Complete the text following strict grammatical and logical rules. Maintain formal tone and precision.\n"
	case types.TransformMode:
		prompt += "Complete the text in a balanced, natural way. Adapt to the existing style and context.\n"
	case types.Creative:
		prompt += "Complete the text creatively and engagingly. Feel free to be imaginative while staying relevant.\n"
	}

	if len(opts.Context) > 0 {
		prompt += "Use the provided context to understand the conversation flow and maintain coherence.\n"
	}

	if len(opts.StopSequences) > 0 {
		prompt += fmt.Sprintf("Stop generation when you encounter any of these sequences: %s\n",
			strings.Join(opts.StopSequences, ", "))
	}

	prompt += fmt.Sprintf("Limit your completion to approximately %d characters.\n", opts.MaxLength)
	prompt += "Return only the completion text, no explanations or metadata."

	return prompt
}

// buildCompleteUserPrompt creates the user prompt for completion
func buildCompleteUserPrompt(partialText string, opts CompleteOptions) string {
	prompt := ""

	// Add context if provided
	if len(opts.Context) > 0 {
		prompt += "Context:\n"
		for i, ctx := range opts.Context {
			prompt += fmt.Sprintf("%d. %s\n", i+1, ctx)
		}
		prompt += "\n"
	}

	prompt += fmt.Sprintf("Complete this text: %s", partialText)

	return prompt
}

// processCompletionResponse processes the LLM response
func processCompletionResponse(response, originalText string, opts CompleteOptions) string {
	// Clean up the response
	response = strings.TrimSpace(response)

	// Remove any unwanted prefixes that might repeat the original text
	if strings.HasPrefix(response, originalText) {
		response = strings.TrimPrefix(response, originalText)
		response = strings.TrimSpace(response)
	}

	// Apply stop sequences
	for _, stopSeq := range opts.StopSequences {
		if idx := strings.Index(response, stopSeq); idx != -1 {
			response = response[:idx]
			break
		}
	}

	// Combine original with completion
	completedText := originalText
	if response != "" {
		// Add space if needed
		if !strings.HasSuffix(originalText, " ") && !strings.HasPrefix(response, " ") {
			completedText += " "
		}
		completedText += response
	}

	// Limit total length
	maxTotalLength := len(originalText) + opts.MaxLength
	if len(completedText) > maxTotalLength {
		completedText = completedText[:maxTotalLength]
		// Try to cut at word boundary
		if lastSpace := strings.LastIndex(completedText, " "); lastSpace > len(originalText) {
			completedText = completedText[:lastSpace]
		}
	}

	return completedText
}

// CompleteFieldResult contains the result of completing a field in a struct
type CompleteFieldResult[T any] struct {
	Data       T              `json:"data"`       // The struct with the completed field
	Field      string         `json:"field"`      // The field that was completed
	Original   string         `json:"original"`   // Original field value
	Completed  string         `json:"completed"`  // Completed field value
	Length     int            `json:"length"`     // Characters added
	Confidence float64        `json:"confidence"` // Confidence score (0.0-1.0)
	Metadata   map[string]any `json:"metadata"`   // Additional metadata
}

// CompleteFieldOptions extends CompleteOptions with field-specific settings
type CompleteFieldOptions struct {
	CompleteOptions
	FieldName string // The field to complete (must be a string field)
}

// NewCompleteFieldOptions creates CompleteFieldOptions with defaults
func NewCompleteFieldOptions(fieldName string) CompleteFieldOptions {
	return CompleteFieldOptions{
		CompleteOptions: NewCompleteOptions(),
		FieldName:       fieldName,
	}
}

// WithFieldName sets the field to complete
func (opts CompleteFieldOptions) WithFieldName(fieldName string) CompleteFieldOptions {
	opts.FieldName = fieldName
	return opts
}

// WithContext provides previous messages/text for context
func (opts CompleteFieldOptions) WithContext(context []string) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithContext(context)
	return opts
}

// WithMaxLength sets the maximum completion length
func (opts CompleteFieldOptions) WithMaxLength(maxLength int) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithMaxLength(maxLength)
	return opts
}

// WithTemperature sets the creativity level
func (opts CompleteFieldOptions) WithTemperature(temperature float32) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithTemperature(temperature)
	return opts
}

// WithIntelligence sets the intelligence level
func (opts CompleteFieldOptions) WithIntelligence(intelligence types.Speed) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithIntelligence(intelligence)
	return opts
}

// WithMode sets the reasoning mode
func (opts CompleteFieldOptions) WithMode(mode types.Mode) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithMode(mode)
	return opts
}

// CompleteField completes a specific string field in a struct and returns a new copy
// with the completed field. The struct context is used to inform the completion.
//
// Example:
//
//	type BlogPost struct {
//	    Title string `json:"title"`
//	    Body  string `json:"body"`
//	}
//	post := BlogPost{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming"}
//	result, err := CompleteField[BlogPost](post, NewCompleteFieldOptions("Body"))
//	// result.Data.Body now contains the completed text
func CompleteField[T any](ctx context.Context, provider llm.Provider, data T, opts CompleteFieldOptions) (CompleteFieldResult[T], error) {
	logger := telemetry.GetLogger()
	logger.Debug("Starting complete field operation", "requestID", opts.RequestID, "field", opts.FieldName)

	result := CompleteFieldResult[T]{
		Data:     data,
		Field:    opts.FieldName,
		Metadata: make(map[string]any),
	}

	// Validate field name
	if opts.FieldName == "" {
		return result, fmt.Errorf("field name is required")
	}

	// Get the value using reflection
	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return result, fmt.Errorf("data must be a struct, got %T", data)
	}

	// Find the field
	field := val.FieldByName(opts.FieldName)
	if !field.IsValid() {
		return result, fmt.Errorf("field %q not found in struct", opts.FieldName)
	}
	if field.Kind() != reflect.String {
		return result, fmt.Errorf("field %q must be a string, got %s", opts.FieldName, field.Kind())
	}

	originalValue := field.String()
	result.Original = originalValue

	if strings.TrimSpace(originalValue) == "" {
		return result, fmt.Errorf("field %q is empty, nothing to complete", opts.FieldName)
	}

	// Build context from other fields in the struct
	structContext := buildStructContext(val, opts.FieldName)
	if len(structContext) > 0 {
		// Prepend struct context to user-provided context
		opts.CompleteOptions.Context = append(structContext, opts.CompleteOptions.Context...)
	}

	// Complete the field value
	completeResult, err := completeImpl(ctx, provider, originalValue, opts.CompleteOptions)
	if err != nil {
		logger.Error("Complete field operation failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to complete field: %w", err)
	}

	result.Completed = completeResult.Text
	result.Length = completeResult.Length
	result.Confidence = completeResult.Confidence
	result.Metadata = completeResult.Metadata
	result.Metadata["field"] = opts.FieldName

	// Create a new copy of the struct with the completed field
	newData, err := copyWithUpdatedField(data, opts.FieldName, completeResult.Text)
	if err != nil {
		logger.Error("Failed to update struct field", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("failed to update field: %w", err)
	}
	result.Data = newData

	logger.Debug("Complete field operation succeeded", "requestID", opts.RequestID, "field", opts.FieldName, "length", result.Length)

	return result, nil
}

// buildStructContext extracts context from other fields in the struct
func buildStructContext(val reflect.Value, excludeField string) []string {
	var context []string

	typ := val.Type()
	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == excludeField {
			continue
		}

		fieldVal := val.Field(i)

		// Only include exported string fields with content
		if !field.IsExported() {
			continue
		}

		var fieldStr string
		switch fieldVal.Kind() {
		case reflect.String:
			fieldStr = fieldVal.String()
		case reflect.Slice:
			if fieldVal.Len() > 0 {
				// For slices, try to get a summary
				items := make([]string, 0, fieldVal.Len())
				for j := 0; j < fieldVal.Len() && j < 5; j++ {
					items = append(items, fmt.Sprintf("%v", fieldVal.Index(j).Interface()))
				}
				fieldStr = strings.Join(items, ", ")
				if fieldVal.Len() > 5 {
					fieldStr += fmt.Sprintf(" (and %d more)", fieldVal.Len()-5)
				}
			}
		default:
			fieldStr = fmt.Sprintf("%v", fieldVal.Interface())
		}

		if strings.TrimSpace(fieldStr) != "" && fieldStr != "0" && fieldStr != "false" {
			// Use json tag if available for cleaner context
			jsonTag := field.Tag.Get("json")
			if jsonTag != "" {
				jsonTag = strings.Split(jsonTag, ",")[0]
			} else {
				jsonTag = field.Name
			}
			context = append(context, fmt.Sprintf("%s: %s", jsonTag, fieldStr))
		}
	}

	return context
}

// copyWithUpdatedField creates a copy of the struct with the specified field updated
func copyWithUpdatedField[T any](data T, fieldName, newValue string) (T, error) {
	var result T

	// Marshal to JSON
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return result, fmt.Errorf("failed to marshal struct: %w", err)
	}

	// Unmarshal to map for modification
	var dataMap map[string]any
	if err := json.Unmarshal(jsonBytes, &dataMap); err != nil {
		return result, fmt.Errorf("failed to unmarshal to map: %w", err)
	}

	// Find the JSON key for the field
	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	typ := val.Type()

	jsonKey := fieldName
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Name == fieldName {
			tag := typ.Field(i).Tag.Get("json")
			if tag != "" {
				jsonKey = strings.Split(tag, ",")[0]
			}
			break
		}
	}

	// Update the field
	dataMap[jsonKey] = newValue

	// Marshal back
	updatedBytes, err := json.Marshal(dataMap)
	if err != nil {
		return result, fmt.Errorf("failed to marshal updated map: %w", err)
	}

	// Unmarshal to result
	if err := json.Unmarshal(updatedBytes, &result); err != nil {
		return result, fmt.Errorf("failed to unmarshal to struct: %w", err)
	}

	return result, nil
}
