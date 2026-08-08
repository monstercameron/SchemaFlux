package ops

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
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
	log := logger.GetLogger()
	log.Debug("Starting suggest operation", "requestID", opts.CommonOptions.RequestID, "inputType", fmt.Sprintf("%T", input), "topN", opts.TopN)

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Suggest operation validation failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, fmt.Errorf("invalid options: %w", err)
	}

	opOptions := opts.toOpOptions()

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
	if opts.CommonOptions.Steering != "" {
		steering = opts.CommonOptions.Steering + ". " + steering
	}
	opOptions.Steering = steering

	ctx, cancel := operationContext(resolvedContext(opts.CommonOptions, opts.OpOptions), config.GetTimeout())
	defer cancel()

	// Marshal input for LLM
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Suggest operation marshal failed", "requestID", opts.CommonOptions.RequestID, "error", err)
		return nil, fmt.Errorf("failed to marshal input: %w", err)
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

	// Clean up markdown code blocks from response
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	// Parse the response - try direct array first.
	//
	// The accumulator is declared per attempt on purpose. It used to be one
	// slice shared by every branch below, and encoding/json PARTIALLY POPULATES
	// a slice before returning an error: decoding `[{"name":"a"},{"text":"b"}]`
	// into []string appends two zero-value strings and then fails. The failure
	// was handled, the two empties were not, and the object-extraction branch
	// further down appended to the same slice -- so a caller asking for two
	// suggestions received ["", "", "a", "b"], and suggestions[0] was empty.
	var suggestions []T
	if err := ParseJSONStrict(response, &suggestions); err == nil {
		// Successfully parsed as array
		// Silent truncation was the anti-pattern AtMost's own doc comment names:
		// it "turns a model that ignored the instruction into a result that
		// looks obedient". A caller asking for three and getting seven should
		// find out, rather than receiving a plausible three with no sign that
		// the ranking they are about to trust was cut down locally.
		if err := AtMost(suggestions, opts.TopN); err != nil {
			log.Error("Suggest returned more suggestions than requested",
				"requestID", opts.CommonOptions.RequestID, "error", err)
			return nil, fmt.Errorf("suggest: %w", err)
		}
		log.Debug("Suggest operation succeeded", "requestID", opts.CommonOptions.RequestID, "suggestionsCount", len(suggestions))
		return suggestions, nil
	}

	// Try to extract from a structured response with "suggestions" key
	var result struct {
		Suggestions []T `json:"suggestions"`
	}
	if err := ParseJSONStrict(response, &result); err == nil && len(result.Suggestions) > 0 {
		suggestions = result.Suggestions
		if err := AtMost(suggestions, opts.TopN); err != nil {
			log.Error("Suggest returned more suggestions than requested",
				"requestID", opts.CommonOptions.RequestID, "error", err)
			return nil, fmt.Errorf("suggest: %w", err)
		}
		log.Debug("Suggest operation succeeded", "requestID", opts.CommonOptions.RequestID, "suggestionsCount", len(suggestions))
		return suggestions, nil
	}

	// If T is string, try to extract strings from array of objects.
	//
	// extracted, not `suggestions`: see the note on the first attempt above. A
	// failed decode there can have left partial zero values behind, and
	// appending to it produced empty suggestions ahead of the real ones.
	var extracted []T
	var rawArray []map[string]any
	if err := ParseJSONStrict(response, &rawArray); err == nil {
		for _, item := range rawArray {
			// Try common field names for suggestions
			for _, key := range []string{"suggestion", "text", "content", "name", "value", "description"} {
				if val, ok := item[key]; ok {
					if str, ok := val.(string); ok {
						var t T
						// Check if T is string
						if _, ok := any(t).(string); ok {
							extracted = append(extracted, any(str).(T))
							break
						}
					}
				}
			}
		}
		if len(extracted) > 0 {
			if err := AtMost(extracted, opts.TopN); err != nil {
				log.Error("Suggest returned more suggestions than requested",
					"requestID", opts.CommonOptions.RequestID, "error", err)
				return nil, fmt.Errorf("suggest: %w", err)
			}
			log.Debug("Suggest operation succeeded (extracted)", "requestID", opts.CommonOptions.RequestID, "suggestionsCount", len(extracted))
			return extracted, nil
		}
	}

	// Accept a single suggestion object as a one-item result.
	var singleObject map[string]any
	if err := ParseJSONStrict(response, &singleObject); err == nil && len(singleObject) > 0 {
		var t T
		if _, ok := any(t).(string); ok {
			for _, key := range []string{"suggestion", "text", "content", "name", "value", "description"} {
				if val, ok := singleObject[key]; ok {
					if str, ok := val.(string); ok && strings.TrimSpace(str) != "" {
						return []T{any(str).(T)}, nil
					}
				}
			}
		}

		if bytes, err := json.Marshal(singleObject); err == nil {
			var parsed T
			if err := json.Unmarshal(bytes, &parsed); err == nil {
				return []T{parsed}, nil
			}
		}
	}

	// The response body is deliberately not logged. It is derived from the
	// caller's input, and AGENTS.md forbids putting the caller's payload in a
	// log line -- a 200-character prefix is still the caller's data. The length
	// and the request ID are enough to correlate with a diagnostic capture,
	// which is the mechanism that exists for looking at bodies.
	log.Error("Suggest operation parse failed",
		"requestID", opts.CommonOptions.RequestID, "responseBytes", len(response))
	return nil, fmt.Errorf("failed to parse suggestions from response")
}

// SuggestWithResult provides detailed suggestion results with scores and reasons
func SuggestWithResult[T any](input any, opts SuggestOptions) (SuggestResult[T], error) {
	result := SuggestResult[T]{
		Metadata: make(map[string]any),
	}

	// Enable scores and reasons if not already set
	opts.IncludeScores = opts.IncludeScores || opts.Ranked
	opts.IncludeReasons = opts.IncludeReasons || true

	suggestions, err := Suggest[T](input, opts)
	if err != nil {
		return result, err
	}

	result.Suggestions = suggestions

	// For now, return basic result - full implementation would parse scores/reasons from LLM
	// This would require more sophisticated prompt engineering and response parsing

	return result, nil
}
