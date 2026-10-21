// Normalize operation for standardizing format and terminology.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
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

	// Fields to normalize (empty = all). A changed top-level field outside this
	// list is refused. Names are top-level field names, compared without regard
	// to case; a nested path such as "address.street" names no top-level field.
	Fields []string

	// Fields to skip. A changed top-level field in this list is refused.
	SkipFields []string

	// Locale for normalization
	Locale string

	// Strict asks the model to leave a value unchanged, and to say so in its
	// warnings, when it is not sure of the normalization. It is guidance to the
	// model: Go has no way to tell an uncertain normalization from a certain one,
	// so the answer is not checked against it.
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
		return fmt.Errorf("normalize case must be lower, upper, title, or sentence")
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
	return mergeEmbeddedOpOptions(n.CommonOptions, n.OpOptions)
}

func (n NormalizeOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return n.CommonOptions, n.OpOptions
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
//
// What the answer has to satisfy, beyond being the right shape: it carries the
// normalized value, the changes and the warnings; the fields that differ from
// the input are computed here, and none of them is a field in SkipFields, and
// when Fields is set none is outside it. An answer that breaks one is refused,
// and the result beside the error is empty. Standard, Rules, FixTypos,
// NormalizeCase, NormalizeWhitespace, Locale and Strict are instructions to the
// model that Go cannot check against the answer; they are not enforced.
//
// ChangedFields names top-level fields when T is a struct or a map. When T is
// anything else (a string, a slice, a number) the value is the only thing there
// is, and a change to it is reported as one change named "$".
func Normalize[T any](input T, opts NormalizeOptions) (NormalizeResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting normalize operation")

	// Validate options
	if err := opts.Validate(); err != nil {
		return NormalizeResult[T]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var cancel context.CancelFunc
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	typeSchema := GenerateTypeSchema(inputType)

	// Marshal input
	inputJSON, err := promptJSON(input)
	if err != nil {
		log.Error("Normalize operation failed: marshal error", "error", loggedError(err))
		return NormalizeResult[T]{}, types.Newf(types.KindInvalidRequest, "normalize", "the input could not be written as JSON")
	}

	// Build normalization instructions. The standard, the rules, the field names
	// and the locale are the caller's text: they are written as JSON strings, and
	// the rules in the order of their field names, so that the same options give
	// the same prompt and a value cannot read as one of the instructions.
	var instructions []string

	if opts.Standard != "" {
		instructions = append(instructions, fmt.Sprintf("Apply %s standard formatting", quotedText(opts.Standard)))
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

	for _, field := range sortedKeys(opts.Rules) {
		instructions = append(instructions, fmt.Sprintf("For %s: %s", quotedText(field), quotedText(opts.Rules[field])))
	}

	mappingsDesc := ""
	if len(opts.CanonicalMappings) > 0 {
		mappings := make([]string, 0, len(opts.CanonicalMappings))
		for _, from := range sortedKeys(opts.CanonicalMappings) {
			mappings = append(mappings, fmt.Sprintf("%s -> %s", quotedText(from), quotedText(opts.CanonicalMappings[from])))
		}
		mappingsDesc = fmt.Sprintf("\nCanonical mappings:\n%s", strings.Join(mappings, "\n"))
	}

	fieldsDesc := ""
	if len(opts.Fields) > 0 {
		fieldsDesc = fmt.Sprintf("\nOnly normalize these fields: %s", quotedList(opts.Fields))
	}

	skipDesc := ""
	if len(opts.SkipFields) > 0 {
		skipDesc = fmt.Sprintf("\nSkip these fields: %s", quotedList(opts.SkipFields))
	}

	localeDesc := ""
	if opts.Locale != "" {
		localeDesc = fmt.Sprintf("\nLocale: %s", quotedText(opts.Locale))
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
  "normalized": {},
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

	// Declare the response shape rather than only describing it in prose
	// (S-002, CI-008). The schema is generated from a struct with the fields the
	// answer is read for, so it cannot drift from what this operation actually
	// accepts -- and a provider with structured output enforces it instead of
	// being asked politely.
	var declared struct {
		Normalized T                 `json:"normalized"`
		Changes    []NormalizeChange `json:"changes"`
		Warnings   []string          `json:"warnings"`
	}
	opt.ResponseFormat = "json"
	// The schema is the plan's own and is only sent, never changed.
	if schema := typePlanFor(reflect.TypeOf(declared)).sharedSchema(); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = "normalize_response"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Normalize operation LLM call failed", "error", loggedError(err))
		return NormalizeResult[T]{}, fmt.Errorf("normalization failed: %w", err)
	}

	result, err := readNormalizeAnswer(input, opts, response)
	if err != nil {
		log.Error("Normalize operation refused the answer", "error", loggedError(err))
		return NormalizeResult[T]{}, err
	}

	log.Debug("Normalize operation succeeded", "totalChanges", result.TotalChanges)
	return result, nil
}

// readNormalizeAnswer reads the model's answer and works out, from the input and
// the answer alone, which fields changed.
func readNormalizeAnswer[T any](input T, opts NormalizeOptions, response string) (NormalizeResult[T], error) {
	const op = "normalize"

	answer, err := readAnswerObject(op, response)
	if err != nil {
		return NormalizeResult[T]{}, err
	}

	var result NormalizeResult[T]
	// "normalized" is decoded from its raw member: a plain T field would read an
	// answer that left it out as the zero value, and the caller would be handed
	// an empty record as a normalization of theirs.
	if err := answer.decode("normalized", &result.Normalized); err != nil {
		return NormalizeResult[T]{}, err
	}
	if err := answer.decode("changes", &result.Changes); err != nil {
		return NormalizeResult[T]{}, err
	}
	if err := answer.decode("warnings", &result.Warnings); err != nil {
		return NormalizeResult[T]{}, err
	}

	// The verified half. A value diff cannot say *why* something changed, so
	// the model's reasons stay its own; what Go can establish is which fields
	// moved, and whether the account covers them.
	result.ChangedFields = changedFieldsBetween(input, result.Normalized)

	// Fields and SkipFields are stated in the prompt. The diff is exact, so the
	// answer can be held to them.
	skipped := lowerSet(opts.SkipFields)
	allowed := lowerSet(opts.Fields)
	for _, field := range result.ChangedFields {
		if field == whollyChanged {
			continue
		}
		if skipped[field] {
			return NormalizeResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"a field named in SkipFields was changed")
		}
		if len(allowed) > 0 && !allowed[field] {
			return NormalizeResult[T]{}, types.Newf(types.KindInvariantViolation, op,
				"a field outside Fields was changed")
		}
	}

	result.Unreported = fieldsNotMentioned(result.ChangedFields, result.Changes)

	// The count follows the diff rather than the narrative, because a caller
	// checking TotalChanges is asking what happened to their data.
	result.TotalChanges = len(result.ChangedFields)
	return result, nil
}

// lowerSet is the set of the given names, folded to lower case the way
// changedFieldsBetween names fields.
func lowerSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[strings.ToLower(strings.TrimSpace(name))] = true
	}
	return set
}

