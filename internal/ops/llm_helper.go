package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/pricing"
	"github.com/monstercameron/schemaflux/telemetry"
)

// providerMu guards the two package-level hooks below.
//
// They are written by SetDefaultProvider (which every client construction and
// every schemafluxtest.Install calls) and read by every operation in the
// library, so a test that installs a fake while another goroutine runs an
// operation is a data race — one the race detector could not see on the machine
// this was written on, because -race does not run on windows/arm64 (CI-002).
//
// The lock is a stopgap, not the design. Two clients still cannot hold
// different providers, because there is only one of these. That is IN-004.
var providerMu sync.RWMutex

var defaultProvider llm.Provider

// ErrNoProvider is returned when an operation runs before the library has a
// provider. It names the way out, because the usual cause is an Init that
// returned an error the caller discarded.
var ErrNoProvider = errors.New(
	"no LLM provider configured: call schemaflux.Init(key) or schemaflux.InitWithEnv(path) " +
		"and check the error, or set one of SCHEMAFLUX_API_KEY, SCHEMAFLUX_OPENAI_API_KEY, " +
		"OPENAI_API_KEY, or OPENAI in the environment")

// LLMCaller is the function type for calling the LLM
type LLMCaller func(ctx context.Context, system, user string, opts types.OpOptions) (string, error)

// Custom LLM caller for testing
var customLLMCaller LLMCaller

// setLLMCaller sets a custom LLM caller (for testing)
func setLLMCaller(caller LLMCaller) {
	providerMu.Lock()
	defer providerMu.Unlock()
	customLLMCaller = caller
}

// SetDefaultProvider sets the default LLM provider for operations
func SetDefaultProvider(p llm.Provider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	defaultProvider = p
}

// currentHooks reads both globals under one lock, so an operation cannot see a
// caller from one installation and a provider from the next.
func currentHooks() (LLMCaller, llm.Provider) {
	providerMu.RLock()
	defer providerMu.RUnlock()
	return customLLMCaller, defaultProvider
}

// callLLM executes an LLM request using the default provider
func callLLM(ctx context.Context, systemPrompt, userPrompt string, opts types.OpOptions) (string, error) {
	caller, provider := currentHooks()

	// Use custom caller if set (for testing)
	if caller != nil {
		return caller(ctx, systemPrompt, userPrompt, opts)
	}

	if provider == nil {
		// Name the way out. "no LLM provider configured" is true but leaves the
		// caller nowhere to go, and it is what they see when Init returned an
		// error they discarded.
		return "", ErrNoProvider
	}
	return CallLLM(ctx, provider, systemPrompt, userPrompt, opts)
}

