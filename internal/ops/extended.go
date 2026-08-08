// package ops - Extended operations for data validation, formatting, and analysis
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

// ValidateOptions configures the Validate operation
type ValidateOptions struct {
	CommonOptions
	types.OpOptions

	// Rules as natural language validation requirements
	Rules string

	// Schema hints for field-level validation
	SchemaHints map[string]string

	// Enable auto-correction suggestions
	AutoCorrect bool

	// Severity threshold to fail validation ("error", "warning", "info")
	FailOn string

	// Custom validators as field -> rule mappings
	FieldRules map[string]string

	// Include detailed explanations
	IncludeExplanations bool
}

// NewValidateOptions creates ValidateOptions with defaults
func NewValidateOptions() ValidateOptions {
	return ValidateOptions{
		CommonOptions: CommonOptions{
			Mode:         types.Strict,
			Intelligence: types.Fast,
		},
		AutoCorrect:         true,
		FailOn:              "error",
		IncludeExplanations: true,
	}
}

// Validate validates ValidateOptions
func (v ValidateOptions) Validate() error {
	if err := v.CommonOptions.Validate(); err != nil {
		return err
	}
	validFailOn := map[string]bool{"error": true, "warning": true, "info": true}
	if v.FailOn != "" && !validFailOn[v.FailOn] {
		return fmt.Errorf("invalid failOn: %s", v.FailOn)
	}
	return nil
}

// WithRules sets validation rules
func (v ValidateOptions) WithRules(rules string) ValidateOptions {
	v.Rules = rules
	return v
}

// WithSchemaHints sets schema hints
func (v ValidateOptions) WithSchemaHints(hints map[string]string) ValidateOptions {
	v.SchemaHints = hints
	return v
}

// WithAutoCorrect enables auto-correction
func (v ValidateOptions) WithAutoCorrect(autoCorrect bool) ValidateOptions {
	v.AutoCorrect = autoCorrect
	return v
}

// WithFailOn sets the severity threshold
func (v ValidateOptions) WithFailOn(failOn string) ValidateOptions {
	v.FailOn = failOn
	return v
}

// WithFieldRules sets field-specific validation rules
func (v ValidateOptions) WithFieldRules(rules map[string]string) ValidateOptions {
	v.FieldRules = rules
	return v
}

// WithIncludeExplanations enables detailed explanations
func (v ValidateOptions) WithIncludeExplanations(include bool) ValidateOptions {
	v.IncludeExplanations = include
	return v
}

// WithSteering sets the steering prompt
func (v ValidateOptions) WithSteering(steering string) ValidateOptions {
	v.CommonOptions = v.CommonOptions.WithSteering(steering)
	return v
}

// WithMode sets the mode
func (v ValidateOptions) WithMode(mode types.Mode) ValidateOptions {
	v.CommonOptions = v.CommonOptions.WithMode(mode)
	return v
}

// WithIntelligence sets the intelligence level
func (v ValidateOptions) WithIntelligence(intelligence types.Speed) ValidateOptions {
	v.CommonOptions = v.CommonOptions.WithIntelligence(intelligence)
	return v
}

func (v ValidateOptions) toOpOptions() types.OpOptions {
	return v.CommonOptions.toOpOptions()
}

// ValidationIssue represents a single validation problem
type ValidationIssue struct {
	Field       string `json:"field,omitempty"`
	Severity    string `json:"severity"` // "error", "warning", "info"
	Message     string `json:"message"`
	Suggestion  string `json:"suggestion,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

// ValidationResult contains the results of a validation operation (legacy)
type ValidationResult struct {
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues"`
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64  `json:"confidence"`
	Suggestions     []string `json:"suggestions"`
}

