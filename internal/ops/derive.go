// package ops - Derive operation for inferring new typed data from existing
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// DeriveOptions configures the Derive operation
type DeriveOptions struct {
	// Fields specifies which fields to derive (empty means all fields in target type)
	Fields []string

	// Rules provides natural language derivation rules per field
	Rules map[string]string

	// IncludeReasoning includes per-field reasoning
	IncludeReasoning bool

	// MinConfidence is the minimum acceptable confidence per field
	MinConfidence float64

	// Common options
	Steering      string
	Mode          types.Mode
	Intelligence  types.Speed
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// Derivation describes how a field was derived
type Derivation struct {
	// Field is the name of the derived field
	Field string `json:"field"`

	// SourceFields lists input fields used to derive this value
	SourceFields []string `json:"source_fields,omitempty"`

	// Method describes how the derivation was performed
	Method string `json:"method"`

	// Reasoning explains the derivation logic
	Reasoning string `json:"reasoning,omitempty"`

	// ModelConfidence for this specific derivation
	ModelConfidence float64 `json:"confidence"`
}

// DeriveResult contains the derived data and derivation information
type DeriveResult[U any] struct {
	// Derived is the newly inferred data
	Derived U `json:"derived"`

	// Derivations describes how each field was derived
	Derivations []Derivation `json:"derivations,omitempty"`

	// FieldConfidence maps field names to confidence scores
	FieldConfidence map[string]float64 `json:"field_confidence"`

	// ModelOverallConfidence is the average confidence across all fields
	ModelOverallConfidence float64 `json:"overall_confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Derive infers new typed fields/structure from existing data.
//
// Type parameters:
//   - T: Input data type
//   - U: Output/derived data type
//
// Examples:
//
//	// Example 1: Enrich person with derived demographics
//	type Person struct {
//	    Name      string `json:"name"`
//	    BirthYear int    `json:"birth_year"`
//	    City      string `json:"city"`
//	}
//	type EnrichedPerson struct {
//	    Person
//	    Age        int    `json:"age"`
//	    Generation string `json:"generation"`
//	    Region     string `json:"region"`
//	    Timezone   string `json:"timezone"`
//	}
//	result, err := Derive[Person, EnrichedPerson](person)
//	fmt.Printf("%s is %s generation\n", result.Derived.Name, result.Derived.Generation)
//
//	// Example 2: Derive transaction risk features
//	type Transaction struct {
//	    Amount    float64 `json:"amount"`
//	    Merchant  string  `json:"merchant"`
//	    Timestamp string  `json:"timestamp"`
//	    Location  string  `json:"location"`
//	}
//	type RiskFeatures struct {
//	    Transaction
//	    RiskScore   float64  `json:"risk_score"`
//	    RiskFactors []string `json:"risk_factors"`
//	    TimeOfDay   string   `json:"time_of_day"`
//	}
//	result, err := Derive[Transaction, RiskFeatures](txn, DeriveOptions{
//	    Rules: map[string]string{
//	        "risk_score": "0-1 based on amount, time, location",
//	    },
//	})
//
//	// Example 3: Derive product categories
//	result, err := Derive[RawProduct, CategorizedProduct](product, DeriveOptions{
//	    Fields: []string{"category", "subcategory", "tags"},
//	    IncludeReasoning: true,
//	})
//	for _, d := range result.Derivations {
//	    fmt.Printf("%s: %s (%.0f%% confident)\n", d.Field, d.Method, d.ModelConfidence*100)
//	}
func Derive[T any, U any](input T, opts ...DeriveOptions) (DeriveResult[U], error) {
	log := logger.GetLogger()
	log.Debug("Starting derive operation")

	var result DeriveResult[U]
	result.FieldConfidence = make(map[string]float64)
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := DeriveOptions{
		IncludeReasoning: true,
		MinConfidence:    0.6,
		Mode:             types.TransformMode,
		Intelligence:     types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeDeriveOptions(opt, opts[0])
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Derive operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Get input and output type schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build fields description
	fieldsDesc := ""
	if len(opt.Fields) > 0 {
		fieldsDesc = fmt.Sprintf("\n\nFocus on deriving these fields: %s", strings.Join(opt.Fields, ", "))
	}

	// Build rules description
	rulesDesc := ""
	if len(opt.Rules) > 0 {
		var parts []string
		for field, rule := range opt.Rules {
			parts = append(parts, fmt.Sprintf("- %s: %s", field, rule))
		}
		rulesDesc = fmt.Sprintf("\n\nDerivation rules:\n%s", strings.Join(parts, "\n"))
	}

	reasoningNote := ""
	if opt.IncludeReasoning {
		reasoningNote = "\nInclude reasoning for each derivation."
	}

	systemPrompt := fmt.Sprintf(`You are a data analysis and inference expert. Derive new structured data from input data.

Input schema: %s
Output schema: %s%s%s%s

Return a JSON object with:
{
  "derived": %s,
  "derivations": [
    {
      "field": "field_name",
      "source_fields": ["input_field1", "input_field2"],
      "method": "calculation/inference/aggregation/etc",
      "reasoning": "explanation of derivation",
      "confidence": 0.0-1.0
    }
  ],
  "field_confidence": {"field_name": 0.0-1.0, ...},
  "overall_confidence": 0.0-1.0
}

Rules:
- Derive values logically from the input data
- Each field should have confidence >= %.0f%%
- Explain the derivation method used`,
		inputSchema, outputSchema, fieldsDesc, rulesDesc, reasoningNote, outputSchema, opt.MinConfidence*100)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Derive the output data from this input:

%s%s`, string(inputJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Derive operation LLM call failed", "error", err)
		return result, fmt.Errorf("derivation failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Derived                json.RawMessage    `json:"derived"`
		Derivations            []Derivation       `json:"derivations"`
		FieldConfidence        map[string]float64 `json:"field_confidence"`
		ModelOverallConfidence float64            `json:"overall_confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		// The response is the caller's record derived from; it does not belong
		// in a log line (X-03).
		log.Error("Derive operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse derivation result: %w", err)
	}

	// The option is documented as "the minimum acceptable confidence per field",
	// and it was a sentence in the prompt. Per-field first, then the overall
	// claim: a derivation whose one weak field is the one the caller cares about
	// should not pass because the average carried it.
	for field, reported := range parsed.FieldConfidence {
		if err := AtLeastConfidence(reported, opt.MinConfidence); err != nil {
			log.Error("Derived field is below the configured confidence floor", "field", field, "error", err)
			return result, fmt.Errorf("derivation rejected for field %q: %w", field, err)
		}
	}
	if err := AtLeastConfidence(parsed.ModelOverallConfidence, opt.MinConfidence); err != nil {
		log.Error("Derivation is below the configured confidence floor", "error", err)
		return result, fmt.Errorf("derivation rejected: %w", err)
	}

	// Parse derived data
	if len(parsed.Derived) > 0 {
		if err := json.Unmarshal(parsed.Derived, &result.Derived); err != nil {
			log.Error("Derive operation failed: derived data parse error", "error", err)
			return result, fmt.Errorf("failed to parse derived data: %w", err)
		}
	}

	result.Derivations = parsed.Derivations
	result.FieldConfidence = parsed.FieldConfidence
	result.ModelOverallConfidence = parsed.ModelOverallConfidence

	log.Debug("Derive operation succeeded",
		"derivations", len(result.Derivations),
		"overallConfidence", result.ModelOverallConfidence)

	return result, nil
}

// mergeDeriveOptions merges user options with defaults
func mergeDeriveOptions(defaults, user DeriveOptions) DeriveOptions {
	if user.Fields != nil {
		defaults.Fields = user.Fields
	}
	if user.Rules != nil {
		defaults.Rules = user.Rules
	}
	if user.MinConfidence > 0 {
		defaults.MinConfidence = user.MinConfidence
	}
	// IncludeReasoning is a bool, check explicitly
	defaults.IncludeReasoning = user.IncludeReasoning
	if user.Steering != "" {
		defaults.Steering = user.Steering
	}
	if user.Mode != 0 {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != 0 {
		defaults.Intelligence = user.Intelligence
	}
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}
