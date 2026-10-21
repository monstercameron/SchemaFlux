// package ops - Procedural programming operations for control flow and state management
package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Decision represents a decision point with typed options
type Decision[T any] struct {
	Value       T
	Condition   func(any) bool
	Description string
	Priority    int
}

// DecisionResult contains the result of a decision operation
type DecisionResult struct {
	SelectedIndex int
	Explanation   string
	// ModelConfidence is the model's own claim about this result, not a measurement.
	// It is not calibrated and is not comparable across models or prompts.
	ModelConfidence float64
	Alternatives    []int

	// Fallback reports that the configured fallback index was selected because
	// the model could not be reached or its answer could not be used (see
	// DecideOptions.Fallback for the failures that count).
	// ModelConfidence is zero then: no model's answer was used.
	Fallback bool

	// ByCondition reports that the branch was taken because its own Condition
	// held, without asking a model. ModelConfidence is zero then too.
	ByCondition bool
}

// DecideOptions configures a decision. It carries the usual operation options
// plus the fallback policy, which has to be stated rather than assumed: a
// decision that silently takes branch zero when the provider is down is worse
// than one that fails.
type DecideOptions struct {
	types.OpOptions

	// Fallback names the index to select when the model could not be reached or
	// its answer could not be used: a failure of kind provider unavailable,
	// circuit open, rate limited or timeout, or of an invalid answer (malformed
	// output, a schema or invariant violation, a batch protocol or evidence
	// violation, repair exhausted). Nil means there is no fallback and the call
	// returns an error instead of guessing.
	//
	// Any other failure is returned and the fallback is not taken: a budget, a
	// policy, a cancellation, a configuration, authentication, permission or
	// invalid-request failure is something the caller has to act on, and
	// selecting a branch would hide it behind a decision that looks made. So is a
	// truncated answer, a context too large for the model, a refusal and a
	// recovered panic. A failure of the call that nothing classified is read as
	// a provider that could not be reached.
	Fallback *int
}

// NewDecideOptions returns options with no fallback: a failed decision is an
// error until the caller says otherwise.
func NewDecideOptions() DecideOptions {
	return DecideOptions{OpOptions: types.OpOptions{Intelligence: types.Fast, Mode: types.TransformMode}}
}

// WithFallback selects index when the decision cannot be made.
func (o DecideOptions) WithFallback(index int) DecideOptions {
	o.Fallback = &index
	return o
}

// WithIntelligence sets the quality/speed tier.
func (o DecideOptions) WithIntelligence(speed types.Speed) DecideOptions {
	o.Intelligence = speed
	return o
}

// WithMode sets the reasoning mode.
func (o DecideOptions) WithMode(mode types.Mode) DecideOptions {
	o.Mode = mode
	return o
}