// ValidateResult contains the full results of validation.
// Type parameter T specifies the type being validated.
type ValidateResult[T any] struct {
	// Valid indicates if the data passes all validation rules
	Valid bool `json:"valid"`

	// Errors are critical issues that must be fixed
	Errors []ValidationIssue `json:"errors,omitempty"`

	// Warnings are issues that should be addressed
	Warnings []ValidationIssue `json:"warnings,omitempty"`

	// Info are minor suggestions
	Info []ValidationIssue `json:"info,omitempty"`

	// Corrected is the auto-corrected version of the input (if AutoCorrect enabled)
	Corrected *T `json:"corrected,omitempty"`

	// ModelConfidence score for the validation assessment
	ModelConfidence float64 `json:"confidence"`

	// Summary provides an overall assessment
	Summary string `json:"summary,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Deprecated: use ValidateHybrid, which returns the shared
// types.JudgmentResult instead of this operation's own ValidateResult, or
// ValidateDeterministically when every rule is a Go-decidable field rule and
// no provider call should ever happen. Validate's old name did not say
// which of those two it was -- a caller reading "Validate(x, opts)" could
// not tell, without reading FieldRules and Rules, whether this was about to
// make a network call. See OP-206 (ARC-22, TRU-30): a model-assisted review
// must not be spelled the same as a deterministic check.
//
// Validate checks if data meets specified criteria using LLM interpretation.
//
// Type parameter T specifies the type being validated.
//
// Examples:
//
//	// Basic validation with rules
//	result, err := Validate[Person](person, NewValidateOptions().
//	    WithRules("age must be 18-100, email must be valid"))
//	if !result.Valid {
//	    for _, err := range result.Errors {
//	        fmt.Printf("Error in %s: %s\n", err.Field, err.Message)
//	    }
//	}
//
//	// Validation with auto-correction
//	result, err := Validate[Address](address, NewValidateOptions().
//	    WithRules("valid US address format").
//	    WithAutoCorrect(true))
//	if result.Corrected != nil {
//	    address = *result.Corrected
//	}
//
//	// Field-specific validation
//	result, err := Validate[User](user, NewValidateOptions().
//	    WithFieldRules(map[string]string{
//	        "email": "valid email format",
//	        "age": "between 18 and 120",
//	        "password": "at least 8 characters, one uppercase, one number",
//	    }))
func Validate[T any](data T, opts ValidateOptions) (ValidateResult[T], error) {
	return validateHybridCore(data, opts)
}

// ValidateHybrid checks data against deterministic field rules and, when
// anything remains that Go cannot decide (free-text Rules, SchemaHints, or a
// FieldRules entry applyDeterministicRules does not understand), a model.
// It is the model-assisted twin of ValidateDeterministically and returns the
// shared types.JudgmentResult instead of ValidateResult, so a caller
// handling "the judgment came back bad" can write that once against
// JudgmentResult rather than once per operation's own field names.
//
// Behavior is identical to the deprecated Validate: same deterministic
// rules applied first, same provider-call shortcut when nothing is left for
// a model to decide, same failOn semantics deciding the verdict. Only the
// result shape differs.
func ValidateHybrid[T any](data T, opts ValidateOptions) (types.JudgmentResult[T], error) {
	vr, err := validateHybridCore(data, opts)
	if err != nil {
		return types.JudgmentResult[T]{}, err
	}
	return validateResultToJudgment(data, vr), nil
}

// ValidateDeterministically checks data against FieldRules that Go can
// decide exactly -- "email", "iso3166-alpha2", "min:18", and the rest
// applyDeterministicRules understands -- and makes no provider call, ever.
// Not "no provider call when it turns out nothing needed one": if opts.Rules
// is non-empty, opts.SchemaHints is non-empty, or a FieldRules entry is not
// one applyDeterministicRules recognizes, this returns an error rather than
// silently skipping what it cannot decide or silently sending it to a
// model anyway. A validator that quietly drops a rule it cannot check is
// reporting success on data it never actually validated -- exactly the
// failure AGENTS.md's "never fail open" rule exists to prevent.
//
// Use ValidateHybrid when Rules, SchemaHints, or a judgment-call field rule
// are part of what needs checking.
func ValidateDeterministically[T any](data T, opts ValidateOptions) (types.JudgmentResult[T], error) {
	var zero types.JudgmentResult[T]

	if err := opts.Validate(); err != nil {
		return zero, fmt.Errorf("invalid options: %w", err)
	}

	if strings.TrimSpace(opts.Rules) != "" {
		return zero, fmt.Errorf("validate deterministically: Rules is free text and requires model judgment; use ValidateHybrid")
	}
	if len(opts.SchemaHints) > 0 {
		return zero, fmt.Errorf("validate deterministically: SchemaHints requires model judgment; use ValidateHybrid")
	}

	issues, remaining := applyDeterministicRules(data, opts.FieldRules)
	if len(remaining) > 0 {
		return zero, fmt.Errorf("validate deterministically: field rules for %v are not decidable in Go; use ValidateHybrid",
			sortedKeys(remaining))
	}

	verdict := types.VerdictPass
	if len(issues) > 0 {
		verdict = types.VerdictFail
	}

	var jIssues []types.JudgmentIssue
	for _, issue := range issues {
		jIssues = append(jIssues, types.JudgmentIssue{
			Subject:  issue.Field,
			Severity: normalizeSeverity(issue.Severity),
			Message:  issue.Message,
			Evidence: evidenceSlice(issue.Explanation),
		})
	}

	return types.JudgmentResult[T]{
		Subject: data,
		Verdict: verdict,
		Issues:  jIssues,
		Summary: fmt.Sprintf("checked %d field rules deterministically", len(opts.FieldRules)),
		// ModelConfidence is left at its zero value deliberately: no model
		// was consulted, so there is no claim to report, and reporting 0.0
		// as though it were a low-confidence score would misstate that.
	}, nil
}

// validateResultToJudgment translates ValidateResult[T] -- Validate's own
// bool-plus-three-severities shape -- into the shared types.JudgmentResult.
// Errors, warnings, and info are concatenated in that order because that is
// the order ValidateResult already reported certainty in: deterministic
// findings first, then the model's errors, then its lesser findings.
func validateResultToJudgment[T any](data T, vr ValidateResult[T]) types.JudgmentResult[T] {
	verdict := types.VerdictPass
	if !vr.Valid {
		verdict = types.VerdictFail
	}

	var issues []types.JudgmentIssue
	issues = append(issues, validationIssuesToJudgment(vr.Errors)...)
	issues = append(issues, validationIssuesToJudgment(vr.Warnings)...)
	issues = append(issues, validationIssuesToJudgment(vr.Info)...)

	claims := map[string]any{}
	if vr.Corrected != nil {
		// Corrected is the model's proposed fix. Nothing here verified that
		// applying it actually resolves the issues found, so it is a claim
		// -- not a delivered result -- and lives beside ModelConfidence
		// rather than as a field of its own on JudgmentResult.
		claims["corrected"] = vr.Corrected
	}
	if v, ok := vr.Metadata["deterministic_only"]; ok {
		claims["deterministic_only"] = v
	}
	if len(claims) == 0 {
		claims = nil
	}

	return types.JudgmentResult[T]{
		Subject:         data,
		Verdict:         verdict,
		Issues:          issues,
		Summary:         vr.Summary,
		ModelConfidence: vr.ModelConfidence,
		ModelClaims:     claims,
	}
}

// validationIssuesToJudgment converts one severity bucket of
// ValidationIssue into JudgmentIssue. Severity is read from the issue
// itself (deterministic issues and the model's own issues both set it)
// rather than from which bucket it came from, so a model that mislabels an
// error as a warning is not silently relabelled back by this conversion --
// the disagreement, if any, is the model's, and normalizeSeverity only
// folds the vocabulary, not the model's judgment about severity.
func validationIssuesToJudgment(issues []ValidationIssue) []types.JudgmentIssue {
	if len(issues) == 0 {
		return nil
	}
	out := make([]types.JudgmentIssue, 0, len(issues))
	for _, issue := range issues {
		out = append(out, types.JudgmentIssue{
			Subject:    issue.Field,
			Severity:   normalizeSeverity(issue.Severity),
			Message:    issue.Message,
			Suggestion: issue.Suggestion,
			Evidence:   evidenceSlice(issue.Explanation),
		})
	}
	return out
}

// validateHybridCore is Validate's original implementation, unchanged. Both
// the deprecated Validate and the new ValidateHybrid call this so they
// cannot drift: a difference between what the old name returns and what the
// new name returns would recreate the two-return-types-that-execute-
// differently problem T-01 already names.
func validateHybridCore[T any](data T, opts ValidateOptions) (ValidateResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting validate operation")

	var result ValidateResult[T]
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
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert data to JSON for validation
	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Error("Validate operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal data: %w", err)
	}

	// Decide first what a machine can decide.
	//
	// Every rule in this operation's own documented example -- a valid email, an
	// ISO country code, a minimum age -- is exact in Go and a judgement call in
	// a model. Rules this layer understands are applied here; the rest go to the
	// model, and if nothing is left there is no provider call at all.
	deterministicIssues, remainingFieldRules := applyDeterministicRules(data, opts.FieldRules)

	// Build rules description
	rulesDesc := opts.Rules
	if len(remainingFieldRules) > 0 {
		var fieldRulesStr []string
		for _, field := range sortedKeys(remainingFieldRules) {
			fieldRulesStr = append(fieldRulesStr, fmt.Sprintf("- %s: %s", field, remainingFieldRules[field]))
		}
		if rulesDesc != "" {
			rulesDesc += "\n\nField-specific rules:\n" + strings.Join(fieldRulesStr, "\n")
		} else {
			rulesDesc = "Field-specific rules:\n" + strings.Join(fieldRulesStr, "\n")
		}
	}

	if len(opts.SchemaHints) > 0 {
		var hintsStr []string
		for _, field := range sortedKeys(opts.SchemaHints) {
			hintsStr = append(hintsStr, fmt.Sprintf("- %s: %s", field, opts.SchemaHints[field]))
		}
		rulesDesc += "\n\nSchema hints:\n" + strings.Join(hintsStr, "\n")
	}

	correctionNote := ""
	if opts.AutoCorrect {
		correctionNote = `
If any issues are found and correction is possible, include a "corrected" field with the fixed version of the input data.`
	}

	explanationNote := ""
	if opts.IncludeExplanations {
		explanationNote = "\nInclude explanations for each issue."
	}

	systemPrompt := fmt.Sprintf(`You are a data validation expert. Validate the provided data against the given rules.%s%s

Severity levels:
- "error": Critical issues that must be fixed
- "warning": Issues that should be addressed
- "info": Minor suggestions for improvement

Return a JSON object with:
{
  "valid": boolean (true if no errors),
  "errors": [{"field": "fieldName", "severity": "error", "message": "Issue description", "suggestion": "How to fix", "explanation": "Why this is wrong"}],
  "warnings": [{"field": "fieldName", "severity": "warning", "message": "Issue", "suggestion": "How to fix"}],
  "info": [{"severity": "info", "message": "Suggestion"}],
  "corrected": { corrected data if applicable },
  "confidence": 0.0-1.0,
  "summary": "Overall assessment"
}`, correctionNote, explanationNote)

	userPrompt := fmt.Sprintf(`Validate this data:
%s

Against these rules:
%s`, string(dataJSON), rulesDesc)

	// Nothing left for the model: every rule was decided exactly, so the answer
	// is already complete and a provider call would only add latency, cost, and
	// a chance of disagreeing with arithmetic.
	if len(remainingFieldRules) == 0 && strings.TrimSpace(opts.Rules) == "" && len(opts.SchemaHints) == 0 {
		result.Errors = deterministicIssues
		result.Valid = len(deterministicIssues) == 0
		result.Summary = fmt.Sprintf("checked %d field rules deterministically", len(opts.FieldRules))
		result.Metadata["deterministic_only"] = true
		log.Debug("Validate answered without a provider call",
			"rules", len(opts.FieldRules), "issues", len(deterministicIssues))
		return result, nil
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Validate operation LLM call failed", "error", err)
		return result, fmt.Errorf("validation failed: %w", err)
	}

	// Parse the response into a flexible structure first
	var llmResult struct {
		Valid           bool              `json:"valid"`
		Errors          []ValidationIssue `json:"errors,omitempty"`
		Warnings        []ValidationIssue `json:"warnings,omitempty"`
		Info            []ValidationIssue `json:"info,omitempty"`
		Corrected       json.RawMessage   `json:"corrected,omitempty"`
		ModelConfidence float64           `json:"confidence"`
		Summary         string            `json:"summary,omitempty"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		// A validator must not guess. The previous fallback inferred validity
		// from strings.Contains(response, "valid"), which is also satisfied by
		// the substring inside "invalid" -- so a response saying the data was
		// invalid returned Valid: true with a nil error, and the caller's
		// `if !result.Valid` gate never fired. A parse failure in a validator
		// is a failure, not a verdict.
		log.Error("Validate operation failed: parse error", "error", err)
		return result, fmt.Errorf("validate: could not parse the validation response: %w", err)
	}

	result.Valid = llmResult.Valid
	// The deterministic findings come first, because they are the ones that are
	// certain. A model that missed a malformed email does not get to overrule
	// mail.ParseAddress by omission.
	result.Errors = append(append([]ValidationIssue{}, deterministicIssues...), llmResult.Errors...)
	result.Warnings = llmResult.Warnings
	result.Info = llmResult.Info
	result.ModelConfidence = llmResult.ModelConfidence
	result.Summary = llmResult.Summary

	// Parse corrected data if present. A correction that does not fit T is
	// reported: swallowing the error left Corrected nil, which is
	// indistinguishable from "the model offered no correction", so a caller
	// waiting on AutoCorrect saw nothing and had no idea why.
	if len(llmResult.Corrected) > 0 && string(llmResult.Corrected) != "null" {
		var corrected T
		if err := json.Unmarshal(llmResult.Corrected, &corrected); err != nil {
			log.Error("Validate: the correction did not match the target type", "error", err)
			return result, fmt.Errorf("validate: the model's correction did not match the target type: %w", err)
		}
		result.Corrected = &corrected
	}

	// Validity is derived from the issues, always. It previously came from the
	// model's own `valid` field unless FailOn was set, which gave one documented
	// field two different meanings depending on an option -- and let a response
	// claiming `"valid": true` alongside a populated errors array be reported as
	// valid.
	failOn := opts.FailOn
	if failOn == "" {
		failOn = "error"
	}
	switch failOn {
	case "error":
		result.Valid = len(result.Errors) == 0
	case "warning":
		result.Valid = len(result.Errors) == 0 && len(result.Warnings) == 0
	case "info":
		result.Valid = len(result.Errors) == 0 && len(result.Warnings) == 0 && len(result.Info) == 0
	default:
		// Unreachable by construction: ValidateOptions.Validate runs as this
		// function's first step and already rejects any FailOn outside
		// {"", "error", "warning", "info"}. Kept as defence in depth rather
		// than deleted -- if that check is ever relaxed, this is what stops an
		// unknown severity from silently defaulting to "valid" -- but noted so
		// the next person measuring coverage does not spend an afternoon trying
		// to reach it.
		return result, fmt.Errorf("validate: unknown FailOn severity %q, want error, warning, or info", opts.FailOn)
	}

	log.Debug("Validate operation succeeded", "valid", result.Valid, "errorCount", len(result.Errors), "warningCount", len(result.Warnings))
	return result, nil
}

