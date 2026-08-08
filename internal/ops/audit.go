// package ops - Audit operation for deep inspection and anomaly detection
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

// AuditOptions configures the Audit operation
type AuditOptions struct {
	// Policies lists audit rules/policies to check against
	Policies []string

	// Categories specifies which categories of issues to check
	// e.g., "security", "compliance", "quality", "consistency"
	Categories []string

	// Threshold is the minimum severity to report (0.0-1.0)
	Threshold float64

	// Deep enables recursive inspection of nested structures
	Deep bool

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

// AuditFinding represents a single issue discovered during audit
type AuditFinding struct {
	// Category of the finding (security, compliance, quality, etc.)
	Category string `json:"category"`

	// Severity from 0.0 (info) to 1.0 (critical)
	Severity float64 `json:"severity"`

	// Field is the path to the affected field (e.g., "user.address.zip")
	Field string `json:"field,omitempty"`

	// Issue describes what was found
	Issue string `json:"issue"`

	// Evidence is the specific value or pattern that triggered the finding
	Evidence string `json:"evidence,omitempty"`

	// Recommendation suggests how to fix the issue
	Recommendation string `json:"recommendation,omitempty"`

	// Policy is the policy/rule that was violated (if applicable)
	Policy string `json:"policy,omitempty"`
}

// AuditSummary provides aggregate statistics
type AuditSummary struct {
	// TotalFindings is the count of all findings
	TotalFindings int `json:"total_findings"`

	// BySeverity counts findings by severity level
	BySeverity map[string]int `json:"by_severity"`

	// ByCategory counts findings by category
	ByCategory map[string]int `json:"by_category"`

	// Critical flags if any critical (severity >= 0.9) findings exist
	Critical bool `json:"critical"`

	// PassesAudit indicates if data passes the audit (no high severity findings)
	PassesAudit bool `json:"passes_audit"`
}

// AuditResult contains the complete audit output
type AuditResult[T any] struct {
	// Original is the input data that was audited
	Original T `json:"original"`

	// Findings lists all discovered issues
	Findings []AuditFinding `json:"findings"`

	// Summary provides aggregate statistics
	Summary AuditSummary `json:"summary"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Audit performs deep inspection for issues, anomalies, and policy violations.
//
// Type parameters:
//   - T: Type of data to audit
//
// Examples:
//
//	// Example 1: Security audit for customer data
//	type CustomerData struct {
//	    ID         string `json:"id"`
//	    Email      string `json:"email"`
//	    SSN        string `json:"ssn"`
//	    CreditCard string `json:"credit_card"`
//	    Password   string `json:"password"`
//	}
//	result, err := Audit(customer, AuditOptions{
//	    Policies: []string{
//	        "PII must not be stored in plain text",
//	        "Credit cards must be tokenized",
//	        "Passwords must never be stored in plain text",
//	    },
//	    Categories: []string{"security", "compliance"},
//	})
//	if !result.Summary.PassesAudit {
//	    for _, f := range result.Findings {
//	        fmt.Printf("[%s] %s: %s\n", f.Category, f.Field, f.Issue)
//	        fmt.Printf("  Recommendation: %s\n", f.Recommendation)
//	    }
//	}
//
//	// Example 2: Financial report consistency check
//	type FinancialReport struct {
//	    Revenue   float64 `json:"revenue"`
//	    Expenses  float64 `json:"expenses"`
//	    NetIncome float64 `json:"net_income"`
//	}
//	result, err := Audit(report, AuditOptions{
//	    Policies: []string{"Net income must equal revenue minus expenses"},
//	    Categories: []string{"consistency"},
//	})
//	fmt.Printf("Issues by severity: %v\n", result.Summary.BySeverity)
//
//	// Example 3: Data quality audit
//	result, err := Audit(records, AuditOptions{
//	    Categories: []string{"quality", "completeness"},
//	    Threshold: 0.3,  // Report low+ severity
//	    Deep: true,
//	})
//	fmt.Printf("Total findings: %d, Critical: %v\n", result.Summary.TotalFindings, result.Summary.Critical)
//
// Deprecated: use AuditWithModel, which returns the shared
// types.JudgmentResult and whose name says what Audit's did not -- that
// this inspection is model-assisted, not a deterministic check. See OP-206
// (ARC-22, TRU-30).
func Audit[T any](data T, opts ...AuditOptions) (AuditResult[T], error) {
	return auditCore(data, opts...)
}

// AuditWithModel performs the same model-assisted inspection as the
// deprecated Audit -- policy violations, anomalies, and issues across the
// requested categories -- and reports it as a types.JudgmentResult: Verdict
// reflects AuditSummary.PassesAudit, and each AuditFinding becomes a
// JudgmentIssue with its 0.0-1.0 severity float mapped onto the same
// critical/error/warning/info levels AuditSummary itself uses, so the two
// views of one audit cannot disagree about which findings are critical.
//
// Behavior -- which findings are produced, how they are filtered by
// Threshold -- is identical to the deprecated Audit; only the result shape
// differs.
func AuditWithModel[T any](data T, opts ...AuditOptions) (types.JudgmentResult[T], error) {
	ar, err := auditCore(data, opts...)
	if err != nil {
		return types.JudgmentResult[T]{}, err
	}
	return auditResultToJudgment(ar), nil
}

// auditResultToJudgment translates AuditResult[T] into the shared shape.
func auditResultToJudgment[T any](ar AuditResult[T]) types.JudgmentResult[T] {
	verdict := types.VerdictPass
	if !ar.Summary.PassesAudit {
		verdict = types.VerdictFail
	}

	issues := make([]types.JudgmentIssue, 0, len(ar.Findings))
	for _, f := range ar.Findings {
		issues = append(issues, types.JudgmentIssue{
			Subject:    f.Field,
			Category:   f.Category,
			Severity:   severityFromFraction(f.Severity),
			Message:    f.Issue,
			Suggestion: f.Recommendation,
			Evidence:   evidenceSlice(f.Evidence),
		})
	}

	summary := fmt.Sprintf("%d finding(s) across %d categor(ies), critical=%v",
		ar.Summary.TotalFindings, len(ar.Summary.ByCategory), ar.Summary.Critical)

	return types.JudgmentResult[T]{
		Subject: ar.Original,
		Verdict: verdict,
		Issues:  issues,
		Summary: summary,
		ModelClaims: map[string]any{
			"by_severity": ar.Summary.BySeverity,
			"by_category": ar.Summary.ByCategory,
			"critical":    ar.Summary.Critical,
		},
		// Audit's findings carry no result-level model confidence -- each
		// AuditFinding is a severity judgement, not a scored claim -- so
		// ModelConfidence is left at its zero value rather than invented
		// from the findings.
	}
}

// auditCore is Audit's original implementation, unchanged. The deprecated
// Audit and the new AuditWithModel both call this so neither can silently
// drift from the other.
func auditCore[T any](data T, opts ...AuditOptions) (AuditResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting audit operation")

	var result AuditResult[T]
	result.Original = data
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := AuditOptions{
		Threshold:    0.0, // Report everything
		Deep:         true,
		Mode:         types.TransformMode,
		Intelligence: types.Smart,
	}
	if len(opts) > 0 {
		opt = mergeAuditOptions(opt, opts[0])
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert input to JSON
	inputJSON, err := json.Marshal(data)
	if err != nil {
		log.Error("Audit operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal data: %w", err)
	}

	// Get schema
	schema := GenerateTypeSchema(reflect.TypeOf(data))

	// Build policies description
	policiesDesc := ""
	if len(opt.Policies) > 0 {
		var parts []string
		for _, p := range opt.Policies {
			parts = append(parts, fmt.Sprintf("- %s", p))
		}
		policiesDesc = fmt.Sprintf("\n\nPolicies to check:\n%s", strings.Join(parts, "\n"))
	}

	// Build categories description
	categoriesDesc := ""
	if len(opt.Categories) > 0 {
		categoriesDesc = fmt.Sprintf("\n\nFocus on categories: %s", strings.Join(opt.Categories, ", "))
	}

	systemPrompt := fmt.Sprintf(`You are a data audit expert. Perform deep inspection to find issues, anomalies, and policy violations.

Data schema: %s%s%s

Severity threshold: %.2f (only report findings at or above this level)
Deep inspection: %v

Return a JSON object with:
{
  "findings": [
    {
      "category": "security/compliance/quality/consistency/completeness",
      "severity": 0.0-1.0,
      "field": "path.to.field",
      "issue": "description of the problem",
      "evidence": "specific value or pattern",
      "recommendation": "how to fix",
      "policy": "violated policy if applicable"
    }
  ]
}

Severity levels:
- 0.0-0.2: Info (observations, suggestions)
- 0.3-0.4: Low (minor issues, style concerns)
- 0.5-0.6: Medium (should be addressed)
- 0.7-0.8: High (must be fixed)
- 0.9-1.0: Critical (immediate action required)

Categories to check:
- security: Sensitive data exposure, injection risks, weak validation
- compliance: Regulatory issues, policy violations
- quality: Data accuracy, formatting, consistency
- consistency: Internal contradictions, mismatched values
- completeness: Missing required data, null/empty where not expected

Be thorough but precise. Only report genuine issues.`,
		schema, policiesDesc, categoriesDesc, opt.Threshold, opt.Deep)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Audit this data for issues and anomalies:

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
		log.Error("Audit operation LLM call failed", "error", err)
		return result, fmt.Errorf("audit failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Findings []AuditFinding `json:"findings"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Audit operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse audit result: %w", err)
	}

	// Filter by threshold
	for _, f := range parsed.Findings {
		if f.Severity >= opt.Threshold {
			result.Findings = append(result.Findings, f)
		}
	}

	// Build summary
	result.Summary = buildAuditSummary(result.Findings)

	log.Debug("Audit operation succeeded",
		"findings", result.Summary.TotalFindings,
		"critical", result.Summary.Critical,
		"passes", result.Summary.PassesAudit)

	return result, nil
}

