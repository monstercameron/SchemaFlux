// package ops - Negotiate operation for reconciling competing constraints
package ops

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
)

// NegotiateOptions configures the Negotiate operation
type NegotiateOptions struct {
	// Priorities maps constraint names to their importance weights (0.0-1.0)
	Priorities map[string]float64

	// Constraints as natural language requirements
	Constraints []string

	// MinSatisfaction is the minimum acceptable satisfaction score (0.0-1.0)
	MinSatisfaction float64

	// MaxAlternatives limits the number of alternative solutions returned
	MaxAlternatives int

	// Strategy guides the negotiation approach ("balanced", "maximize_primary", "pareto")
	Strategy string

	// Common options
	Steering      string
	Mode          types.Mode
	Intelligence  types.Speed
	Context       gocontext.Context
	RequestID     string
	CorrelationID string
}

// Tradeoff describes what was sacrificed to gain something else
type Tradeoff struct {
	// Sacrificed is what was reduced or given up
	Sacrificed string `json:"sacrificed"`

	// Gained is what was improved or obtained
	Gained string `json:"gained"`

	// Impact describes the magnitude of the tradeoff
	Impact string `json:"impact"`

	// Reasoning explains why this tradeoff was made
	Reasoning string `json:"reasoning,omitempty"`
}

