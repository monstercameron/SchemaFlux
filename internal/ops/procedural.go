// package ops - Procedural programming operations for control flow and state management
package ops

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/types"
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
	// the model could not be reached or its answer could not be used. It is the
	// only case in which ModelConfidence is not the model's own number.
	Fallback bool
}

// DecideOptions configures a decision. It carries the usual operation options
// plus the fallback policy, which has to be stated rather than assumed: a
// decision that silently takes branch zero when the provider is down is worse
// than one that fails.
type DecideOptions struct {
	types.OpOptions

	// Fallback names the index to select when the provider fails or returns an
	// answer that cannot be used. Nil means there is no fallback and the call
	// returns an error instead of guessing.
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
	log := logger.GetLogger()
	log.Debug("Starting decide operation", "decisionsCount", len(decisions))

	var zero T
	result := DecisionResult{SelectedIndex: -1}

	if len(decisions) == 0 {
		log.Error("Decide operation failed: no decisions provided")
		return zero, result, fmt.Errorf("decide: no decisions provided")
	}

	var decideOpts DecideOptions
	if len(opts) > 0 {
		decideOpts = opts[0]
	}
	if decideOpts.Fallback != nil {
		if index := *decideOpts.Fallback; index < 0 || index >= len(decisions) {
			return zero, result, fmt.Errorf("decide: fallback index %d is out of range for %d decisions", index, len(decisions))
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// Report a fallback selection, or the error that made one necessary.
	fallback := func(cause error) (T, DecisionResult, error) {
		if decideOpts.Fallback == nil {
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
			result.SelectedIndex = i
			result.ModelConfidence = 1.0
			result.Explanation = fmt.Sprintf("Condition met for: %s", decision.Description)
			return decision.Value, result, nil
		}
	}

	// If no programmatic condition matches, use LLM for decision
	opt := applyDefaults(decideOpts.OpOptions)
	llmCtx, cancel := context.WithTimeout(ctx, config.GetTimeout())
	defer cancel()

	// Prepare decision options for LLM
	var options []string
	for i, decision := range decisions {
		options = append(options, fmt.Sprintf("%d. %s", i, decision.Description))
	}

	systemPrompt := `You are a decision-making expert. Analyze the context and choose the best option.
Return a JSON object with:
{
  "selected": <index>,
  "explanation": "reason for selection",
  "confidence": 0.0-1.0,
  "alternatives": [other viable option indices]
}`

	userPrompt := fmt.Sprintf(`Context:
%v

Options:
%s

Choose the best option based on the context.`, situation, strings.Join(options, "\n"))

	response, err := callLLM(llmCtx, systemPrompt, userPrompt, opt)
	if err != nil {
		log.Warn("Decide operation LLM call failed", "error", err)
		return fallback(fmt.Errorf("decide: %w", err))
	}

	// Parse LLM response
	var llmResult struct {
		Selected        int     `json:"selected"`
		Explanation     string  `json:"explanation"`
		ModelConfidence float64 `json:"confidence"`
		Alternatives    []int   `json:"alternatives"`
	}

	if err := ParseJSONStrict(response, &llmResult); err != nil {
		log.Warn("Decide operation response was not usable", "error", err)
		return fallback(fmt.Errorf("decide: the model's answer could not be parsed: %w", err))
	}

	if llmResult.Selected < 0 || llmResult.Selected >= len(decisions) {
		log.Warn("Decide operation selected an out-of-range option", "selected", llmResult.Selected)
		return fallback(fmt.Errorf("decide: the model selected option %d, which is out of range for %d decisions", llmResult.Selected, len(decisions)))
	}

	result.SelectedIndex = llmResult.Selected
	result.Explanation = llmResult.Explanation
	result.ModelConfidence = llmResult.ModelConfidence
	result.Alternatives = llmResult.Alternatives
	log.Debug("Decide operation succeeded", "selectedIndex", llmResult.Selected, "confidence", llmResult.ModelConfidence)
	return decisions[llmResult.Selected].Value, result, nil
}

// GuardResult represents the result of a guard check
type GuardResult struct {
	CanProceed   bool
	FailedChecks []string
	Suggestions  []string
	RetryAfter   *time.Duration
}

// Guard checks if conditions are met before proceeding
// Guard checks conditions before proceeding, and asks the model for
// suggestions on whatever failed.
//
// ctx governs that call. Guard used to take no context at all, so the
// suggestion call could not be cancelled and ignored any deadline the caller
// had set.
func Guard[T any](ctx context.Context, state T, checks ...func(T) (bool, string)) GuardResult {
	log := logger.GetLogger()
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

	// Generate suggestions for failed checks using LLM if available
	if !result.CanProceed && len(result.FailedChecks) > 0 {
		ctx, cancel := operationContext(ctx, 2*time.Second)
		defer cancel()

		systemPrompt := "You are a helpful assistant. Suggest how to fix these issues."
		userPrompt := fmt.Sprintf("Issues:\n%s", strings.Join(result.FailedChecks, "\n"))

		opt := types.OpOptions{Intelligence: types.Quick}
		response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
		if err != nil {
			log.Warn("Guard operation LLM call failed, proceeding without suggestions", "error", err)
		} else {
			result.Suggestions = strings.Split(response, "\n")
			log.Debug("Guard operation succeeded", "suggestionsCount", len(result.Suggestions))
		}
	}

	return result
}

// StateMachine represents a finite state machine
type StateMachine[S comparable, E any] struct {
	Current     S
	States      map[S]StateDefinition[S, E]
	Transitions map[S]map[string]S // Current state -> Event type -> Next state
	History     []S
	mu          sync.RWMutex
}

// StateDefinition defines a state in the state machine
type StateDefinition[S comparable, E any] struct {
	Name    S
	OnEnter func() error
	OnExit  func() error
	Timeout *time.Duration
}

// NewStateMachine creates a new state machine
func NewStateMachine[S comparable, E any](initial S) *StateMachine[S, E] {
	return &StateMachine[S, E]{
		Current:     initial,
		States:      make(map[S]StateDefinition[S, E]),
		Transitions: make(map[S]map[string]S),
		History:     []S{initial},
	}
}

// AddState adds a state to the machine
func (sm *StateMachine[S, E]) AddState(state StateDefinition[S, E]) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.States[state.Name] = state
}

// AddTransition adds a transition rule
func (sm *StateMachine[S, E]) AddTransition(from S, eventType string, to S) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.Transitions[from] == nil {
		sm.Transitions[from] = make(map[string]S)
	}
	sm.Transitions[from][eventType] = to
}

