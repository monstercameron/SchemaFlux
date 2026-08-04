package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

var defaultProvider llm.Provider

// LLMCaller is the function type for calling the LLM
type LLMCaller func(ctx context.Context, system, user string, opts types.OpOptions) (string, error)

// Custom LLM caller for testing
var customLLMCaller LLMCaller

// setLLMCaller sets a custom LLM caller (for testing)
func setLLMCaller(caller LLMCaller) {
	customLLMCaller = caller
}

// SetDefaultProvider sets the default LLM provider for operations
func SetDefaultProvider(p llm.Provider) {
	defaultProvider = p
}

// callLLM executes an LLM request using the default provider
func callLLM(ctx context.Context, systemPrompt, userPrompt string, opts types.OpOptions) (string, error) {
	// Use custom caller if set (for testing)
	if customLLMCaller != nil {
		return customLLMCaller(ctx, systemPrompt, userPrompt, opts)
	}

	if defaultProvider == nil {
		// Try to initialize a default provider (e.g. OpenAI from env)
		// For now, just return error if not set
		return "", fmt.Errorf("no LLM provider configured")
	}
	return CallLLM(ctx, defaultProvider, systemPrompt, userPrompt, opts)
}

// CallLLM executes an LLM request using the provided provider
func CallLLM(ctx context.Context, provider llm.Provider, systemPrompt, userPrompt string, opts types.OpOptions) (string, error) {
	log := logger.GetLogger()

	// Determine model
	model := config.GetModel(opts.Intelligence, provider.Name())
	maxTokens := config.GetMaxTokens(opts.Intelligence)
	temperature := config.GetTemperature(opts.Mode)
	effectiveSystemPrompt := applySteering(systemPrompt, opts.Steering)
	responseFormat := inferResponseFormat(effectiveSystemPrompt, userPrompt)

	req := llm.CompletionRequest{
		Model:          model,
		SystemPrompt:   strengthenSystemPrompt(effectiveSystemPrompt, responseFormat),
		UserPrompt:     userPrompt,
		Temperature:    float64(temperature),
		MaxTokens:      maxTokens,
		ResponseFormat: responseFormat,
	}

	start := time.Now()
	ctx, tracking := requesttracking.Ensure(ctx, opts.RequestID, opts.CorrelationID)
	requestID := tracking.RequestID
	correlationID := tracking.CorrelationID
	opts.RequestID = requestID
	opts.CorrelationID = correlationID

	log.Debug("LLM request started",
		"requestID", requestID,
		"correlationID", correlationID,
		"provider", provider.Name(),
		"model", model,
		"responseFormat", responseFormat,
		"maxTokens", maxTokens,
		"mode", opts.Mode.String(),
		"intelligence", opts.Intelligence.String(),
	)

	maxRetries, retryBackoff := provider.RetryPolicy()
	if maxRetries <= 0 {
		maxRetries = config.GetLLMMaxRetries()
	}
	if retryBackoff <= 0 {
		retryBackoff = config.GetLLMRetryBackoff()
	}

	attempts := maxRetries + 1
	var (
		resp llm.CompletionResponse
		err  error
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err = provider.Complete(ctx, req)
		if err == nil {
			if validationErr := validateLLMCompletion(resp); validationErr != nil {
				err = validationErr
			}
		}

		if err == nil {
			break
		}

		if attempt == attempts || !isRetryableLLMError(err) {
			log.Error("LLM request failed",
				"requestID", requestID,
				"correlationID", correlationID,
				"provider", provider.Name(),
				"model", model,
				"responseFormat", responseFormat,
				"attempt", attempt,
				"maxAttempts", attempts,
				"duration_ms", time.Since(start).Milliseconds(),
				"error", err,
			)
			return "", err
		}

		delay := retryDelay(retryBackoff, attempt)
		log.Warn("LLM request retry scheduled",
			"requestID", requestID,
			"correlationID", correlationID,
			"provider", provider.Name(),
			"model", model,
			"responseFormat", responseFormat,
			"attempt", attempt,
			"nextAttempt", attempt+1,
			"backoff_ms", delay.Milliseconds(),
			"error", err,
		)

		if sleepErr := waitForRetry(ctx, delay); sleepErr != nil {
			return "", sleepErr
		}
	}

	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}

	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}

	usage := resp.Usage
	cost := pricing.CalculateCost(&usage, actualModel, actualProvider)
	metadata := &types.ResultMetadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
		TraceID:       telemetry.GetTraceID(ctx),
		SpanID:        telemetry.GetSpanID(ctx),
		StartTime:     start,
		EndTime:       time.Now(),
		Duration:      time.Since(start),
		Model:         actualModel,
		Provider:      actualProvider,
		Mode:          opts.Mode,
		Intelligence:  opts.Intelligence,
		TokenUsage:    &usage,
		CostInfo:      cost,
		Custom: map[string]any{
			"response_format": responseFormat,
		},
	}

	pricing.TrackCost(cost, metadata)
	telemetry.RecordLLMMetrics(metadata)

	log.Info("LLM request completed",
		"requestID", requestID,
		"correlationID", correlationID,
		"provider", actualProvider,
		"model", actualModel,
		"responseFormat", responseFormat,
		"duration_ms", metadata.Duration.Milliseconds(),
		"tokens_total", usage.TotalTokens,
		"cost_usd", cost.TotalCost,
		"finishReason", resp.FinishReason,
	)

	return resp.Content, nil
}

