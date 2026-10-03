package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// DeriveOptions configures the Derive operation.
//
// Passing a DeriveOptions value to Derive replaces the default of its boolean
// field with the value's own: Derive with no options has IncludeReasoning on,
// but Derive(x, DeriveOptions{Rules: ...}) has it off, because a bool that is
// false cannot be told from one the caller never set. A caller who passes
// options and wants reasoning sets IncludeReasoning: true. The same holds for
// MinConfidence, which is a number: 0 stands for "not set" and means the
// default of 0.6, so a floor of zero cannot be asked for and a caller who
// wants the floor out of the way sets a value any score meets, such as 0.001.
// A MinConfidence that is not a number from 0 to 1 (a NaN, an infinity, a
// negative value, anything above 1) is refused before the model is asked.
// Telling "unset" from "false" or zero needs the options redesigned; until then
// the behaviour is this.
type DeriveOptions struct {
	// Fields specifies which fields to derive (empty means all fields in target
	// type). When it is set, the answer has to report a confidence for each of
	// them, so that a weak field cannot escape the floor by being left out of
	// field_confidence. When it is empty the floor is applied to the fields the
	// model reports, since which fields were derived is not known to Go.
	Fields []string

	// Rules provides natural language derivation rules per field
	Rules map[string]string

	// IncludeReasoning asks the model to include reasoning for each derivation.
	// It is a request and is not checked.
	IncludeReasoning bool

	// MinConfidence is the minimum acceptable confidence per field, and for the
	// overall confidence. An answer below it is refused.
	MinConfidence float64

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping. These option types spell their common fields
	// out rather than embedding CommonOptions, so the pin has to be declared
	// here too -- otherwise `.Model(...)` on the builder would be a setter
	// that changes nothing, which this repository fails the build over.
	Model string
	// Effort is how much the model reasons before it answers. The
	// builder sets it here for the same reason it sets Model here.
	Effort types.Effort

	// MaxOutputTokens is the ceiling on the answer's length. Zero means the tier's.
	MaxOutputTokens int
	// WebSearch declares the provider's web-search tool on the call.
	WebSearch bool

	Context       context.Context
	RequestID     string
	CorrelationID string
}

