package ops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// CompleteResult contains the completion result and metadata
type CompleteResult struct {
	Text     string `json:"text"`     // The completed text
	Original string `json:"original"` // The original partial text
	Length   int    `json:"length"`   // Length of completion
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64        `json:"confidence"` // ModelConfidence score (0.0-1.0)
	Metadata        map[string]any `json:"metadata"`   // Additional metadata
}

// CompleteOptions configures the Complete operation
type CompleteOptions struct {
	types.OpOptions

	// Background is prose the model should take into account: previous
	// messages, surrounding text, known facts.
	//
	// It was called Context, which collided with the embedded
	// types.OpOptions.Context -- a context.Context, for cancellation. Two
	// different things reachable through the same selector, one of which
	// carries a deadline and one of which is prompt material. The collision
	// produced a compile error when cancellation was threaded through, which
	// is more warning than a caller reading the field list ever got.
	Background []string

	MaxLength int // Maximum length of completion, in characters

	// StopSequences end the completion: the text is cut at the earliest place
	// in it where any of them occurs, and the sequence itself is dropped. An
	// empty sequence matches everywhere and is ignored.
	StopSequences []string

	// Temperature, TopP and TopK are checked by Validate and recorded in the
	// result's Metadata. They are not sent to the model: the provider interface
	// carries no sampling settings, so setting them changes nothing about the
	// completion. They stay until the options are redesigned.
	Temperature float32 // Creativity level (0.0-2.0)
	TopP        float32 // Nucleus sampling (0.0-1.0)
	TopK        int     // Top-k sampling
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

// WithBackground provides previous messages or surrounding text for the model
// to take into account. It is prompt material, not a context.Context.
func (opts CompleteOptions) WithBackground(background []string) CompleteOptions {
	opts.Background = background
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
		return errors.New("MaxLength must be positive")
	}
	// Written as the range that is allowed rather than the ranges that are
	// not, so a NaN, which is neither below nor above anything, is refused.
	if !(opts.Temperature >= 0 && opts.Temperature <= 2.0) {
		return errors.New("temperature must be between 0.0 and 2.0")
	}
	if !(opts.TopP > 0 && opts.TopP <= 1.0) {
		return errors.New("TopP must be between 0.0 and 1.0")
	}
	if opts.TopK <= 0 {
		return errors.New("TopK must be positive")
	}
	return nil
}