// Deprecated: use Validate, which returns ValidateResult[T] with typed issues,
// severities, and an optional correction. ValidateLegacy returns
// ValidationResult -- a bool, a []string, and a model-reported confidence --
// which cannot express which field failed or how badly.
//
// It stays because removing an exported function before 1.0 would break the
// smoke runner and anyone who copied from it, and it is marked because until
// now nothing in this repository carried a single deprecation marker: the
// README said the legacy path was compatibility-only and no linter, IDE, or
// staticcheck run would ever tell a user they were on it (F-06, A-09).
//
// ValidateLegacy is the legacy validation function for backward compatibility
func ValidateLegacy[T any](data T, rules string, opts ...types.OpOptions) (ValidationResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting legacy validate operation")

	opt := applyDefaults(opts...)

	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert data to JSON for validation
	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Error("Validate operation failed: marshal error", "error", err)
		return ValidationResult{}, fmt.Errorf("failed to marshal data: %w", err)
	}

	systemPrompt := `You are a data validation expert. Validate the provided data against the given rules.

Return a JSON object with:
{
  "valid": boolean,
  "issues": ["list of validation issues, empty if valid"],
  "confidence": 0.0-1.0,
  "suggestions": ["list of suggestions to fix issues, empty if valid"]
}`

	userPrompt := fmt.Sprintf(`Validate this data:
%s

Against these rules:
%s`, string(dataJSON), rules)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Validate operation LLM call failed", "error", err)
		return ValidationResult{}, fmt.Errorf("validation failed: %w", err)
	}

	var result ValidationResult
	if err := ParseJSONStrict(response, &result); err != nil {
		// The old fallback here was `strings.Contains(lower, "valid")`, which
		// reports Valid on a response saying the data is INVALID -- "invalid"
		// contains "valid". A validation gate that fails open on an unparseable
		// answer is worse than one that errors, because the caller proceeds
		// believing the check ran.
		//
		// There is no honest way to recover a verdict from a body that did not
		// parse, so this refuses instead of guessing. ValidateLegacy is
		// deprecated, but a deprecated function that silently approves bad data
		// is still approving bad data.
		log.Error("ValidateLegacy operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse validation result: %w", err)
	}

	log.Debug("Validate operation succeeded", "valid", result.Valid, "issuesCount", len(result.Issues))
	return result, nil
}

