// Infer operation for smart missing data inference.

package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
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

// Infer smartly fills in missing fields in partial data using LLM inference.
//
// The fields the caller supplied (the ones that are not the zero value of their
// type) have to come back unchanged, and every field of T that has no omitempty
// tag has to be in the answer; an answer that rewrites a supplied field or
// leaves a field out is refused, and the zero value of T is returned with the
// error. Nothing in the result marks which fields were inferred: a caller that
// needs that compares the result with what it passed in.
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
	log := loggerFor(opts.OpOptions.Context)
	log.Debug("Starting infer operation", "requestID", opts.RequestID, "dataType", fmt.Sprintf("%T", partialData))

	var zero T

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Infer operation validation failed", "requestID", opts.RequestID, "error", err)
		return zero, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	// Get type information
	targetType := reflect.TypeOf(zero)
	typeSchema := GenerateTypeSchema(targetType)

	// Marshal partial data to JSON
	partialJSON, err := promptJSON(partialData)
	if err != nil {
		log.Error("Infer operation marshal failed", "requestID", opts.RequestID, "error", err)
		return zero, types.Newf(types.KindInvalidRequest, "infer", "the partial data could not be written as JSON")
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

	// Build user prompt. The background is the caller's text, so it is written
	// as a JSON string and cannot read as an instruction.
	userPrompt := fmt.Sprintf("Complete this partial data by inferring missing fields:\n%s", string(partialJSON))

	if opts.Background != "" {
		userPrompt += fmt.Sprintf("\n\nAdditional context: %s", quotedText(opts.Background))
	}

	// Call LLM for inference
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Infer operation LLM call failed", "requestID", opts.RequestID, "error", err)
		return zero, fmt.Errorf("inference failed: %w", err)
	}

	result, err := readInferAnswer(partialData, response)
	if err != nil {
		log.Error("Infer operation refused the answer", "requestID", opts.RequestID, "error", err)
		return zero, err
	}

	log.Debug("Infer operation succeeded", "requestID", opts.RequestID)

	return result, nil
}

// readInferAnswer reads the model's answer and checks it against the partial
// data it was given.
func readInferAnswer[T any](partial T, response string) (T, error) {
	const op = "infer"
	var zero T

	var result T
	if err := ParseJSON(response, &result); err != nil {
		return zero, types.Newf(types.KindMalformedOutput, op, "the response could not be read as the type that was asked for")
	}
	var answered any
	if err := ParseJSON(response, &answered); err != nil {
		return zero, types.Newf(types.KindMalformedOutput, op, "the response is not JSON")
	}

	// An object answer has to carry every field the type has, with a null only
	// where the Go type can hold one. Decoding alone reads a missing field as the
	// zero value, which is indistinguishable from the model having inferred it.
	targetType := reflect.TypeOf((*T)(nil)).Elem()
	required, nullable := requiredJSONFields(targetType)
	if len(required) > 0 {
		object, isObject := answered.(map[string]any)
		if !isObject {
			return zero, types.Newf(types.KindSchemaViolation, op, "the response is not a JSON object")
		}
		for _, name := range required {
			value, found := lookupFold(object, name)
			if !found || (value == nil && !nullable[name]) {
				return zero, types.Newf(types.KindSchemaViolation, op, "the answer has no value for the field %q", name)
			}
		}
	}

	// What the caller supplied is a fact the model was given, not a draft. An
	// answer that changes one has replaced the caller's data with its own and
	// would otherwise be returned as a completion of it.
	encoded, err := json.Marshal(partial)
	if err != nil {
		return zero, types.Newf(types.KindInvalidRequest, op, "the partial data could not be written as JSON")
	}
	var supplied any
	if err := json.Unmarshal(encoded, &supplied); err != nil {
		return zero, types.Newf(types.KindInvalidRequest, op, "the partial data could not be read back as JSON")
	}
	if path, changed := changedSuppliedValue(supplied, answered, ""); changed {
		return zero, types.Newf(types.KindInvariantViolation, op, "the answer changed the supplied value at %q", path)
	}
	return result, nil
}

// lookupFold finds a member of a JSON object by name the way encoding/json
// does when it decodes into a struct: an exact match first, then one that
// differs only in case.
func lookupFold(object map[string]any, name string) (any, bool) {
	if value, found := object[name]; found {
		return value, true
	}
	for key, value := range object {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

// isUnsuppliedJSON reports whether a decoded JSON value is the zero value of
// its kind, which is how a field the caller did not fill in looks.
func isUnsuppliedJSON(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// changedSuppliedValue reports the path of the first value the caller supplied
// that the answer does not carry unchanged. A value that is the zero value of
// its kind was not supplied. Objects are compared member by member, so a nested
// record the caller half filled in can have its other members filled in, and
// arrays are compared item by item and have to keep their length.
func changedSuppliedValue(supplied, answered any, path string) (string, bool) {
	if isUnsuppliedJSON(supplied) {
		return "", false
	}
	switch s := supplied.(type) {
	case map[string]any:
		a, ok := answered.(map[string]any)
		if !ok {
			return path, true
		}
		for _, key := range sortedKeys(s) {
			value, found := lookupFold(a, key)
			childPath := path + "/" + key
			if !found {
				if isUnsuppliedJSON(s[key]) {
					continue
				}
				return childPath, true
			}
			if changedPath, changed := changedSuppliedValue(s[key], value, childPath); changed {
				return changedPath, true
			}
		}
		return "", false
	case []any:
		a, ok := answered.([]any)
		if !ok || len(a) != len(s) {
			return path, true
		}
		for i := range s {
			if changedPath, changed := changedSuppliedValue(s[i], a[i], fmt.Sprintf("%s/%d", path, i)); changed {
				return changedPath, true
			}
		}
		return "", false
	default:
		if supplied != answered {
			return path, true
		}
		return "", false
	}
}