// toOpOptions converts CompleteOptions to types.OpOptions
func (opts CompleteOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Complete intelligently completes partial text using LLM intelligence.
// It uses context from previous messages/text to generate coherent completions.
// It takes no provider argument. It used to, and it was the only operation in
// the library that did -- which leaked internal/llm into a public signature and
// made Complete the one call a caller had to configure differently from every
// other one. A caller who needs a specific provider installs it on the client;
// that is what the client is.
func Complete(ctx context.Context, partialText string, opts CompleteOptions) (CompleteResult, error) {
	return completeImpl(ctx, partialText, opts)
}

func completeImpl(ctx context.Context, partialText string, opts CompleteOptions) (CompleteResult, error) {
	if ctx == nil {
		ctx = opts.OpOptions.Context
	}
	logger := loggerFor(ctx)
	logger.Debug("Starting complete operation", "requestID", opts.RequestID, "partialLength", len(partialText))

	// Every return that carries an error carries the zero result with it.
	result := CompleteResult{
		Original: partialText,
		Metadata: make(map[string]any),
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		logger.Error("Complete operation validation failed", "requestID", opts.RequestID, "error", loggedError(err))
		return CompleteResult{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Validate input
	if strings.TrimSpace(partialText) == "" {
		logger.Error("Complete operation failed: empty input", "requestID", opts.RequestID)
		return CompleteResult{}, types.Classified(types.KindConfiguration, errors.New("partial text cannot be empty"))
	}

	// The default deadline applies to every caller who set none. It was
	// applied only when ctx was nil, so a caller who passed any context, which
	// includes every call made through a Client, ran with no timeout.
	ctx, cancel := operationContext(ctx, 30*time.Second)
	defer cancel()

	// Build prompt
	systemPrompt := buildCompleteSystemPrompt(opts)
	userPrompt := buildCompleteUserPrompt(partialText, opts)

	// Call the LLM through the same path as every other operation.
	opOpts := opts.toOpOptions()
	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		logger.Error("Complete operation LLM call failed", "requestID", opts.RequestID, "error", loggedError(err))
		return CompleteResult{}, fmt.Errorf("completion failed: %w", err)
	}

	// Process response
	completedText := processCompletionResponse(response, partialText, opts)

	// A completion that adds nothing is not a completion. The model returned
	// nothing, or only the text it was given, or only a stop sequence, and
	// reporting that as a success left the caller holding their own input
	// under a nil error.
	if completedText == partialText {
		logger.Error("Complete operation failed: the model added no text", "requestID", opts.RequestID)
		return CompleteResult{}, types.Newf(types.KindMalformedOutput, "complete", "the model's answer added no text to the input")
	}

	result.Text = completedText
	// Characters, as MaxLength is: a byte difference reported 3 per Japanese
	// character.
	result.Length = runeLen(completedText) - runeLen(partialText)

	// Add metadata. The model that answered is not recorded here: this layer is
	// not told which one it was, and a constant standing in for it was worse
	// than no entry.
	result.Metadata["temperature"] = opts.Temperature
	result.Metadata["max_length"] = opts.MaxLength
	result.Metadata["context_messages"] = len(opts.Background)

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

	if len(opts.Background) > 0 {
		prompt += "Use the provided context to understand the conversation flow and maintain coherence.\n"
	}

	// The sequences are the caller's text, written as a JSON list so that one of
	// them cannot read as an instruction. An empty one is left out: it matches
	// everywhere and is ignored when the answer is cut.
	if stops := nonEmptyStopSequences(opts.StopSequences); len(stops) > 0 {
		// A list of strings always encodes.
		quoted, _ := promptJSON(stops)
		prompt += fmt.Sprintf("Stop generation when you encounter any of these sequences: %s\n", quoted)
	}

	prompt += fmt.Sprintf("Limit your completion to approximately %d characters.\n", opts.MaxLength)
	prompt += "Return only the completion text, no explanations or metadata."

	return prompt
}

// buildCompleteUserPrompt creates the user prompt for completion
func buildCompleteUserPrompt(partialText string, opts CompleteOptions) string {
	prompt := ""

	// Add context if provided
	if len(opts.Background) > 0 {
		prompt += "Context:\n"
		for i, note := range opts.Background {
			quoted, _ := promptJSON(note)
			prompt += fmt.Sprintf("%d. %s\n", i+1, quoted)
		}
		prompt += "\n"
	}

	quotedText, _ := promptJSON(partialText)
	prompt += fmt.Sprintf("Complete this text: %s", quotedText)

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

	// Cut at the earliest place any stop sequence occurs. Taking the first one in
	// the caller's list that occurs at all cut "xAyB" at "B" when the sequences
	// were ["B", "A"], leaving the "A" in the text. An empty sequence is found at
	// offset 0 and wiped the whole completion, so those are skipped.
	if cut := earliestStop(response, opts.StopSequences); cut >= 0 {
		response = response[:cut]
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

	// Limit total length. MaxLength is documented in characters, and these were
	// byte offsets: a cut landing mid-rune produced invalid UTF-8, so any
	// non-ASCII completion could come back corrupted at exactly the boundary
	// the caller asked for.
	maxTotalLength := runeLen(originalText) + opts.MaxLength
	completedText = truncateAtWord(completedText, maxTotalLength, runeLen(originalText))

	return completedText
}

// nonEmptyStopSequences is the stop sequences that can match anything.
func nonEmptyStopSequences(sequences []string) []string {
	var kept []string
	for _, sequence := range sequences {
		if sequence != "" {
			kept = append(kept, sequence)
		}
	}
	return kept
}

// earliestStop is the byte offset at which the text first contains any of the
// sequences, or -1 when it contains none. Empty sequences are not considered.
func earliestStop(text string, sequences []string) int {
	earliest := -1
	for _, sequence := range nonEmptyStopSequences(sequences) {
		if index := strings.Index(text, sequence); index >= 0 && (earliest < 0 || index < earliest) {
			earliest = index
		}
	}
	return earliest
}

// CompleteFieldResult contains the result of completing a field in a struct
type CompleteFieldResult[T any] struct {
	Data      T      `json:"data"`      // The struct with the completed field
	Field     string `json:"field"`     // The field that was completed
	Original  string `json:"original"`  // Original field value
	Completed string `json:"completed"` // Completed field value
	Length    int    `json:"length"`    // Characters added
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64        `json:"confidence"` // ModelConfidence score (0.0-1.0)
	Metadata        map[string]any `json:"metadata"`   // Additional metadata
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

// WithBackground provides previous messages or surrounding text. Prompt
// material, not a context.Context -- see CompleteOptions.Background.
func (opts CompleteFieldOptions) WithBackground(background []string) CompleteFieldOptions {
	opts.CompleteOptions = opts.CompleteOptions.WithBackground(background)
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
// The copy is made with reflection and is shallow: every other field of the
// struct, exported or not and whatever its json tag says, comes back exactly as
// it was given, and slices and maps in it are shared with the original rather
// than duplicated. The field to complete has to be an exported string field of
// the struct itself or one promoted from an embedded struct.
//
// The other fields are shown to the model as context in the names and the form
// encoding/json gives them, and a field the type tags `json:"-"` is not shown,
// since it is not serialised and what is not serialised is not sent to a
// provider. The field being completed is sent whatever its tag, because the
// caller named it.
//
// Example:
//
//	type BlogPost struct {
//	    Title string `json:"title"`
//	    Body  string `json:"body"`
//	}
//	post := BlogPost{Title: "AI in Healthcare", Body: "Artificial intelligence is transforming"}
//	result, err := CompleteField[BlogPost](ctx, post, NewCompleteFieldOptions("Body"))
//	// result.Data.Body now contains the completed text
func CompleteField[T any](ctx context.Context, data T, opts CompleteFieldOptions) (CompleteFieldResult[T], error) {
	if ctx == nil {
		ctx = opts.OpOptions.Context
	}
	logger := loggerFor(ctx)
	logger.Debug("Starting complete field operation", "requestID", opts.RequestID)

	// Validate field name
	if opts.FieldName == "" {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, errors.New("field name is required"))
	}

	// Get the value using reflection
	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("data must be a struct, got %T", data))
	}

	// Find the field. It has to be one the copy below can set, which is checked
	// now rather than after the model has been paid for.
	structField, ok := val.Type().FieldByName(opts.FieldName)
	if !ok {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("field %q not found in struct", opts.FieldName))
	}
	if !structField.IsExported() {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("field %q is not exported, so it cannot be completed", opts.FieldName))
	}
	field, err := val.FieldByIndexErr(structField.Index)
	if err != nil {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("field %q cannot be read: it is promoted through an embedded pointer that is nil", opts.FieldName))
	}
	if field.Kind() != reflect.String {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("field %q must be a string, got %s", opts.FieldName, field.Kind()))
	}

	originalValue := field.String()

	result := CompleteFieldResult[T]{
		Data:     data,
		Field:    opts.FieldName,
		Original: originalValue,
		Metadata: make(map[string]any),
	}

	if strings.TrimSpace(originalValue) == "" {
		return CompleteFieldResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("field %q is empty, nothing to complete", opts.FieldName))
	}

	// Build context from other fields in the struct
	structContext := buildStructContext(val, opts.FieldName)
	if len(structContext) > 0 {
		// Prepend the struct's own fields to whatever background the caller gave.
		opts.CompleteOptions.Background = append(structContext, opts.CompleteOptions.Background...)
	}

	// Complete the field value
	completeResult, err := completeImpl(ctx, originalValue, opts.CompleteOptions)
	if err != nil {
		logger.Error("Complete field operation failed", "requestID", opts.RequestID, "error", loggedError(err))
		return CompleteFieldResult[T]{}, fmt.Errorf("failed to complete field: %w", err)
	}

	// Create a new copy of the struct with the completed field
	newData, err := copyWithUpdatedField(data, opts.FieldName, completeResult.Text)
	if err != nil {
		logger.Error("Failed to update struct field", "requestID", opts.RequestID, "error", loggedError(err))
		return CompleteFieldResult[T]{}, fmt.Errorf("failed to update field: %w", err)
	}

	result.Data = newData
	result.Completed = completeResult.Text
	result.Length = completeResult.Length
	result.ModelConfidence = completeResult.ModelConfidence
	result.Metadata = completeResult.Metadata
	result.Metadata["field"] = opts.FieldName

	logger.Debug("Complete field operation succeeded", "requestID", opts.RequestID, "length", result.Length)

	return result, nil
}

