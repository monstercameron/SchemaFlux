// package ops - Verify operation for fact-checking claims against knowledge
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// VerifyOptions configures the Verify operation
type VerifyOptions struct {
	CommonOptions
	types.OpOptions

	// Knowledge sources to verify against
	Sources []any

	// Verification strictness ("strict", "moderate", "lenient")
	Strictness string

	// Include evidence for verdicts
	IncludeEvidence bool

	// Include reasoning explanation
	ExplainReasoning bool

	// Check for logical consistency
	CheckLogic bool

	// Check for factual accuracy
	CheckFacts bool

	// Check for internal consistency
	CheckConsistency bool

	// Domain for verification
	Domain string

	// Trusted source indicators
	TrustedSources []string

	// Minimum confidence to mark as verified
	MinConfidence float64
}

// NewVerifyOptions creates VerifyOptions with defaults
func NewVerifyOptions() VerifyOptions {
	return VerifyOptions{
		CommonOptions: CommonOptions{
			Mode:         types.Strict,
			Intelligence: types.Smart,
		},
		Strictness:       "moderate",
		IncludeEvidence:  true,
		ExplainReasoning: true,
		CheckLogic:       true,
		CheckFacts:       true,
		CheckConsistency: true,
		MinConfidence:    0.7,
	}
}

// Validate validates VerifyOptions
func (v VerifyOptions) Validate() error {
	if err := v.CommonOptions.Validate(); err != nil {
		return err
	}
	validStrictness := map[string]bool{"strict": true, "moderate": true, "lenient": true}
	if v.Strictness != "" && !validStrictness[v.Strictness] {
		return fmt.Errorf("invalid strictness: %s", v.Strictness)
	}
	if v.MinConfidence < 0 || v.MinConfidence > 1 {
		return fmt.Errorf("min confidence must be between 0 and 1, got %f", v.MinConfidence)
	}
	return nil
}

// WithSources sets knowledge sources
func (v VerifyOptions) WithSources(sources []any) VerifyOptions {
	v.Sources = sources
	return v
}

// WithStrictness sets verification strictness
func (v VerifyOptions) WithStrictness(strictness string) VerifyOptions {
	v.Strictness = strictness
	return v
}

// WithIncludeEvidence enables evidence inclusion
func (v VerifyOptions) WithIncludeEvidence(include bool) VerifyOptions {
	v.IncludeEvidence = include
	return v
}

// WithExplainReasoning enables reasoning explanation
func (v VerifyOptions) WithExplainReasoning(explain bool) VerifyOptions {
	v.ExplainReasoning = explain
	return v
}

// WithCheckLogic enables logic checking
func (v VerifyOptions) WithCheckLogic(check bool) VerifyOptions {
	v.CheckLogic = check
	return v
}

// WithCheckFacts enables fact checking
func (v VerifyOptions) WithCheckFacts(check bool) VerifyOptions {
	v.CheckFacts = check
	return v
}

// WithCheckConsistency enables consistency checking
func (v VerifyOptions) WithCheckConsistency(check bool) VerifyOptions {
	v.CheckConsistency = check
	return v
}

// WithDomain sets the verification domain
func (v VerifyOptions) WithDomain(domain string) VerifyOptions {
	v.Domain = domain
	return v
}

// WithTrustedSources sets trusted source indicators
func (v VerifyOptions) WithTrustedSources(sources []string) VerifyOptions {
	v.TrustedSources = sources
	return v
}

// WithMinConfidence sets minimum confidence for verification
func (v VerifyOptions) WithMinConfidence(confidence float64) VerifyOptions {
	v.MinConfidence = confidence
	return v
}

// WithSteering sets the steering prompt
func (v VerifyOptions) WithSteering(steering string) VerifyOptions {
	v.CommonOptions = v.CommonOptions.WithSteering(steering)
	return v
}

// WithMode sets the mode
func (v VerifyOptions) WithMode(mode types.Mode) VerifyOptions {
	v.CommonOptions = v.CommonOptions.WithMode(mode)
	return v
}

// WithIntelligence sets the intelligence level
func (v VerifyOptions) WithIntelligence(intelligence types.Speed) VerifyOptions {
	v.CommonOptions = v.CommonOptions.WithIntelligence(intelligence)
	return v
}

