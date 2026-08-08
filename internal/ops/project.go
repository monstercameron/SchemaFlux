// package ops - Project operation for semantic structure transformation
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ProjectOptions configures the Project operation
type ProjectOptions struct {
	// Mappings explicitly maps source fields to target fields
	Mappings map[string]string

	// Exclude names source fields to withhold. They are removed from the
	// payload before it is serialised, matched by JSON field name
	// case-insensitively at every level, so their values never reach the
	// provider. The output is scanned for the removed values and a projection
	// containing one is an error. See the Project doc comment for the limits:
	// this is a field-name filter, not a classifier.
	Exclude []string

	// InferMissing allows inferring target fields not in source
	InferMissing bool

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

	// Mappings describes how each field was mapped
	Mappings []FieldMapping `json:"mappings"`

	// Lost names source fields that do not appear in the projection, computed
	// by diffing the two field sets rather than taken from the model.
	Lost []string `json:"lost,omitempty"`

	// Inferred names projected fields with no counterpart in the source,
	// computed the same way.
	Inferred []string `json:"inferred,omitempty"`

	// ModelClaimedLost and ModelClaimedInferred are the model's own account of
	// the same two things.
	//
	// They are kept because where the computed and claimed sets disagree, the
	// disagreement is the interesting part: a field the model says it inferred
	// and the diff says came from the source is a different problem from the
	// reverse. They are claims, not measurements, and are named so.
	ModelClaimedLost     []string `json:"model_claimed_lost,omitempty"`
	ModelClaimedInferred []string `json:"model_claimed_inferred,omitempty"`

	// ModelConfidence in the projection quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
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
	log := logger.GetLogger()
	log.Debug("Starting project operation")

	var result ProjectResult[U]
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := ProjectOptions{
		InferMissing: false,
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeProjectOptions(opt, opts[0])
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
		log.Error("Project operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Get schemas
	inputSchema := GenerateTypeSchema(reflect.TypeOf(input))
	var zero U
	outputSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build mappings description
	mappingsDesc := ""
	if len(opt.Mappings) > 0 {
		var parts []string
		for _, src := range sortedKeys(opt.Mappings) {
			parts = append(parts, fmt.Sprintf("- %s → %s", src, opt.Mappings[src]))
		}
		mappingsDesc = fmt.Sprintf("\n\nExplicit mappings:\n%s", strings.Join(parts, "\n"))
	}

	// The excluded fields are already gone from the payload; this only tells the
	// model not to invent them.
	excludeDesc := ""
	if len(opt.Exclude) > 0 {
		excludeDesc = fmt.Sprintf("\n\nThese source fields have been withheld and must not appear in the output: %s",
			strings.Join(sortedStrings(opt.Exclude), ", "))
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
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Project this data to the target schema:

%s%s`, string(inputJSON), steeringNote)

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
		log.Error("Project operation LLM call failed", "error", err)
		return result, fmt.Errorf("projection failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Projected       json.RawMessage `json:"projected"`
		Mappings        []FieldMapping  `json:"mappings"`
		Lost            []string        `json:"lost"`
		Inferred        []string        `json:"inferred"`
		ModelConfidence float64         `json:"confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		// The response is the caller's record projected; it does not belong in
		// a log line (X-03).
		log.Error("Project operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse projection result: %w", err)
	}

	// Parse projected data
	if len(parsed.Projected) > 0 {
		if err := json.Unmarshal(parsed.Projected, &result.Projected); err != nil {
			log.Error("Project operation failed: projected data parse error", "error", err)
			return result, fmt.Errorf("failed to parse projected data: %w", err)
		}
	}

	// The strip above means the model never saw the excluded values, so it
	// cannot echo one back. This is the belt to that braces: if an excluded
	// value ever reaches the output — through a later change, a shape the strip
	// missed, or a value the caller also put in Steering — that is an error,
	// not a projection.
	if leaked := findLeakedValues(result.Projected, excludedValues); leaked != "" {
		log.Error("Project operation produced an excluded value", "field", leaked)
		return ProjectResult[U]{}, fmt.Errorf("project: the projection contains the value of excluded field %q", leaked)
	}

	result.Mappings = parsed.Mappings

	// Lost and Inferred are computed, not reported.
	//
	// They were whatever the model said they were -- an audit trail written by
	// the thing being audited. A model that drops a field and does not mention
	// it produces a projection that claims to have lost nothing, and the
	// caller's only evidence that something is missing is the thing that is
	// missing. Both are now a set difference over the actual field names, which
	// Go can compute exactly.
	//
	// The model's own account is kept alongside as a claim, because where the
	// two disagree the disagreement is the interesting part: a field the model
	// says it inferred and Go says came from the source is a different problem
	// from the reverse.
	sourceFields := jsonFieldNamesOfValue(input)
	producedFields := jsonFieldNamesOfValue(result.Projected)

	result.Lost = missingFrom(sourceFields, producedFields)
	result.Inferred = missingFrom(producedFields, sourceFields)
	result.ModelClaimedLost = parsed.Lost
	result.ModelClaimedInferred = parsed.Inferred
	result.ModelConfidence = parsed.ModelConfidence

	log.Debug("Project operation succeeded",
		"mappings", len(result.Mappings),
		"lost", len(result.Lost),
		"inferred", len(result.Inferred),
		"confidence", result.ModelConfidence)

	return result, nil
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

// jsonFieldNamesOfValue returns the JSON field names a value actually carries,
// by marshalling it and reading the keys.
//
// Marshalling rather than reflecting on the type is deliberate: `omitempty`
// means a field declared on the struct may not be present in the output, and
// the question here is what the projection *contains*, not what its type could
// contain.
func jsonFieldNamesOfValue(value any) map[string]struct{} {
	names := map[string]struct{}{}

	encoded, err := json.Marshal(value)
	if err != nil {
		return names
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		// Not an object -- a slice, a scalar, a string. There are no field
		// names to diff, and reporting every element as lost would be worse
		// than reporting nothing.
		return names
	}

	for key := range object {
		names[strings.ToLower(key)] = struct{}{}
	}
	return names
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