// NormalizeText is a convenience function for normalizing plain text
func NormalizeText(input string, opts NormalizeOptions) (string, error) {
	result, err := Normalize(input, opts)
	if err != nil {
		return "", err
	}
	return result.Normalized, nil
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
// know which of their records is missing. The first failure also stops the
// batch: items that have not started are not sent, so one rejected request does
// not spend the rest of the batch.
func NormalizeBatch[T any](items []T, opts NormalizeOptions) ([]NormalizeResult[T], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting normalize batch operation", "itemCount", len(items))

	if len(items) == 0 {
		return nil, nil
	}

	// Each item runs under the batch's own context, so that cancelling the batch
	// reaches the call in flight.
	base := opts.CommonOptions.Context
	if base == nil {
		base = opts.OpOptions.Context
	}

	results, _, err := MapReduce(flowContext(base), items,
		MapReduceOptions{ChunkSize: 1, Concurrency: opts.Concurrency},
		func(ctx context.Context, chunk []T) (NormalizeResult[T], error) {
			perItem := opts
			useContext(&perItem.CommonOptions, &perItem.OpOptions, ctx)
			return Normalize(chunk[0], perItem)
		})
	if err != nil {
		var failed ChunkError
		if errors.As(err, &failed) {
			log.Error("NormalizeBatch failed for item", "index", failed.Index, "error", loggedError(failed.Err))
			return nil, fmt.Errorf("failed to normalize item %d: %w", failed.Index, failed.Err)
		}
		return nil, fmt.Errorf("failed to normalize the batch: %w", err)
	}

	log.Debug("NormalizeBatch succeeded", "itemCount", len(items))
	return results, nil
}

// whollyChanged is the name given to the change of a value that has no fields:
// a string, a number, a slice, or a value that was an object and is not one now.
const whollyChanged = "$"

// changedFieldsBetween names the top-level fields whose values differ.
//
// Values are compared as canonical JSON, so 1284.50 and 1284.5 are the same
// value and a reordered map is not a change. When either side is not a JSON
// object there are no fields to compare, and the two values are compared whole:
// a difference is reported as one change named "$". It used to report none, so a
// model that rewrote the whole of a string came back as a normalization that
// changed nothing.
func changedFieldsBetween(before, after any) []string {
	source, sourceIsObject := jsonFieldsOfObject(before)
	result, resultIsObject := jsonFieldsOfObject(after)

	if !sourceIsObject || !resultIsObject {
		if canonicalKey(before) != canonicalKey(after) {
			return []string{whollyChanged}
		}
		return nil
	}

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
// A change to a value that has no fields is accounted for by any reported
// change, because there is only one thing it could be about.
func fieldsNotMentioned(changed []string, reported []NormalizeChange) []string {
	mentioned := make(map[string]bool, len(reported))
	for _, change := range reported {
		mentioned[strings.ToLower(strings.TrimSpace(change.Field))] = true
	}

	var missing []string
	for _, field := range changed {
		if field == whollyChanged {
			if len(reported) == 0 {
				missing = append(missing, field)
			}
			continue
		}
		if !mentioned[strings.ToLower(field)] {
			missing = append(missing, field)
		}
	}
	return missing
}

// jsonValuesOf decodes a value into its top-level fields, keyed by lower-case
// name so the two sides of a diff line up whatever case the tags use. A value
// that is not a JSON object has none.
func jsonValuesOf(value any) map[string]any {
	values, _ := jsonFieldsOfObject(value)
	return values
}

// jsonFieldsOfObject is jsonValuesOf that also says whether the value was a JSON
// object at all, which an object with no fields and a string both look the same
// without.
func jsonFieldsOfObject(value any) (map[string]any, bool) {
	values := map[string]any{}

	encoded, err := json.Marshal(value)
	if err != nil {
		return values, false
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
		return values, false
	}

	for key, entry := range object {
		values[strings.ToLower(key)] = entry
	}
	return values, true
}