func validateLLMCompletion(resp llm.CompletionResponse) error {
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("provider returned empty completion content")
	}
	return nil
}

func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	msg := strings.ToLower(err.Error())

	nonRetryable := []string{
		"api key is required",
		"unauthorized",
		"forbidden",
		"invalid api key",
		"invalid_request_error",
		"status 400",
		"status 401",
		"status 403",
		"status 404",
		"status 422",
	}
	for _, needle := range nonRetryable {
		if strings.Contains(msg, needle) {
			return false
		}
	}

	retryable := []string{
		"timeout",
		"temporary",
		"connection reset",
		"connection refused",
		"i/o timeout",
		"rate limit",
		"throttled",
		"try again later",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"empty response",
		"incomplete",
		"no completion choices",
		"provider returned empty completion content",
		"status 408",
		"status 409",
		"status 429",
		"status 500",
		"status 502",
		"status 503",
		"status 504",
	}
	for _, needle := range retryable {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	if attempt <= 1 {
		return base
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > 5*time.Second {
			return 5 * time.Second
		}
	}
	return delay
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func applySteering(systemPrompt, steering string) string {
	steering = strings.TrimSpace(steering)
	if steering == "" {
		return systemPrompt
	}
	return strings.TrimSpace(systemPrompt + "\n\nAdditional instructions:\n" + steering)
}

func inferResponseFormat(systemPrompt, userPrompt string) string {
	combined := strings.ToLower(systemPrompt + "\n" + userPrompt)
	jsonSignals := []string{
		"return a json object",
		"return a json array",
		"return only valid json",
		"return only json",
		"valid json",
		"json object",
		"json array",
		"matching the schema",
	}
	for _, signal := range jsonSignals {
		if strings.Contains(combined, signal) {
			return "json"
		}
	}
	return "text"
}

func strengthenSystemPrompt(systemPrompt, responseFormat string) string {
	baseRules := strings.TrimSpace(`Perform the semantic task faithfully using the provided input.
Do not merely restate schemas, field names, or type descriptions.
Infer, compare, rank, validate, transform, or summarize based on the actual content.`)

	if responseFormat != "json" {
		return strings.TrimSpace(baseRules + "\n\n" + systemPrompt)
	}

	jsonRules := strings.TrimSpace(`After reasoning about the task, return only the final JSON answer.
Do not include markdown fences, prose, placeholders, or schema descriptions.
Every field must be populated with task-relevant values supported by the input or clearly inferred from it.`)

	return strings.TrimSpace(baseRules + "\n\n" + jsonRules + "\n\n" + systemPrompt)
}
