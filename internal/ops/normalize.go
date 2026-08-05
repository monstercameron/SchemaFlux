// package ops - Normalize operation for standardizing format/terminology
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

// NormalizeOptions configures the Normalize operation
type NormalizeOptions struct {
	CommonOptions
	types.OpOptions

	// Standard to normalize to (e.g., "US_POSTAL", "ISO_8601", "SI_UNITS")
	Standard string

	// Custom normalization rules
	Rules map[string]string

	// Fix typos and spelling errors
	FixTypos bool

	// Normalize case ("lower", "upper", "title", "sentence")
	NormalizeCase string

	// Normalize whitespace
	NormalizeWhitespace bool

	// Canonical mappings (e.g., {"USA": "United States", "UK": "United Kingdom"})
	CanonicalMappings map[string]string

	// Fields to normalize (empty = all)
	Fields []string

	// Fields to skip
	SkipFields []string

	// Locale for normalization
	Locale string

	// Strict mode - fail if normalization uncertain
	Strict bool
}

// NewNormalizeOptions creates NormalizeOptions with defaults
func NewNormalizeOptions() NormalizeOptions {
	return NormalizeOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		FixTypos:            true,
		NormalizeWhitespace: true,
		Strict:              false,
	}
}

// Validate validates NormalizeOptions
func (n NormalizeOptions) Validate() error {
	if err := n.CommonOptions.Validate(); err != nil {
		return err
	}
	validCases := map[string]bool{"": true, "lower": true, "upper": true, "title": true, "sentence": true}
	if !validCases[n.NormalizeCase] {
		return fmt.Errorf("invalid normalize case: %s", n.NormalizeCase)
	}
	return nil
}

// WithStandard sets the normalization standard
func (n NormalizeOptions) WithStandard(standard string) NormalizeOptions {
	n.Standard = standard
	return n
}

// WithRules sets custom normalization rules
func (n NormalizeOptions) WithRules(rules map[string]string) NormalizeOptions {
	n.Rules = rules
	return n
}

// WithFixTypos enables typo fixing
func (n NormalizeOptions) WithFixTypos(fix bool) NormalizeOptions {
	n.FixTypos = fix
	return n
}

// WithNormalizeCase sets the case normalization
func (n NormalizeOptions) WithNormalizeCase(caseType string) NormalizeOptions {
	n.NormalizeCase = caseType
	return n
}

// WithNormalizeWhitespace enables whitespace normalization
func (n NormalizeOptions) WithNormalizeWhitespace(normalize bool) NormalizeOptions {
	n.NormalizeWhitespace = normalize
	return n
}

// WithCanonicalMappings sets canonical value mappings
func (n NormalizeOptions) WithCanonicalMappings(mappings map[string]string) NormalizeOptions {
	n.CanonicalMappings = mappings
	return n
}

// WithFields sets fields to normalize
func (n NormalizeOptions) WithFields(fields []string) NormalizeOptions {
	n.Fields = fields
	return n
}

// WithSkipFields sets fields to skip
func (n NormalizeOptions) WithSkipFields(fields []string) NormalizeOptions {
	n.SkipFields = fields
	return n
}

// WithLocale sets the locale for normalization
func (n NormalizeOptions) WithLocale(locale string) NormalizeOptions {
	n.Locale = locale
	return n
}

// WithStrict enables strict mode
func (n NormalizeOptions) WithStrict(strict bool) NormalizeOptions {
	n.Strict = strict
	return n
}

// WithSteering sets the steering prompt
func (n NormalizeOptions) WithSteering(steering string) NormalizeOptions {
	n.CommonOptions = n.CommonOptions.WithSteering(steering)
	return n
}

// WithMode sets the mode
func (n NormalizeOptions) WithMode(mode types.Mode) NormalizeOptions {
	n.CommonOptions = n.CommonOptions.WithMode(mode)
	return n
}

// WithIntelligence sets the intelligence level
func (n NormalizeOptions) WithIntelligence(intelligence types.Speed) NormalizeOptions {
	n.CommonOptions = n.CommonOptions.WithIntelligence(intelligence)
	return n
}

func (n NormalizeOptions) toOpOptions() types.OpOptions {
	return n.CommonOptions.toOpOptions()
}

// NormalizeChange represents a single normalization change
type NormalizeChange struct {
	Field      string `json:"field"`
	Original   string `json:"original"`
	Normalized string `json:"normalized"`
	Reason     string `json:"reason,omitempty"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64 `json:"confidence,omitempty"`
}

// NormalizeResult contains the results of normalization
type NormalizeResult[T any] struct {
	Normalized   T                 `json:"normalized"`
	Changes      []NormalizeChange `json:"changes"`
	TotalChanges int               `json:"total_changes"`
	Warnings     []string          `json:"warnings,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
}

