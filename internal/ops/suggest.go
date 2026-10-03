package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// SuggestResult contains the result of a suggestion operation
type SuggestResult[T any] struct {
	Suggestions []T            `json:"suggestions"`        // The suggested items
	Scores      []float64      `json:"scores,omitempty"`   // ModelConfidence scores (if ranked)
	Reasons     []string       `json:"reasons,omitempty"`  // Reasons for each suggestion
	Metadata    map[string]any `json:"metadata,omitempty"` // Additional metadata
}

// SuggestStrategy defines how suggestions are generated
type SuggestStrategy string

const (
	SuggestContextual SuggestStrategy = "contextual" // Based on current context/state
	SuggestPattern    SuggestStrategy = "pattern"    // Based on patterns in data
	SuggestGoal       SuggestStrategy = "goal"       // Based on stated goals
	SuggestHybrid     SuggestStrategy = "hybrid"     // Combination approach
)

// SuggestOptions configures the Suggest operation
type SuggestOptions struct {
	types.OpOptions
	CommonOptions

	// Suggestion strategy
	Strategy SuggestStrategy

	// Maximum number of suggestions to return
	TopN int

	// Whether to rank suggestions by relevance/confidence
	Ranked bool

	// Include confidence scores for each suggestion
	IncludeScores bool

	// Include reasoning for each suggestion
	IncludeReasons bool

	// Domain context for more relevant suggestions
	Domain string

	// Specific constraints or requirements
	Constraints []string

	// Custom suggestion categories
	Categories []string
}

// NewSuggestOptions creates SuggestOptions with defaults
func NewSuggestOptions() SuggestOptions {
	return SuggestOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Smart,
		},
		Strategy:       SuggestContextual,
		TopN:           5,
		Ranked:         true,
		IncludeScores:  false,
		IncludeReasons: false,
	}
}

// Validate checks if the options are valid
func (opts SuggestOptions) Validate() error {
	if err := opts.CommonOptions.Validate(); err != nil {
		return err
	}

	if opts.TopN < 1 {
		return fmt.Errorf("topN must be at least 1, got %d", opts.TopN)
	}

	validStrategies := map[SuggestStrategy]bool{
		SuggestContextual: true,
		SuggestPattern:    true,
		SuggestGoal:       true,
		SuggestHybrid:     true,
	}
	if !validStrategies[opts.Strategy] {
		return fmt.Errorf("invalid strategy: %s", opts.Strategy)
	}

	return nil
}

// WithStrategy sets the suggestion strategy
func (opts SuggestOptions) WithStrategy(strategy SuggestStrategy) SuggestOptions {
	opts.Strategy = strategy
	return opts
}

// WithTopN sets the maximum number of suggestions to return
func (opts SuggestOptions) WithTopN(n int) SuggestOptions {
	opts.TopN = n
	return opts
}

// WithRanked enables/disables ranking of suggestions
func (opts SuggestOptions) WithRanked(ranked bool) SuggestOptions {
	opts.Ranked = ranked
	return opts
}

// WithIncludeScores includes confidence scores for suggestions
func (opts SuggestOptions) WithIncludeScores(include bool) SuggestOptions {
	opts.IncludeScores = include
	return opts
}

// WithIncludeReasons includes reasoning for each suggestion
func (opts SuggestOptions) WithIncludeReasons(include bool) SuggestOptions {
	opts.IncludeReasons = include
	return opts
}

// WithDomain sets the domain context for suggestions
func (opts SuggestOptions) WithDomain(domain string) SuggestOptions {
	opts.Domain = domain
	return opts
}

// WithConstraints sets specific constraints for suggestions
func (opts SuggestOptions) WithConstraints(constraints []string) SuggestOptions {
	opts.Constraints = constraints
	return opts
}

// WithCategories sets suggestion categories
func (opts SuggestOptions) WithCategories(categories []string) SuggestOptions {
	opts.Categories = categories
	return opts
}