// FormatResult contains the formatted output with metadata
type FormatResult struct {
	// Text is the formatted content
	Text string `json:"text"`

	// FormatApplied describes the format that was applied
	FormatApplied string `json:"format_applied,omitempty"`

	// ModelConfidence score for the formatting quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// TransformationNotes describe how the data was transformed
	TransformationNotes []string `json:"transformation_notes,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Format converts data to a specific output format using LLM interpretation.
// For metadata including transformation notes and confidence, use FormatWithMetadata.
//
// Examples:
//
//	// Format as markdown table
//	formatted, err := Format(data, "markdown table with headers")
//
//	// Format as professional bio
//	bio, err := Format(person, "professional bio in third person")
func Format(data any, template string, opts ...types.OpOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting format operation")

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert data to string representation
	var dataStr string
	switch v := data.(type) {
	case string:
		dataStr = v
	default:
		dataJSON, err := json.Marshal(data)
		if err != nil {
			dataStr = fmt.Sprintf("%v", data)
		} else {
			dataStr = string(dataJSON)
		}
	}

	systemPrompt := `You are a formatting expert. Convert the provided data into the requested format.
Follow the template instructions precisely and produce clean, well-formatted output.`

	userPrompt := fmt.Sprintf(`Format this data:
%s

Into this format:
%s`, dataStr, template)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Format operation LLM call failed", "error", err)
		return "", fmt.Errorf("formatting failed: %w", err)
	}

	log.Debug("Format operation succeeded", "outputLength", len(response))
	return strings.TrimSpace(response), nil
}

// FormatWithMetadata formats data with additional metadata including
// what format was applied, transformation notes, and confidence score.
func FormatWithMetadata(data any, template string, opts ...types.OpOptions) (FormatResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting format with metadata operation")

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert data to string representation
	var dataStr string
	switch v := data.(type) {
	case string:
		dataStr = v
	default:
		dataJSON, err := json.Marshal(data)
		if err != nil {
			dataStr = fmt.Sprintf("%v", data)
		} else {
			dataStr = string(dataJSON)
		}
	}

	systemPrompt := `You are a formatting expert. Convert the provided data into the requested format.

Respond ONLY with valid JSON in this exact format:
{
  "text": "The formatted output here",
  "format_applied": "Description of format used",
  "transformation_notes": ["Note about what was changed", "Another transformation"],
  "confidence": 0.9
}

Rules:
- "text": The complete formatted output
- "format_applied": Brief description of the format that was applied
- "transformation_notes": List of notable transformations made
- "confidence": A value from 0.0 to 1.0 indicating formatting quality`

	userPrompt := fmt.Sprintf(`Format this data:
%s

Into this format:
%s`, dataStr, template)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("FormatWithMetadata operation LLM call failed", "error", err)
		return FormatResult{}, fmt.Errorf("formatting failed: %w", err)
	}

	// Parse JSON response
	var parsed struct {
		Text                string   `json:"text"`
		FormatApplied       string   `json:"format_applied"`
		TransformationNotes []string `json:"transformation_notes"`
		ModelConfidence     float64  `json:"confidence"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		// The fallback returned the raw body as formatted text with an invented
		// confidence of 0.7, so a refusal or an error page became the caller's
		// formatted output.
		log.Error("FormatWithMetadata could not parse the response", "error", err)
		return FormatResult{}, fmt.Errorf("formatting failed: %w", err)
	}

	result := FormatResult{
		Text:                parsed.Text,
		FormatApplied:       parsed.FormatApplied,
		TransformationNotes: parsed.TransformationNotes,
		ModelConfidence:     parsed.ModelConfidence,
	}

	log.Debug("FormatWithMetadata operation succeeded", "outputLength", len(result.Text))
	return result, nil
}

