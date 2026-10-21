package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ConformOptions configures the Conform operation.
//
// Passing a ConformOptions value to Conform replaces the defaults of its boolean
// fields with the value's own: Conform with no options keeps PreserveUnknown and
// Validate on, but Conform(x, std, ConformOptions{Steering: "..."}) has both
// off, because a bool that is false cannot be told from one the caller never
// set. A caller who passes options and wants those defaults sets
// PreserveUnknown: true and Validate: true. Telling "unset" from "false" needs
// the options redesigned; until then the behaviour is this.
type ConformOptions struct {
	// Strict fails if data cannot fully conform to the standard: Conform
	// returns an error when the model reports any violation.
	Strict bool

	// PreserveUnknown asks the model to keep fields the standard does not
	// cover rather than drop them. It is a request: nothing checks the answer,
	// since which fields the standard covers is the model's judgement.
	PreserveUnknown bool

	// Validate is not read by Conform. It is kept for callers that set it and
	// is the default-on field described above.
	Validate bool

	// CustomRules adds custom conformance rules
	CustomRules map[string]string

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
//	    "E164", ConformOptions{PreserveUnknown: true, Validate: true, Steering: "Assume US country code +1"})
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
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting conform operation")

	// Every return that carries an error carries the zero result with it.
	if standard == "" {
		return ConformResult[T]{}, types.Classified(types.KindConfiguration, errors.New("standard cannot be empty"))
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
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return ConformResult[T]{}, err
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
		log.Error("Conform operation failed: marshal error", "error", loggedError(err))
		return ConformResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// Get type schema
	typeSchema := GenerateTypeSchema(reflect.TypeOf(input))

	// The standard is the caller's text, so it is written as a JSON string
	// wherever the prompt uses it.
	quotedStandard, _ := promptJSON(standard)

	// Build custom rules description, in field order: ranging over the map
	// wrote the rules in a different order on every call. Both halves are the
	// caller's text and are quoted.
	customRulesDesc := ""
	if len(opt.CustomRules) > 0 {
		var parts []string
		for _, field := range sortedKeys(opt.CustomRules) {
			quotedField, _ := promptJSON(field)
			quotedRule, _ := promptJSON(opt.CustomRules[field])
			parts = append(parts, fmt.Sprintf("- %s: %s", quotedField, quotedRule))
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
		quotedStandard, typeSchema, customRulesDesc, strictNote, typeSchema, quotedStandard)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Conform this data to the %s standard:

%s%s`, quotedStandard, string(inputJSON), steeringNote)

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
		log.Error("Conform operation LLM call failed", "error", loggedError(err))
		return ConformResult[T]{}, fmt.Errorf("conformance failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name, and a compliance of zero is the model
	// saying zero. An empty list is how it says there was nothing to adjust or
	// nothing it could not fix.
	var parsed struct {
		Conformed   json.RawMessage `json:"conformed"`
		Adjustments []Adjustment    `json:"adjustments"`
		Violations  []string        `json:"violations"`
		Compliance  float64         `json:"compliance"`
	}

	// The log line carries the error, which describes the shape of the answer,
	// and not the answer.
	if err := decodeAnswer("conform", response, &parsed); err != nil {
		log.Error("Conform operation failed: parse error", "error", loggedError(err))
		return ConformResult[T]{}, fmt.Errorf("failed to parse conformance result: %w", err)
	}

	// Check strict mode violations. The error counts them: what they say is the
	// model's description of the caller's data, and the error is logged.
	if opt.Strict && len(parsed.Violations) > 0 {
		return ConformResult[T]{}, types.Newf(types.KindInvariantViolation, "conform",
			"strict conformance failed: the model reported %d violation(s)", len(parsed.Violations))
	}

	// The conformed value is the answer. The reader refuses one that is missing;
	// a RawMessage can hold a null, which would leave result.Conformed at T's
	// zero value under a nil error, so a null is refused here.
	if isJSONNull(parsed.Conformed) {
		log.Error("Conform operation failed: the conformed value is null")
		return ConformResult[T]{}, types.Newf(types.KindSchemaViolation, "conform",
			"conformed is null, so there is nothing to return")
	}

	// The score is the model's claim about its own work. One that is not on the
	// scale it was asked for is refused.
	if err := onScale("conform", "the compliance score", parsed.Compliance); err != nil {
		return ConformResult[T]{}, err
	}

	var conformed T
	if err := decodeCallerAnswer("conform", string(parsed.Conformed), &conformed); err != nil {
		log.Error("Conform operation failed: conformed data parse error", "error", loggedError(err))
		return ConformResult[T]{}, fmt.Errorf("failed to parse conformed data: %w", err)
	}

	result := ConformResult[T]{
		Conformed:   conformed,
		Adjustments: parsed.Adjustments,
		Violations:  parsed.Violations,
		Compliance:  parsed.Compliance,
		Standard:    standard,
		Metadata:    make(map[string]any),
	}

	log.Debug("Conform operation succeeded",
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