// buildStructContext describes the fields of a struct other than the one being
// completed, as "name: value" lines for the model. A field holding nothing,
// which for a number or a bool is its zero value and for a string is an empty
// or blank one, is left out; a string that happens to read "0" or "false" is a
// value and is kept. A nil pointer is left out and a pointer to a scalar is
// written as the scalar, not as an address.
//
// The fields are the ones encoding/json writes (jsonMembers), named as it names
// them, so that the model reads the names the data is written under and a
// field the type says is not serialised, `json:"-"`, is not sent to a provider
// under any name. The fields of an embedded struct are listed as encoding/json
// lists them. A value that is not text is written as JSON, so that what a
// struct or a map holds is shown as it is written and a field of it that JSON
// leaves out is left out here too; one JSON cannot write is not shown.
func buildStructContext(val reflect.Value, excludeField string) []string {
	var lines []string

	// The field being completed is named by its Go name, as FieldByName finds it.
	var excluded []int
	if field, found := val.Type().FieldByName(excludeField); found {
		excluded = field.Index
	}

	for _, member := range jsonMembers(val.Type()) {
		if excluded != nil && slices.Equal(member.index, excluded) {
			continue
		}
		if !member.field.IsExported() {
			continue
		}
		// A member promoted through an embedded pointer that is nil has no value.
		fieldVal, err := val.FieldByIndexErr(member.index)
		if err != nil {
			continue
		}
		if fieldVal.Kind() == reflect.Ptr || fieldVal.Kind() == reflect.Interface {
			if fieldVal.IsNil() {
				continue
			}
			fieldVal = fieldVal.Elem()
			if fieldVal.Kind() == reflect.Ptr {
				continue
			}
		}
		if !fieldVal.CanInterface() {
			continue
		}

		var fieldStr string
		switch fieldVal.Kind() {
		case reflect.String:
			fieldStr = fieldVal.String()
		case reflect.Slice:
			if fieldVal.Len() > 0 {
				// For slices, try to get a summary
				items := make([]string, 0, min(fieldVal.Len(), 5))
				for j := 0; j < fieldVal.Len() && j < 5; j++ {
					if text, shown := structContextText(fieldVal.Index(j)); shown {
						items = append(items, text)
					}
				}
				fieldStr = strings.Join(items, ", ")
				if fieldStr != "" && fieldVal.Len() > 5 {
					fieldStr += fmt.Sprintf(" (and %d more)", fieldVal.Len()-5)
				}
			}
		default:
			if fieldVal.IsZero() {
				continue
			}
			fieldStr, _ = structContextText(fieldVal)
		}

		if strings.TrimSpace(fieldStr) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", member.name, fieldStr))
	}

	return lines
}

