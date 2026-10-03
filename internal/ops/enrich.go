package ops

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// EnrichOptions configures the Enrich operation
type EnrichOptions struct {
	CommonOptions
	types.OpOptions

	// Fields to derive/add
	DeriveFields []string

	// Field derivation rules (field -> how to derive it)
	DerivationRules map[string]string

	// External context to use for enrichment. It shadows the embedded
	// types.OpOptions.Context, which is the context.Context for cancellation:
	// this one is prompt material.
	Context map[string]any

	// Include confidence scores for derived fields. Enrich checks that every
	// field it reports as added has a score; EnrichInPlace has no place to put
	// scores and refuses the option.
	IncludeConfidence bool

	// Only add fields, don't modify existing. Enrich refuses an answer in
	// which a field the input had comes back with a different value.
	// EnrichInPlace refuses one in which a field that already had a value
	// comes back with a different value, and lets empty fields be filled.
	AddOnly bool

	// Fields to exclude from enrichment: an answer that changes one of them is
	// refused. A field is named as in the JSON encoding of the input, and case
	// is ignored.
	ExcludeFields []string

	// Domain for domain-specific enrichment
	Domain string

	// Depth is "shallow" or "deep". It is checked by Validate and is not sent
	// to the model, so it changes nothing about the enrichment; it stays until
	// the options are redesigned.
	Depth string
}

// NewEnrichOptions creates EnrichOptions with defaults
func NewEnrichOptions() EnrichOptions {
	return EnrichOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		IncludeConfidence: false,
		AddOnly:           true,
		Depth:             "shallow",
	}
}

// Validate validates EnrichOptions
func (e EnrichOptions) Validate() error {
	if err := e.CommonOptions.Validate(); err != nil {
		return err
	}
	validDepths := map[string]bool{"shallow": true, "deep": true}
	if e.Depth != "" && !validDepths[e.Depth] {
		return fmt.Errorf("invalid depth: %s", e.Depth)
	}
	return nil
}

// WithDeriveFields sets fields to derive
func (e EnrichOptions) WithDeriveFields(fields []string) EnrichOptions {
	e.DeriveFields = fields
	return e
}

// WithDerivationRules sets derivation rules
func (e EnrichOptions) WithDerivationRules(rules map[string]string) EnrichOptions {
	e.DerivationRules = rules
	return e
}

// WithContext sets external context for enrichment
func (e EnrichOptions) WithContext(ctx map[string]any) EnrichOptions {
	e.Context = ctx
	return e
}

// WithIncludeConfidence enables confidence scores
func (e EnrichOptions) WithIncludeConfidence(include bool) EnrichOptions {
	e.IncludeConfidence = include
	return e
}

// WithAddOnly only adds fields, doesn't modify existing
func (e EnrichOptions) WithAddOnly(addOnly bool) EnrichOptions {
	e.AddOnly = addOnly
	return e
}

// WithExcludeFields sets fields to exclude from enrichment
func (e EnrichOptions) WithExcludeFields(fields []string) EnrichOptions {
	e.ExcludeFields = fields
	return e
}

// WithDomain sets the domain for enrichment
func (e EnrichOptions) WithDomain(domain string) EnrichOptions {
	e.Domain = domain
	return e
}

// WithDepth sets the enrichment depth
func (e EnrichOptions) WithDepth(depth string) EnrichOptions {
	e.Depth = depth
	return e
}

// WithSteering sets the steering prompt
func (e EnrichOptions) WithSteering(steering string) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithSteering(steering)
	return e
}

// WithMode sets the mode
func (e EnrichOptions) WithMode(mode types.Mode) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithMode(mode)
	return e
}

// WithIntelligence sets the intelligence level
func (e EnrichOptions) WithIntelligence(intelligence types.Speed) EnrichOptions {
	e.CommonOptions = e.CommonOptions.WithIntelligence(intelligence)
	return e
}

func (e EnrichOptions) toOpOptions() types.OpOptions {
	return mergeEmbeddedOpOptions(e.CommonOptions, e.OpOptions)
}

func (e EnrichOptions) optionHalves() (CommonOptions, types.OpOptions) {
	return e.CommonOptions, e.OpOptions
}