// CallLLM executes an LLM request using the provided provider
func CallLLM(ctx context.Context, provider llm.Provider, systemPrompt, userPrompt string, opts types.OpOptions) (string, error) {
	log := logger.GetLogger()

	// Determine model. An empty result means the provider has no mapping and is
	// not OpenAI, so there is no defensible default: sending an OpenAI model ID
	// to another provider produces a 400 that reads as the caller's mistake.
	model := config.GetModel(opts.Intelligence, provider.Name())
	if model == "" {
		return "", fmt.Errorf(
			"no default model for provider %q; set SCHEMAFLUX_MODEL, or SCHEMAFLUX_MODEL_SMART / _FAST / _QUICK. Providers with a built-in mapping: %s",
			provider.Name(), strings.Join(config.KnownProviders(), ", "))
	}

	// Budgets are checked before the request, not after the invoice. With
	// enforcement off -- the default -- this always allows the call and the
	// alert callback does the talking.
	if err := pricing.CheckBudget(); err != nil {
		log.Error("Refusing the request: budget exhausted",
			"provider", provider.Name(), "model", model, "error", err)
		return "", err
	}

	// The tier ceiling is only the default. A caller who set
	// opts.MaxOutputTokens has an opinion about how long the answer should be
	// that is independent of how smart it should be -- I-09 -- and that
	// opinion wins. A caller who set nothing gets exactly what they always got.
	maxTokens := config.GetMaxTokens(opts.Intelligence)
	if opts.MaxOutputTokens > 0 {
		maxTokens = opts.MaxOutputTokens
	}
	temperature := config.GetTemperature(opts.Mode)
	effectiveSystemPrompt := applySteering(systemPrompt, opts.Steering)
	responseFormat := resolveResponseFormat(opts.ResponseFormat, effectiveSystemPrompt)

	// stableSystemPrompt is what actually forms the request's cacheable prefix:
	// the reinforcement boilerplate plus the operation's rendered template,
	// WITHOUT steering. applySteering appends steering as a suffix rather than
	// splicing it into the middle (CA-002 still owes a real Stable/Volatile
	// split, but the suffix placement already means the prefix up to here does
	// not move when only steering changes), so building the cache key from this
	// instead of the final SystemPrompt is what keeps a per-call steering
	// change from minting a new key -- see promptCacheKeyFor.
	stableSystemPrompt := strengthenSystemPrompt(systemPrompt, responseFormat)

	req := llm.CompletionRequest{
		Model:          model,
		SystemPrompt:   strengthenSystemPrompt(effectiveSystemPrompt, responseFormat),
		UserPrompt:     userPrompt,
		Temperature:    float64(temperature),
		MaxTokens:      maxTokens,
		ResponseFormat: responseFormat,
		JSONSchema:     opts.JSONSchema,
		SchemaName:     opts.SchemaName,
		PromptCacheKey: promptCacheKeyFor(opts, model, responseFormat, stableSystemPrompt),
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
	// Negative means "not configured, use the global default". Zero means the
	// caller asked for no retries and gets none: the previous `<= 0` test made
	// Client.WithRetries(0) -- which is documented as the retry ceiling and
	// explicitly clamps negatives to zero -- silently become three, so retries
	// could not be turned off at all.
	if maxRetries < 0 {
		maxRetries = config.GetLLMMaxRetries()
	}
	if retryBackoff <= 0 {
		retryBackoff = config.GetLLMRetryBackoff()
	}

	attempts := maxRetries + 1
	var (
		resp llm.CompletionResponse
		err  error
		// providerCalls counts what was actually sent, which is what the
		// envelope reports and what the bill reflects. Without it a request
		// that succeeded on its third try reported one attempt.
		providerCalls int
	)

	for attempt := 1; attempt <= attempts; attempt++ {
		providerCalls++
		resp, err = provider.Complete(ctx, req)
		if err == nil {
			// A truncated body arrives with the same 200 status as a complete
			// one, and the finish reason is the only thing that says so. This has
			// to run before resp.Content reaches ParseJSON: caught here it is a
			// truncation naming the real cause; caught there it is a parse
			// failure that looks like the model is broken (I-09). classify.go
			// is the only place that decides the kind, so this reads its answer
			// rather than repeating the finish-reason check.
			//
			// Scoped to truncation specifically -- not every kind
			// llm.ClassifyCompletion can return -- because it also classifies
			// plain empty content as KindMalformedOutput, and that case already
			// has its own, deliberately retryable, handling below
			// (validateLLMCompletion / TestCallLLMRetriesEmptyContent). Folding
			// it into this check would make an empty-content retry stop
			// retrying with no task asking for that change.
			if kind := llm.ClassifyCompletion(resp); kind == types.KindOutputTruncated {
				truncatedProvider := resp.Provider
				if truncatedProvider == "" {
					truncatedProvider = provider.Name()
				}
				truncatedModel := resp.Model
				if truncatedModel == "" {
					truncatedModel = model
				}
				err = &types.OperationError{
					Kind:     kind,
					Provider: truncatedProvider,
					Model:    truncatedModel,
					Message:  fmt.Sprintf("response truncated before it could be decoded (finish_reason=%q)", resp.FinishReason),
				}
			} else if validationErr := validateLLMCompletion(resp); validationErr != nil {
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

		delay := nextRetryDelay(err, retryBackoff, attempt)
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
		// Retries, not attempts: RetryCount is the count beyond the first, and
		// the envelope adds the one back. A request that succeeded on its third
		// try used to report a single attempt, because nothing wrote this down.
		RetryCount:   providerCalls - 1,
		TraceID:      telemetry.GetTraceID(ctx),
		SpanID:       telemetry.GetSpanID(ctx),
		StartTime:    start,
		EndTime:      time.Now(),
		Duration:     time.Since(start),
		Model:        actualModel,
		Provider:     actualProvider,
		Mode:         opts.Mode,
		Intelligence: opts.Intelligence,
		TokenUsage:   &usage,
		CostInfo:     cost,
		Custom: map[string]any{
			"response_format": responseFormat,
			// Which contract produced this answer. A stored result that cannot
			// say is a result nobody can reproduce, compare, or trust a year
			// later. S-002.
			"schema_id": opts.SchemaID,
		},
	}

	// Publish the record for whoever wants an envelope. The alternative was to
	// change the return type of every operation at once; this lets Result[T]
	// arrive operation by operation without a flag day, and the plain and
	// detailed forms still execute the identical path -- which is the property
	// that matters, because two return types that execute differently is how
	// the two drift.
	publishCallRecord(ctx, metadata)

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

// promptCacheKeyFor builds the identity sent to the provider as its
// prompt-cache-key hint (the OpenAI Responses `prompt_cache_key`; see
// promptCacheKeyFor's caller and OpenAIProvider.Complete).
//
// (op, tier) used to be treated as enough identity for this. It is not: two
// prompt revisions, or two schema versions, both map to the same (op, tier)
// pair, so a key built from just that routes the second release's request to
// a server holding the FIRST release's prefix. The read misses, the call pays
// full price, and nothing about the failure is visible -- it just looks like
// caching stopped helping. This key instead covers everything that actually
// determines the bytes of the stable prefix:
//
//   - identity: opts.CacheIdentity, the operation-and-schema identity built by
//     ops.SchemaCacheKey (S-002 built that function for exactly this and
//     MW-004; reusing it here rather than a second scheme is deliberate).
//     Empty for operations that do not yet compute a schema identity -- the
//     template hash below still keeps those operations' keys apart from each
//     other, just without the operation-name and schema-version axis.
//   - model: a cache entry written by one model's tokenizer is not addressed
//     the same way by another's; two different models must never share a key.
//   - responseFormat and template: digestOf(stableSystemPrompt) changes the
//     instant a prompt literal changes, which is what makes an edited prompt
//     mint a new key instead of silently reusing a stale one.
//   - opts.Mode: several operations render a different template per mode
//     (BuildExtractSystemPrompt is one), so this is largely redundant with the
//     template digest -- it is kept anyway for operations whose behavior
//     depends on mode without the template text differing.
//
// Steering is deliberately absent. It is the one per-call, human-authored
// piece of the request, appended to the system prompt AFTER the caller passes
// it here (applySteering), so folding it in would change the key on every
// single call and defeat the entire point of naming a STABLE prefix. CA-002.
func promptCacheKeyFor(opts types.OpOptions, model, responseFormat, stableSystemPrompt string) string {
	identity := strings.TrimSpace(opts.CacheIdentity)
	if identity == "" {
		identity = "-"
	}

	parts := []string{
		identity,
		strings.TrimSpace(model),
		digestOf(stableSystemPrompt),
		opts.Mode.String(),
		responseFormat,
	}
	for i, part := range parts {
		if part == "" {
			parts[i] = "-"
		}
	}
	return strings.Join(parts, ":")
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
	// A response carrying no assistant message is deterministic: a reasoning-only
	// response or an empty output list is identical on retry. It matched the
	// "empty response" substring below and cost a full backoff cycle -- seven
	// seconds per call -- to arrive at the same answer.
	if errors.Is(err, llm.ErrNoMessageOutput) {
		return false
	}

	// A rate limit is retryable by construction, and saying so by type rather
	// than by substring keeps the answer from depending on how the message
	// happens to be worded -- or on what the vendor put in the body, which is
	// matched against the non-retryable list below.
	if _, rateLimited := llm.RetryAfterFrom(err); rateLimited {
		return true
	}

	// The shared taxonomy answers this question directly, and answering it in
	// one place is the point: a retry decision that differs between operations
	// is a bug nobody can reproduce. A-007.
	//
	// Deciding from the classified kind rather than from prose also survives
	// the body being redacted out of the message, which is the whole point of
	// the typed error: the string is for a human, the kind is for the code.
	if kind := llm.Classify(err); kind != types.KindUnknown {
		return (&types.OperationError{Kind: kind}).Retryable()
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

// nextRetryDelay picks how long to wait before the next attempt.
//
// The computed backoff is a guess that tops out at five seconds. When a server
// rate-limits per minute it answers with the exact wait it wants, and a
// five-second ceiling cannot clear a sixty-second window -- so every attempt
// landed inside the same closed window and the retry budget bought nothing but
// latency. When the server states a wait, that wait wins.
func nextRetryDelay(err error, base time.Duration, attempt int) time.Duration {
	if wait, rateLimited := llm.RetryAfterFrom(err); rateLimited && wait > 0 {
		// The server's number is already bounded by llm.MaxRetryAfter, and the
		// caller's context deadline still cuts the wait short in waitForRetry.
		return wait
	}
	return retryDelay(base, attempt)
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

// resolveResponseFormat decides whether to ask the provider for structured
// output. An explicit declaration always wins; otherwise the format is inferred
// from the SYSTEM prompt alone.
//
// The user prompt is deliberately not consulted. It used to be, so a caller
// summarising a document that happened to contain the phrase "json object" got
// their text operation silently switched into JSON mode — the response format
// depended on the data, which is an injection-adjacent control path in a
// library whose whole premise is types. The library writes every system prompt,
// so inferring from that is inferring from its own words.
func resolveResponseFormat(declared, systemPrompt string) string {
	switch declared {
	case "json", "text":
		return declared
	}
	return inferResponseFormat(systemPrompt)
}

func inferResponseFormat(systemPrompt string) string {
	combined := strings.ToLower(systemPrompt)
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

// promptReinforcementEnvVar turns the reinforcement block off.
//
// It is on by default and prepended to *every* request, JSON or not, so a
// caller pays for it on every call whether or not their model needs it. Making
// it opt-out is the half of S-007 that can be settled without spending: whether
// it still *helps* is a question about model behaviour, and answering it needs
// a live A/B against a pinned corpus, which is RC-002's kind of measurement and
// costs money by construction.
//
// What can be said without a provider: the block is 3 lines and about 60 tokens
// for a non-JSON request, 6 lines and about 110 for a JSON one, on every call.
// A caller who has measured their own prompts and found it unnecessary can now
// stop paying for it.
const promptReinforcementEnvVar = "SCHEMAFLUX_PROMPT_REINFORCEMENT"

// promptReinforcementEnabled reports whether the reinforcement block is added.
// Anything but an explicit "0"/"false"/"off" leaves it on, because turning it
// off by accident changes behaviour silently.
func promptReinforcementEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(promptReinforcementEnvVar))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

func strengthenSystemPrompt(systemPrompt, responseFormat string) string {
	if !promptReinforcementEnabled() {
		return strings.TrimSpace(systemPrompt)
	}

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

// operationContext derives the context an operation runs under from the
// caller's, applying the configured timeout.
//
// Twenty-nine call sites wrote `context.WithTimeout(context.Background(), ...)`
// instead. Every options struct accepts a Context and every builder exposes a
// Context(...) method, and all of them were ignored: caller cancellation did
// nothing, so an abandoned HTTP request kept paying for tokens, and a deadline
// the caller set was replaced by the library's own.
func operationContext(caller context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if caller == nil {
		caller = context.Background()
	}
	if timeout <= 0 {
		timeout = config.GetTimeout()
	}
	return context.WithTimeout(caller, timeout)
}