// MergeResult contains the merged data with metadata.
// Type parameter T specifies the merged output type.
type MergeResult[T any] struct {
	// Merged is the combined data
	Merged T `json:"merged"`

	// SourcesUsed indicates which source indices contributed
	SourcesUsed []int `json:"sources_used,omitempty"`

	// Conflicts lists any conflicting fields and how they were resolved
	Conflicts []MergeConflict `json:"conflicts,omitempty"`

	// ModelConfidence score for the merge quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Strategy describes the merge strategy that was applied
	Strategy string `json:"strategy,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// MergeConflict represents a conflict during merge
type MergeConflict struct {
	Field       string `json:"field"`
	Values      []any  `json:"values"`
	Resolution  string `json:"resolution"`
	ChosenValue any    `json:"chosen_value"`
}

// Merge intelligently combines multiple data sources into a single result.
// For metadata including conflicts and strategy details, use MergeWithMetadata.
//
// Examples:
//
//	// Merge customer records preferring newest data
//	merged, err := Merge([]Customer{dbRecord, apiResponse, csvRow}, "prefer newest")
func Merge[T any](sources []T, strategy string, opts ...types.OpOptions) (T, error) {
	log := logger.GetLogger()
	log.Debug("Starting merge operation", "sourcesCount", len(sources))

	var result T

	if len(sources) == 0 {
		log.Error("Merge operation failed: no sources provided")
		return result, fmt.Errorf("no sources to merge")
	}

	if len(sources) == 1 {
		return sources[0], nil
	}

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert sources to JSON
	var sourcesJSON []string
	for i, source := range sources {
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			log.Error("Merge operation failed: marshal error", "sourceIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal source %d: %w", i, err)
		}
		sourcesJSON = append(sourcesJSON, string(sourceJSON))
	}

	// Get type information
	typeInfo := GenerateTypeSchema(reflect.TypeOf(result))

	systemPrompt := fmt.Sprintf(`You are a data merging expert. Merge multiple data sources into a single result.
Follow the merging strategy and produce a result matching this schema:
%s

Return only the merged JSON object.`, typeInfo)

	userPrompt := fmt.Sprintf(`Merge these sources:
%s

Using strategy: %s`, strings.Join(sourcesJSON, "\n"), strategy)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Merge operation LLM call failed", "error", err)
		return result, fmt.Errorf("merge failed: %w", err)
	}

	// Parse the merged result
	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("Merge operation failed: unmarshal error", "error", err)
		return result, fmt.Errorf("failed to parse merged result: %w", err)
	}

	log.Debug("Merge operation succeeded")
	return result, nil
}

// MergeWithMetadata combines multiple data sources with additional metadata
// including which sources were used, any conflicts found, and how they were resolved.
func MergeWithMetadata[T any](sources []T, strategy string, opts ...types.OpOptions) (MergeResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting merge with metadata operation", "sourcesCount", len(sources))

	var result MergeResult[T]
	result.Strategy = strategy
	result.Metadata = make(map[string]any)

	if len(sources) == 0 {
		log.Error("MergeWithMetadata operation failed: no sources provided")
		return result, fmt.Errorf("no sources to merge")
	}

	if len(sources) == 1 {
		result.Merged = sources[0]
		result.SourcesUsed = []int{0}
		result.ModelConfidence = 1.0
		return result, nil
	}

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert sources to JSON
	var sourcesJSON []string
	for i, source := range sources {
		sourceJSON, err := json.Marshal(source)
		if err != nil {
			log.Error("MergeWithMetadata operation failed: marshal error", "sourceIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal source %d: %w", i, err)
		}
		sourcesJSON = append(sourcesJSON, fmt.Sprintf("Source %d: %s", i, string(sourceJSON)))
	}

	// Get type information
	var zero T
	typeInfo := GenerateTypeSchema(reflect.TypeOf(zero))

	systemPrompt := fmt.Sprintf(`You are a data merging expert. Merge multiple data sources into a single result.

Respond ONLY with valid JSON in this exact format:
{
  "merged": { the merged data matching this schema: %s },
  "sources_used": [0, 1, 2],
  "conflicts": [
    {"field": "fieldName", "values": ["val1", "val2"], "resolution": "used newest", "chosen_value": "val1"}
  ],
  "confidence": 0.9
}

Rules:
- "merged": The complete merged result
- "sources_used": Array of source indices that contributed to the merge
- "conflicts": Array of conflicts encountered and how they were resolved (empty if none)
- "confidence": A value from 0.0 to 1.0 indicating merge quality`, typeInfo)

	userPrompt := fmt.Sprintf(`Merge these sources:
%s

Using strategy: %s`, strings.Join(sourcesJSON, "\n"), strategy)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("MergeWithMetadata operation LLM call failed", "error", err)
		return result, fmt.Errorf("merge failed: %w", err)
	}

	// Parse JSON response
	var parsed struct {
		Merged          json.RawMessage `json:"merged"`
		SourcesUsed     []int           `json:"sources_used"`
		Conflicts       []MergeConflict `json:"conflicts"`
		ModelConfidence float64         `json:"confidence"`
	}
	if err := ParseJSONStrict(response, &parsed); err != nil {
		// Fallback: try to parse as just the merged object
		log.Debug("MergeWithMetadata JSON parse failed, trying fallback")
		var merged T
		if err := ParseJSONStrict(response, &merged); err != nil {
			log.Error("MergeWithMetadata operation failed: unmarshal error", "error", err)
			return result, fmt.Errorf("failed to parse merged result: %w", err)
		}
		result.Merged = merged

		// ModelConfidence stays at zero, and that is the fix rather than an
		// omission. It was set to 0.7 here -- a number no model produced,
		// written into a field whose whole contract is "this is the MODEL's
		// claim about its own answer". A caller filtering on
		// ModelConfidence >= 0.6 would have accepted every fallback parse on
		// the strength of a constant invented in this function.
		//
		// The model did not report a confidence on this path, because the
		// envelope carrying it is exactly what failed to parse. Zero means it
		// said nothing, which is true.

		// SourcesUsed is likewise left empty. It used to be filled with every
		// index on the assumption that all sources were used; the response that
		// would have said so is the one that did not parse, so the assumption
		// was the same invention in a different field.
		return result, nil
	}

	// Parse the merged data
	if len(parsed.Merged) > 0 {
		if err := json.Unmarshal(parsed.Merged, &result.Merged); err != nil {
			log.Error("MergeWithMetadata operation failed: merged data parse error", "error", err)
			return result, fmt.Errorf("failed to parse merged data: %w", err)
		}
	}

	result.SourcesUsed = parsed.SourcesUsed
	result.Conflicts = parsed.Conflicts
	result.ModelConfidence = parsed.ModelConfidence

	log.Debug("MergeWithMetadata operation succeeded", "sourcesUsed", len(result.SourcesUsed), "conflicts", len(result.Conflicts))
	return result, nil
}

// QuestionOptions configures the Question operation
type QuestionOptions struct {
	CommonOptions
	types.OpOptions

	// Question to ask about the data
	Question string

	// IncludeEvidence includes supporting evidence from the data
	IncludeEvidence bool

	// IncludeConfidence includes confidence score in the result
	IncludeConfidence bool

	// IncludeReasoning includes reasoning chain
	IncludeReasoning bool
}

// NewQuestionOptions creates QuestionOptions with defaults
func NewQuestionOptions(question string) QuestionOptions {
	return QuestionOptions{
		CommonOptions: CommonOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		Question:          question,
		IncludeEvidence:   true,
		IncludeConfidence: true,
		IncludeReasoning:  true,
	}
}

// Validate validates QuestionOptions
func (q QuestionOptions) Validate() error {
	if err := q.CommonOptions.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(q.Question) == "" {
		return fmt.Errorf("question cannot be empty")
	}
	return nil
}

// WithQuestion sets the question
func (q QuestionOptions) WithQuestion(question string) QuestionOptions {
	q.Question = question
	return q
}

// WithIncludeEvidence enables evidence inclusion
func (q QuestionOptions) WithIncludeEvidence(include bool) QuestionOptions {
	q.IncludeEvidence = include
	return q
}

// WithIncludeConfidence enables confidence inclusion
func (q QuestionOptions) WithIncludeConfidence(include bool) QuestionOptions {
	q.IncludeConfidence = include
	return q
}

// WithIncludeReasoning enables reasoning inclusion
func (q QuestionOptions) WithIncludeReasoning(include bool) QuestionOptions {
	q.IncludeReasoning = include
	return q
}

// WithSteering sets the steering prompt
func (q QuestionOptions) WithSteering(steering string) QuestionOptions {
	q.CommonOptions = q.CommonOptions.WithSteering(steering)
	return q
}

// WithMode sets the mode
func (q QuestionOptions) WithMode(mode types.Mode) QuestionOptions {
	q.CommonOptions = q.CommonOptions.WithMode(mode)
	return q
}

// WithIntelligence sets the intelligence level
func (q QuestionOptions) WithIntelligence(intelligence types.Speed) QuestionOptions {
	q.CommonOptions = q.CommonOptions.WithIntelligence(intelligence)
	return q
}

func (q QuestionOptions) toOpOptions() types.OpOptions {
	return q.CommonOptions.toOpOptions()
}

// QuestionResult contains the answer and supporting information.
// Type parameter A specifies the expected answer type.
type QuestionResult[A any] struct {
	// Answer is the typed answer to the question
	Answer A `json:"answer"`

	// ModelConfidence score for the answer (0.0-1.0)
	ModelConfidence float64 `json:"confidence,omitempty"`

	// Reasoning explains how the answer was derived
	Reasoning string `json:"reasoning,omitempty"`

	// Evidence contains supporting data from the input
	Evidence []string `json:"evidence,omitempty"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Question answers questions about data and returns a typed answer.
//
// It is one model round trip: data and question go in, one answer comes
// back. "Asking" (the fluent builder over this operation) is not a
// conversation the way the word suggests -- there is no follow-up question
// inside a single call, and no memory of a prior Question call carried into
// the next one. A caller that wants a real multi-turn exchange assembles it
// itself, one call per turn; Session in session.go is the transcript
// primitive for that against llm.Provider directly (PS-005, Revised
// ARC-20).
//
// Type parameters:
//   - T: Input data type
//   - A: Answer type (can be string, struct, slice, etc.)
//
// Examples:
//
//	// Simple string answer
//	result, err := Question[Report, string](report, NewQuestionOptions("What is the main finding?"))
//	fmt.Println(result.Answer, "confidence:", result.ModelConfidence)
//
//	// Typed answer
//	type TopRisks struct {
//	    Risks []struct {
//	        Name     string  `json:"name"`
//	        Severity string  `json:"severity"`
//	        Score    float64 `json:"score"`
//	    } `json:"risks"`
//	}
//	result, err := Question[Report, TopRisks](report, NewQuestionOptions("What are the top 3 risks?"))
//	for _, risk := range result.Answer.Risks {
//	    fmt.Printf("Risk: %s (%s, %.1f)\n", risk.Name, risk.Severity, risk.Score)
//	}
//
//	// Boolean answer
//	result, err := Question[Document, bool](doc, NewQuestionOptions("Does this document contain PII?"))
//	if result.Answer {
//	    fmt.Println("PII detected with confidence:", result.ModelConfidence)
//	}
func Question[T any, A any](data T, opts QuestionOptions) (QuestionResult[A], error) {
	log := logger.GetLogger()
	log.Debug("Starting question operation")

	var result QuestionResult[A]
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
	ctx, cancel = operationContext(ctx, 0)
	defer cancel()

	// Convert data to string representation
	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Error("Question operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal data: %w", err)
	}

	// Get answer type schema
	var answerZero A
	answerSchema := GenerateTypeSchema(reflect.TypeOf(answerZero))

	// Build response format requirements
	var formatParts []string
	formatParts = append(formatParts, fmt.Sprintf(`"answer": (your answer matching this schema: %s)`, answerSchema))
	if opts.IncludeConfidence {
		formatParts = append(formatParts, `"confidence": 0.0-1.0`)
	}
	if opts.IncludeReasoning {
		formatParts = append(formatParts, `"reasoning": "explanation of how you derived the answer"`)
	}
	if opts.IncludeEvidence {
		formatParts = append(formatParts, `"evidence": ["supporting quotes or facts from the data"]`)
	}

	systemPrompt := fmt.Sprintf(`You are a data analysis expert. Answer questions about the provided data accurately and concisely.
Base your answers only on the information provided.

Return a JSON object with:
{
  %s
}`, strings.Join(formatParts, ",\n  "))

	userPrompt := fmt.Sprintf(`Data:
%s

Question: %s`, string(dataJSON), opts.Question)

	// CI-008. Question describes its envelope in prose only -- the template
	// above contains `(your answer matching this schema: ...)` and `0.0-1.0`,
	// neither of which is a JSON value -- so nothing downstream could ever
	// derive the answer's shape from it. That is not only a mock problem: a
	// provider with native structured output was given no schema to enforce
	// either, so Question ran at ContractPromptOnly while every operation
	// around it ran schema-constrained.
	//
	// The envelope is built here rather than in the prompt because only this
	// code knows A: `answer` carries A's own schema, and the optional fields
	// appear only when the caller asked for them, so the declared shape and the
	// prose stay the same shape rather than drifting apart.
	if answerJSONSchema := GenerateJSONSchema(reflect.TypeOf(answerZero)); answerJSONSchema != nil {
		properties := map[string]any{"answer": answerJSONSchema}
		required := []string{"answer"}
		if opts.IncludeConfidence {
			properties["confidence"] = map[string]any{"type": "number"}
		}
		if opts.IncludeReasoning {
			properties["reasoning"] = map[string]any{"type": "string"}
		}
		if opts.IncludeEvidence {
			properties["evidence"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		}
		opt.JSONSchema = map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		}
		opt.SchemaName = "question_answer"
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Question operation LLM call failed", "error", err)
		return result, fmt.Errorf("question answering failed: %w", err)
	}

	// Parse the response into a flexible structure
	var llmResult struct {
		Answer          json.RawMessage `json:"answer"`
		ModelConfidence float64         `json:"confidence,omitempty"`
		Reasoning       string          `json:"reasoning,omitempty"`
		Evidence        []string        `json:"evidence,omitempty"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		// The string-coercion fallback turned any unparseable body into the
		// answer, with an invented confidence of 0.5 -- so a refusal became a
		// confident answer whenever A was a string. The log line also carried
		// the whole response, which is the caller's data.
		log.Error("Question operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse response: %w", err)
	}

	// Parse the answer into the expected type
	if len(llmResult.Answer) > 0 {
		if err := json.Unmarshal(llmResult.Answer, &result.Answer); err != nil {
			// Try string coercion for simple types
			if strAnswer, ok := any(&result.Answer).(*string); ok {
				*strAnswer = string(llmResult.Answer)
			} else {
				log.Error("Question operation failed: answer parse error", "error", err)
				return result, fmt.Errorf("failed to parse answer: %w", err)
			}
		}
	}

	result.ModelConfidence = llmResult.ModelConfidence
	result.Reasoning = llmResult.Reasoning
	result.Evidence = llmResult.Evidence

	log.Debug("Question operation succeeded", "hasReasoning", result.Reasoning != "", "evidenceCount", len(result.Evidence))
	return result, nil
}

// Deprecated: use Question, which returns a typed answer with its supporting
// detail rather than a bare string. See ValidateLegacy for why this is marked
// rather than removed.
//
// QuestionLegacy answers questions about data (legacy interface)
//
// Examples:
//
//	answer, err := QuestionLegacy(report, "What are the top 3 risks?")
//	summary, err := QuestionLegacy(data, "Summarize the key findings")
func QuestionLegacy(data any, question string, opts ...types.OpOptions) (string, error) {
	log := logger.GetLogger()
	log.Debug("Starting legacy question operation")

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert data to string representation
	var dataStr string
	switch v := data.(type) {
	case string:
		dataStr = v
	default:
		dataJSON, err := json.Marshal(data)
		if err != nil {
			dataStr = fmt.Sprintf("%v", data)
		} else {
			dataStr = string(dataJSON)
		}
	}

	systemPrompt := `You are a data analysis expert. Answer questions about the provided data accurately and concisely.
Base your answers only on the information provided.`

	userPrompt := fmt.Sprintf(`Data:
%s

Question: %s`, dataStr, question)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Question operation LLM call failed", "error", err)
		return "", fmt.Errorf("question answering failed: %w", err)
	}

	log.Debug("Question operation succeeded", "responseLength", len(response))
	return strings.TrimSpace(response), nil
}

// DeduplicateResult contains the results of deduplication
type DeduplicateResult[T any] struct {
	Unique       []T
	Duplicates   [][]T // Groups of duplicates
	TotalRemoved int
}

// Deduplicate removes duplicates using semantic similarity
//
// Examples:
//
//	result, err := Deduplicate(customers, 0.85) // 85% similarity threshold
//	fmt.Printf("Removed %d duplicates\n", result.TotalRemoved)
func Deduplicate[T any](items []T, threshold float64, opts ...types.OpOptions) (DeduplicateResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting deduplicate operation", "itemsCount", len(items), "threshold", threshold)

	result := DeduplicateResult[T]{
		Unique:     []T{},
		Duplicates: [][]T{},
	}

	if len(items) <= 1 {
		result.Unique = items
		return result, nil
	}

	opt := applyDefaults(opts...)
	ctx, cancel := operationContext(opt.Context, config.GetTimeout())
	defer cancel()

	// Convert items to JSON for comparison
	itemsJSON := make([]string, len(items))
	for i, item := range items {
		itemJSON, err := json.Marshal(item)
		if err != nil {
			log.Error("Deduplicate operation failed: marshal error", "itemIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal item %d: %w", i, err)
		}
		itemsJSON[i] = string(itemJSON)
	}

	systemPrompt := fmt.Sprintf(`You are a deduplication expert. Identify duplicate items based on semantic similarity.
Items with similarity >= %.2f should be considered duplicates.

Return a JSON object with:
{
  "groups": [
    [0, 5, 8],  // indices of items that are duplicates of each other
    [2, 7],     // another group of duplicates
    [1],        // unique item
    [3],        // unique item
    ...
  ]
}`, threshold)

	userPrompt := fmt.Sprintf(`Find duplicates in these items:
%s`, strings.Join(itemsJSON, "\n"))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Deduplicate operation LLM call failed", "error", err)
		return result, fmt.Errorf("deduplication failed: %w", err)
	}

	// Parse the grouping response
	var grouping struct {
		Groups [][]int `json:"groups"`
	}

	if err := ParseJSONStrict(response, &grouping); err != nil {
		// Fallback: treat all items as unique if parsing fails
		result.Unique = items
		return result, nil
	}

	// Process groups
	seen := make(map[int]bool)
	for _, group := range grouping.Groups {
		if len(group) == 0 {
			continue
		}

		// Mark all indices as seen
		for _, idx := range group {
			if idx >= 0 && idx < len(items) {
				seen[idx] = true
			}
		}

		if len(group) == 1 {
			// Unique item
			if idx := group[0]; idx >= 0 && idx < len(items) {
				result.Unique = append(result.Unique, items[idx])
			}
		} else {
			// Group of duplicates - keep first, track others
			if idx := group[0]; idx >= 0 && idx < len(items) {
				result.Unique = append(result.Unique, items[idx])
			}

			// Track the duplicate group
			var dupGroup []T
			for _, idx := range group {
				if idx >= 0 && idx < len(items) {
					dupGroup = append(dupGroup, items[idx])
				}
			}
			if len(dupGroup) > 1 {
				result.Duplicates = append(result.Duplicates, dupGroup)
				result.TotalRemoved += len(dupGroup) - 1
			}
		}
	}

	// Add any items not mentioned in groups as unique
	for i, item := range items {
		if !seen[i] {
			result.Unique = append(result.Unique, item)
		}
	}

	log.Debug("Deduplicate operation succeeded", "uniqueCount", len(result.Unique), "duplicatesCount", len(result.Duplicates), "totalRemoved", result.TotalRemoved)
	return result, nil
}
