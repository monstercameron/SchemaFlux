// package ops - Project operation for semantic structure transformation
package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ProjectOptions configures the Project operation
type ProjectOptions struct {
	// Mappings explicitly maps source fields to target fields. Each key has to
	// be a field of the input (when the input has field names to check it
	// against), and each target has to be a field of the projection the model
	// returns (when that has field names).
	Mappings map[string]string

	// Exclude names source fields to withhold. They are removed from the
	// payload before it is serialised, matched by JSON field name
	// case-insensitively at every level, so their values never reach the
	// provider. The output is scanned for the removed values and a projection
	// containing one is an error. See the Project doc comment for the limits:
	// this is a field-name filter, not a classifier.
	Exclude []string

	// InferMissing allows inferring target fields not in source. When it is
	// false, an answer in which the model says it inferred a field is refused.
	InferMissing bool

	// Common options
	Steering     string
	Mode         types.Mode
	Intelligence types.Speed

	// Model pins the exact model for this call, bypassing the Intelligence
	// tier's mapping. These option types spell their common fields out rather
	// than embedding CommonOptions, so the pin has to be declared here too:
	// otherwise `.Model(...)` on the builder would be a setter that changes
	// nothing, which this repository fails the build over.
	Model string
	// Effort is how much the model reasons before it answers. The builder sets
	// it here for the same reason it sets Model here.
	Effort types.Effort

	// MaxOutputTokens is the ceiling on the answer's length. Zero means the tier's.
	MaxOutputTokens int
	// WebSearch declares the provider's web-search tool on the call.
	WebSearch bool

	Context       context.Context
	RequestID     string
	CorrelationID string
}

// FieldMapping describes how a field was mapped
type FieldMapping struct {
	// SourceField is the original field name (empty if inferred)
	SourceField string `json:"source_field,omitempty"`

	// TargetField is the destination field name
	TargetField string `json:"target_field"`

	// Method describes how the mapping was done ("direct", "rename", "transform", "infer")
	Method string `json:"method"`

	// Transformation describes any value transformation applied
	Transformation string `json:"transformation,omitempty"`
}