// buildAuditSummary creates aggregate statistics from findings
func buildAuditSummary(findings []AuditFinding) AuditSummary {
	summary := AuditSummary{
		TotalFindings: len(findings),
		BySeverity:    make(map[string]int),
		ByCategory:    make(map[string]int),
		PassesAudit:   true,
	}

	for _, f := range findings {
		// Categorize severity
		var level string
		switch {
		case f.Severity >= 0.9:
			level = "critical"
			summary.Critical = true
			summary.PassesAudit = false
		case f.Severity >= 0.7:
			level = "high"
			summary.PassesAudit = false
		case f.Severity >= 0.5:
			level = "medium"
		case f.Severity >= 0.3:
			level = "low"
		default:
			level = "info"
		}
		summary.BySeverity[level]++

		// Count by category
		summary.ByCategory[f.Category]++
	}

	return summary
}

// mergeAuditOptions merges user options with defaults
func mergeAuditOptions(defaults, user AuditOptions) AuditOptions {
	if user.Policies != nil {
		defaults.Policies = user.Policies
	}
	if user.Categories != nil {
		defaults.Categories = user.Categories
	}
	if user.Threshold > 0 {
		defaults.Threshold = user.Threshold
	}
	// Deep is a boolean, use explicit assignment
	defaults.Deep = user.Deep
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
