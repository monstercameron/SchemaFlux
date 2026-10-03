package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/types"
)

// DiffResult contains the results of a difference analysis
type DiffResult struct {
	Added    []string     `json:"added"`    // New fields/values that appeared
	Removed  []string     `json:"removed"`  // Fields/values that were removed
	Modified []DiffChange `json:"modified"` // Fields that changed with details
	Summary  string       `json:"summary"`  // LLM-generated explanation of changes

	// SummaryError reports why Summary is absent. The structural comparison is
	// computed locally and is still correct when the model cannot be reached,
	// so Diff succeeds -- but the caller has to be able to tell an explanation
	// that was written from one that was skipped. This used to be smuggled into
	// Summary as the prose "Summary generation failed, but changes detected
	// successfully", which reads like an explanation.
	SummaryError error `json:"-"`
}

// DiffChange represents a single field modification
type DiffChange struct {
	Field      string `json:"field"`       // Field name that changed
	OldValue   any    `json:"old_value"`   // Previous value
	NewValue   any    `json:"new_value"`   // New value
	ChangeType string `json:"change_type"` // "modified", "type_changed", "structure_changed"
}

// DiffOptions configures the Diff operation
type DiffOptions struct {
	types.OpOptions
	// Background is prose about the data, not a context.Context. The name was
	// Context, which collided with the embedded types.OpOptions.Context.
	Background   string
	IgnoreFields []string // Fields to skip in comparison

	// DeepCompare decides how a field that holds a struct is reported when the
	// two structs differ. Either way the difference is reported, never skipped.
	// With it on (the default) the field is reported as "structure_changed",
	// which says the change is somewhere inside a nested structure; with it off
	// the nested struct is compared as one value and reported as "modified".
	// Neither setting names the field inside the nested struct that changed.
	DeepCompare bool
}

// NewDiffOptions creates DiffOptions with defaults
func NewDiffOptions() DiffOptions {
	return DiffOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		DeepCompare: true,
	}
}

// WithBackground sets prose about the data. Not a context.Context.
func (opts DiffOptions) WithBackground(background string) DiffOptions {
	opts.Background = background
	return opts
}

// WithIgnoreFields sets fields to skip in comparison
func (opts DiffOptions) WithIgnoreFields(fields []string) DiffOptions {
	opts.IgnoreFields = fields
	return opts
}

// WithDeepCompare chooses how a changed nested struct is reported:
// "structure_changed" when true, "modified" when false. See DiffOptions.DeepCompare.
func (opts DiffOptions) WithDeepCompare(deep bool) DiffOptions {
	opts.DeepCompare = deep
	return opts
}

// WithIntelligence sets the intelligence level
func (opts DiffOptions) WithIntelligence(intelligence types.Speed) DiffOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate validates DiffOptions
func (opts DiffOptions) Validate() error {
	return nil // No specific validation needed
}