// Transition attempts to transition based on an event
func (sm *StateMachine[S, E]) Transition(event E) (S, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	eventType := reflect.TypeOf(event).Name()
	if eventType == "" {
		eventType = fmt.Sprintf("%T", event)
	}

	// Check if transition exists
	if transitions, ok := sm.Transitions[sm.Current]; ok {
		if nextState, ok := transitions[eventType]; ok {
			// Execute exit handler for current state
			if state, exists := sm.States[sm.Current]; exists && state.OnExit != nil {
				if err := state.OnExit(); err != nil {
					return sm.Current, fmt.Errorf("exit handler failed: %w", err)
				}
			}

			// Transition to new state
			sm.Current = nextState
			sm.History = append(sm.History, nextState)

			// Execute enter handler for new state
			if state, exists := sm.States[nextState]; exists && state.OnEnter != nil {
				if err := state.OnEnter(); err != nil {
					return sm.Current, fmt.Errorf("enter handler failed: %w", err)
				}
			}

			return nextState, nil
		}
	}

	return sm.Current, fmt.Errorf("no transition from %v for event %s", sm.Current, eventType)
}

// GetHistory returns the state transition history
func (sm *StateMachine[S, E]) GetHistory() []S {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	history := make([]S, len(sm.History))
	copy(history, sm.History)
	return history
}

// RetryStrategy defines how to retry operations
type RetryStrategy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
}

// WithRetry executes an operation with retry logic
func WithRetry[T any](operation func() (T, error), strategy RetryStrategy) (T, error) {
	var zero T
	var lastErr error

	delay := strategy.InitialDelay

	for attempt := 0; attempt < strategy.MaxAttempts; attempt++ {
		result, err := operation()
		if err == nil {
			return result, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			return zero, err
		}

		if attempt < strategy.MaxAttempts-1 {
			time.Sleep(delay)

			// Calculate next delay
			delay = time.Duration(float64(delay) * strategy.Multiplier)
			if delay > strategy.MaxDelay {
				delay = strategy.MaxDelay
			}
		}
	}

	return zero, fmt.Errorf("operation failed after %d attempts: %w", strategy.MaxAttempts, lastErr)
}

