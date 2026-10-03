package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// ArbitrateOptions configures the Arbitrate operation
type ArbitrateOptions struct {
	// Rules are the rules every option is evaluated against, in plain
	// language. At least one is required and none may be blank.
	Rules []string

	// Weights is how much each rule counts, in the order of Rules. A rule with
	// no weight counts 1. This library applies the weights: an option's total
	// is the weighted mean of its rule scores. A weight may not be negative,
	// and they may not all be zero.
	Weights []float64

	// RequireAllRules disqualifies an option that fails any rule, whatever it
	// scores. When every option is disqualified there is no winner, and
	// Arbitrate returns an error that is ErrReviewRequired.
	RequireAllRules bool

	// IncludeReasoning asks the model for a reason with every evaluation, one
	// for each option on each rule, which is returned in
	// RuleEvaluation.Reasoning. It is off unless set, here, in
	// NewArbitrateOptions and on the fluent builder alike: the reasons are
	// most of the answer's length, and an arbitration without them takes about
	// half the time. ArbitrateResult.Reasoning, the account of how the options
	// compare, is returned either way.
	IncludeReasoning bool

	// Tiebreaker says which of several options with the same total wins:
	// "first", the earliest in the list; "random", any of them, which makes
	// the result differ from call to call; or "most-confident", the default,
	// the one that passed the most rules, and the earliest of those.
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
	Model string
	// Effort is how much the model reasons before it answers (LX-105). The
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

// RuleEvaluation describes how a rule was evaluated for an option
type RuleEvaluation struct {
	// Rule is the rule, in the caller's own words
	Rule string `json:"rule"`

	// Passed indicates if the option satisfied the rule
	Passed bool `json:"passed"`

	// Score is the degree of satisfaction (0.0-1.0)
	Score float64 `json:"score"`

	// Reasoning is the model's reason for this evaluation. It is empty unless
	// ArbitrateOptions.IncludeReasoning was set.
	Reasoning string `json:"reasoning,omitempty"`
}

// OptionEvaluation describes how an option was evaluated
type OptionEvaluation struct {
	// Index is the option's position in the input array
	Index int `json:"index"`

	// TotalScore is the weighted mean of the rule scores, from 0 to 1. This
	// library works it out from the caller's weights.
	TotalScore float64 `json:"total_score"`

	// RuleResults shows how each rule was evaluated
	RuleResults []RuleEvaluation `json:"rule_results"`

	// Disqualified indicates if the option was disqualified
	Disqualified bool `json:"disqualified"`

	// DisqualifyReason says which rules it failed. This library writes it.
	DisqualifyReason string `json:"disqualify_reason,omitempty"`
}

// ArbitrateResult contains the winning option and full audit trail
type ArbitrateResult[T any] struct {
	// Winner is the selected option
	Winner T `json:"winner"`

	// WinnerIndex is the position of the winner in the input array
	WinnerIndex int `json:"winner_index"`

	// Scores maps each option's index to its total, the weighted mean of its
	// rule scores. It is empty when one option was offered and not evaluated.
	Scores map[int]float64 `json:"scores"`

	// Evaluations contains detailed evaluation for each option
	Evaluations []OptionEvaluation `json:"evaluations"`

	// Reasoning is the model's account of how the options compare on the
	// rules. The model writes it without knowing the weights or the winner,
	// which this library works out afterwards, so it explains the evaluations
	// and not the choice. It names the options as the request did, o1, o2 and
	// so on in the order given: o1 is index 0.
	Reasoning string `json:"reasoning"`

	// ModelConfidence is the model's own claim about its evaluations taken
	// together (0.0-1.0), not a measurement. It is zero when one option was
	// offered and not evaluated.
	ModelConfidence float64 `json:"confidence"`

	// TiesBroken reports that more than one option had the highest total and
	// the tiebreaker chose between them. Metadata["tied"] lists them.
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
//	    IncludeReasoning: true, // a reason with every evaluation, printed below
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
	return arbitrateCore(options, opts...)
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
	// The two identifiers were left out of this merge, so a caller's request
	// id never reached the call.
	if user.RequestID != "" {
		defaults.RequestID = user.RequestID
	}
	if user.CorrelationID != "" {
		defaults.CorrelationID = user.CorrelationID
	}
	return defaults
}