// Decide chooses among decisions, first by the programmatic conditions and then
// by asking the model. situation is prompt data describing the circumstances;
// ctx is the real context governing cancellation.
func Decide[T any](ctx context.Context, situation any, decisions []Decision[T], opts ...DecideOptions) (T, DecisionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	log := loggerFor(ctx)
	log.Debug("Starting decide operation", "decisionsCount", len(decisions))

	var zero T
	result := DecisionResult{SelectedIndex: -1}

	if len(decisions) == 0 {
		log.Error("Decide operation failed: no decisions provided")
		return zero, result, types.Newf(types.KindInvalidRequest, "decide", "no decisions were provided")
	}

	var decideOpts DecideOptions
	if len(opts) > 0 {
		decideOpts = opts[0]
	}
	if decideOpts.Fallback != nil {
		if index := *decideOpts.Fallback; index < 0 || index >= len(decisions) {
			return zero, result, types.Newf(types.KindConfiguration, "decide",
				"the fallback index %d is out of range for %d decisions", index, len(decisions))
		}
	}

	// What no answer can be declared for is refused here, before the
	// conditions are looked at. After them, the same oversized request ran when
	// a condition happened to hold and was refused when none did.
	if len(decisions) > strictEnumBudget {
		return zero, result, types.Newf(types.KindConfiguration, "decide",
			"%d decisions is more than one choice can declare an answer for (at most %d); choose among them in smaller groups",
			len(decisions), strictEnumBudget)
	}

	// Report a fallback selection, or the error that made one necessary. Only
	// a failure the model could not recover from is covered; any other is the
	// caller's to see.
	fallback := func(cause error) (T, DecisionResult, error) {
		if decideOpts.Fallback == nil || !decideFallbackCovers(cause) {
			return zero, result, cause
		}
		index := *decideOpts.Fallback
		result.SelectedIndex = index
		result.Fallback = true
		result.Explanation = fmt.Sprintf("fallback selection: %v", cause)
		result.ModelConfidence = 0
		return decisions[index].Value, result, nil
	}

	// First check programmatic conditions
	for i, decision := range decisions {
		if decision.Condition != nil && decision.Condition(situation) {
			// No model was asked, so there is no model confidence to report.
			// This wrote 1.0 into ModelConfidence, a figure nobody gave.
			result.SelectedIndex = i
			result.ByCondition = true
			result.Explanation = fmt.Sprintf("Condition met for: %s", decision.Description)
			return decision.Value, result, nil
		}
	}

	// If no programmatic condition matches, use LLM for decision
	opt := applyDefaults(decideOpts.OpOptions)
	// operationContext, not WithTimeout: a deadline the caller set is kept.
	llmCtx, cancel := operationContext(ctx, config.GetTimeout())
	defer cancel()

	// The branches go to the model by number, with their descriptions; the
	// answer is declared, with the selection required (LX-024).
	descriptions := make([]string, len(decisions))
	for i, decision := range decisions {
		descriptions[i] = decision.Description
	}
	// A request that cannot be made is the caller's to fix, and is reported
	// with or without a fallback. The fallback is for a provider that fails
	// and an answer that cannot be used; taken here it would be taken on every
	// call, and the mistake behind it never seen.
	//
	// The situation is looked at here and not before the conditions: one that
	// only the caller's own conditions read never has to be shown to a model.
	shown, err := writtenSubject("decide", situation)
	if err != nil {
		return zero, result, err
	}
	userPrompt, err := decideUserPrompt(shown, descriptions)
	if err != nil {
		log.Warn("Decide operation could not write its request", "error", loggedError(err))
		return zero, result, err
	}
	opt.ResponseFormat = "json"
	opt.JSONSchema = decideSchema(len(decisions))
	opt.SchemaName = "decision"

	response, err := callLLM(llmCtx, decideSystemPrompt, userPrompt, opt)
	if err != nil {
		log.Warn("Decide operation LLM call failed", "error", loggedError(err))
		return fallback(err)
	}

	answer, err := readDecideAnswer(response, len(decisions))
	if err != nil {
		log.Warn("Decide operation response was not usable", "error", loggedError(err))
		return fallback(err)
	}

	result.SelectedIndex = answer.selected
	result.Explanation = answer.explanation
	result.ModelConfidence = answer.modelConfidence
	result.Alternatives = answer.alternatives
	log.Debug("Decide operation succeeded", "selectedIndex", answer.selected, "confidence", answer.modelConfidence)
	return decisions[answer.selected].Value, result, nil
}