// toOpOptions converts DiffOptions to types.OpOptions
func (opts DiffOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Diff intelligently compares two data instances and explains differences.
//
// Only exported fields are compared. A field that is unexported cannot be read
// through reflection, so it is skipped, and DiffResult has no place to list it:
// when the two values differ only in such fields, the Summary says that no
// change was found in the fields Diff can read.
//
// Examples:
//
//	// Compare customer records
//	result, err := Diff[Customer](oldCustomer, newCustomer,
//	    NewDiffOptions().WithBackground("Customer management system"))
//
//	// Compare with ignored fields
//	result, err := Diff[Product](oldProduct, newProduct,
//	    NewDiffOptions().WithIgnoreFields([]string{"last_updated"}))
func Diff[T any](oldData, newData T, opts DiffOptions) (DiffResult, error) {
	return diffImpl(oldData, newData, opts)
}

func diffImpl[T any](oldData, newData T, opts DiffOptions) (DiffResult, error) {
	log := loggerFor(opts.OpOptions.Context)
	log.Debug("Starting diff operation", "requestID", opts.RequestID)

	result := DiffResult{}

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Diff operation validation failed", "requestID", opts.RequestID, "error", err)
		return result, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Early exit for identical data (optimization)
	if reflect.DeepEqual(oldData, newData) {
		result.Summary = "No changes detected - data is identical"
		log.Debug("Diff operation completed: no changes", "requestID", opts.RequestID)
		return result, nil
	}

	// Perform structural comparison
	changes, err := compareData(oldData, newData, opts)
	if err != nil {
		log.Error("Diff operation comparison failed", "requestID", opts.RequestID, "error", err)
		return result, fmt.Errorf("comparison failed: %w", err)
	}

	result.Added = changes.Added
	result.Removed = changes.Removed
	result.Modified = changes.Modified

	// Generate intelligent summary if intelligence level requires it and there are changes
	//
	// The summary is written from the changes in fields that are written as
	// JSON. A field the type tags `json:"-"` is compared and reported on the
	// result, which stays with the caller, and is not sent to a provider.
	hasChanges := len(result.Added) > 0 || len(result.Removed) > 0 || len(result.Modified) > 0
	sendable := diffWithoutUnsentFields(changes, oldData, newData)
	hasSendable := len(sendable.Added) > 0 || len(sendable.Removed) > 0 || len(sendable.Modified) > 0
	if (opts.Intelligence != types.Fast || len(sendable.Modified) > 0) && hasSendable {
		summary, err := generateDiffSummary(oldData, newData, sendable, opts)
		if err != nil {
			// The structural diff above is the answer and it is already
			// computed; the summary is a garnish. Report its absence rather
			// than failing the operation or inventing prose for it.
			result.SummaryError = err
			log.Warn("Diff operation summary generation failed", "requestID", opts.RequestID, "error", err)
		} else {
			result.Summary = summary
		}
	} else if !hasChanges {
		if changes.unreadable {
			result.Summary = "No changes detected in the exported fields; the instances differ only in fields Diff cannot read"
		} else {
			result.Summary = "No changes detected between the data instances"
		}
	} else if !hasSendable && (opts.Intelligence != types.Fast || len(result.Modified) > 0) {
		// A summary was due and every change is in a field the type does not write
		// as JSON, so there is nothing that may be sent to write it from.
		result.Summary = "No summary was written: every change is in a field the type does not write as JSON, and such a field is not sent to a provider"
	}

	log.Debug("Diff operation succeeded", "requestID", opts.RequestID, "added", len(result.Added), "removed", len(result.Removed), "modified", len(result.Modified))

	return result, nil
}

// comparisonResult holds intermediate comparison data
type comparisonResult struct {
	Added    []string
	Removed  []string
	Modified []DiffChange

	// unreadable is set when a field was skipped because it is unexported.
	unreadable bool
}

// diffWithoutUnsentFields is the changes less the ones in a field that the
// type leaves out of JSON, `json:"-"`. The comparison names a field by its Go
// name and carries its values, so a change of that kind sent to the summary
// would send a field the type says is not serialised, by name and by value. A
// field is left out if either side's type leaves it out.
func diffWithoutUnsentFields(changes comparisonResult, oldData, newData any) comparisonResult {
	unsent := map[string]bool{}
	for _, data := range []any{oldData, newData} {
		value, isNil := diffUnwrap(reflect.ValueOf(data))
		if isNil || value.Kind() != reflect.Struct {
			continue
		}
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if role, _ := jsonFieldRole(field); role == jsonLeftOut {
				unsent[field.Name] = true
			}
		}
	}
	if len(unsent) == 0 {
		return changes
	}

	kept := comparisonResult{unreadable: changes.unreadable}
	for _, name := range changes.Added {
		if !unsent[name] {
			kept.Added = append(kept.Added, name)
		}
	}
	for _, name := range changes.Removed {
		if !unsent[name] {
			kept.Removed = append(kept.Removed, name)
		}
	}
	for _, change := range changes.Modified {
		if !unsent[change.Field] {
			kept.Modified = append(kept.Modified, change)
		}
	}
	return kept
}

// diffUnwrap follows pointers down to the value they point at. It reports
// whether it met a nil pointer on the way, in which case the returned value is
// that nil pointer.
func diffUnwrap(value reflect.Value) (reflect.Value, bool) {
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return value, true
		}
		value = value.Elem()
	}
	return value, false
}

