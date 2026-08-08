// package ops - Arbitrate operation for rule-based decisions with audit trail
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ArbitrateOptions configures the Arbitrate operation
type ArbitrateOptions struct {
	// Rules are natural language rules that must be evaluated
	Rules []string

	// Weights assigns importance to each rule (same order as Rules)
	Weights []float64

	// RequireAllRules fails if any option doesn't satisfy all rules
	RequireAllRules bool

	// IncludeReasoning includes detailed reasoning for each evaluation
	IncludeReasoning bool

	// Tiebreaker specifies how to break ties ("first", "random", "most-confident")
	Tiebreaker string

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

// RuleEvaluation describes how a rule was evaluated for an option
type RuleEvaluation struct {
	// Rule is the rule text
	Rule string `json:"rule"`

	// Passed indicates if the option satisfied the rule
	Passed bool `json:"passed"`

	// Score is the degree of satisfaction (0.0-1.0)
	Score float64 `json:"score"`

	// Reasoning explains the evaluation
	Reasoning string `json:"reasoning,omitempty"`
}

// OptionEvaluation describes how an option was evaluated
type OptionEvaluation struct {
	// Index is the option's position in the input array
	Index int `json:"index"`

	// TotalScore is the weighted sum of all rule scores
	TotalScore float64 `json:"total_score"`

	// RuleResults shows how each rule was evaluated
	RuleResults []RuleEvaluation `json:"rule_results"`

	// Disqualified indicates if the option was disqualified
	Disqualified bool `json:"disqualified"`

	// DisqualifyReason explains why it was disqualified
	DisqualifyReason string `json:"disqualify_reason,omitempty"`
}

// ArbitrateResult contains the winning option and full audit trail
type ArbitrateResult[T any] struct {
	// Winner is the selected option
	Winner T `json:"winner"`

	// WinnerIndex is the position of the winner in the input array
	WinnerIndex int `json:"winner_index"`

	// Scores maps option indices to their total scores
	Scores map[int]float64 `json:"scores"`

	// Evaluations contains detailed evaluation for each option
	Evaluations []OptionEvaluation `json:"evaluations"`

	// Reasoning explains the overall decision
	Reasoning string `json:"reasoning"`

	// ModelConfidence in the decision (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// TiesBroken indicates if ties were broken
	TiesBroken bool `json:"ties_broken"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Arbitrate makes decisions between typed options using explicit rules with full audit trail.
//
// Type parameter T specifies the type of options being arbitrated.
//
// Examples:
//
//	// Example 1: Candidate selection with hiring criteria
//	type Candidate struct {
//	    Name       string   `json:"name"`
//	    YearsExp   int      `json:"years_experience"`
//	    Skills     []string `json:"skills"`
//	    Salary     int      `json:"salary_expectation"`
//	}
//	candidates := []Candidate{
//	    {Name: "Alice", YearsExp: 5, Skills: []string{"Go", "K8s"}, Salary: 150000},
//	    {Name: "Bob", YearsExp: 8, Skills: []string{"Java", "Docker"}, Salary: 160000},
//	}
//	result, err := Arbitrate(candidates, ArbitrateOptions{
//	    Rules: []string{
//	        "Must have at least 3 years experience",
//	        "Must know Go or Python",
//	        "Salary under $155,000",
//	    },
//	})
//	fmt.Printf("Selected: %s (score: %.0f%%)\n", result.Winner.Name, result.Scores[result.WinnerIndex]*100)
//	for _, eval := range result.Evaluations {
//	    for _, r := range eval.RuleResults {
//	        fmt.Printf("%s: %v - %s\n", r.Rule, r.Passed, r.Reasoning)
//	    }
//	}
//
//	// Example 2: Loan approval with strict requirements
//	type LoanApp struct {
//	    Name        string  `json:"name"`
//	    Amount      float64 `json:"amount"`
//	    CreditScore int     `json:"credit_score"`
//	    Income      float64 `json:"income"`
//	}
//	result, err := Arbitrate(applications, ArbitrateOptions{
//	    Rules: []string{
//	        "Credit score must be at least 650",
//	        "Loan amount must not exceed 40% of income",
//	        "Must have employment verification",
//	    },
//	    RequireAllRules: true, // All rules must pass
//	})
//
//	// Example 3: Vendor selection with weighted criteria
//	result, err := Arbitrate(vendors, ArbitrateOptions{
//	    Rules: []string{"Price competitive", "Quality rating > 4.0", "Delivery < 5 days"},
//	    Weights: []float64{0.4, 0.35, 0.25},
//	    IncludeReasoning: true,
//	})
//
//	// Simple case with defaults
//	result, err := Arbitrate(options, ArbitrateOptions{
//	    Rules: []string{"best overall value"},
//	})
func Arbitrate[T any](options []T, opts ...ArbitrateOptions) (ArbitrateResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting arbitrate operation", "optionCount", len(options))

	var result ArbitrateResult[T]
	result.Scores = make(map[int]float64)
	result.Metadata = make(map[string]any)

	if len(options) == 0 {
		return result, fmt.Errorf("no options to arbitrate")
	}

	if len(options) == 1 {
		result.Winner = options[0]
		result.WinnerIndex = 0
		result.Scores[0] = 1.0
		result.ModelConfidence = 1.0
		result.Reasoning = "Only one option provided"
		return result, nil
	}

	// Apply defaults
	opt := ArbitrateOptions{
		IncludeReasoning: true,
		Tiebreaker:       "most-confident",
		Mode:             types.TransformMode,
		Intelligence:     types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeArbitrateOptions(opt, opts[0])
	}

	if len(opt.Rules) == 0 {
		return result, fmt.Errorf("at least one rule is required")
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := operationContext(ctx, 0)
	defer cancel()

	// Convert options to JSON with indices
	var optionsJSON []string
	for i, option := range options {
		optJSON, err := json.Marshal(option)
		if err != nil {
			log.Error("Arbitrate operation failed: marshal error", "optionIndex", i, "error", err)
			return result, fmt.Errorf("failed to marshal option %d: %w", i, err)
		}
		optionsJSON = append(optionsJSON, fmt.Sprintf("Option %d: %s", i, string(optJSON)))
	}

	// Get type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build rules with weights
	var rulesDesc []string
	for i, rule := range opt.Rules {
		weight := 1.0
		if i < len(opt.Weights) {
			weight = opt.Weights[i]
		}
		rulesDesc = append(rulesDesc, fmt.Sprintf("- Rule %d (weight %.2f): %s", i+1, weight, rule))
	}

	requireAllNote := ""
	if opt.RequireAllRules {
		requireAllNote = "\nMode: RequireAllRules - disqualify any option that fails ANY rule."
	}

	systemPrompt := fmt.Sprintf(`You are a decision arbitration expert. Evaluate options against rules and select the best one.

Option schema: %s

Rules to evaluate:
%s%s

Tiebreaker: %s

Return a JSON object with:
{
  "winner_index": 0,
  "scores": {"0": 0.85, "1": 0.72, ...},
  "evaluations": [
    {
      "index": 0,
      "total_score": 0.85,
      "rule_results": [
        {"rule": "rule text", "passed": true, "score": 0.9, "reasoning": "explanation"}
      ],
      "disqualified": false,
      "disqualify_reason": ""
    }
  ],
  "reasoning": "overall explanation of the decision",
  "confidence": 0.0-1.0,
  "ties_broken": false
}

Rules:
- Evaluate EVERY option against EVERY rule
- Calculate weighted scores for each option
- Disqualify options that fail required rules
- Select the option with the highest total score
- Break ties using the specified tiebreaker method
- Provide complete audit trail`,
		typeSchema, strings.Join(rulesDesc, "\n"), requireAllNote, opt.Tiebreaker)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Arbitrate between these options:

%s%s`, strings.Join(optionsJSON, "\n\n"), steeringNote)

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
		log.Error("Arbitrate operation LLM call failed", "error", err)
		return result, fmt.Errorf("arbitration failed: %w", err)
	}

	// Parse response
	var parsed struct {
		WinnerIndex     int                `json:"winner_index"`
		Scores          map[string]float64 `json:"scores"`
		Evaluations     []OptionEvaluation `json:"evaluations"`
		Reasoning       string             `json:"reasoning"`
		ModelConfidence float64            `json:"confidence"`
		TiesBroken      bool               `json:"ties_broken"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Arbitrate operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse arbitration result: %w", err)
	}

	// Validate winner index
	if parsed.WinnerIndex < 0 || parsed.WinnerIndex >= len(options) {
		return result, fmt.Errorf("invalid winner index: %d", parsed.WinnerIndex)
	}

	result.Winner = options[parsed.WinnerIndex]
	result.WinnerIndex = parsed.WinnerIndex
	result.Evaluations = parsed.Evaluations
	result.Reasoning = parsed.Reasoning
	result.ModelConfidence = parsed.ModelConfidence
	result.TiesBroken = parsed.TiesBroken

	// Convert scores from string keys to int keys
	for key, score := range parsed.Scores {
		var idx int
		fmt.Sscanf(key, "%d", &idx)
		result.Scores[idx] = score
	}

	log.Debug("Arbitrate operation succeeded",
		"winnerIndex", result.WinnerIndex,
		"confidence", result.ModelConfidence,
		"tiesBroken", result.TiesBroken)

	return result, nil
}

// mergeArbitrateOptions merges user options with defaults
func mergeArbitrateOptions(defaults, user ArbitrateOptions) ArbitrateOptions {
	if user.Rules != nil {
		defaults.Rules = user.Rules
	}
	if user.Weights != nil {
		defaults.Weights = user.Weights
	}
	defaults.RequireAllRules = user.RequireAllRules
	defaults.IncludeReasoning = user.IncludeReasoning
	if user.Tiebreaker != "" {
		defaults.Tiebreaker = user.Tiebreaker
	}
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