// ProjectResult contains the projected data and mapping information
type ProjectResult[U any] struct {
	// Projected is the transformed data in the target schema
	Projected U `json:"projected"`

	// Mappings describes how each field was mapped. It is the model's own
	// account and is not checked.
	Mappings []FieldMapping `json:"mappings"`

	// Lost names source fields whose name does not appear among the field
	// names of the model's projected answer. It is computed here from that
	// answer and the input rather than taken from the model.
	//
	// It is a comparison of top-level field names, over every element when
	// either side is a list of objects. A field the projection renamed
	// therefore appears here, and Mappings is where the model says where it
	// went. FieldsCompared says whether the comparison was made.
	Lost []string `json:"lost,omitempty"`

	// Inferred names fields of the model's projected answer with no
	// counterpart by name in the source, computed the same way. A renamed
	// field appears here as well as in Lost.
	Inferred []string `json:"inferred,omitempty"`

	// FieldsCompared reports that Lost and Inferred were computed: the input
	// and the answer both had object field names to compare. When it is false
	// (a projection of scalars, or of an empty list) empty lists mean nothing
	// was compared, not that nothing was lost or inferred.
	FieldsCompared bool `json:"fields_compared"`

	// ModelClaimedLost and ModelClaimedInferred are the model's own account of
	// the same two things.
	//
	// They are kept because where the computed and claimed sets disagree, the
	// disagreement is the interesting part: a field the model says it inferred
	// and the diff says came from the source is a different problem from the
	// reverse. They are claims, not measurements, and are named so.
	ModelClaimedLost     []string `json:"model_claimed_lost,omitempty"`
	ModelClaimedInferred []string `json:"model_claimed_inferred,omitempty"`

	// ModelConfidence in the projection quality (0.0-1.0). It is the model's
	// own claim, and an answer that does not report one in that range is
	// refused.
	ModelConfidence float64 `json:"confidence"`

	// Metadata is not populated.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Project transforms data structure while preserving semantics.
//
// Type parameters:
//   - T: Input data type
//   - U: Output/projected data type
//
// # What Exclude does, and does not, guarantee
//
// Excluded fields are removed from the payload before it is serialised, so
// their values never reach the provider. Matching is on the JSON field name,
// case-insensitively, at every level of the document. The projected output is
// then scanned for the removed values, and a projection containing one is an
// error rather than a result.
//
// This is a field-name filter, not a classifier. It removes what you name and
// nothing else: an SSN that also appears inside a free-text "notes" field is
// still sent, because the notes field was not excluded. Naming the fields
// correctly is the caller's job, and the guarantee is only as good as that
// list. For anything stronger, do not send the record.
//
// The operation is still a model call: everything not excluded is transmitted.
//
// # What the answer is checked for
//
// The answer is refused when it has no projected value, when that value shares
// no field with U, when it reports no confidence from 0 to 1, when the
// projection lacks a target the caller's Mappings name, and when InferMissing
// is false and the model says it inferred a field. Lost and Inferred are worked
// out here from the field names of the model's answer and of the input, and
// FieldsCompared says whether that comparison could be made.
//
// Examples:
//
//	// Example 1: Derive a public profile, withholding two fields
//	type InternalUser struct {
//	    ID           string `json:"id"`
//	    Email        string `json:"email"`
//	    PasswordHash string `json:"password_hash"`
//	    SSN          string `json:"ssn"`
//	    FirstName    string `json:"first_name"`
//	    LastName     string `json:"last_name"`
//	    CreatedAt    string `json:"created_at"`
//	}
//	type PublicProfile struct {
//	    UserID      string `json:"user_id"`
//	    DisplayName string `json:"display_name"`
//	    MemberSince string `json:"member_since"`
//	}
//	result, err := Project[InternalUser, PublicProfile](user, ProjectOptions{
//	    Mappings: map[string]string{"id": "user_id", "created_at": "member_since"},
//	    Exclude: []string{"password_hash", "ssn"},
//	    InferMissing: true,
//	    Steering: "Combine first_name and last_name into display_name",
//	})
//	// email, first_name, and last_name were sent; password_hash and ssn were not.
//	for _, m := range result.Mappings {
//	    fmt.Printf("%s → %s (%s)\n", m.SourceField, m.TargetField, m.Method)
//	}
//
//	// Example 2: Project order to invoice summary
//	type Order struct {
//	    ID       string      `json:"id"`
//	    Items    []OrderItem `json:"items"`
//	    Customer Customer    `json:"customer"`
//	    Created  string      `json:"created"`
//	}
//	type InvoiceSummary struct {
//	    InvoiceNumber string  `json:"invoice_number"`
//	    CustomerName  string  `json:"customer_name"`
//	    TotalAmount   float64 `json:"total_amount"`
//	    ItemCount     int     `json:"item_count"`
//	    Date          string  `json:"date"`
//	}
//	result, err := Project[Order, InvoiceSummary](order)
//
//	// Example 3: Schema migration with field transforms
//	result, err := Project[OldSchema, NewSchema](data, ProjectOptions{
//	    Mappings: map[string]string{"old_field": "new_field"},
//	    Steering: "Convert date formats from MM/DD/YYYY to ISO8601",
//	})
//	fmt.Printf("Lost fields: %v, Inferred: %v\n", result.Lost, result.Inferred)
func Project[T any, U any](input T, opts ...ProjectOptions) (ProjectResult[U], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting project operation")

	// Apply defaults
	opt := NewProjectOptions()
	if len(opts) > 0 {
		opt = mergeProjectOptions(opt, opts[0])
	}
	if err := opt.validate(); err != nil {
		return ProjectResult[U]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert input to JSON, with the excluded fields removed before anything
	// leaves the process. Interpolating Exclude into the prompt as a hint --
	// which is all it used to do -- sent the value anyway and asked the model
	// not to look at it.
	inputJSON, excludedValues, err := marshalWithoutExcluded(input, opt.Exclude)
	if err != nil {
		log.Error("Project operation failed: marshal error", "error", loggedError(err))
		return ProjectResult[U]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}

	// The names the caller gave have to be fields of the input. The whole
	// input is read, including what Exclude withholds: a mapping from a
	// withheld field is still a mapping from a field that exists.
	sourceJSON, err := json.Marshal(input)
	if err != nil {
		return ProjectResult[U]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal input: %w", err))
	}
	sourceNames, sourceNamed := rawFieldNames(sourceJSON)
	if sourceNamed {
		sources := sortedKeys(opt.Mappings)
		if err := checkFieldNamesIn("project", "Mappings", sources, sourceNames); err != nil {
			return ProjectResult[U]{}, err
		}
	}

	// Get schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build mappings description. Both sides of each mapping are the caller's
	// text, so each is written as a JSON string.
	mappingsDesc := ""
	if len(opt.Mappings) > 0 {
		var parts []string
		for _, src := range sortedKeys(opt.Mappings) {
			parts = append(parts, fmt.Sprintf("- %s \u2192 %s", parsePromptQuote(src), parsePromptQuote(opt.Mappings[src])))
		}
		mappingsDesc = fmt.Sprintf("\n\nExplicit mappings:\n%s", strings.Join(parts, "\n"))
	}

	// The excluded fields are already gone from the payload; this only tells the
	// model not to invent them.
	excludeDesc := ""
	if len(opt.Exclude) > 0 {
		excludeDesc = fmt.Sprintf("\n\nThese source fields have been withheld and must not appear in the output: %s",
			parsePromptQuote(sortedStrings(opt.Exclude)))
	}

	inferNote := ""
	if opt.InferMissing {
		inferNote = "\nInferMissing: true - derive target fields not present in source"
	}

	systemPrompt := fmt.Sprintf(`You are a data projection expert. Transform data from one structure to another while preserving semantics.

Source schema: %s
Target schema: %s%s%s%s

Return a JSON object with:
{
  "projected": %s,
  "mappings": [
    {
      "source_field": "original_field",
      "target_field": "destination_field",
      "method": "direct/rename/transform/infer",
      "transformation": "description of any value transformation"
    }
  ],
  "lost": ["fields that couldn't be mapped"],
  "inferred": ["target fields that were inferred"],
  "confidence": 0.0-1.0
}

Methods:
- "direct": Field copied directly (same name and type)
- "rename": Field renamed but value unchanged
- "transform": Value transformed (aggregated, computed, formatted)
- "infer": Value derived/inferred (not from a single source field)

Rules:
- Map source fields to matching target fields semantically
- Preserve data meaning even when field names differ
- Note any source fields that couldn't be projected
- Mark inferred fields clearly`,
		inputSchema, outputSchema, mappingsDesc, excludeDesc, inferNote, outputSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", parsePromptQuote(opt.Steering))
	}

	userPrompt := fmt.Sprintf(`Project this data to the target schema:

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
		log.Error("Project operation LLM call failed", "error", loggedError(err))
		return ProjectResult[U]{}, fmt.Errorf("projection failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name, and a confidence of zero is the model
	// saying zero.
	var parsed struct {
		Projected       json.RawMessage `json:"projected"`
		Mappings        []FieldMapping  `json:"mappings"`
		Lost            []string        `json:"lost"`
		Inferred        []string        `json:"inferred"`
		ModelConfidence float64         `json:"confidence"`
	}

	if err := decodeAnswer("project", response, &parsed); err != nil {
		// The response is the caller's record projected; it does not belong in
		// a log line.
		log.Error("Project operation failed: parse error", "error", loggedError(err))
		return ProjectResult[U]{}, fmt.Errorf("failed to parse projection result: %w", err)
	}

	// The projection is what the caller asked for. The reader refuses an answer
	// without it; a RawMessage can hold a null, which is not a projection of
	// nothing, and a zero U would pass for the result.
	if rawValueAbsent(parsed.Projected) {
		log.Error("Project operation failed: the projected value is null")
		return ProjectResult[U]{}, types.Newf(types.KindSchemaViolation, "project", "projected is null")
	}
	if confidence := parsed.ModelConfidence; !(confidence >= 0 && confidence <= 1) {
		return ProjectResult[U]{}, types.Newf(types.KindSchemaViolation, "project", "the answer's confidence is outside 0 to 1")
	}

	// Decoded into a local, and only assigned to the result once it has
	// passed: json.Unmarshal fills what it can before it reports an error.
	var projected U
	if err := decodeCallerAnswer("project", string(parsed.Projected), &projected); err != nil {
		log.Error("Project operation failed: projected data parse error", "error", loggedError(err))
		return ProjectResult[U]{}, fmt.Errorf("failed to parse projected data: %w", err)
	}

	// The strip above means the model never saw the excluded values, so it
	// cannot echo one back. This is the belt to that braces: if an excluded
	// value ever reaches the output — through a later change, a shape the strip
	// missed, or a value the caller also put in Steering — that is an error,
	// not a projection.
	if leaked := findLeakedValues(projected, excludedValues); leaked != "" {
		log.Error("Project operation produced an excluded value", "field", leaked)
		return ProjectResult[U]{}, types.Classified(types.KindInvariantViolation, fmt.Errorf("project: the projection contains the value of excluded field %q", leaked))
	}

	// InferMissing false is the caller saying not to invent fields. The model's
	// own answer says when it did: a mapping by the "infer" method, or a field
	// listed as inferred.
	if !opt.InferMissing {
		if claimsInference(parsed.Mappings, parsed.Inferred) {
			return ProjectResult[U]{}, types.Newf(types.KindInvariantViolation, "project", "the answer says it inferred fields, and InferMissing is false")
		}
	}

	producedNames, producedNamed := rawFieldNames(parsed.Projected)

	// The targets the caller named for explicit mappings have to be in the
	// projection, when the projection has field names to look in.
	if producedNamed {
		targets := make([]string, 0, len(opt.Mappings))
		for _, source := range sortedKeys(opt.Mappings) {
			targets = append(targets, opt.Mappings[source])
		}
		if missing := namesMissingFrom(targets, producedNames); len(missing) > 0 {
			return ProjectResult[U]{}, types.Newf(types.KindInvariantViolation, "project", "the projection lacks the fields the explicit mappings name as targets: %s", strings.Join(missing, ", "))
		}
	}

	result := ProjectResult[U]{
		Projected:            projected,
		Mappings:             parsed.Mappings,
		ModelClaimedLost:     parsed.Lost,
		ModelClaimedInferred: parsed.Inferred,
		ModelConfidence:      parsed.ModelConfidence,
	}

	// Lost and Inferred are computed, not reported.
	//
	// They used to be whatever the model said they were -- an audit trail
	// written by the thing being audited. A model that drops a field and does
	// not mention it produces a projection that claims to have lost nothing.
	// Both are a set difference over the field names of the answer the model
	// returned, which Go can compute exactly. The names are read from the raw
	// answer: the decoded U carries every field of its type whether or not the
	// model supplied it, so a diff over U is fixed by the pair of types.
	//
	// The model's own account is kept alongside as a claim, because where the
	// two disagree the disagreement is the interesting part.
	if sourceNamed && producedNamed {
		result.Lost = missingFrom(sourceNames, producedNames)
		result.Inferred = missingFrom(producedNames, sourceNames)
		result.FieldsCompared = true
	}

	log.Debug("Project operation succeeded",
		"mappings", len(result.Mappings),
		"lost", len(result.Lost),
		"inferred", len(result.Inferred),
		"confidence", result.ModelConfidence)

	return result, nil
}

// validate refuses a call that would weaken a lock on its context, and an
// option value that cannot be meant: a mapping with a blank end, which the
// prompt would state as nothing.
func (opt ProjectOptions) validate() error {
	// The ceiling a caller asks for is read against the lock as well: it reaches
	// the request now, so a lock that did not look at it could be raised past.
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return err
	}
	for _, source := range sortedKeys(opt.Mappings) {
		if strings.TrimSpace(source) == "" || strings.TrimSpace(opt.Mappings[source]) == "" {
			return types.Newf(types.KindConfiguration, "project", "a mapping has a blank source or target")
		}
	}
	return nil
}

// claimsInference reports whether the model's own answer says it inferred a
// field.
func claimsInference(mappings []FieldMapping, inferred []string) bool {
	if len(inferred) > 0 {
		return true
	}
	for _, mapping := range mappings {
		if strings.EqualFold(strings.TrimSpace(mapping.Method), "infer") {
			return true
		}
	}
	return false
}

// mergeProjectOptions merges user options with defaults
func mergeProjectOptions(defaults, user ProjectOptions) ProjectOptions {
	if user.Mappings != nil {
		defaults.Mappings = user.Mappings
	}
	if user.Exclude != nil {
		defaults.Exclude = user.Exclude
	}
	defaults.InferMissing = user.InferMissing

	common := mergeCallFields(
		callFields{defaults.Steering, defaults.Mode, defaults.Intelligence, defaults.Model, defaults.Effort, defaults.MaxOutputTokens, defaults.WebSearch, defaults.Context, defaults.RequestID, defaults.CorrelationID},
		callFields{user.Steering, user.Mode, user.Intelligence, user.Model, user.Effort, user.MaxOutputTokens, user.WebSearch, user.Context, user.RequestID, user.CorrelationID})
	defaults.Steering, defaults.Mode, defaults.Intelligence, defaults.Model = common.Steering, common.Mode, common.Intelligence, common.Model
	defaults.Effort, defaults.MaxOutputTokens, defaults.WebSearch = common.Effort, common.MaxOutputTokens, common.WebSearch
	defaults.Context, defaults.RequestID, defaults.CorrelationID = common.Context, common.RequestID, common.CorrelationID
	return defaults
}

// callFields are the fields Pivot and Project's options spell out by hand
// rather than embedding CommonOptions, gathered so that the rule for merging a
// caller's options over the defaults is written once. RequestID and
// CorrelationID are among them: a merge that forgot them sent every call out
// without the identifiers its caller set.
type callFields struct {
	Steering        string
	Mode            types.Mode
	Intelligence    types.Speed
	Model           string
	Effort          types.Effort
	MaxOutputTokens int
	WebSearch       bool
	Context         context.Context
	RequestID       string
	CorrelationID   string
}

// mergeCallFields takes each of the user's fields that says something over the
// default's. An unset Mode or Intelligence says nothing, so a caller's own
// choice is never read as silence and silence never overrides a default.
func mergeCallFields(defaults, user callFields) callFields {
	if user.Steering != "" {
		defaults.Steering = user.Steering
	}
	if user.Mode != types.ModeUnset {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != types.TierUnset {
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

// rawValueAbsent reports whether a field of the model's answer was left out or
// null.
func rawValueAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// rawFieldNames returns the lower-cased top-level field names a JSON document
// carries, and whether it carries any to compare: an object has its keys, an
// array of objects has the keys of every element, and anything else (scalars,
// an empty array, a list of lists) has none and reports false.
//
// It reads the document as written. Reading the typed value the document was
// decoded into names every field of the type, supplied or not, and a list
// decodes into no names at all.
func rawFieldNames(raw []byte) (map[string]struct{}, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		names := make(map[string]struct{}, len(object))
		for key := range object {
			names[strings.ToLower(key)] = struct{}{}
		}
		return names, true
	}

	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, false
	}
	names := map[string]struct{}{}
	found := false
	for _, element := range elements {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(element, &fields); err != nil || fields == nil {
			continue
		}
		found = true
		for key := range fields {
			names[strings.ToLower(key)] = struct{}{}
		}
	}
	return names, found
}

// checkFieldNamesIn refuses a caller's option that names a field the input does
// not have. A dotted path is read by its first segment. The option and the name
// are the caller's own configuration, not payload. op is the operation that was
// called, Project or Pivot, and is the one the error names.
func checkFieldNamesIn(op, option string, names []string, fields map[string]struct{}) error {
	for _, name := range names {
		first, _, _ := strings.Cut(strings.TrimSpace(name), ".")
		if _, present := fields[strings.ToLower(first)]; !present {
			return types.Newf(types.KindConfiguration, op, "%s names %q, which is not a field of the input", option, name)
		}
	}
	return nil
}

// namesMissingFrom returns the names that are not among the fields, sorted. A
// dotted path is read by its first segment.
func namesMissingFrom(names []string, fields map[string]struct{}) []string {
	var missing []string
	for _, name := range names {
		first, _, _ := strings.Cut(strings.TrimSpace(name), ".")
		if _, present := fields[strings.ToLower(first)]; !present {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// missingFrom returns the names in `from` that do not appear in `in`, sorted so
// the result is stable across runs.
func missingFrom(from, in map[string]struct{}) []string {
	var missing []string
	for name := range from {
		if _, present := in[name]; !present {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