// Suggest generates context-aware suggestions based on input data and current state
//
// Examples:
//
//	// Basic suggestions
//	suggestions, err := Suggest[Action](currentState, NewSuggestOptions())
//
//	// Ranked suggestions with reasons
//	suggestions, err := Suggest[string](context, NewSuggestOptions().
//	    WithRanked(true).
//	    WithTopN(5).
//	    WithIncludeReasons(true))
//
//	// Domain-specific suggestions
//	suggestions, err := Suggest[ConfigOption](currentConfig,
//	    NewSuggestOptions().WithDomain("data-processing"))
func Suggest[T any](input any, opts SuggestOptions) ([]T, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting suggest operation", "requestID", opts.CommonOptions.RequestID, "inputType", fmt.Sprintf("%T", input), "topN", opts.TopN)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Suggest operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Both halves of the options, as every other operation reads them. The
	// promoted CommonOptions method read only that half, so a Context, Model or
	// Intelligence set on the embedded OpOptions was ignored.
	opOptions := mergeEmbeddedOpOptions(opts.CommonOptions, opts.OpOptions)

	// Build suggestion instructions
	var instructions []string

	instructions = append(instructions, fmt.Sprintf("Generate suggestions using %s strategy", opts.Strategy))

	if opts.Domain != "" {
		instructions = append(instructions, fmt.Sprintf("Domain context: %s", opts.Domain))
	}

	if len(opts.Constraints) > 0 {
		instructions = append(instructions, fmt.Sprintf("Constraints: %s", strings.Join(opts.Constraints, ", ")))
	}

	if len(opts.Categories) > 0 {
		instructions = append(instructions, fmt.Sprintf("Categories: %s", strings.Join(opts.Categories, ", ")))
	}

	if opts.Ranked {
		instructions = append(instructions, "Rank suggestions by relevance")
	}

	if opts.IncludeScores {
		instructions = append(instructions, "Include confidence scores (0-1)")
	}

	if opts.IncludeReasons {
		instructions = append(instructions, "Provide reasoning for each suggestion")
	}

	instructions = append(instructions, fmt.Sprintf("Return top %d suggestions", opts.TopN))

	steering := strings.Join(instructions, ". ")
	if callerSteering := effectiveSteering(opts.CommonOptions, opts.OpOptions); callerSteering != "" {
		steering = callerSteering + ". " + steering
	}
	opOptions.Steering = steering

	// From the options built above, so the deadline inside them is the one in use.
	ctx, cancel := operationContext(opOptions.Context, config.GetTimeout())
	defer cancel()

	// Marshal input for LLM
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Suggest operation marshal failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	systemPrompt := `You are an expert suggestion engine. Generate contextually relevant suggestions based on the provided input and requirements.

Rules:
- Analyze the input data and current context
- Generate practical, actionable suggestions
- Consider the specified constraints and domain
- Return a JSON array of strings like: ["suggestion 1", "suggestion 2", ...]
- If returning structured data, use fields: name, description, priority, category
- Each suggestion should be relevant and helpful
- If ranking is requested, order by relevance (most relevant first)
- Return ONLY valid JSON, no explanations or markdown`

	userPrompt := fmt.Sprintf("Generate suggestions based on this input:\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOptions)
	if err != nil {
		log.Error("Suggest operation LLM call failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	suggestions, err := decodeSuggestions[T](response)
	if err != nil {
		// The response body is deliberately not logged. It is derived from the
		// caller's input, and AGENTS.md forbids putting the caller's payload in a
		// log line -- a 200-character prefix is still the caller's data. The length
		// and the request ID are enough to correlate with a diagnostic capture,
		// which is the mechanism that exists for looking at bodies.
		log.Error("Suggest operation parse failed",
			"requestID", opts.CommonOptions.RequestID, "responseBytes", len(response), "error", err)
		return nil, err
	}

	// Silent truncation was the anti-pattern AtMost's own doc comment names: it
	// "turns a model that ignored the instruction into a result that looks
	// obedient". A caller asking for three and getting seven should find out,
	// rather than receiving a plausible three with no sign that the ranking
	// they are about to trust was cut down locally.
	if err := AtMost(suggestions, opts.TopN); err != nil {
		log.Error("Suggest returned more suggestions than requested",
			"requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, types.Newf(types.KindInvariantViolation, "suggest", "%s", err.Error())
	}
	log.Debug("Suggest operation succeeded", "requestID", opts.CommonOptions.RequestID, "suggestionsCount", len(suggestions))
	return suggestions, nil
}

// suggestionTextKeys are the keys a string suggestion may be found under when
// the model answers with objects instead of strings.
var suggestionTextKeys = []string{"suggestion", "text", "content", "name", "value", "description"}

// decodeSuggestions reads the model's answer as a list of suggestions of type T.
//
// Four shapes are accepted: an array of suggestions, an object whose
// "suggestions" key holds one, an array of objects from which a string is taken
// (T a string), and a single suggestion object. Every element has to be a
// usable suggestion, and an answer that holds none is refused: an error object,
// an empty list, an object that shares no field with T, null, and a blank
// string. It used to decode with json.Unmarshal and no further check, so for a
// struct T each of those came back as one zero-valued suggestion and a nil
// error.
//
// Nothing is returned beside an error. The decoding is element by element into
// a fresh value, so a failed decode cannot leave partly filled values behind.
func decodeSuggestions[T any](response string) ([]T, error) {
	refuse := func(format string, args ...any) ([]T, error) {
		return nil, types.Newf(types.KindSchemaViolation, "suggest", format, args...)
	}

	body := cleanJSON(response)
	var top any
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		return nil, types.Newf(types.KindMalformedOutput, "suggest", "the response is not JSON")
	}

	switch typed := top.(type) {
	case []any:
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			return refuse("the response is not a list of suggestions")
		}
		return decodeSuggestionList[T](raw)

	case map[string]any:
		if _, wrapped := typed["suggestions"]; wrapped {
			var wrapper struct {
				Suggestions []json.RawMessage `json:"suggestions"`
			}
			if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
				return refuse("the suggestions key does not hold a list")
			}
			return decodeSuggestionList[T](wrapper.Suggestions)
		}
		one, detail, ok := decodeSuggestion[T](json.RawMessage(body))
		if !ok {
			return refuse("the response is an object that holds no suggestion%s", detail)
		}
		return []T{one}, nil
	}
	return refuse("the response is not a list of suggestions")
}

// decodeSuggestionList decodes each element of a list and refuses the list if
// it is empty or any element is not a usable suggestion.
func decodeSuggestionList[T any](raw []json.RawMessage) ([]T, error) {
	if len(raw) == 0 {
		return nil, types.Newf(types.KindSchemaViolation, "suggest", "the response holds no suggestions")
	}
	out := make([]T, 0, len(raw))
	for i, element := range raw {
		suggestion, detail, ok := decodeSuggestion[T](element)
		if !ok {
			return nil, types.Newf(types.KindSchemaViolation, "suggest", "suggestion %d is not usable%s", i, detail)
		}
		out = append(out, suggestion)
	}
	return out, nil
}

// decodeSuggestion decodes one suggestion. It reports false for null, for a
// blank string, for an object that shares no field with a struct T, for an
// object that leaves out a member T requires, and for anything that does not fit
// T. When it can say which member, detail names it, in the library's own words
// and led by ": ", and is empty otherwise.
func decodeSuggestion[T any](raw json.RawMessage) (suggestion T, detail string, ok bool) {
	var zero, value T
	if text := strings.TrimSpace(string(raw)); text == "" || text == "null" {
		return zero, "", false
	}

	switch target := any(&value).(type) {
	case *string:
		// A string answer is a JSON string, or an object holding one under a
		// key such as "text".
		var plain string
		if err := json.Unmarshal(raw, &plain); err == nil {
			if strings.TrimSpace(plain) == "" {
				return zero, "", false
			}
			*target = plain
			return value, "", true
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			return zero, "", false
		}
		for _, key := range suggestionTextKeys {
			if text, found := object[key].(string); found && strings.TrimSpace(text) != "" {
				*target = text
				return value, "", true
			}
		}
		return zero, "", false
	}

	if len(jsonKeyNames(reflect.TypeOf(&value))) > 0 {
		// A struct, or a pointer to one. It is the caller's type, so a member the
		// suggestion leaves out is the zero value and what a field has to carry is
		// not decided here; the suggestion has to be about the type, which is to
		// say share at least one member with it, or it would be one suggestion
		// with every field empty.
		if err := decodeCallerAnswer("suggest", string(raw), &value); err != nil {
			var refusal *types.OperationError
			if errors.As(err, &refusal) && refusal.Message != "" {
				return zero, ": " + refusal.Message, false
			}
			return zero, "", false
		}
		return value, "", true
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, "", false
	}
	return value, "", true
}

// SuggestWithResult returns the suggestions in a SuggestResult.
//
// Only Suggestions is filled. Scores and Reasons are never read from the answer
// (the model is told to answer with a list of suggestions and nothing else, and
// nothing parses anything beside that), and Metadata is an empty map. An empty
// Scores means that none was read, not that none was asked for: IncludeScores
// and IncludeReasons add a sentence to the prompt and are not read back. This
// form used to switch both on, which asked the model for scores and reasons it
// was then not able to return.
func SuggestWithResult[T any](input any, opts SuggestOptions) (SuggestResult[T], error) {
	suggestions, err := Suggest[T](input, opts)
	if err != nil {
		return SuggestResult[T]{}, err
	}
	return SuggestResult[T]{
		Suggestions: suggestions,
		Metadata:    make(map[string]any),
	}, nil
}
