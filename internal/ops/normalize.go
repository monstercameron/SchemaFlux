// package ops - Normalize operation for standardizing format/terminology
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

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

	// Concurrency bounds how many items NormalizeBatch has in flight at once.
	// Zero means DefaultConcurrency. The limit that matters is the provider's
	// rate limit, not the machine's, which is why the default is modest.
	Concurrency int

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

// WithConcurrency bounds how many items NormalizeBatch has in flight.
func (n NormalizeOptions) WithConcurrency(concurrency int) NormalizeOptions {
	n.Concurrency = concurrency
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
	Normalized T `json:"normalized"`

	// Changes is the model's account of what it altered, and it is the model's
	// account: a normalization's changes are *value* differences, and there is
	// no way to recover the reason for one by diffing. OP-308 kept this as a
	// claim for that reason -- unlike Project's Lost and Inferred, which are a
	// field-set difference Go can compute exactly (OP-302).
	//
	// What is verified is the count and the coverage: ChangedFields below is
	// computed, so a model that alters a field and does not mention it no
	// longer produces a normalization claiming to have changed nothing.
	Changes []NormalizeChange `json:"changes"`

	// ChangedFields names the fields whose values actually differ between the
	// input and the result, computed rather than reported.
	ChangedFields []string `json:"changed_fields,omitempty"`

	// Unreported names the fields that changed and that Changes does not
	// mention. A non-empty list means the model's account is incomplete, which
	// is exactly the thing a caller reading an audit trail needs to know.
	Unreported []string `json:"unreported,omitempty"`

	TotalChanges int            `json:"total_changes"`
	Warnings     []string       `json:"warnings,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
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

	// The verified half. A value diff cannot say *why* something changed, so
	// the model's reasons stay its own; what Go can establish is which fields
	// moved, and whether the account covers them.
	result.ChangedFields = changedFieldsBetween(input, result.Normalized)
	result.Unreported = fieldsNotMentioned(result.ChangedFields, parsed.Changes)

	// The count follows the diff rather than the narrative, because a caller
	// checking TotalChanges is asking what happened to their data.
	result.TotalChanges = len(result.ChangedFields)
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

// NormalizeBatch normalizes a slice of items with bounded concurrency.
//
// It was a serial loop: one blocking provider call per item, so normalizing two
// hundred records took two hundred round trips end to end and the caller had no
// way to say otherwise. The concurrency is bounded rather than unlimited
// because the limit that matters here is the provider's rate limit, not the
// machine's.
//
// Results stay in input order regardless of which item finishes first, and the
// first failure names the item's index -- a caller who gets an error needs to
// know which of their records is missing.
func NormalizeBatch[T any](items []T, opts NormalizeOptions) ([]NormalizeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting normalize batch operation", "itemCount", len(items))

	if len(items) == 0 {
		return nil, nil
	}

	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	results := make([]NormalizeResult[T], len(items))
	errs := make([]error, len(items))

	var wg sync.WaitGroup
	indices := make(chan int)

	go func() {
		defer close(indices)
		for i := range items {
			indices <- i
		}
	}()

	workers := concurrency
	if workers > len(items) {
		workers = len(items)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				result, err := Normalize(items[i], opts)
				if err != nil {
					errs[i] = err
					continue
				}
				results[i] = result
			}
		}()
	}
	wg.Wait()

	// The lowest-indexed failure is reported, so the error is the same whichever
	// order the workers happened to finish in.
	for i, err := range errs {
		if err != nil {
			log.Error("NormalizeBatch failed for item", "index", i, "error", err)
			return nil, fmt.Errorf("failed to normalize item %d: %w", i, err)
		}
	}

	log.Debug("NormalizeBatch succeeded", "itemCount", len(items))
	return results, nil
}

// changedFieldsBetween names the top-level fields whose values differ.
//
// Values are compared as canonical JSON, so 1284.50 and 1284.5 are the same
// value and a reordered map is not a change.
func changedFieldsBetween(before, after any) []string {
	source := jsonValuesOf(before)
	result := jsonValuesOf(after)

	var changed []string
	for _, name := range sortedKeys(source) {
		next, present := result[name]
		if !present {
			changed = append(changed, name)
			continue
		}
		if canonicalKey(source[name]) != canonicalKey(next) {
			changed = append(changed, name)
		}
	}
	for _, name := range sortedKeys(result) {
		if _, present := source[name]; !present {
			changed = append(changed, name)
		}
	}

	sort.Strings(changed)
	return changed
}

// fieldsNotMentioned returns the changed fields the model's account leaves out.
func fieldsNotMentioned(changed []string, reported []NormalizeChange) []string {
	mentioned := make(map[string]bool, len(reported))
	for _, change := range reported {
		mentioned[strings.ToLower(strings.TrimSpace(change.Field))] = true
	}

	var missing []string
	for _, field := range changed {
		if !mentioned[strings.ToLower(field)] {
			missing = append(missing, field)
		}
	}
	return missing
}

// jsonValuesOf decodes a value into its top-level fields, keyed by lower-case
// name so the two sides of a diff line up whatever case the tags use.
func jsonValuesOf(value any) map[string]any {
	values := map[string]any{}

	encoded, err := json.Marshal(value)
	if err != nil {
		return values
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return values
	}

	for key, entry := range object {
		values[strings.ToLower(key)] = entry
	}
	return values
}