func isRetryableError(err error) bool {
	// More comprehensive check for retryable errors.
	s := strings.ToLower(err.Error())
	retryableSubstrings := []string{
		"timeout", "temporary", "connection reset", "connection refused",
		"i/o timeout", "rate limit", "throttled", "try again later",
		"service unavailable", "503", "429", "504",
	}

	for _, sub := range retryableSubstrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// LoopWhile executes a function while a condition is true
func LoopWhile[T any](
	state T,
	condition func(T) bool,
	body func(T) (T, error),
	maxIterations int,
) (T, error) {
	iterations := 0
	current := state

	for condition(current) && iterations < maxIterations {
		next, err := body(current)
		if err != nil {
			return current, fmt.Errorf("loop body failed at iteration %d: %w", iterations, err)
		}
		current = next
		iterations++
	}

	if iterations >= maxIterations {
		return current, fmt.Errorf("max iterations (%d) reached", maxIterations)
	}

	return current, nil
}

// Switch provides multi-way branching with typed returns
func Switch[T comparable, R any](value T, cases map[T]func() R, defaultCase func() R) R {
	if fn, ok := cases[value]; ok {
		return fn()
	}
	if defaultCase != nil {
		return defaultCase()
	}
	var zero R
	return zero
}

// IfElse provides conditional execution with typed returns
func IfElse[T any](condition bool, ifTrue func() T, ifFalse func() T) T {
	if condition {
		return ifTrue()
	}
	return ifFalse()
}

// Try provides exception-like error handling
func Try[T any](operation func() (T, error)) (result T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
		}
	}()

	return operation()
}

// Workflow represents a complex multi-step workflow
type Workflow struct {
	Name  string
	Steps []WorkflowStep
	State map[string]any
	mu    sync.RWMutex
}

// WorkflowStep represents a step in a workflow
type WorkflowStep struct {
	Name         string
	Execute      func(context.Context, map[string]any) error
	Compensate   func(map[string]any) error // Rollback action
	CanRetry     bool
	MaxRetries   int
	Dependencies []string // Names of steps that must complete first
}

// NewWorkflow creates a new workflow
func NewWorkflow(name string) *Workflow {
	return &Workflow{
		Name:  name,
		Steps: []WorkflowStep{},
		State: make(map[string]any),
	}
}

// AddStep adds a step to the workflow
func (w *Workflow) AddStep(step WorkflowStep) *Workflow {
	w.Steps = append(w.Steps, step)
	return w
}

// Execute runs the workflow
func (w *Workflow) Execute(ctx context.Context) error {
	completed := make(map[string]bool)

	for _, step := range w.Steps {
		// Check dependencies
		for _, dep := range step.Dependencies {
			if !completed[dep] {
				return fmt.Errorf("dependency %s not met for step %s", dep, step.Name)
			}
		}

		// Execute step with retry
		attempts := 1
		if step.CanRetry && step.MaxRetries > 0 {
			attempts = step.MaxRetries
		}

		var stepErr error
		for attempt := 0; attempt < attempts; attempt++ {
			stepErr = step.Execute(ctx, w.State)
			if stepErr == nil {
				completed[step.Name] = true
				break
			}

			if attempt < attempts-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
			}
		}

		if stepErr != nil {
			// Execute compensation for completed steps in reverse order
			for i := len(w.Steps) - 1; i >= 0; i-- {
				if completed[w.Steps[i].Name] && w.Steps[i].Compensate != nil {
					_ = w.Steps[i].Compensate(w.State)
				}
			}
			return fmt.Errorf("step %s failed: %w", step.Name, stepErr)
		}
	}

	return nil
}

// SetState sets a value in the workflow state
func (w *Workflow) SetState(key string, value any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.State[key] = value
}

// GetState gets a value from the workflow state
func (w *Workflow) GetState(key string) (any, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	val, ok := w.State[key]
	return val, ok
}
