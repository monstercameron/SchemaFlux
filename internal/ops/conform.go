// package ops - Conform operation for transforming data to match standards
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

// ConformOptions configures the Conform operation
type ConformOptions struct {
	// Strict fails if data cannot fully conform to the standard
	Strict bool

	// PreserveUnknown keeps fields not covered by the standard
	PreserveUnknown bool

	// Validate performs validation after conforming
	Validate bool

	// CustomRules adds custom conformance rules
	CustomRules map[string]string

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping (TC-005). These option types spell their common fields
	// out rather than embedding CommonOptions, so the pin has to be declared
	// here too -- otherwise `.Model(...)` on the builder would be a setter
	// that changes nothing, which this repository fails the build over.
	Model         string
	Context       context.Context
	RequestID     string
	CorrelationID string
}

// Adjustment describes a change made to conform data
type Adjustment struct {
	// Field is the field that was adjusted
	Field string `json:"field"`

	// OriginalValue is the value before adjustment
	OriginalValue any `json:"original_value"`

	// ConformedValue is the value after adjustment
	ConformedValue any `json:"conformed_value"`

	// Rule is the standard/rule that required this adjustment
	Rule string `json:"rule"`

	// Description explains what was changed
	Description string `json:"description"`
}

// ConformResult contains the conformed data and adjustment information
type ConformResult[T any] struct {
	// Conformed is the data transformed to match the standard
	Conformed T `json:"conformed"`

	// Adjustments lists all changes made
	Adjustments []Adjustment `json:"adjustments,omitempty"`

	// Violations lists issues that couldn't be conformed
	Violations []string `json:"violations,omitempty"`

	// Compliance is the overall compliance score (0.0-1.0)
	Compliance float64 `json:"compliance"`

	// Standard is the standard that was applied
	Standard string `json:"standard"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Conform transforms data to match a schema or standard.
//
// Type parameter T specifies the type being conformed.
//
// Common standards: "USPS", "ISO8601", "E164", "RFC5322", "JSON-LD", "Schema.org"
//
// Examples:
//
//	// Example 1: Standardize addresses to USPS format
//	type Address struct {
//	    Name    string `json:"name"`
//	    Street  string `json:"street"`
//	    City    string `json:"city"`
//	    State   string `json:"state"`
//	    ZipCode string `json:"zip_code"`
//	}
//	raw := Address{Name: "john doe", Street: "123 n main st apt 4",
//	    City: "los angeles", State: "california", ZipCode: "90210"}
//	result, err := Conform(raw, "USPS")
//	// Result: {Name: "JOHN DOE", Street: "123 N MAIN ST APT 4",
//	//         City: "LOS ANGELES", State: "CA", ZipCode: "90210"}
//	for _, adj := range result.Adjustments {
//	    fmt.Printf("%s: %v → %v\n", adj.Field, adj.OriginalValue, adj.ConformedValue)
//	}
//
//	// Example 2: Normalize phone numbers to E164
//	type Contact struct {
//	    Phone    string `json:"phone"`
//	    AltPhone string `json:"alt_phone"`
//	}
//	result, err := Conform(Contact{Phone: "(555) 123-4567", AltPhone: "1-800-FLOWERS"},
//	    "E164", ConformOptions{Steering: "Assume US country code +1"})
//	// Result: {Phone: "+15551234567", AltPhone: "+18003569377"}
//
//	// Example 3: Standardize dates to ISO8601
//	type Event struct {
//	    Name  string `json:"name"`
//	    Start string `json:"start"`
//	    End   string `json:"end"`
//	}
//	result, err := Conform(Event{Name: "Meeting", Start: "Jan 15, 2024 3pm",
//	    End: "1/15/24 4:00 PM"}, "ISO8601")
//	// Result: {Start: "2024-01-15T15:00:00Z", End: "2024-01-15T16:00:00Z"}
//
//	// Example 4: Custom standard
//	result, err := Conform(product, "SKU format: CAT-BRAND-NUM (uppercase, max 20 chars)")
func Conform[T any](input T, standard string, opts ...ConformOptions) (ConformResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting conform operation", "standard", standard)

	var result ConformResult[T]
	result.Standard = standard
	result.Metadata = make(map[string]any)

	if standard == "" {
		return result, fmt.Errorf("standard cannot be empty")
	}

	// Apply defaults
	opt := ConformOptions{
		PreserveUnknown: true,
		Validate:        true,
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeConformOptions(opt, opts[0])
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
		log.Error("Conform operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Get type schema
	typeSchema := GenerateTypeSchema(reflect.TypeOf(input))

	// Build custom rules description
	customRulesDesc := ""
	if len(opt.CustomRules) > 0 {
		var parts []string
		for field, rule := range opt.CustomRules {
			parts = append(parts, fmt.Sprintf("- %s: %s", field, rule))
		}
		customRulesDesc = fmt.Sprintf("\n\nCustom rules:\n%s", strings.Join(parts, "\n"))
	}

	strictNote := ""
	if opt.Strict {
		strictNote = "\nStrict mode: fail if any field cannot be fully conformed."
	}

	if opt.PreserveUnknown {
		strictNote += "\nKeep source fields the standard does not define rather than dropping them."
	} else {
		strictNote += "\nDrop source fields the standard does not define."
	}

	systemPrompt := fmt.Sprintf(`You are a data standards compliance expert. Transform data to conform to the %s standard.

Data schema: %s%s%s

Return a JSON object with:
{
  "conformed": %s,
  "adjustments": [
    {
      "field": "field_name",
      "original_value": "original",
      "conformed_value": "adjusted",
      "rule": "the rule applied",
      "description": "what was changed and why"
    }
  ],
  "violations": ["issues that could not be fixed"],
  "compliance": 0.0-1.0
}

Standard: %s
Known standards:
- USPS: US Postal Service address format (uppercase, abbreviated states, ZIP+4)
- ISO8601: Date/time format (YYYY-MM-DDTHH:MM:SSZ)
- E164: International phone numbers (+1234567890)
- RFC5322: Email address format
- JSON-LD: Linked Data format
- Schema.org: Structured data vocabulary

Rules:
- Transform data to match the standard exactly
- Document all changes in adjustments
- List any violations that couldn't be fixed
- Calculate compliance as ratio of conforming fields`,
		standard, typeSchema, customRulesDesc, strictNote, typeSchema, standard)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Conform this data to the %s standard:

%s%s`, standard, string(inputJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Model:         opt.Model,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Conform operation LLM call failed", "error", err)
		return result, fmt.Errorf("conformance failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Conformed   json.RawMessage `json:"conformed"`
		Adjustments []Adjustment    `json:"adjustments"`
		Violations  []string        `json:"violations"`
		Compliance  float64         `json:"compliance"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Conform operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse conformance result: %w", err)
	}

	// Check strict mode violations
	if opt.Strict && len(parsed.Violations) > 0 {
		return result, fmt.Errorf("strict conformance failed: %v", parsed.Violations)
	}

	// The conformed value is the answer. A response without one is a failure,
	// not a success that produced nothing.
	//
	// This was guarded by `if len(parsed.Conformed) > 0`, so a response
	// carrying any other recognised field -- `{"compliance":0.9}` was enough to
	// satisfy the decoder's field check -- returned a nil error with
	// result.Conformed left at T's zero value. A caller reading
	// `if err == nil { use(result.Conformed) }` got an empty struct and no
	// indication anything was wrong, which is the shape of failure this
	// library exists to refuse: reporting success while being wrong.
	if len(parsed.Conformed) == 0 || string(parsed.Conformed) == "null" {
		log.Error("Conform operation failed: the response carried no conformed value",
			"standard", standard)
		return result, fmt.Errorf(
			"conform: the response carried no `conformed` value, so there is nothing to return")
	}
	if err := json.Unmarshal(parsed.Conformed, &result.Conformed); err != nil {
		log.Error("Conform operation failed: conformed data parse error", "error", err)
		return result, fmt.Errorf("failed to parse conformed data: %w", err)
	}

	result.Adjustments = parsed.Adjustments
	result.Violations = parsed.Violations
	result.Compliance = parsed.Compliance

	log.Debug("Conform operation succeeded",
		"standard", standard,
		"adjustments", len(result.Adjustments),
		"violations", len(result.Violations),
		"compliance", result.Compliance)

	return result, nil
}

// mergeConformOptions merges user options with defaults
func mergeConformOptions(defaults, user ConformOptions) ConformOptions {
	// Strict and PreserveUnknown are bools, handle explicitly
	defaults.Strict = user.Strict
	defaults.PreserveUnknown = user.PreserveUnknown
	defaults.Validate = user.Validate
	if user.CustomRules != nil {
		defaults.CustomRules = user.CustomRules
	}
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
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}