// Normalize standardizes data format and terminology to a canonical form.
// It can fix typos, standardize formats, apply canonical mappings, and more.
//
// Type parameter T specifies the type of data to normalize.
//
// Examples:
//
//	// Normalize addresses to US postal standard
//	result, err := Normalize(addresses, NewNormalizeOptions().
//	    WithStandard("US_POSTAL").
//	    WithFixTypos(true))
//
//	// Normalize with custom mappings
//	result, err := Normalize(data, NewNormalizeOptions().
//	    WithCanonicalMappings(map[string]string{
//	        "USA": "United States",
//	        "UK": "United Kingdom",
//	    }))
//
//	// Normalize specific fields
//	result, err := Normalize(records, NewNormalizeOptions().
//	    WithFields([]string{"name", "email", "phone"}).
//	    WithNormalizeCase("lower"))
func Normalize[T any](input T, opts NormalizeOptions) (NormalizeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting normalize operation")

	var result NormalizeResult[T]
	result.Metadata = make(map[string]any)

	// Validate options
	if err := opts.Validate(); err != nil {
		return result, fmt.Errorf("invalid options: %w", err)
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	typeSchema := GenerateTypeSchema(inputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Normalize operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build normalization instructions
	var instructions []string

	if opts.Standard != "" {
		instructions = append(instructions, fmt.Sprintf("Apply %s standard formatting", opts.Standard))
	}

	if opts.FixTypos {
		instructions = append(instructions, "Fix typos and spelling errors")
	}

	if opts.NormalizeCase != "" {
		instructions = append(instructions, fmt.Sprintf("Normalize case to %s", opts.NormalizeCase))
	}

	if opts.NormalizeWhitespace {
		instructions = append(instructions, "Normalize whitespace (remove extra spaces, trim)")
	}

	for field, rule := range opts.Rules {
		instructions = append(instructions, fmt.Sprintf("For %s: %s", field, rule))
	}

	mappingsDesc := ""
	if len(opts.CanonicalMappings) > 0 {
		mappings := make([]string, 0, len(opts.CanonicalMappings))
		for _, from := range sortedKeys(opts.CanonicalMappings) {
			mappings = append(mappings, fmt.Sprintf("%s -> %s", from, opts.CanonicalMappings[from]))
		}
		mappingsDesc = fmt.Sprintf("\nCanonical mappings:\n%s", strings.Join(mappings, "\n"))
	}

	fieldsDesc := ""
	if len(opts.Fields) > 0 {
		fieldsDesc = fmt.Sprintf("\nOnly normalize these fields: %s", strings.Join(opts.Fields, ", "))
	}

	skipDesc := ""
	if len(opts.SkipFields) > 0 {
		skipDesc = fmt.Sprintf("\nSkip these fields: %s", strings.Join(opts.SkipFields, ", "))
	}

	localeDesc := ""
	if opts.Locale != "" {
		localeDesc = fmt.Sprintf("\nLocale: %s", opts.Locale)
	}

	strictNote := ""
	if opts.Strict {
		strictNote = "\nStrict mode: if uncertain about normalization, leave unchanged and add warning."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at data normalization and standardization.

Schema:
%s

Normalization instructions:
%s%s%s%s%s%s

Return a JSON object with:
{
  "normalized": <the normalized data>,
  "changes": [
    {
      "field": "field_name",
      "original": "original value",
      "normalized": "normalized value",
      "reason": "why this change was made",
      "confidence": 0.95
    }
  ],
  "warnings": ["any warnings or uncertain normalizations"]
}`, typeSchema, strings.Join(instructions, "\n"), mappingsDesc, fieldsDesc, skipDesc, localeDesc, strictNote)

	userPrompt := fmt.Sprintf("Normalize this data:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Normalize operation LLM call failed", "error", err)
		return result, fmt.Errorf("normalization failed: %w", err)
	}

	// Parse the response
	var parsed struct {
		Normalized T                 `json:"normalized"`
		Changes    []NormalizeChange `json:"changes"`
		Warnings   []string          `json:"warnings"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Normalize operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse normalized result: %w", err)
	}

	result.Normalized = parsed.Normalized
	result.Changes = parsed.Changes
	result.TotalChanges = len(parsed.Changes)
	result.Warnings = parsed.Warnings

	log.Debug("Normalize operation succeeded", "totalChanges", result.TotalChanges)
	return result, nil
}

// NormalizeText is a convenience function for normalizing plain text
func NormalizeText(input string, opts NormalizeOptions) (string, error) {
	result, err := Normalize(input, opts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result.Normalized), nil
}

// NormalizeBatch normalizes a slice of items
func NormalizeBatch[T any](items []T, opts NormalizeOptions) ([]NormalizeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting normalize batch operation", "itemCount", len(items))

	results := make([]NormalizeResult[T], len(items))

	for i, item := range items {
		result, err := Normalize(item, opts)
		if err != nil {
			log.Error("NormalizeBatch failed for item", "index", i, "error", err)
			return nil, fmt.Errorf("failed to normalize item %d: %w", i, err)
		}
		results[i] = result
	}

	log.Debug("NormalizeBatch succeeded", "itemCount", len(items))
	return results, nil
}