// compareData performs the actual data comparison using reflection.
//
// Only exported fields are compared: Value.Interface panics on a value reached
// through an unexported field, so every field is checked before it is read, and
// a skipped one is recorded in the result's unreadable flag.
func compareData(oldData, newData any, opts DiffOptions) (comparisonResult, error) {
	result := comparisonResult{}

	// Either side may be a pointer and the other a plain struct (T is `any`),
	// so each is unwrapped on its own. Asking the second one IsNil when only
	// the first had been checked for being a pointer panicked on a struct.
	oldValue, oldNil := diffUnwrap(reflect.ValueOf(oldData))
	newValue, newNil := diffUnwrap(reflect.ValueOf(newData))
	if oldNil && newNil {
		return result, nil
	}
	if oldNil || newNil {
		return result, types.Classified(types.KindConfiguration, errors.New("cannot compare nil and non-nil pointers"))
	}

	// Ensure both are structs
	if oldValue.Kind() != reflect.Struct || newValue.Kind() != reflect.Struct {
		return result, types.Classified(types.KindConfiguration, errors.New("diff operation requires struct types"))
	}

	oldType := oldValue.Type()
	newType := newValue.Type()

	// Create ignored fields map for O(1) lookup (optimization)
	ignoredFields := make(map[string]bool, len(opts.IgnoreFields))
	for _, field := range opts.IgnoreFields {
		ignoredFields[field] = true
	}

	// Pre-allocate slices with estimated capacity (optimization)
	numFields := oldValue.NumField()
	if newValue.NumField() > numFields {
		numFields = newValue.NumField()
	}
	result.Added = make([]string, 0, numFields/4)        // Estimate 25% added
	result.Removed = make([]string, 0, numFields/4)      // Estimate 25% removed
	result.Modified = make([]DiffChange, 0, numFields/2) // Estimate 50% modified

	// Compare each field in old struct
	for i := 0; i < oldValue.NumField(); i++ {
		field := oldType.Field(i)
		fieldName := field.Name

		// A field that cannot be read cannot be compared.
		if !field.IsExported() {
			result.unreadable = true
			continue
		}

		// Skip ignored fields (O(1) lookup)
		if ignoredFields[fieldName] {
			continue
		}

		// Only a field the new struct declares itself counts as present there.
		// FieldByName on the Value also finds fields promoted through an
		// embedded pointer, and panics when that pointer is nil.
		newField, present := newType.FieldByName(fieldName)
		if !present || len(newField.Index) != 1 {
			// Field removed in new struct
			result.Removed = append(result.Removed, fieldName)
			continue
		}

		// Compare field values
		change := compareField(fieldName, oldValue.Field(i), newValue.Field(newField.Index[0]), opts.DeepCompare)
		if change != nil {
			result.Modified = append(result.Modified, *change)
		}
	}

	// Check for added fields in new struct
	for i := 0; i < newValue.NumField(); i++ {
		field := newType.Field(i)
		fieldName := field.Name

		if !field.IsExported() {
			result.unreadable = true
			continue
		}

		// Skip ignored fields (O(1) lookup)
		if ignoredFields[fieldName] {
			continue
		}

		// Check if field exists in old struct
		if oldField, present := oldType.FieldByName(fieldName); !present || len(oldField.Index) != 1 {
			result.Added = append(result.Added, fieldName)
		}
	}

	return result, nil
}