// NegotiateResult contains the negotiated solution and metadata
type NegotiateResult[T any] struct {
	// Solution is the optimal result balancing all constraints
	Solution T `json:"solution"`

	// Satisfaction maps constraint names to how well they were met (0.0-1.0)
	Satisfaction map[string]float64 `json:"satisfaction"`

	// OverallSatisfaction is the weighted average satisfaction
	OverallSatisfaction float64 `json:"overall_satisfaction"`

	// Tradeoffs describes what was sacrificed for what
	Tradeoffs []Tradeoff `json:"tradeoffs,omitempty"`

	// Alternatives are other Pareto-optimal solutions
	Alternatives []T `json:"alternatives,omitempty"`

	// Reasoning explains the negotiation process
	Reasoning string `json:"reasoning,omitempty"`

	// ModelConfidence in the solution quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`

	// Metadata contains additional operation information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Negotiate reconciles competing constraints to find an optimal typed solution.
//
// Type parameter T specifies the output type that balances the constraints.
//
// Examples:
//
//	// Example 1: Project planning with competing constraints
//	type ProjectPlan struct {
//	    Duration int      `json:"duration_weeks"`
//	    Budget   int      `json:"budget"`
//	    Features []string `json:"features"`
//	}
//	constraints := map[string]any{
//	    "max_budget": 100000,
//	    "deadline": "2024-06-01",
//	    "required_features": []string{"auth", "dashboard"},
//	    "desired_features": []string{"analytics", "mobile"},
//	}
//	result, err := Negotiate[ProjectPlan](constraints)
//	fmt.Printf("Plan: %d weeks, $%d\n", result.Solution.Duration, result.Solution.Budget)
//	for _, t := range result.Tradeoffs {
//	    fmt.Printf("Tradeoff: gave up %s for %s\n", t.Sacrificed, t.Gained)
//	}
//
//	// Example 2: Resource allocation with priorities
//	type Allocation struct {
//	    CPUCores  int `json:"cpu_cores"`
//	    MemoryGB  int `json:"memory_gb"`
//	    StorageGB int `json:"storage_gb"`
//	}
//	result, err := Negotiate[Allocation](demands, NegotiateOptions{
//	    Strategy: "balanced",
//	    Priorities: map[string]float64{
//	        "performance": 0.5,
//	        "cost": 0.3,
//	        "reliability": 0.2,
//	    },
//	})
//
//	// Example 3: Salary negotiation
//	type Offer struct {
//	    Salary     int    `json:"salary"`
//	    RemoteDays int    `json:"remote_days"`
//	    Equity     string `json:"equity"`
//	}
//	result, err := Negotiate[Offer](map[string]any{
//	    "candidate_min": 150000,
//	    "company_max": 140000,
//	    "remote_preference": "3-5 days",
//	}, NegotiateOptions{Steering: "Find creative compensation solutions"})
func Negotiate[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting negotiate operation")

	var result NegotiateResult[T]
	result.Satisfaction = make(map[string]float64)
	result.Metadata = make(map[string]any)

	// Apply defaults
	opt := NegotiateOptions{
		MinSatisfaction: 0.6,
		MaxAlternatives: 3,
		Strategy:        "balanced",
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeNegotiateOptions(opt, opts[0])
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = gocontext.Background()
	}
	ctx, cancel := gocontext.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert constraints to JSON
	constraintsJSON, err := json.Marshal(constraints)
	if err != nil {
		log.Error("Negotiate operation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal constraints: %w", err)
	}

	// Get target type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build priorities description
	prioritiesDesc := ""
	if len(opt.Priorities) > 0 {
		var parts []string
		for name, weight := range opt.Priorities {
			parts = append(parts, fmt.Sprintf("%s (weight: %.2f)", name, weight))
		}
		prioritiesDesc = fmt.Sprintf("\n\nPriorities:\n%s", strings.Join(parts, "\n"))
	}

	// Build constraints description
	constraintsDesc := ""
	if len(opt.Constraints) > 0 {
		constraintsDesc = fmt.Sprintf("\n\nAdditional constraints:\n- %s", strings.Join(opt.Constraints, "\n- "))
	}

	systemPrompt := fmt.Sprintf(`You are a negotiation and optimization expert. Find the best solution that balances competing constraints.

Strategy: %s
Minimum acceptable satisfaction: %.0f%%%s%s

Return a JSON object matching this schema:
{
  "solution": %s,
  "satisfaction": {"constraint_name": 0.0-1.0, ...},
  "overall_satisfaction": 0.0-1.0,
  "tradeoffs": [{"sacrificed": "what was reduced", "gained": "what was improved", "impact": "low/medium/high", "reasoning": "why"}],
  "alternatives": [alternative solutions matching the schema above],
  "reasoning": "explanation of negotiation process",
  "confidence": 0.0-1.0
}

Rules:
- "solution" must be a valid instance of the target schema
- "satisfaction" shows how well each constraint was satisfied (1.0 = fully satisfied)
- "tradeoffs" explains what compromises were made
- "alternatives" provides up to %d Pareto-optimal alternatives
- Balance all constraints according to their priorities`,
		opt.Strategy, opt.MinSatisfaction*100, prioritiesDesc, constraintsDesc, typeSchema, opt.MaxAlternatives)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Find the optimal solution that balances these constraints:

%s%s`, string(constraintsJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Negotiate operation LLM call failed", "error", err)
		return result, fmt.Errorf("negotiation failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Solution            json.RawMessage   `json:"solution"`
		Satisfaction        map[string]any    `json:"satisfaction"`
		OverallSatisfaction float64           `json:"overall_satisfaction"`
		Tradeoffs           []Tradeoff        `json:"tradeoffs"`
		Alternatives        []json.RawMessage `json:"alternatives"`
		Reasoning           string            `json:"reasoning"`
		ModelConfidence     float64           `json:"confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Negotiate operation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse negotiation result: %w", err)
	}

	// Parse solution
	if len(parsed.Solution) > 0 {
		if err := json.Unmarshal(parsed.Solution, &result.Solution); err != nil {
			log.Error("Negotiate operation failed: solution parse error", "error", err)
			return result, fmt.Errorf("failed to parse solution: %w", err)
		}
	}

	// Parse alternatives
	for _, alt := range parsed.Alternatives {
		var alternative T
		if err := json.Unmarshal(alt, &alternative); err == nil {
			result.Alternatives = append(result.Alternatives, alternative)
		}
	}

	for key, value := range parsed.Satisfaction {
		if score, ok := normalizeFloat(value); ok {
			result.Satisfaction[key] = score
		}
	}
	result.OverallSatisfaction = parsed.OverallSatisfaction
	result.Tradeoffs = parsed.Tradeoffs
	result.Reasoning = parsed.Reasoning
	result.ModelConfidence = parsed.ModelConfidence

	log.Debug("Negotiate operation succeeded",
		"overallSatisfaction", result.OverallSatisfaction,
		"tradeoffs", len(result.Tradeoffs),
		"alternatives", len(result.Alternatives))

	return result, nil
}