func (v VerifyOptions) toOpOptions() types.OpOptions {
	return v.CommonOptions.toOpOptions()
}

// ClaimVerification represents the verification of a single claim
type ClaimVerification struct {
	Claim   string `json:"claim"`
	Verdict string `json:"verdict"` // "verified", "false", "partially_true", "unverifiable", "misleading"
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64  `json:"confidence"`
	Evidence        []string `json:"evidence,omitempty"`
	Reasoning       string   `json:"reasoning,omitempty"`
	Sources         []int    `json:"sources,omitempty"`
	Corrections     string   `json:"corrections,omitempty"`
}

// LogicIssue represents a logical problem found
type LogicIssue struct {
	Type        string `json:"type"` // "contradiction", "non_sequitur", "circular", "false_premise"
	Description string `json:"description"`
	Location    string `json:"location,omitempty"`
	Severity    string `json:"severity"` // "critical", "major", "minor"
}

// ConsistencyIssue represents an internal consistency problem
type ConsistencyIssue struct {
	Type        string   `json:"type"` // "contradiction", "inconsistency", "ambiguity"
	Description string   `json:"description"`
	Items       []string `json:"conflicting_items"`
	Suggestion  string   `json:"suggestion,omitempty"`
}

// VerifyResult contains the results of verification
type VerifyResult struct {
	OverallVerdict         string              `json:"overall_verdict"`
	ModelOverallConfidence float64             `json:"overall_confidence"`
	Claims                 []ClaimVerification `json:"claims"`
	LogicIssues            []LogicIssue        `json:"logic_issues,omitempty"`
	ConsistencyIssues      []ConsistencyIssue  `json:"consistency_issues,omitempty"`
	Summary                string              `json:"summary"`
	ModelTrustScore        float64             `json:"trust_score"`
	Metadata               map[string]any      `json:"metadata,omitempty"`
}

// Deprecated: use VerifyWithModel, which returns the shared
// types.JudgmentResult and whose name says what Verify's did not -- that
// this is a model-assisted review, not a deterministic check. See OP-206
// (ARC-22, TRU-30).
//
// Verify fact-checks claims against knowledge sources and checks for consistency.
// Different from Validate (schema/rule checking) - Verify checks factual accuracy.
//
// Examples:
//
//	// Verify claims against knowledge base
//	result, err := Verify(claims, NewVerifyOptions().
//	    WithSources(knowledgeBase).
//	    WithExplainReasoning(true))
//
//	// Strict fact-checking
//	result, err := Verify(article, NewVerifyOptions().
//	    WithStrictness("strict").
//	    WithCheckFacts(true).
//	    WithCheckLogic(true))
//
//	// Domain-specific verification
//	result, err := Verify(medicalClaims, NewVerifyOptions().
//	    WithDomain("medical").
//	    WithTrustedSources([]string{"PubMed", "WHO"}))
func Verify(input any, opts VerifyOptions) (VerifyResult, error) {
	return verifyCore(input, opts)
}

// VerifyWithModel fact-checks claims against knowledge sources using a
// model, and reports the result as a types.JudgmentResult: Verdict is
// derived from the model's overall_verdict, Issues holds every claim that
// was not verified plus every logic and consistency problem found, and the
// model's trust score and per-claim confidences travel in ModelClaims and
// JudgmentIssue.ModelConfidence rather than sitting beside the verdict as
// though a model's self-report and a fact-check were the same kind of
// thing.
//
// Behavior -- which claims are found, what verdict each gets, what the
// logic and consistency checks report -- is identical to the deprecated
// Verify; only the result shape and the confidence floor's error wrapping
// differ.
func VerifyWithModel(input any, opts VerifyOptions) (types.JudgmentResult[any], error) {
	vr, err := verifyCore(input, opts)
	if err != nil {
		return types.JudgmentResult[any]{}, err
	}
	return verifyResultToJudgment(input, vr), nil
}