// compareField compares individual field values
func compareField(fieldName string, oldValue, newValue reflect.Value, deepCompare bool) *DiffChange {
	// A value reached through an unexported field cannot be turned into an
	// interface. compareData does not pass one, and this keeps a caller that
	// does from panicking.
	if (oldValue.IsValid() && !oldValue.CanInterface()) || (newValue.IsValid() && !newValue.CanInterface()) {
		return nil
	}

	// Handle nil values
	if !oldValue.IsValid() && !newValue.IsValid() {
		return nil
	}
	if !oldValue.IsValid() || !newValue.IsValid() {
		return &DiffChange{
			Field:      fieldName,
			OldValue:   nil,
			NewValue:   nil,
			ChangeType: "structure_changed",
		}
	}

	// Handle pointer types
	if oldValue.Kind() == reflect.Ptr && newValue.Kind() == reflect.Ptr {
		if oldValue.IsNil() && newValue.IsNil() {
			return nil
		}
		if oldValue.IsNil() || newValue.IsNil() {
			return &DiffChange{
				Field:      fieldName,
				OldValue:   formatValue(oldValue),
				NewValue:   formatValue(newValue),
				ChangeType: "modified",
			}
		}
		oldValue = oldValue.Elem()
		newValue = newValue.Elem()
	}

	// Handle different types
	if oldValue.Kind() != newValue.Kind() {
		return &DiffChange{
			Field:      fieldName,
			OldValue:   formatValue(oldValue),
			NewValue:   formatValue(newValue),
			ChangeType: "type_changed",
		}
	}

	// Compare based on type
	switch oldValue.Kind() {
	case reflect.Struct:
		// A nested struct that differs is a change whatever DeepCompare says;
		// the option only chooses the label. With it off this case returned
		// nothing, so Diff reported "No changes" for two values that
		// reflect.DeepEqual had already shown to differ.
		if !reflect.DeepEqual(oldValue.Interface(), newValue.Interface()) {
			changeType := "modified"
			if deepCompare {
				changeType = "structure_changed"
			}
			return &DiffChange{
				Field:      fieldName,
				OldValue:   formatValue(oldValue),
				NewValue:   formatValue(newValue),
				ChangeType: changeType,
			}
		}
	case reflect.Slice, reflect.Array:
		if !reflect.DeepEqual(oldValue.Interface(), newValue.Interface()) {
			return &DiffChange{
				Field:      fieldName,
				OldValue:   formatValue(oldValue),
				NewValue:   formatValue(newValue),
				ChangeType: "modified",
			}
		}
	case reflect.Map:
		if !reflect.DeepEqual(oldValue.Interface(), newValue.Interface()) {
			return &DiffChange{
				Field:      fieldName,
				OldValue:   formatValue(oldValue),
				NewValue:   formatValue(newValue),
				ChangeType: "modified",
			}
		}
	default:
		// Primitive types: direct comparison
		if !reflect.DeepEqual(oldValue.Interface(), newValue.Interface()) {
			return &DiffChange{
				Field:      fieldName,
				OldValue:   oldValue.Interface(),
				NewValue:   newValue.Interface(),
				ChangeType: "modified",
			}
		}
	}

	return nil
}

// formatValue formats a reflect.Value for display
func formatValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	return v.Interface()
}

// generateDiffSummary uses LLM to create an intelligent summary of changes
func generateDiffSummary(oldData, newData any, changes comparisonResult, opts DiffOptions) (string, error) {
	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	// Marshal data for prompt (only when needed)
	oldJSON, err := json.MarshalIndent(oldData, "", "  ")
	if err != nil {
		return "", types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal old data: %w", err))
	}

	newJSON, err := json.MarshalIndent(newData, "", "  ")
	if err != nil {
		return "", types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal new data: %w", err))
	}

	changesJSON, err := json.MarshalIndent(changes, "", "  ")
	if err != nil {
		return "", types.Classified(types.KindInvalidRequest, fmt.Errorf("failed to marshal changes: %w", err))
	}

	// Build system prompt
	systemPrompt := `You are an expert data analyst specializing in change detection and impact analysis.
Given two versions of data and the detected changes, provide a concise but insightful summary explaining:
1. What the changes represent in practical terms
2. Any patterns or significance of the changes
3. Potential implications or business impact
Keep the summary under 200 words and focus on actionable insights.`

	// Build user prompt using strings.Builder for efficiency
	var promptBuilder strings.Builder
	promptBuilder.Grow(1024) // Pre-allocate capacity
	promptBuilder.WriteString("Analyze these data changes:\n\nOLD DATA:\n")
	promptBuilder.Write(oldJSON)
	promptBuilder.WriteString("\n\nNEW DATA:\n")
	promptBuilder.Write(newJSON)
	promptBuilder.WriteString("\n\nDETECTED CHANGES:\n")
	promptBuilder.Write(changesJSON)

	if opts.Background != "" {
		// The caller's text, quoted so that it cannot read as an instruction.
		quoted, _ := promptJSON(opts.Background)
		promptBuilder.WriteString("\n\nCONTEXT: ")
		promptBuilder.Write(quoted)
	}

	userPrompt := promptBuilder.String()

	// Call LLM for summary
	opt := opts.toOpOptions()

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		return "", fmt.Errorf("summary generation failed: %w", err)
	}

	// The summary is free text, so there is no shape to check -- but an empty
	// body is not a summary, and reporting it as one leaves the caller unable
	// to tell "nothing to say" from "nothing came back".
	summary := strings.TrimSpace(response)
	if summary == "" {
		return "", types.Classified(types.KindMalformedOutput, errors.New("summary generation returned an empty body"))
	}

	return summary, nil
}