// Derivation describes how a field was derived
type Derivation struct {
	// Field is the name of the derived field
	Field string `json:"field"`

	// SourceFields lists input fields used to derive this value. A value that
	// rests on no input field (a constant, a default) has none to list.
	SourceFields []string `json:"source_fields,omitempty"`

	// Method describes how the derivation was performed
	Method string `json:"method"`

	// Reasoning explains the derivation logic. It is asked for only when
	// DeriveOptions.IncludeReasoning is on, so the type leaves it optional and
	// Derive refuses an answer without it when the option asked for it.
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
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting derive operation")

	// Every return that carries an error carries the zero result with it.

	// Apply defaults
	opt := DeriveOptions{
		IncludeReasoning: true,
		MinConfidence:    0.6,
		Mode:             types.TransformMode,
		Intelligence:     types.Fast,
	}
	if len(opts) > 0 {
		// The caller's floor is looked at before the merge, which drops one
		// that is not above zero: a NaN or a negative value would otherwise be
		// taken for "not set" and replaced by the default without a word.
		if err := checkDeriveOptions(opts[0]); err != nil {
			return DeriveResult[U]{}, err
		}
		opt = mergeDeriveOptions(opt, opts[0])
	}
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return DeriveResult[U]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Derive operation failed: marshal error", "error", err)
		return DeriveResult[U]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// Get input and output type schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build fields description
	fieldsDesc := ""
	if len(opt.Fields) > 0 {
		// The caller's text, written as a JSON list.
		quotedFields, _ := promptJSON(opt.Fields)
		fieldsDesc = fmt.Sprintf("\n\nFocus on deriving these fields: %s", quotedFields)
	}

	// Build rules description
	rulesDesc := ""
	if len(opt.Rules) > 0 {
		var parts []string
		// In field order, and quoted: the rules are the caller's text, and
		// ranging over the map wrote them in a different order on every call.
		for _, field := range sortedKeys(opt.Rules) {
			quotedField, _ := promptJSON(field)
			quotedRule, _ := promptJSON(opt.Rules[field])
			parts = append(parts, fmt.Sprintf("- %s: %s", quotedField, quotedRule))
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
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Derive operation LLM call failed", "error", err)
		return DeriveResult[U]{}, fmt.Errorf("derivation failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name, and an overall confidence of zero is the
	// model saying zero.
	var parsed struct {
		Derived                json.RawMessage    `json:"derived"`
		Derivations            []Derivation       `json:"derivations"`
		FieldConfidence        map[string]float64 `json:"field_confidence"`
		ModelOverallConfidence float64            `json:"overall_confidence"`
	}

	if err := decodeAnswer("derive", response, &parsed); err != nil {
		// The response is the caller's record derived from; it does not belong
		// in a log line.
		log.Error("Derive operation failed: parse error", "error", err)
		return DeriveResult[U]{}, fmt.Errorf("failed to parse derivation result: %w", err)
	}

	// The derived value is the answer. The reader refuses one that is missing; a
	// RawMessage can hold a null, which would leave Derived at U's zero value
	// under a nil error, and with no field_confidence in it the per-field floor
	// below had nothing to look at.
	if isJSONNull(parsed.Derived) {
		log.Error("Derive operation failed: the derived value is null")
		return DeriveResult[U]{}, types.Newf(types.KindSchemaViolation, "derive",
			"derived is null, so there is nothing to return")
	}

	// Reasoning is asked for only when IncludeReasoning is on, so it is optional
	// in the type (Derivation.Reasoning is omitempty) and is held here to what
	// the option asked: a derivation without an explanation, or with a blank one,
	// is not the account the caller requested.
	if opt.IncludeReasoning {
		for i, derivation := range parsed.Derivations {
			if strings.TrimSpace(derivation.Reasoning) == "" {
				log.Error("Derive operation failed: a derivation has no reasoning", "derivation", i)
				return DeriveResult[U]{}, types.Newf(types.KindSchemaViolation, "derive",
					"derivations[%d].reasoning is missing, and reasoning was asked for", i)
			}
		}
	}

	// The option is documented as "the minimum acceptable confidence per field",
	// and it was a sentence in the prompt. Per-field first, then the overall
	// claim: a derivation whose one weak field is the one the caller cares about
	// should not pass because the average carried it. The fields are taken in
	// name order so that the one a refusal names is the same on every call.
	for _, field := range sortedKeys(parsed.FieldConfidence) {
		if err := AtLeastConfidence(parsed.FieldConfidence[field], opt.MinConfidence); err != nil {
			log.Error("Derived field is below the configured confidence floor", "field", field, "error", err)
			return DeriveResult[U]{}, fmt.Errorf("derivation rejected for field %q: %w", field, err)
		}
	}
	// A field the caller asked to have derived has to be scored: the loop above
	// only sees what the model chose to report, so a weak field left out of
	// field_confidence passed unexamined.
	reported := make(map[string]struct{}, len(parsed.FieldConfidence))
	for field := range parsed.FieldConfidence {
		reported[deriveFieldKey(field)] = struct{}{}
	}
	for _, field := range opt.Fields {
		if _, ok := reported[deriveFieldKey(field)]; !ok {
			log.Error("Derive operation failed: a requested field has no confidence", "field", field)
			return DeriveResult[U]{}, types.Newf(types.KindSchemaViolation, "derive",
				"field_confidence does not score the requested field %q", field)
		}
	}
	if err := AtLeastConfidence(parsed.ModelOverallConfidence, opt.MinConfidence); err != nil {
		log.Error("Derivation is below the configured confidence floor", "error", err)
		return DeriveResult[U]{}, fmt.Errorf("derivation rejected: %w", err)
	}

	// Parse derived data
	var derived U
	if err := decodeCallerAnswer("derive", string(parsed.Derived), &derived); err != nil {
		log.Error("Derive operation failed: derived data parse error", "error", err)
		return DeriveResult[U]{}, fmt.Errorf("failed to parse derived data: %w", err)
	}

	result := DeriveResult[U]{
		Derived:                derived,
		Derivations:            parsed.Derivations,
		FieldConfidence:        parsed.FieldConfidence,
		ModelOverallConfidence: parsed.ModelOverallConfidence,
		Metadata:               make(map[string]any),
	}
	if result.FieldConfidence == nil {
		result.FieldConfidence = make(map[string]float64)
	}

	log.Debug("Derive operation succeeded",
		"derivations", len(result.Derivations),
		"overallConfidence", result.ModelOverallConfidence)

	return result, nil
}

// deriveFieldKey is a field name as it is compared between the caller's Fields
// and the model's field_confidence: without case and without the separators
// that tell snake_case, kebab-case and CamelCase apart, since the caller may
// write the Go name and the model the JSON name.
func deriveFieldKey(name string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(name))
}

// checkDeriveOptions refuses a floor in the caller's options that no answer
// could be held to. Zero is the value of an option that was not set and is
// accepted: it stands for the default. The error names the option and not the
// value.
func checkDeriveOptions(user DeriveOptions) error {
	if math.IsNaN(user.MinConfidence) || user.MinConfidence < 0 || user.MinConfidence > 1 {
		return types.Newf(types.KindConfiguration, "derive", "MinConfidence has to be a number from 0 to 1")
	}
	return nil
}

// mergeDeriveOptions merges user options with defaults. The numbers are checked
// by checkDeriveOptions before this runs, so what is dropped here is only zero.
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
	if user.Model != "" {
		defaults.Model = user.Model
	}
	if user.Effort != types.EffortDefault {
		defaults.Effort = user.Effort
	}
	if user.MaxOutputTokens > 0 {
		defaults.MaxOutputTokens = user.MaxOutputTokens
	}
	if user.WebSearch {
		defaults.WebSearch = true
	}
	if user.Context != nil {
		defaults.Context = user.Context
	}
	if user.RequestID != "" {
		defaults.RequestID = user.RequestID
	}
	if user.CorrelationID != "" {
		defaults.CorrelationID = user.CorrelationID
	}
	return defaults
}
