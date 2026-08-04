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
	Claim       string   `json:"claim"`
	Verdict     string   `json:"verdict"` // "verified", "false", "partially_true", "unverifiable", "misleading"
	Confidence  float64  `json:"confidence"`
	Evidence    []string `json:"evidence,omitempty"`
	Reasoning   string   `json:"reasoning,omitempty"`
	Sources     []int    `json:"sources,omitempty"`
	Corrections string   `json:"corrections,omitempty"`
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
	OverallVerdict    string              `json:"overall_verdict"`
	OverallConfidence float64             `json:"overall_confidence"`
	Claims            []ClaimVerification `json:"claims"`
	LogicIssues       []LogicIssue        `json:"logic_issues,omitempty"`
	ConsistencyIssues []ConsistencyIssue  `json:"consistency_issues,omitempty"`
	Summary           string              `json:"summary"`
	TrustScore        float64             `json:"trust_score"`
	Metadata          map[string]any      `json:"metadata,omitempty"`
}

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
	if err := ParseJSON(response, &result); err != nil {
		log.Error("Verify operation failed: parse error", "error", err)
		return result, fmt.Errorf("failed to parse verification result: %w", err)
	}

	log.Debug("Verify operation succeeded",
		"overallVerdict", result.OverallVerdict,
		"claimCount", len(result.Claims),
		"trustScore", result.TrustScore)
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
		Claim:      claim,
		Verdict:    result.OverallVerdict,
		Confidence: result.OverallConfidence,
		Reasoning:  result.Summary,
	}, nil
}