// EnrichResult contains the enriched data and metadata
type EnrichResult[T any] struct {
	Enriched T `json:"enriched"`

	// AddedFields names the fields the enrichment produced that the source did
	// not have, computed by diffing the two field sets rather than taken from
	// the model.
	//
	// It was whatever the model said it was: an audit trail written by the
	// thing being audited. A model that adds a field and does not mention it
	// produced an enrichment claiming to have added nothing, and the caller's
	// only evidence was the field itself.
	AddedFields []string `json:"added_fields"`

	// ModelClaimedAddedFields is the model's own account of the same thing,
	// kept because a disagreement between the two is the interesting part.
	ModelClaimedAddedFields []string `json:"model_claimed_added_fields,omitempty"`

	ModelConfidence map[string]float64 `json:"confidence,omitempty"`
	Derivations     map[string]string  `json:"derivations,omitempty"`
	Metadata        map[string]any     `json:"metadata,omitempty"`
}

// Enrich adds derived or inferred fields to data using LLM intelligence.
// It can infer missing data, calculate derived fields, and add context.
//
// Type parameter T specifies the input type.
// Type parameter U specifies the enriched output type (can have additional fields).
//
// Examples:
//
//	// Enrich customer data with inferred fields
//	result, err := Enrich[Customer, EnrichedCustomer](customer, NewEnrichOptions().
//	    WithDeriveFields([]string{"risk_score", "segment", "lifetime_value"}))
//
//	// Enrich with specific rules
//	result, err := Enrich[Product, EnrichedProduct](product, NewEnrichOptions().
//	    WithDerivationRules(map[string]string{
//	        "category": "infer from product name and description",
//	        "price_tier": "low/medium/high based on price",
//	    }))
//
//	// Domain-specific enrichment
//	result, err := Enrich[Article, AnalyzedArticle](article, NewEnrichOptions().
//	    WithDomain("news").
//	    WithDeriveFields([]string{"bias", "credibility", "topics"}))
func Enrich[T any, U any](input T, opts EnrichOptions) (EnrichResult[U], error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting enrich operation")

	// Every return that carries an error carries the zero result with it.

	// Validate options
	if err := opts.Validate(); err != nil {
		return EnrichResult[U]{}, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opt.Context, 0)
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	outputType := reflect.TypeFor[U]()

	inputSchema := GenerateTypeSchema(inputType)
	outputSchema := GenerateTypeSchema(outputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("Enrich operation failed: marshal error", "error", err)
		return EnrichResult[U]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// Build the requests the options make of the model.
	notes, err := enrichRequestNotes(opts)
	if err != nil {
		return EnrichResult[U]{}, err
	}

	addOnlyDesc := ""
	if opts.AddOnly {
		addOnlyDesc = "\nOnly add new fields, do not modify existing field values."
	}

	confidenceNote := ""
	if opts.IncludeConfidence {
		confidenceNote = "\nInclude confidence scores (0.0-1.0) for each derived field."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at data enrichment and inference. Add derived fields to the input data.

Input schema:
%s

Output schema (enriched):
%s

Fields to derive:
%s%s%s%s%s%s

Return a JSON object with:
{
  "enriched": {},
  "added_fields": ["list of fields that were added"],
  "confidence": {"field": 0.95},
  "derivations": {"field": "how it was derived"}
}`, inputSchema, outputSchema, enrichDerivationInstructions(opts), notes.context, notes.domain, notes.exclude, addOnlyDesc, confidenceNote)

	userPrompt := fmt.Sprintf("Enrich this data:\n\n%s", string(inputJSON))

	// The response shape, with the enriched value typed.
	type enrichSchemaShape struct {
		Enriched        U                  `json:"enriched"`
		AddedFields     []string           `json:"added_fields"`
		ModelConfidence map[string]float64 `json:"confidence"`
		Derivations     map[string]string  `json:"derivations"`
	}
	// A schema is built from that shape when one can be, so that it cannot drift
	// from what the answer is read into. Two of its members are maps, which
	// strict mode cannot express, so no schema is built for any U and none is
	// sent: the request asks for JSON and describes the shape in the prompt, and
	// the answer is held to it when it is read. The branch stays for the day
	// the shape has none.
	opt.ResponseFormat = "json"
	// The schema is the plan's own and is only sent, never changed.
	if schema := typePlanFor(reflect.TypeOf(enrichSchemaShape{})).sharedSchema(); schema != nil {
		opt.JSONSchema = schema
		opt.SchemaName = "enrich_response"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Enrich operation LLM call failed", "error", err)
		return EnrichResult[U]{}, fmt.Errorf("enrichment failed: %w", err)
	}

	// Parse the response. The enriched value is read as raw JSON first, so that
	// one the model left out, or gave as null, can be told from an enriched
	// value that is legitimately empty: decoding straight into U made both the
	// zero U, and the AddOnly comparison below then ran against that zero value.
	//
	// The prompt asks for the enriched value, the added fields and the
	// derivations every time, and an answer without one is refused by name. The
	// confidence scores are asked for only under IncludeConfidence, so that member
	// is optional here; when the option asks for them, the check below refuses an
	// added field that holds a value and has no score.
	var parsed struct {
		Enriched        json.RawMessage    `json:"enriched"`
		AddedFields     []string           `json:"added_fields"`
		ModelConfidence map[string]float64 `json:"confidence,omitempty"` // asked for only under IncludeConfidence
		Derivations     map[string]string  `json:"derivations"`
	}
	if err := decodeAnswer("enrich", response, &parsed); err != nil {
		log.Error("Enrich operation failed: parse error", "error", err)
		return EnrichResult[U]{}, fmt.Errorf("failed to parse enriched result: %w", err)
	}
	// A RawMessage can hold a null, which the reader lets through.
	if isJSONNull(parsed.Enriched) {
		log.Error("Enrich operation failed: the enriched value is null")
		return EnrichResult[U]{}, types.Newf(types.KindSchemaViolation, "enrich",
			"enriched is null, so there is nothing to return")
	}
	var enriched U
	if err := decodeCallerAnswer("enrich", string(parsed.Enriched), &enriched); err != nil {
		log.Error("Enrich operation failed: enriched value parse error", "error", err)
		return EnrichResult[U]{}, fmt.Errorf("failed to parse enriched result: %w", err)
	}

	// AddOnly means what it says, and it was not being checked.
	//
	// The option is documented as "only adds fields, doesn't modify existing",
	// and it appeared in the prompt and nowhere else -- so a response that
	// rewrote an existing value came back with a nil error and the caller's
	// data silently changed. Enrich is supposed to be additive; a caller who
	// set AddOnly did so precisely because the existing values matter.
	//
	// The comparison is on the JSON encoding rather than on Go values because
	// the input and the answer can be different types (Enrich[T, U]), so a
	// field-by-field reflect comparison has nothing stable to compare across.
	if opts.AddOnly {
		if changed, ok := changedExistingField(input, enriched); ok {
			log.Error("Enrich operation failed: an existing field changed under AddOnly",
				"field", changed)
			return EnrichResult[U]{}, types.Newf(types.KindInvariantViolation, "enrich",
				"AddOnly was set and field %q was changed; enrichment must add, not overwrite", changed)
		}
	}
	// ExcludeFields is "do not modify these fields", and was only a sentence in
	// the prompt.
	if changed, ok := changedNamedField(input, enriched, opts.ExcludeFields); ok {
		log.Error("Enrich operation failed: an excluded field changed", "field", changed)
		return EnrichResult[U]{}, types.Newf(types.KindInvariantViolation, "enrich",
			"field %q is in ExcludeFields and was changed", changed)
	}

	// The scores are the model's claims. One off the scale it was asked for is
	// refused, and when scores were asked for every added field has to have one.
	for _, field := range sortedKeys(parsed.ModelConfidence) {
		if err := onScale("enrich", "a field's confidence", parsed.ModelConfidence[field]); err != nil {
			return EnrichResult[U]{}, err
		}
	}

	// Computed, not reported. See the field's own comment.
	sourceFields := fieldNamesOfValue(input)
	producedFields := fieldNamesOfValue(enriched)
	addedFields := missingFrom(producedFields, sourceFields)

	if opts.IncludeConfidence {
		scored := make(map[string]struct{}, len(parsed.ModelConfidence))
		for field := range parsed.ModelConfidence {
			scored[strings.ToLower(field)] = struct{}{}
		}
		// A field the answer leaves empty has nothing to be confident about,
		// so only the added fields that hold a value need a score.
		held := make(map[string]any)
		for key, value := range jsonObjectOf(enriched) {
			held[strings.ToLower(key)] = value
		}
		for _, field := range addedFields {
			if jsonValueIsEmpty(held[field]) {
				continue
			}
			if _, ok := scored[strings.ToLower(field)]; !ok {
				log.Error("Enrich operation failed: an added field has no confidence", "field", field)
				return EnrichResult[U]{}, types.Newf(types.KindSchemaViolation, "enrich",
					"IncludeConfidence was set and the added field %q has no confidence score", field)
			}
		}
	}

	result := EnrichResult[U]{
		Enriched:                enriched,
		AddedFields:             addedFields,
		ModelClaimedAddedFields: parsed.AddedFields,
		ModelConfidence:         parsed.ModelConfidence,
		Derivations:             parsed.Derivations,
		Metadata:                make(map[string]any),
	}
	if result.ModelConfidence == nil {
		result.ModelConfidence = make(map[string]float64)
	}
	if result.Derivations == nil {
		result.Derivations = make(map[string]string)
	}

	log.Debug("Enrich operation succeeded", "addedFields", len(result.AddedFields))
	return result, nil
}

// fieldNamesOfValue returns the lower-cased JSON field names a value carries
// once it is written, which is what the enrichment contains and not what its
// type could contain: a field left out by omitempty is not one. A list has the
// names of the fields of its elements, so enriching a list reports what was
// added to it; reading the encoded value as a single object found no names for
// one. A value with no fields to name, a scalar or a list of them, has none.
func fieldNamesOfValue(value any) map[string]struct{} {
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]struct{}{}
	}
	names, _ := rawFieldNames(encoded)
	if names == nil {
		return map[string]struct{}{}
	}
	return names
}

// EnrichInPlace enriches data without changing the type (adds to map or fills empty fields).
//
// AddOnly (on by default) is enforced: an answer in which a field that already
// had a value comes back with a different value is refused, and so is one that
// changes a field named in ExcludeFields. A field that was empty may be filled.
// Domain and Context reach the prompt as they do in Enrich. IncludeConfidence is
// refused, since the result type has no place to carry scores.
func EnrichInPlace[T any](input T, opts EnrichOptions) (T, error) {
	log := loggerForOptions(opts.CommonOptions, opts.OpOptions)
	log.Debug("Starting enrich in-place operation")

	var none T

	// Validate options
	if err := opts.Validate(); err != nil {
		return none, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}
	if opts.IncludeConfidence {
		return none, types.Newf(types.KindConfiguration, "enrich",
			"EnrichInPlace returns the enriched value itself and has nowhere to put confidence scores; use Enrich")
	}

	opt := opts.toOpOptions()

	ctx, cancel := operationContext(opt.Context, 0)
	defer cancel()

	// Get type information
	inputType := reflect.TypeOf(input)
	typeSchema := GenerateTypeSchema(inputType)

	// Marshal input
	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Error("EnrichInPlace failed: marshal error", "error", err)
		return none, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	notes, err := enrichRequestNotes(opts)
	if err != nil {
		return none, err
	}

	keepDesc := ""
	if opts.AddOnly {
		keepDesc = "\nDo not change a field that already has a value; fill only the fields that are empty."
	}

	systemPrompt := fmt.Sprintf(`You are an expert at data enrichment. Fill in or enhance fields in the input data.

Schema:
%s

Fields to derive/fill:
%s%s%s%s%s

Return ONLY the enriched data matching the schema (no wrapper object).`, typeSchema, enrichDerivationInstructions(opts), notes.context, notes.domain, notes.exclude, keepDesc)

	userPrompt := fmt.Sprintf("Enrich this data:\n\n%s", string(inputJSON))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("EnrichInPlace LLM call failed", "error", err)
		return none, fmt.Errorf("enrichment failed: %w", err)
	}

	// Decoded into a local, so that an answer that fails halfway leaves nothing
	// in the result.
	var enriched T
	if err := decodeCallerAnswer("enrich", response, &enriched); err != nil {
		log.Error("EnrichInPlace failed: parse error", "error", err)
		return none, fmt.Errorf("failed to parse enriched result: %w", err)
	}

	// An answer with one recognised field is returned whole by the decoder, so
	// {"keywords":["x"]} came back as a T with every other field zeroed. What
	// the caller already had is compared with what came back.
	if opts.AddOnly {
		if changed, ok := changedFilledField(input, enriched, nil, true); ok {
			log.Error("EnrichInPlace failed: a field that had a value changed under AddOnly", "field", changed)
			return none, types.Newf(types.KindInvariantViolation, "enrich",
				"AddOnly was set and field %q, which had a value, was changed", changed)
		}
	}
	if changed, ok := changedNamedField(input, enriched, opts.ExcludeFields); ok {
		log.Error("EnrichInPlace failed: an excluded field changed", "field", changed)
		return none, types.Newf(types.KindInvariantViolation, "enrich",
			"field %q is in ExcludeFields and was changed", changed)
	}

	log.Debug("EnrichInPlace succeeded")
	return enriched, nil
}

// enrichNotes are the prompt lines the options Context, Domain and
// ExcludeFields add, each empty when its option is.
type enrichNotes struct {
	context, domain, exclude string
}

// enrichRequestNotes renders the options Enrich and EnrichInPlace share. The
// caller's text is written as JSON, and a context that cannot be written is an
// error rather than a line with nothing after it.
func enrichRequestNotes(opts EnrichOptions) (enrichNotes, error) {
	var notes enrichNotes

	if len(opts.Context) > 0 {
		contextJSON, err := json.Marshal(opts.Context)
		if err != nil {
			return enrichNotes{}, types.Newf(types.KindConfiguration, "enrich", "the context cannot be written as JSON")
		}
		notes.context = fmt.Sprintf("\nAdditional context for enrichment:\n%s", string(contextJSON))
	}

	if opts.Domain != "" {
		quoted, _ := promptJSON(opts.Domain)
		notes.domain = fmt.Sprintf("\nDomain: %s (use domain-specific knowledge)", quoted)
	}

	if len(opts.ExcludeFields) > 0 {
		quoted, _ := promptJSON(opts.ExcludeFields)
		notes.exclude = fmt.Sprintf("\nDo not modify these fields: %s", quoted)
	}

	return notes, nil
}

// enrichDerivationInstructions lists what the options ask to be derived: the
// fields in the order given, then the rules in field order (ranging over the map
// wrote them in a different order on every call), the caller's text quoted.
func enrichDerivationInstructions(opts EnrichOptions) string {
	var instructions []string
	for _, field := range opts.DeriveFields {
		quoted, _ := promptJSON(field)
		instructions = append(instructions, fmt.Sprintf("- %s: infer from available data", quoted))
	}
	for _, field := range sortedKeys(opts.DerivationRules) {
		quotedField, _ := promptJSON(field)
		quotedRule, _ := promptJSON(opts.DerivationRules[field])
		instructions = append(instructions, fmt.Sprintf("- %s: %s", quotedField, quotedRule))
	}
	if len(instructions) == 0 {
		instructions = append(instructions, "- infer the most useful missing or derived fields from the available data")
	}
	return strings.Join(instructions, "\n")
}

// changedNamedField reports the first of the named fields that is in both the
// input and the answer with a different value. Names match the JSON keys with
// case ignored. Any change is a change, including filling a field that was empty.
func changedNamedField(input, enriched any, names []string) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	return changedFilledField(input, enriched, wanted, false)
}

// changedFilledField is changedExistingField for an in-place enrichment: a field
// whose value in the input is empty is not protected, since filling empty fields
// is what the operation is for, unless allowFill is false. A nil only checks
// every field; otherwise only the lower-cased names in it.
func changedFilledField(input, enriched any, only map[string]struct{}, allowFill bool) (string, bool) {
	before := jsonObjectOf(input)
	after := jsonObjectOf(enriched)
	if before == nil || after == nil {
		return "", false
	}

	for _, name := range sortedKeys(before) {
		if only != nil {
			if _, named := only[strings.ToLower(name)]; !named {
				continue
			}
		}
		original := before[name]
		produced, present := after[name]
		if !present {
			continue
		}
		if allowFill && jsonValueIsEmpty(original) {
			continue
		}
		if !reflect.DeepEqual(original, produced) {
			return name, true
		}
	}
	return "", false
}

// jsonValueIsEmpty reports whether a decoded JSON value holds nothing: null, an
// empty string, zero, false, an empty list or an empty object.
func jsonValueIsEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case float64:
		return typed == 0
	case bool:
		return !typed
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// changedExistingField reports the first field present in both the input and
// the answer whose value differs, which is what AddOnly forbids.
//
// It compares the JSON encodings because Enrich's input and output are separate
// type parameters: there is no common Go type to walk, and the wire shape is
// what both sides agree on anyway. A field absent from either side is not a
// change -- one is an addition, which is the whole point of the operation, and
// the other is a removal that the field-set accounting already reports.
func changedExistingField(input, enriched any) (string, bool) {
	before := jsonObjectOf(input)
	after := jsonObjectOf(enriched)
	if before == nil || after == nil {
		return "", false
	}

	for name, original := range before {
		produced, present := after[name]
		if !present {
			continue
		}
		if !reflect.DeepEqual(original, produced) {
			return name, true
		}
	}
	return "", false
}

// jsonObjectOf renders a value as a generic JSON object, or nil when it is not
// one. A non-object input (a string, a slice) has no named fields for AddOnly
// to protect, so there is nothing to compare.
func jsonObjectOf(value any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil
	}
	return object
}
