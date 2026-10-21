// Negotiate operation for reconciling competing constraints.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// NegotiateOptions configures the Negotiate operation
type NegotiateOptions struct {
	// Priorities maps constraint names to their importance weights (0.0-1.0)
	Priorities map[string]float64

	// Constraints as natural language requirements
	Constraints []string

	// MinSatisfaction is the minimum acceptable satisfaction score, a number
	// from 0 to 1. An answer whose overall satisfaction is below it is refused.
	// Zero is the unset value and means the default of 0.6, so a floor of
	// nothing cannot be asked for; a caller who wants the floor out of the way
	// sets a value any score meets, such as 0.001. A NaN, an infinity or a
	// number outside 0 to 1 is refused before the model is asked.
	MinSatisfaction float64

	// MaxAlternatives limits the number of alternative solutions returned. Zero
	// is the unset value and means the default of 3; a negative number is
	// refused before the model is asked.
	MaxAlternatives int

	// Strategy guides the negotiation approach ("balanced", "maximize_primary",
	// "pareto"). It is guidance to the model, written into the prompt as a quoted
	// string; Go does not check the answer against it.
	Strategy string

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

	// Metadata contains additional operation information. Settle does not write
	// to it.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Negotiate reconciles competing constraints to find an optimal typed solution.
//
// It is one model round trip: the constraints go in, a single response comes
// back parsed into T, Tradeoffs, and Reasoning. Nothing about the name
// implies otherwise on the wire -- there is no back-and-forth with the model
// and no session the model's answer can be revised inside. A caller that
// wants an actual multi-turn negotiation (propose, get pushback, revise) has
// to build that loop itself, one Negotiate call per turn; Session in
// session.go is the transcript primitive for doing that against
// llm.Provider directly, but this operation does not use it (PS-005,
// Revised ARC-20).
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
//
// Deprecated: use Settle. This operation takes a set of constraints and
// produces a settlement in a single model round trip; it does not negotiate,
// and a conversational name on a one-round-trip implementation is the thing
// PS-005 forbids. Settle is the same operation under a name that says so.
func Negotiate[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	return Settle[T](constraints, opts...)
}

// Settle produces a settlement satisfying the given constraints, in one model
// round trip. For an actual multi-turn exchange, drive a Session yourself: the
// transcript shape exists (session.go) precisely because this operation is not
// one.
//
// What the answer has to satisfy, beyond being the right shape: it carries a
// solution, a satisfaction for each constraint it names, an overall
// satisfaction and a confidence, each a number from 0 to 1; every alternative
// fits T; the overall satisfaction meets MinSatisfaction; and there are no more
// alternatives than MaxAlternatives. An answer that breaks one is refused, and
// the result beside the error is empty. Strategy and the Pareto-optimality of
// the alternatives are guidance to the model: Go cannot verify either.
//
// The options are one value; a second one is a configuration error, as it is
// for Audit and Arbitrate.
func Settle[T any](constraints any, opts ...NegotiateOptions) (NegotiateResult[T], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting negotiate operation")

	if err := checkOneOptionsValue("negotiate", len(opts)); err != nil {
		return NegotiateResult[T]{}, err
	}

	// Apply defaults
	opt := NegotiateOptions{
		MinSatisfaction: 0.6,
		MaxAlternatives: 3,
		Strategy:        "balanced",
		Mode:            types.TransformMode,
		Intelligence:    types.Fast,
	}
	if len(opts) > 0 {
		// The caller's numbers are looked at before the merge, which drops one
		// that is not above zero: a NaN or a negative value would otherwise be
		// taken for "not set" and replaced by the default without a word.
		if err := checkNegotiateOptions(opts[0]); err != nil {
			return NegotiateResult[T]{}, err
		}
		opt = mergeNegotiateOptions(opt, opts[0])
	}
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return NegotiateResult[T]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// operationContext, not WithTimeout: a deadline the caller set is kept.
	ctx, cancel := operationContext(ctx, config.GetTimeout())
	defer cancel()

	// Convert constraints to JSON
	constraintsJSON, err := promptJSON(constraints)
	if err != nil {
		log.Error("Negotiate operation failed: marshal error", "error", loggedError(err))
		return NegotiateResult[T]{}, types.Newf(types.KindInvalidRequest, "negotiate", "the constraints could not be written as JSON")
	}

	// Get target type schema
	var zero T
	typeSchema := GenerateTypeSchema(reflect.TypeOf(zero))

	// Build priorities description. The names are the caller's text: they are
	// written as JSON strings, and in the order of their names, so that the same
	// options give the same prompt.
	prioritiesDesc := ""
	if len(opt.Priorities) > 0 {
		var parts []string
		for _, name := range sortedKeys(opt.Priorities) {
			parts = append(parts, fmt.Sprintf("%s (weight: %.2f)", quotedText(name), opt.Priorities[name]))
		}
		prioritiesDesc = fmt.Sprintf("\n\nPriorities:\n%s", strings.Join(parts, "\n"))
	}

	// Build constraints description
	constraintsDesc := ""
	if len(opt.Constraints) > 0 {
		quoted := make([]string, len(opt.Constraints))
		for i, constraint := range opt.Constraints {
			quoted[i] = quotedText(constraint)
		}
		constraintsDesc = fmt.Sprintf("\n\nAdditional constraints:\n- %s", strings.Join(quoted, "\n- "))
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
		quotedText(opt.Strategy), opt.MinSatisfaction*100, prioritiesDesc, constraintsDesc, typeSchema, opt.MaxAlternatives)

	userPrompt := fmt.Sprintf(`Find the optimal solution that balances these constraints:

%s`, string(constraintsJSON))

	// Build OpOptions for LLM call. The steering goes through the options and
	// not into the message by hand, so that the caller's text is delimited as
	// the caller's.
	opOpts := types.OpOptions{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Steering:        opt.Steering,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Negotiate operation LLM call failed", "error", loggedError(err))
		return NegotiateResult[T]{}, fmt.Errorf("negotiation failed: %w", err)
	}

	result, err := readSettleAnswer[T](response, opt)
	if err != nil {
		// The raw response is deliberately NOT logged. It can carry
		// caller-derived content, and AGENTS.md forbids putting the caller's
		// payload in a log line.
		log.Error("Negotiate operation refused the answer", "error", loggedError(err))
		return NegotiateResult[T]{}, err
	}

	log.Debug("Negotiate operation succeeded",
		"overallSatisfaction", result.OverallSatisfaction,
		"tradeoffs", len(result.Tradeoffs),
		"alternatives", len(result.Alternatives))

	return result, nil
}

// readSettleAnswer reads the model's answer to a settlement request and checks
// it against the options the request was made under.
func readSettleAnswer[T any](response string, opt NegotiateOptions) (NegotiateResult[T], error) {
	const op = "negotiate"

	answer, err := readAnswerObject(op, response)
	if err != nil {
		return NegotiateResult[T]{}, err
	}

	var result NegotiateResult[T]
	if err := answer.decode("solution", &result.Solution); err != nil {
		return NegotiateResult[T]{}, err
	}

	alternatives, err := answer.elements("alternatives")
	if err != nil {
		return NegotiateResult[T]{}, err
	}
	for i, raw := range alternatives {
		if isJSONNull(raw) {
			return NegotiateResult[T]{}, answer.refuse("alternative %d is null", i)
		}
		var alternative T
		if err := json.Unmarshal(raw, &alternative); err != nil {
			// An alternative of the wrong shape is a failure of the answer,
			// not an entry to leave out of it.
			return NegotiateResult[T]{}, answer.refuse("alternative %d does not fit the solution type", i)
		}
		result.Alternatives = append(result.Alternatives, alternative)
	}

	var satisfaction map[string]json.RawMessage
	if err := answer.decode("satisfaction", &satisfaction); err != nil {
		return NegotiateResult[T]{}, err
	}
	result.Satisfaction = make(map[string]float64, len(satisfaction))
	for _, name := range sortedKeys(satisfaction) {
		score, ok := fractionOf(satisfaction[name])
		if !ok {
			return NegotiateResult[T]{}, answer.refuse("an entry of \"satisfaction\" is not a number from 0 to 1")
		}
		result.Satisfaction[name] = score
	}

	if result.OverallSatisfaction, err = answer.fraction("overall_satisfaction"); err != nil {
		return NegotiateResult[T]{}, err
	}
	if result.ModelConfidence, err = answer.fraction("confidence"); err != nil {
		return NegotiateResult[T]{}, err
	}
	if result.Reasoning, err = answer.text("reasoning"); err != nil {
		return NegotiateResult[T]{}, err
	}

	tradeoffs, err := answer.elements("tradeoffs")
	if err != nil {
		return NegotiateResult[T]{}, err
	}
	for i, raw := range tradeoffs {
		var fields struct {
			Sacrificed *string `json:"sacrificed"`
			Gained     *string `json:"gained"`
			Impact     *string `json:"impact"`
			Reasoning  string  `json:"reasoning"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil || fields.Sacrificed == nil || fields.Gained == nil || fields.Impact == nil {
			return NegotiateResult[T]{}, answer.refuse("tradeoff %d does not say what was sacrificed, what was gained and the impact", i)
		}
		result.Tradeoffs = append(result.Tradeoffs, Tradeoff{
			Sacrificed: *fields.Sacrificed,
			Gained:     *fields.Gained,
			Impact:     *fields.Impact,
			Reasoning:  fields.Reasoning,
		})
	}

	// MinSatisfaction was stated in the prompt as "Minimum acceptable
	// satisfaction: %.0f%%" and compared against nothing, so a model that openly
	// reported failing it -- 0.1 against a 0.9 floor -- still came back as a
	// usable result with a nil error. The number is the model's claim, but the
	// floor is the caller's, and checking one against the other is what makes
	// the option mean what its name says.
	if opt.MinSatisfaction > 0 && result.OverallSatisfaction < opt.MinSatisfaction {
		return NegotiateResult[T]{}, types.Newf(types.KindInvariantViolation, op,
			"the model reported overall satisfaction %.2f, below the %.2f floor this operation was configured with",
			result.OverallSatisfaction, opt.MinSatisfaction)
	}

	// MaxAlternatives was described in the prompt as "up to %d" and never
	// counted.
	if err := AtMost(result.Alternatives, opt.MaxAlternatives); err != nil {
		return NegotiateResult[T]{}, types.Newf(types.KindInvariantViolation, op, "%v", err)
	}

	return result, nil
}

// checkOneOptionsValue refuses a call that was given more than one options
// value. The options are one value: the variadic form is there so that they can
// be left out, and a second value was ignored without a word. It is the rule
// Audit and Arbitrate state in the same words.
func checkOneOptionsValue(op string, given int) error {
	if given > 1 {
		return types.Newf(types.KindConfiguration, op,
			"%d option values were given, and only one is read", given)
	}
	return nil
}

// checkNegotiateOptions refuses a number in the caller's options that no
// reading of the option could use. Zero is the value of an option that was not
// set and is accepted: it stands for the default. The error names the option
// and not the value.
func checkNegotiateOptions(user NegotiateOptions) error {
	if math.IsNaN(user.MinSatisfaction) || user.MinSatisfaction < 0 || user.MinSatisfaction > 1 {
		return types.Newf(types.KindConfiguration, "negotiate", "MinSatisfaction has to be a number from 0 to 1")
	}
	if user.MaxAlternatives < 0 {
		return types.Newf(types.KindConfiguration, "negotiate", "MaxAlternatives cannot be negative")
	}
	return nil
}

// mergeNegotiateOptions merges user options with defaults: an option the caller
// left at its zero value keeps its default. The numbers are checked by
// checkNegotiateOptions before this runs, so what is dropped here is only zero.
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
	if user.Mode != types.ModeUnset {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != types.TierUnset {
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

// NegotiateAdversarial conducts a two-party adversarial negotiation.
//
// This models real-world negotiations where two parties with opposing interests
// must find common ground. The leverage parameter determines who has more power
// and thus who should concede more.
//
// It is, like Negotiate, one model round trip -- both positions and the
// leverage go in together, and a single response comes back with the whole
// deal already settled. There is no actual back-and-forth between "our" and
// "their" side across separate calls, despite the two-party framing. See
// Negotiate's doc comment for where a real multi-turn transcript would live
// (Session, session.go) if a caller needs one.
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
//
// Deprecated: use SettleAdversarial. One model round trip, not an exchange.
// See PS-005.
func NegotiateAdversarial[T any](negotiation AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	return SettleAdversarial(negotiation, opts...)
}

// SettleAdversarial produces a settlement between opposed positions in one
// model round trip.
//
// What the answer has to satisfy, beyond being the right shape: it carries a
// deal that fits T, a deal_reached flag, the term movements, and the two
// satisfactions and the confidence as numbers from 0 to 1. An answer that
// leaves one out, or gives it in another kind, is refused, and the result
// beside the error is empty. How much each party conceded is the model's
// account and is not checked against the leverage.
//
// The options are one value; a second one is a configuration error.
func SettleAdversarial[T any](negotiation AdversarialContext[T], opts ...AdversarialOptions) (AdversarialResult[T], error) {
	// The first line is written before the options are looked at, so the client is
	// found from the context they carry.
	var callerCtx context.Context
	if len(opts) > 0 {
		callerCtx = opts[0].Context
	}
	log := loggerFor(callerCtx)
	log.Debug("Starting adversarial negotiation")

	if err := checkOneOptionsValue("negotiate", len(opts)); err != nil {
		return AdversarialResult[T]{}, err
	}

	// Apply defaults. A field the caller left unset keeps its default, by the
	// same rule Settle uses.
	opt := AdversarialOptions{
		Strategy:     "balanced",
		Mode:         types.TransformMode,
		Intelligence: types.Fast,
	}
	if len(opts) > 0 {
		opt = mergeAdversarialOptions(opt, opts[0])
	}
	if err := checkFlatLockedLimits(opt.Context, opt.Mode, opt.Intelligence, opt.MaxOutputTokens); err != nil {
		return AdversarialResult[T]{}, err
	}

	// Get context
	ctx := opt.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// operationContext, not WithTimeout: a deadline the caller set is kept.
	ctx, cancel := operationContext(ctx, config.GetTimeout())
	defer cancel()

	// Convert the negotiation to JSON
	negotiationJSON, err := promptJSON(negotiation)
	if err != nil {
		log.Error("Adversarial negotiation failed: marshal error", "error", loggedError(err))
		return AdversarialResult[T]{}, types.Newf(types.KindInvalidRequest, "negotiate", "the negotiation could not be written as JSON")
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
		quotedText(opt.Strategy), typeSchema)

	userPrompt := fmt.Sprintf(`Analyze this adversarial negotiation and determine the final deal:

%s`, string(negotiationJSON))

	// Build OpOptions for LLM call. The steering goes through the options and
	// not into the message by hand, so that the caller's text is delimited as
	// the caller's.
	opOpts := types.OpOptions{
		Mode:            opt.Mode,
		Intelligence:    opt.Intelligence,
		Model:           opt.Model,
		Effort:          opt.Effort,
		MaxOutputTokens: opt.MaxOutputTokens,
		WebSearch:       opt.WebSearch,
		Steering:        opt.Steering,
		Context:         ctx,
		RequestID:       opt.RequestID,
		CorrelationID:   opt.CorrelationID,
	}

	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		log.Error("Adversarial negotiation LLM call failed", "error", loggedError(err))
		return AdversarialResult[T]{}, fmt.Errorf("adversarial negotiation failed: %w", err)
	}

	result, err := readAdversarialAnswer[T](response)
	if err != nil {
		// See Settle: the response body stays out of the log.
		log.Error("Adversarial negotiation refused the answer", "error", loggedError(err))
		return AdversarialResult[T]{}, err
	}

	log.Debug("Adversarial negotiation succeeded",
		"dealReached", result.DealReached,
		"ourSatisfaction", result.OurSatisfaction)

	return result, nil
}

// readAdversarialAnswer reads the model's answer to an adversarial negotiation.
func readAdversarialAnswer[T any](response string) (AdversarialResult[T], error) {
	answer, err := readAnswerObject("negotiate", response)
	if err != nil {
		return AdversarialResult[T]{}, err
	}

	var result AdversarialResult[T]
	if err := answer.decode("deal", &result.Deal); err != nil {
		return AdversarialResult[T]{}, err
	}
	if result.DealReached, err = answer.flag("deal_reached"); err != nil {
		return AdversarialResult[T]{}, err
	}
	if result.WhoConcededMore, err = answer.text("who_conceded_more"); err != nil {
		return AdversarialResult[T]{}, err
	}
	if result.OurSatisfaction, err = answer.fraction("our_satisfaction"); err != nil {
		return AdversarialResult[T]{}, err
	}
	if result.TheirSatisfaction, err = answer.fraction("their_satisfaction"); err != nil {
		return AdversarialResult[T]{}, err
	}
	if result.ModelConfidence, err = answer.fraction("confidence"); err != nil {
		return AdversarialResult[T]{}, err
	}
	// The prompt's JSON shape does not list the reasoning, so it is read when
	// the model gives it and is not required.
	if err := answer.decodeOptional("reasoning", &result.Reasoning); err != nil {
		return AdversarialResult[T]{}, err
	}

	movements, err := answer.elements("term_movements")
	if err != nil {
		return AdversarialResult[T]{}, err
	}
	for i, raw := range movements {
		var fields struct {
			Term       *string `json:"term"`
			OurAsk     any     `json:"our_ask"`
			TheirOffer any     `json:"their_offer"`
			FinalValue any     `json:"final_value"`
			Movement   *string `json:"movement"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil || fields.Term == nil || fields.Movement == nil {
			return AdversarialResult[T]{}, answer.refuse("term movement %d does not name its term and its movement", i)
		}
		result.TermMovements = append(result.TermMovements, TermMovement{
			Term:       *fields.Term,
			OurAsk:     fields.OurAsk,
			TheirOffer: fields.TheirOffer,
			FinalValue: fields.FinalValue,
			Movement:   *fields.Movement,
		})
	}
	return result, nil
}

// mergeAdversarialOptions merges user options with defaults by the rule
// mergeNegotiateOptions uses: a field the caller left unset keeps its default.
// It used to take Mode and Intelligence from the caller whatever they held, so
// an options value that carried only a Steering replaced the Fast tier with
// the unset one.
func mergeAdversarialOptions(defaults, user AdversarialOptions) AdversarialOptions {
	if user.Strategy != "" {
		defaults.Strategy = user.Strategy
	}
	if user.Steering != "" {
		defaults.Steering = user.Steering
	}
	if user.Mode != types.ModeUnset {
		defaults.Mode = user.Mode
	}
	if user.Intelligence != types.TierUnset {
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