// verifyVerdict maps Verify's five-word verdict vocabulary onto the shared
// four-value Verdict. "mixed" and "misleading" both become VerdictMixed:
// each describes a claim that is neither cleanly right nor cleanly wrong,
// and forcing them into Pass or Fail would overstate or understate what the
// model actually found.
func verifyVerdict(overall string) types.Verdict {
	switch overall {
	case "verified":
		return types.VerdictPass
	case "false":
		return types.VerdictFail
	case "partially_true", "misleading", "mixed":
		return types.VerdictMixed
	case "unverifiable":
		return types.VerdictUnknown
	default:
		return types.VerdictUnknown
	}
}

// verifyResultToJudgment translates VerifyResult into the shared shape.
// Only claims the model did not mark "verified" become issues -- a claim it
// found accurate is not a finding to report as wrong, and treating every
// claim (verified or not) as an "issue" would make Issues mean something
// different for Verify than it does for the other three operations feeding
// JudgmentResult.
func verifyResultToJudgment(input any, vr VerifyResult) types.JudgmentResult[any] {
	var issues []types.JudgmentIssue
	var evidence []string

	for _, claim := range vr.Claims {
		evidence = append(evidence, claim.Evidence...)
		if claim.Verdict == "verified" {
			continue
		}
		severity := "warning"
		switch claim.Verdict {
		case "false":
			severity = "error"
		case "unverifiable":
			severity = "info"
		}
		msg := claim.Reasoning
		if msg == "" {
			msg = claim.Corrections
		}
		issues = append(issues, types.JudgmentIssue{
			Subject:         claim.Claim,
			Category:        "fact:" + claim.Verdict,
			Severity:        severity,
			Message:         msg,
			Suggestion:      claim.Corrections,
			Evidence:        claim.Evidence,
			ModelConfidence: claim.ModelConfidence,
		})
	}

	for _, li := range vr.LogicIssues {
		issues = append(issues, types.JudgmentIssue{
			Subject:  li.Location,
			Category: "logic:" + li.Type,
			Severity: normalizeSeverity(li.Severity),
			Message:  li.Description,
		})
	}

	for _, ci := range vr.ConsistencyIssues {
		issues = append(issues, types.JudgmentIssue{
			Category:   "consistency:" + ci.Type,
			Severity:   "warning",
			Message:    ci.Description,
			Suggestion: ci.Suggestion,
			Evidence:   ci.Items,
		})
	}

	return types.JudgmentResult[any]{
		Subject:         input,
		Verdict:         verifyVerdict(vr.OverallVerdict),
		Issues:          issues,
		Evidence:        evidence,
		Summary:         vr.Summary,
		ModelConfidence: vr.ModelOverallConfidence,
		ModelClaims: map[string]any{
			"trust_score": vr.ModelTrustScore,
			"claims":      vr.Claims,
		},
	}
}