// structContextText is how one value is written in a line of context: a string
// as it is, and anything else as the JSON it is written as. It reports false
// for a value JSON cannot write, which is not shown.
func structContextText(value reflect.Value) (string, bool) {
	if !value.IsValid() || !value.CanInterface() {
		return "", false
	}
	if value.Kind() == reflect.String {
		return value.String(), true
	}
	written, err := promptJSON(value.Interface())
	if err != nil {
		return "", false
	}
	return string(written), true
}

// copyWithUpdatedField returns a copy of the struct, or of the struct a pointer
// points to, with one string field set. The copy is made by assignment, so a
// field the JSON encoding would have lost (an unexported one, a `json:"-"` one,
// an int64 above 2^53) is carried over untouched, and the target is found by its
// Go name, so a tag such as `json:",omitempty"` cannot send the new value to a
// key that is not the field's. The copy is shallow.
func copyWithUpdatedField[T any](data T, fieldName, newValue string) (T, error) {
	var zero T

	source := reflect.ValueOf(data)
	isPointer := source.Kind() == reflect.Ptr
	if isPointer {
		if source.IsNil() {
			return zero, types.Classified(types.KindConfiguration, errors.New("data is a nil pointer"))
		}
		source = source.Elem()
	}
	if source.Kind() != reflect.Struct {
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("data must be a struct, got %T", data))
	}

	structField, ok := source.Type().FieldByName(fieldName)
	if !ok || !structField.IsExported() {
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("field %q is not an exported field of the struct", fieldName))
	}

	duplicate := reflect.New(source.Type())
	duplicate.Elem().Set(source)
	target, err := duplicate.Elem().FieldByIndexErr(structField.Index)
	if err != nil {
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("field %q is promoted through an embedded pointer that is nil", fieldName))
	}
	if target.Kind() != reflect.String || !target.CanSet() {
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("field %q cannot be set", fieldName))
	}
	target.SetString(newValue)

	var copied any = duplicate.Elem().Interface()
	if isPointer {
		copied = duplicate.Interface()
	}
	typed, ok := copied.(T)
	if !ok {
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("the copy is not a %T", data))
	}
	return typed, nil
}
