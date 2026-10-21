package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// ComposeOptions configures the Compose operation.
//
// Passing a ComposeOptions value to Assemble replaces the defaults of its
// boolean fields with the value's own: Assemble with no options has Validate on,
// but Assemble(parts, ComposeOptions{Template: "..."}) has it off, because a
// bool that is false cannot be told from one the caller never set. Telling
// "unset" from "false" needs the options redesigned; until then the behaviour
// is this.
type ComposeOptions struct {
	// Template provides a structure hint for composition
	Template string

	// MergeStrategy specifies how to handle conflicts: "first", "last",
	// "combine" or "smart". Anything else is refused. The strategy is a request
	// for how the model resolves two sources that disagree; which source won
	// is its judgement and is not checked.
	MergeStrategy string

	// FillGaps allows inferring missing required fields. When it is false an
	// answer that lists filled gaps is refused.
	FillGaps bool

	// Validate is not read by Assemble. It is kept for callers that set it and
	// is the default-on field described above.
	Validate bool

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

// ComposedField describes how a field was composed
type ComposedField struct {
	// Field is the path to the composed field
	Field string `json:"field"`

	// Sources lists which input parts contributed to this field
	Sources []int `json:"sources"`

	// Method describes how the value was determined
	// "single" - from one source, "merged" - combined from multiple, "inferred" - filled gap
	Method string `json:"method"`

	// Conflicts notes if there were conflicting values
	Conflicts bool `json:"conflicts"`

	// Resolution explains how conflicts were resolved (if any)
	Resolution string `json:"resolution,omitempty"`
}

// ComposeResult contains the composed object and assembly information
type ComposeResult[T any] struct {
	// Composed is the assembled object
	Composed T `json:"composed"`

	// FieldSources documents where each field came from
	FieldSources []ComposedField `json:"field_sources"`

	// ConflictsResolved counts how many conflicts were resolved
	ConflictsResolved int `json:"conflicts_resolved"`

	// GapsFilled lists fields that were inferred (not from any part)
	GapsFilled []string `json:"gaps_filled,omitempty"`

	// UnusedParts lists indices of parts that contributed nothing
	UnusedParts []int `json:"unused_parts,omitempty"`

	// Completeness is the model's own claim, from 0.0 to 1.0, of how complete
	// the composed object is. It is not computed from the fields and not
	// checked against them. An answer without one, and a score outside 0.0 to
	// 1.0, are refused.
	Completeness float64 `json:"completeness"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Assemble builds a complex typed object from multiple parts.
//
// Type parameters:
//   - T: Target type to compose
//
// Examples:
//
//	// Example 1: Build company profile from multiple data sources
//	type CompanyProfile struct {
//	    Name        string   `json:"name"`
//	    Industry    string   `json:"industry"`
//	    Employees   int      `json:"employees"`
//	    Revenue     float64  `json:"revenue"`
//	    Products    []string `json:"products"`
//	    Competitors []string `json:"competitors"`
//	}
//	parts := []any{
//	    map[string]any{"name": "Acme Inc", "industry": "Tech"},          // LinkedIn
//	    map[string]any{"employees": 500, "revenue": 50000000},           // SEC filing
//	    map[string]any{"products": []string{"Widget", "Gadget"}},       // Product DB
//	    map[string]any{"competitors": []string{"TechCorp", "InnoLabs"}}, // Research
//	}
//	result, err := Assemble[CompanyProfile](parts, ComposeOptions{
//	    MergeStrategy: "smart",
//	})
//	for _, fs := range result.FieldSources {
//	    fmt.Printf("%s came from sources %v (%s)\n", fs.Field, fs.Sources, fs.Method)
//	}
//
//	// Example 2: Compose document from sections
//	type Document struct {
//	    Title      string `json:"title"`
//	    Abstract   string `json:"abstract"`
//	    Body       string `json:"body"`
//	    Conclusion string `json:"conclusion"`
//	}
//	result, err := Assemble[Document]([]any{titleSection, abstractSection, bodySection, conclusionSection}, ComposeOptions{
//	    Template: "Academic paper structure",
//	    FillGaps: true,
//	})
//	fmt.Printf("Completeness: %.0f%%\n", result.Completeness*100)
//
//	// Example 3: Merge user data from multiple forms
//	result, err := Assemble[UserProfile]([]any{step1Data, step2Data, step3Data}, ComposeOptions{
//	    MergeStrategy: "last",  // Later form submissions win
//	})
//	if result.ConflictsResolved > 0 {
//	    fmt.Printf("Resolved %d conflicts\n", result.ConflictsResolved)
//	}
func Assemble[T any](parts []any, opts ...ComposeOptions) (ComposeResult[T], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting compose operation", "parts", len(parts))

	// Every return that carries an error carries the zero result with it.

	// Apply defaults
	opt := ComposeOptions{
		MergeStrategy: "smart",
		FillGaps:      false,
		Validate:      true,
		Mode:          types.TransformMode,
		Intelligence:  types.Smart,
	}
	if len(opts) > 0 {
		opt = mergeComposeOptions(opt, opts[0])
	}
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return ComposeResult[T]{}, err
	}

	// The strategy is written into the prompt, and only these four mean anything
	// to it.
	switch opt.MergeStrategy {
	case "first", "last", "combine", "smart":
	default:
		return ComposeResult[T]{}, types.Newf(types.KindConfiguration, "compose",
			`the merge strategy must be "first", "last", "combine" or "smart"`)
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert parts to JSON with indices
	partsJSON := make([]string, len(parts))
	for i, part := range parts {
		data, err := json.Marshal(part)
		if err != nil {
			log.Error("Compose operation failed: marshal error for part", "index", i, "error", loggedError(err))
			return ComposeResult[T]{}, types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal part %d: %w", i, err))
		}
		partsJSON[i] = fmt.Sprintf(`{"index": %d, "data": %s}`, i, string(data))
	}
	allPartsJSON := "[" + strings.Join(partsJSON, ",\n") + "]"

	// Get target schema
	var zero T
	targetSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build template description
	templateDesc := ""
	if opt.Template != "" {
		// The caller's text, written as a JSON string so that it cannot read as
		// an instruction.
		quotedTemplate, _ := promptJSON(opt.Template)
		templateDesc = fmt.Sprintf("\n\nTemplate/guidance: %s", quotedTemplate)
	}

	systemPrompt := fmt.Sprintf(`You are a data composition expert. Assemble a complete object from multiple parts.

Target schema: %s%s

Merge strategy: %s
Fill gaps: %v

Return a JSON object with:
{
  "composed": %s,
  "field_sources": [
    {
      "field": "path.to.field",
      "sources": [0, 2],
      "method": "single/merged/inferred",
      "conflicts": true/false,
      "resolution": "explanation if conflicts"
    }
  ],
  "conflicts_resolved": 0,
  "gaps_filled": ["field1", "field2"],
  "unused_parts": [1, 3],
  "completeness": 0.0-1.0
}

Merge strategies:
- "first": Use first source's value
- "last": Use last source's value
- "combine": Merge arrays, concatenate strings where appropriate
- "smart": Use best judgment based on data quality and completeness

Rules:
- Map each part's fields to the target schema
- Track which parts contributed to each field
- Note and resolve conflicts when multiple parts provide the same field
- Mark unused parts that contributed nothing
- Only fill gaps if FillGaps is true`,
		targetSchema, templateDesc, opt.MergeStrategy, opt.FillGaps, targetSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Compose these parts into a single object:

%s%s`, allPartsJSON, steeringNote)

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
		log.Error("Compose operation LLM call failed", "error", loggedError(err))
		return ComposeResult[T]{}, fmt.Errorf("composition failed: %w", err)
	}

	// Parse the response. The prompt asks for every member, so an answer that
	// leaves one out is refused by name, and a completeness of zero is the model
	// saying zero.
	var parsed struct {
		Composed          json.RawMessage `json:"composed"`
		FieldSources      []ComposedField `json:"field_sources"`
		ConflictsResolved int             `json:"conflicts_resolved"`
		GapsFilled        []string        `json:"gaps_filled"`
		UnusedParts       []int           `json:"unused_parts"`
		Completeness      float64         `json:"completeness"`
	}

	// The log line carries the error, which describes the shape of the answer,
	// and not the answer.
	if err := decodeAnswer("compose", response, &parsed); err != nil {
		log.Error("Compose operation failed: parse error", "error", loggedError(err))
		return ComposeResult[T]{}, fmt.Errorf("failed to parse composition result: %w", err)
	}

	// The composed object is the answer, and a null is not one: a RawMessage can
	// hold it, so the reader lets it through.
	if isJSONNull(parsed.Composed) {
		log.Error("Compose operation failed: the composed object is null")
		return ComposeResult[T]{}, types.Newf(types.KindSchemaViolation, "compose",
			"composed is null, so there is nothing to return")
	}
	var composed T
	if err := decodeCallerAnswer("compose", string(parsed.Composed), &composed); err != nil {
		log.Error("Compose operation failed: composed object parse error", "error", loggedError(err))
		return ComposeResult[T]{}, fmt.Errorf("failed to parse composed object: %w", err)
	}

	// What Go can check about the account the model gave of its work. Gaps are
	// only to be filled when the caller allowed it, and the parts it names have
	// to be parts that were sent.
	if !opt.FillGaps && len(parsed.GapsFilled) > 0 {
		return ComposeResult[T]{}, types.Newf(types.KindInvariantViolation, "compose",
			"the model reported filling %d gap(s) although FillGaps is off", len(parsed.GapsFilled))
	}
	for _, index := range parsed.UnusedParts {
		if index < 0 || index >= len(parts) {
			return ComposeResult[T]{}, types.Newf(types.KindInvariantViolation, "compose",
				"unused_parts names a part that was not sent (%d parts were sent)", len(parts))
		}
	}
	for _, source := range parsed.FieldSources {
		for _, index := range source.Sources {
			if index < 0 || index >= len(parts) {
				return ComposeResult[T]{}, types.Newf(types.KindInvariantViolation, "compose",
					"field_sources names a part that was not sent (%d parts were sent)", len(parts))
			}
		}
	}
	if err := onScale("compose", "the completeness score", parsed.Completeness); err != nil {
		return ComposeResult[T]{}, err
	}

	result := ComposeResult[T]{
		Composed:          composed,
		FieldSources:      parsed.FieldSources,
		ConflictsResolved: parsed.ConflictsResolved,
		GapsFilled:        parsed.GapsFilled,
		UnusedParts:       parsed.UnusedParts,
		Completeness:      parsed.Completeness,
		Metadata:          make(map[string]any),
	}

	log.Debug("Compose operation succeeded",
		"field_sources", len(result.FieldSources),
		"conflicts_resolved", result.ConflictsResolved,
		"completeness", result.Completeness)

	return result, nil
}

// mergeComposeOptions merges user options with defaults
func mergeComposeOptions(defaults, user ComposeOptions) ComposeOptions {
	if user.Template != "" {
		defaults.Template = user.Template
	}
	if user.MergeStrategy != "" {
		defaults.MergeStrategy = user.MergeStrategy
	}
	defaults.FillGaps = user.FillGaps
	defaults.Validate = user.Validate
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