// verifyCore is Verify's original implementation, unchanged. The
// deprecated Verify and the new VerifyWithModel both call this so neither
// can silently drift from the other.
func verifyCore(input any, opts VerifyOptions) (VerifyResult, error) {
	log := logger.GetLogger()
	log.Debug("Starting verify operation")

	var result VerifyResult
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

	// Convert input to string
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Verify operation failed: input normalization error", "error", err)
		return result, fmt.Errorf("failed to normalize input: %w", err)
	}

	// Build sources description
	sourcesDesc := ""
	if len(opts.Sources) > 0 {
		sourcesJSON := make([]string, len(opts.Sources))
		for i, source := range opts.Sources {
			sourceJSON, _ := json.Marshal(source)
			sourcesJSON[i] = fmt.Sprintf("[Source %d]\n%s", i, string(sourceJSON))
		}
		sourcesDesc = fmt.Sprintf("\nKnowledge sources:\n%s", strings.Join(sourcesJSON, "\n\n"))
	}

	strictnessDesc := ""
	switch opts.Strictness {
	case "strict":
		strictnessDesc = "Be very strict - require strong evidence for verification."
	case "moderate":
		strictnessDesc = "Use moderate standards - accept reasonable evidence."
	case "lenient":
		strictnessDesc = "Be lenient - give benefit of the doubt when evidence is limited."
	}

	checksDesc := ""
	var checks []string
	if opts.CheckFacts {
		checks = append(checks, "factual accuracy")
	}
	if opts.CheckLogic {
		checks = append(checks, "logical consistency")
	}
	if opts.CheckConsistency {
		checks = append(checks, "internal consistency")
	}
	if len(checks) > 0 {
		checksDesc = fmt.Sprintf("\nCheck for: %s", strings.Join(checks, ", "))
	}

	domainDesc := ""
	if opts.Domain != "" {
		domainDesc = fmt.Sprintf("\nDomain: %s (apply domain-specific knowledge)", opts.Domain)
	}

	trustedDesc := ""
	if len(opts.TrustedSources) > 0 {
		trustedDesc = fmt.Sprintf("\nTrusted sources: %s", strings.Join(opts.TrustedSources, ", "))
	}

	evidenceNote := ""
	if opts.IncludeEvidence {
		evidenceNote = "\nProvide evidence supporting or refuting each claim."
	}

	reasoningNote := ""
	if opts.ExplainReasoning {
		reasoningNote = "\nExplain the reasoning for each verdict."
	}

	systemPrompt := fmt.Sprintf(`You are an expert fact-checker and verification specialist.

Strictness: %s%s%s%s%s%s%s

Minimum confidence for "verified" verdict: %.0f%%

Verdict options:
- "verified": The claim is accurate and supported by evidence
- "false": The claim is demonstrably incorrect
- "partially_true": The claim has some truth but is misleading or incomplete
- "misleading": The claim is technically true but presented in a misleading way
- "unverifiable": Cannot be verified with available information

Return a JSON object with:
{
  "overall_verdict": "verified|false|partially_true|misleading|mixed",
  "overall_confidence": 0.85,
  "claims": [
    {
      "claim": "The specific claim being verified",
      "verdict": "verified",
      "confidence": 0.9,
      "evidence": ["Evidence supporting the verdict"],
      "reasoning": "Why this verdict was reached",
      "sources": [0, 1],
      "corrections": "Correct information if claim is false"
    }
  ],
  "logic_issues": [
    {
      "type": "contradiction",
      "description": "What the issue is",
      "location": "Where it occurs",
      "severity": "major"
    }
  ],
  "consistency_issues": [
    {
      "type": "inconsistency",
      "description": "What's inconsistent",
      "conflicting_items": ["item1", "item2"],
      "suggestion": "How to resolve"
    }
  ],
  "summary": "Overall assessment",
  "trust_score": 0.75
}`, strictnessDesc, sourcesDesc, checksDesc, domainDesc, trustedDesc, evidenceNote, reasoningNote, opts.MinConfidence*100)

	userPrompt := fmt.Sprintf("Verify this content:\n\n%s", inputStr)

	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Error("Verify operation LLM call failed", "error", err)
		return result, fmt.Errorf("verification failed: %w", err)
	}

	// Parse the response
	if err := ParseJSONStrict(response, &result); err != nil {
		log.Error("Verify operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse verification result: %w", err)
	}

	// MinConfidence defaults to 0.7 here and was prompt-only, so a caller who
	// read the field name and believed a floor was active got verifications the
	// model itself scored below it, reported as success. Enforcing it does not
	// turn a model claim into a measurement -- it makes the option mean what its
	// name says.
	if err := AtLeastConfidence(result.ModelOverallConfidence, opts.MinConfidence); err != nil {
		log.Error("Verification is below the configured confidence floor", "error", err)
		return result, fmt.Errorf("verification rejected: %w", err)
	}

	log.Debug("Verify operation succeeded",
		"overallVerdict", result.OverallVerdict,
		"claimCount", len(result.Claims),
		"trustScore", result.ModelTrustScore)
	return result, nil
}

// VerifyClaim verifies a single claim
func VerifyClaim(claim string, opts VerifyOptions) (ClaimVerification, error) {
	result, err := Verify(claim, opts)
	if err != nil {
		return ClaimVerification{}, err
	}
	if len(result.Claims) > 0 {
		return result.Claims[0], nil
	}
	return ClaimVerification{
		Claim:           claim,
		Verdict:         result.OverallVerdict,
		ModelConfidence: result.ModelOverallConfidence,
		Reasoning:       result.Summary,
	}, nil
}