// decideFallbackCovers reports whether a failure is one Decide's fallback
// stands in for: the model could not be reached (unavailable, circuit open,
// rate limited, timeout) or its answer could not be used (malformed output,
// schema, invariant or evidence violation, batch protocol violation, repair
// exhausted). It reads the kind the way the rest of the library does, so a
// bare context error is read for what it is.
//
// A failure of the model's call that nothing classified is a provider that
// could not be reached, and is read as one: it is how the call path has always
// treated it (it is retried as one), and a provider's own plain error is the
// usual case. Decide reads only the error of that call and of reading its
// answer, so nothing else can arrive here without a kind.
//
// Everything else is returned by Decide: a budget, a policy, a cancellation or
// a configuration failure would be hidden by a branch taken in its place. A
// recovered panic is an invariant violation by kind and a failure of the
// library itself by its mark, and is not an answer the model gave.
func decideFallbackCovers(err error) bool {
	if types.NarrowingOf(err) == types.NarrowPanic {
		return false
	}
	switch llm.Classify(err) {
	case types.KindUnknown, types.KindProviderUnavailable, types.KindCircuitOpen, types.KindRateLimited, types.KindTimeout,
		types.KindMalformedOutput, types.KindSchemaViolation, types.KindBatchProtocolViolation,
		types.KindInvariantViolation, types.KindEvidenceViolation, types.KindRepairExhausted:
		return true
	}
	return false
}

// GuardResult represents the result of a guard check
type GuardResult struct {
	CanProceed   bool
	FailedChecks []string
	Suggestions  []string
	RetryAfter   *time.Duration
}

// Guard runs the checks against state and reports which of them failed. It
// calls no model and reads ctx only to find the logger the client's lines go to;
// GuardWithSuggestions is the form that asks a model.
func Guard[T any](ctx context.Context, state T, checks ...func(T) (bool, string)) GuardResult {
	log := loggerFor(ctx)
	log.Debug("Starting guard operation", "checksCount", len(checks))

	result := GuardResult{
		CanProceed:   true,
		FailedChecks: []string{},
		Suggestions:  []string{},
	}

	for _, check := range checks {
		passed, message := check(state)
		if !passed {
			result.CanProceed = false
			result.FailedChecks = append(result.FailedChecks, message)
		}
	}

	// No provider call. A caller handing this function a list of Go predicates
	// has every reason to believe it runs Go predicates, and a model call here
	// would spend their money and send the failed-check messages, built from
	// their own state, to a provider they never named.
	return result
}

// GuardWithSuggestions runs the same checks as Guard and then asks a model how
// to fix the ones that failed.
//
// It exists as a separate function rather than an option because the
// difference between the two is whether a provider is called at all, and that
// is not a detail to bury in a struct field. The caller's own options are used
// -- their tier, their context and deadline -- rather than the Quick tier and
// two-second timeout Guard used to impose.
//
// The failed-check messages are sent to the provider. They are written by the
// caller's own check functions and may quote the caller's state, which is
// exactly why this is opt-in: see AGENTS.md on never sending the caller's
// payload anywhere they did not ask for.
//
// A failure to produce suggestions is not a failure of the guard. The verdict
// is already decided in Go by then, and returning an error would turn a
// provider outage into a blocked operation that Go had already approved.
func GuardWithSuggestions[T any](ctx context.Context, state T, opts types.OpOptions, checks ...func(T) (bool, string)) GuardResult {
	log := loggerFor(ctx)

	result := Guard(ctx, state, checks...)
	if result.CanProceed || len(result.FailedChecks) == 0 {
		return result
	}

	ctx, cancel := operationContext(ctx, config.GetTimeout())
	defer cancel()

	// The messages come from the caller's own check functions and may quote
	// their state, so they are written as a JSON list: a message with a newline
	// in it cannot start a line the model reads as an instruction.
	systemPrompt := "You are a helpful assistant. Suggest how to fix these issues."
	userPrompt := fmt.Sprintf("Issues: %s", parsePromptQuote(result.FailedChecks))

	response, err := callLLM(ctx, systemPrompt, userPrompt, opts)
	if err != nil {
		log.Warn("Guard suggestions unavailable; the verdict itself is unaffected", "error", loggedError(err))
		return result
	}

	for _, line := range strings.Split(response, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			result.Suggestions = append(result.Suggestions, trimmed)
		}
	}
	return result
}