// mergeNegotiateOptions merges user options with defaults
func mergeNegotiateOptions(defaults, user NegotiateOptions) NegotiateOptions {
	if user.Priorities != nil {
		defaults.Priorities = user.Priorities
	}
	if user.Constraints != nil {
		defaults.Constraints = user.Constraints
	}
	if user.MinSatisfaction > 0 {
		defaults.MinSatisfaction = user.MinSatisfaction
	}
	if user.MaxAlternatives > 0 {
		defaults.MaxAlternatives = user.MaxAlternatives
	}
	if user.Strategy != "" {
		defaults.Strategy = user.Strategy
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
	if user.Context != nil {
		defaults.Context = user.Context
	}
	return defaults
}

func normalizeFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// =============================================================================
// ADVERSARIAL NEGOTIATION API
// =============================================================================

// AdversarialPosition represents one party's position in a negotiation
type AdversarialPosition[T any] struct {
	// Position is the party's ideal/starting position
	Position T `json:"position"`

	// MustHaves are non-negotiable requirements
	MustHaves []string `json:"must_haves,omitempty"`

	// WalkAway is the point at which this party exits (BATNA)
	WalkAway map[string]any `json:"walk_away,omitempty"`
}

// AdversarialContext provides the negotiation dynamics between two parties
type AdversarialContext[T any] struct {
	// Ours is our party's position
	Ours AdversarialPosition[T] `json:"ours"`

	// Theirs is the other party's position
	Theirs AdversarialPosition[T] `json:"theirs"`

	// OurLeverage describes our bargaining power ("strong", "weak", "balanced")
	OurLeverage string `json:"our_leverage"`

	// Relationship is the negotiation style ("collaborative", "competitive", "mixed")
	Relationship string `json:"relationship,omitempty"`
}

// TermMovement tracks how a specific term moved during negotiation
type TermMovement struct {
	// Term is the name of the negotiated item
	Term string `json:"term"`

	// OurAsk is what we initially wanted
	OurAsk any `json:"our_ask"`

	// TheirOffer is what they initially offered
	TheirOffer any `json:"their_offer"`

	// FinalValue is the agreed-upon value
	FinalValue any `json:"final_value"`

	// Movement indicates who conceded ("we_concede", "they_concede", "split", "deadlock")
	Movement string `json:"movement"`
}

// AdversarialResult contains the outcome of an adversarial negotiation
type AdversarialResult[T any] struct {
	// Deal is the final negotiated agreement
	Deal T `json:"deal"`

	// DealReached indicates if parties reached agreement
	DealReached bool `json:"deal_reached"`

	// TermMovements shows per-term analysis of who moved
	TermMovements []TermMovement `json:"term_movements"`

	// WhoConcededMore summarizes overall concession balance
	WhoConcededMore string `json:"who_conceded_more"`

	// OurSatisfaction is how well our interests were served (0.0-1.0)
	OurSatisfaction float64 `json:"our_satisfaction"`

	// TheirSatisfaction is how well their interests were served (0.0-1.0)
	TheirSatisfaction float64 `json:"their_satisfaction"`

	// Reasoning explains the negotiation dynamics
	Reasoning string `json:"reasoning,omitempty"`

	// ModelConfidence in the result quality (0.0-1.0)
	ModelConfidence float64 `json:"confidence"`
}

// AdversarialOptions configures the adversarial negotiation
type AdversarialOptions struct {
	// Strategy guides the approach ("aggressive", "balanced", "accommodating")
	Strategy string

	// Common options
	Steering string

	// Mode is the reasoning approach. It was absent, so the fluent builder's
	// .Strict(), .TransformMode(), and .Creative() compiled, chained, and
	// changed nothing -- there was nowhere for the value to go (F-02).
	Mode          types.Mode
	Intelligence  types.Speed
	Context       gocontext.Context
	RequestID     string
	CorrelationID string
}

// NegotiateAdversarial conducts a two-party adversarial negotiation.
//
// This models real-world negotiations where two parties with opposing interests
// must find common ground. The leverage parameter determines who has more power
// and thus who should concede more.
//
// Type parameter T specifies the structure of positions and the final deal.
//
// Examples:
//
//	// Salary negotiation
//	type SalaryTerms struct {
//	    BaseSalary int `json:"base_salary"`
//	    RemoteDays int `json:"remote_days"`
//	    Bonus      int `json:"bonus"`
//	}
//	ctx := AdversarialContext[SalaryTerms]{
//	    Ours:        AdversarialPosition[SalaryTerms]{Position: SalaryTerms{BaseSalary: 160000, RemoteDays: 5, Bonus: 20000}},
//	    Theirs:      AdversarialPosition[SalaryTerms]{Position: SalaryTerms{BaseSalary: 130000, RemoteDays: 2, Bonus: 5000}},
//	    OurLeverage: "strong",
//	}
//	result, err := NegotiateAdversarial[SalaryTerms](ctx)
//	// result.Deal has the final terms
//	// result.TermMovements shows who moved on each term
//	// result.WhoConcededMore indicates "they" since we had strong leverage
func NegotiateAdversarial[T any](context AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	log := logger.GetLogger()
	log.Debug("Starting adversarial negotiation")

	var result AdversarialResult[T]

	// Apply defaults. Mode and Intelligence are taken verbatim when the caller
	// supplied options at all, because their zero values -- Strict and Smart --
	// are real choices. Testing `!= 0` to decide whether to copy them, as this
	// did, makes those two settings unrepresentable through the merge (F-01),
	// and RequestID and CorrelationID were not copied at all.
	opt := AdversarialOptions{
		Strategy:     "balanced",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
	if len(opts) > 0 {
		user := opts[0]
		opt.Mode = user.Mode
		opt.Intelligence = user.Intelligence
		opt.Steering = user.Steering
		opt.RequestID = user.RequestID
		opt.CorrelationID = user.CorrelationID
		if user.Strategy != "" {
			opt.Strategy = user.Strategy
		}
		if user.Context != nil {
			opt.Context = user.Context
		}
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = gocontext.Background()
	}
	ctx, cancel := gocontext.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Convert context to JSON
	contextJSON, err := json.Marshal(context)
	if err != nil {
		log.Error("Adversarial negotiation failed: marshal error", "error", err)
		return result, fmt.Errorf("failed to marshal context: %w", err)
	}

	// Get target type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	systemPrompt := fmt.Sprintf(`You are an expert negotiation analyst. Analyze this adversarial negotiation between two parties.

Strategy: %s

The input contains:
- "ours": Our party's position and requirements
- "theirs": Their party's position and requirements  
- "our_leverage": Our bargaining power (strong/weak/balanced)

CRITICAL: The leverage determines who concedes more:
- If our_leverage is "strong": THEY should concede more, final values closer to our position
- If our_leverage is "weak": WE should concede more, final values closer to their position
- If our_leverage is "balanced": Both parties move equally toward middle

Return a JSON object:
{
  "deal": %s,
  "deal_reached": true/false,
  "term_movements": [
    {"term": "field_name", "our_ask": value, "their_offer": value, "final_value": value, "movement": "we_concede|they_concede|split"}
  ],
  "who_conceded_more": "we|they|equal",
  "our_satisfaction": 0.0-1.0,
  "their_satisfaction": 0.0-1.0,
  "confidence": 0.0-1.0
}

Rules:
- "deal" must match the position schema
- "term_movements" must cover each field showing movement direction
- "movement" values: "we_concede" (we moved toward them), "they_concede" (they moved toward us), "split" (both moved)
- Leverage MUST influence the outcome - stronger party gets better terms`,
		opt.Strategy, typeSchema)

	steeringNote := ""
	if opt.Steering != "" {
		steeringNote = fmt.Sprintf("\n\nAdditional guidance: %s", opt.Steering)
	}

	userPrompt := fmt.Sprintf(`Analyze this adversarial negotiation and determine the final deal:

%s%s`, string(contextJSON), steeringNote)

	// Build OpOptions for LLM call
	opOpts := types.OpOptions{
		Mode:          opt.Mode,
		Intelligence:  opt.Intelligence,
		Context:       ctx,
		RequestID:     opt.RequestID,
		CorrelationID: opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Adversarial negotiation LLM call failed", "error", err)
		return result, fmt.Errorf("adversarial negotiation failed: %w", err)
	}

	// Parse response
	var parsed struct {
		Deal              json.RawMessage `json:"deal"`
		DealReached       bool            `json:"deal_reached"`
		TermMovements     []TermMovement  `json:"term_movements"`
		WhoConcededMore   string          `json:"who_conceded_more"`
		OurSatisfaction   float64         `json:"our_satisfaction"`
		TheirSatisfaction float64         `json:"their_satisfaction"`
		Reasoning         string          `json:"reasoning"`
		ModelConfidence   float64         `json:"confidence"`
	}

	if err := ParseJSONStrict(response, &parsed); err != nil {
		log.Error("Adversarial negotiation failed: parse error", "error", err, "response", response)
		return result, fmt.Errorf("failed to parse result: %w", err)
	}

	// Parse deal
	if len(parsed.Deal) > 0 {
		if err := json.Unmarshal(parsed.Deal, &result.Deal); err != nil {
			log.Error("Adversarial negotiation failed: deal parse error", "error", err)
			return result, fmt.Errorf("failed to parse deal: %w", err)
		}
	}

	result.DealReached = parsed.DealReached
	result.TermMovements = parsed.TermMovements
	result.WhoConcededMore = parsed.WhoConcededMore
	result.OurSatisfaction = parsed.OurSatisfaction
	result.TheirSatisfaction = parsed.TheirSatisfaction
	result.Reasoning = parsed.Reasoning
	result.ModelConfidence = parsed.ModelConfidence

	log.Debug("Adversarial negotiation succeeded",
		"dealReached", result.DealReached,
		"whoConceded", result.WhoConcededMore,
		"ourSatisfaction", result.OurSatisfaction)

	return result, nil
}
