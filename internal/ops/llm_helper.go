package ops

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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

// providerContextKey scopes a provider to one call chain via context, rather
// than through the package globals above that every client construction and
// every schemafluxtest.Install rewrites.
//
// TI-002 / IN-004: "a mutex around a package global passes -race and still
// fails [the isolation] test" -- the lock in providerMu makes concurrent
// writes to defaultProvider safe, but there is still only one defaultProvider,
// so building a second Client silently repoints every operation the first
// Client's caller is still running. A context value does not have this
// problem: it travels with the call that carries it, and a second Client's
// construction cannot reach into a context a first Client's caller already
// holds.
//
// This lives on context rather than on types.OpOptions -- the shape the task
// sketched -- for a reason that is not stylistic: internal/llm.Provider is
// defined in a package that imports internal/types (for TokenUsage and
// friends), so types.OpOptions cannot hold an llm.Provider field without
// creating an import cycle. The alternative, an OpOptions field typed `any`
// that ops type-asserts back to llm.Provider, was rejected: it is a field
// that compiles for any value and only fails at the call site, which is the
// exact kind of silent-wrong-type landmine dead_options_test.go exists to
// keep out of this API. Context carries the same information with no cycle
// and no untyped field, and every operation already threads opts.Context (or
// an explicit ctx parameter derived from it) into callLLM -- see
// operationContext and the ~60 call sites in this package -- so the seam
// needs no change to any of them.
type providerContextKey struct{}

// WithProvider returns a context carrying p as the provider operations run
// under. It takes priority over both the test caller (customLLMCaller) and
// the package-level default (defaultProvider) in callLLM below, so a Client
// that attaches its own provider to the context is unaffected by another
// Client's construction -- see client.go's Client.Context, the intended
// caller of this function.
//
// A nil p leaves ctx unchanged rather than storing a nil provider. Storing it
// would make providerFromContext return a non-nil interface holding a nil
// value with a nil method set -- providerFromContext's own nil check would
// pass on the interface being non-nil, and the caller would silently fall
// through to a nil Provider instead of to the global default, which is a
// worse failure than never having called WithProvider at all.
func WithProvider(ctx context.Context, p llm.Provider) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, providerContextKey{}, p)
}

// providerFromContext reads the per-call provider WithProvider attached, or
// nil when none was.
func providerFromContext(ctx context.Context) llm.Provider {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(providerContextKey{}).(llm.Provider)
	return p
}

// callLLM executes an LLM request, preferring a provider attached to ctx by
// WithProvider over the package-level default -- see providerContextKey's
// comment for why the per-call path has to win.
func callLLM(ctx context.Context, systemPrompt, userPrompt string, opts types.OpOptions) (string, error) {
	if p := providerFromContext(ctx); p != nil {
		return CallLLM(ctx, p, systemPrompt, userPrompt, opts)
	}

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
	// The response format is inferred from the system prompt alone, which this
	// library writes. It used to be inferred from the system prompt *with
	// steering already appended*, so a caller whose steering mentioned JSON
	// silently switched their text operation into JSON mode -- the same
	// data-controls-the-control-path defect resolveResponseFormat's comment
	// describes for the user prompt, one argument over.
	responseFormat := resolveResponseFormat(opts.ResponseFormat, systemPrompt)

	// CA-002: the system prompt is the stable segment and steering is the
	// volatile one, so they go in different places.
	//
	// Steering used to be appended to the system block, which meant the block
	// a provider caches was different bytes on every call that steered
	// differently -- and a prefix cache that never sees the same prefix twice
	// is a cache that never hits. It goes in the user message now, where a
	// per-call instruction belongs: two calls differing only in steering send a
	// byte-identical system prompt.
	//
	// Before the caller's content, not after it. Instruction-after-data is the
	// more injection-resistant order in the abstract, but every prompt this
	// library writes ends with the data -- "Items:\n[...]" -- and several
	// things downstream depend on that, including the shape-answering local
	// provider, which finds the items by taking the last JSON array in the
	// message. Putting steering last silently changed what "the items" were
	// and made a MapReduce return nothing. The ordering is a real trade-off
	// and this is the side of it that does not break the format everything
	// else already agreed on.
	stableSystemPrompt := strengthenSystemPrompt(systemPrompt, responseFormat)

	req := llm.CompletionRequest{
		Model:          model,
		SystemPrompt:   stableSystemPrompt,
		UserPrompt:     applySteering(userPrompt, opts.Steering),
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

	// prevWait threads the decorrelated-jitter sequence across this one call's
	// attempts. It is local to this call, not a package variable: two
	// concurrent CallLLM invocations retrying the same kind of failure must
	// each correlate their wait with their OWN previous attempt, never with
	// each other's -- correlating across calls would reintroduce the
	// synchronized-retry-wave problem jitter exists to avoid, just one level
	// up. A-008.
	prevWait := retryBackoff

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
			publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
			return "", err
		}

		delay := nextRetryDelay(err, retryBackoff, prevWait, maxComputedRetryDelay, retryRandFloat)
		prevWait = delay
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
			publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
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
	noteCacheReads(req.PromptCacheKey, usage)
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

// isRetryableLLMError decides whether the identical request is worth sending
// again.
//
// A-008: this used to carry two substring lists -- a nonRetryable set checked
// first, a retryable set checked second -- as a fallback for whatever
// llm.Classify did not recognise. That is a second opinion about retry
// disposition living beside the first one, and the two could disagree: a
// 500 whose body happened to mention "invalid_request_error" (a vendor's OWN
// classifier, inside the body of an unrelated failure) matched the
// nonRetryable list and lost a retry it was entitled to, and there was no way
// to tell from the caller's side which list had won. llm.Classify is the one
// place internal/llm/classify.go decides what kind of failure something is,
// and types.OperationError.Retryable is the one place that turns a kind into
// a retry decision (A-007); asking either question a second way here is the
// bug this closes.
func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	// The caller's own context ending is not something classify.go's taxonomy
	// is in a position to see: the next attempt would run under a context
	// that is already done, so "try again" cannot mean anything here
	// regardless of what kind the wrapped error turns out to be. This is a
	// fact about the local call, not a second opinion about the taxonomy.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// A response carrying no assistant message is deterministic: a
	// reasoning-only response or an empty output list is identical on retry.
	// Not a kind classify.go's taxonomy carries (it is a shape of successful
	// response, not a transport failure), so it stays a direct sentinel check.
	if errors.Is(err, llm.ErrNoMessageOutput) {
		return false
	}

	kind := llm.Classify(err)
	if kind == types.KindUnknown {
		// classify.go's own text fallback is deliberately narrow: it matches
		// only phrases this library itself produces, never a vendor's, so
		// KindUnknown means nobody has taught the taxonomy about this error's
		// *shape* yet -- not that the failure is known to be permanent.
		// Refusing to retry on that gap is exactly the guess the removed
		// substring lists were making, just inverted. The attempt budget
		// already bounds what being wrong costs; failing fast on ignorance
		// does not save the caller anything and can throw away a retry that
		// would have succeeded. This is the case the task's verify line names:
		// "an unknown error does not silently fail fast."
		return true
	}
	return (&types.OperationError{Kind: kind}).Retryable()
}

// maxComputedRetryDelay bounds the JITTERED backoff computed below, the same
// way it always has: a server rate-limiting per minute answers with the exact
// wait it wants (llm.RetryAfterFrom, honoured unjittered in nextRetryDelay),
// and a short ceiling on the computed guess cannot clear a per-minute window
// on its own -- that is CB-03's fix, unrelated to and unaffected by adding
// jitter on top.
const maxComputedRetryDelay = 5 * time.Second

// nextRetryDelay picks how long to wait before the next attempt.
//
// A server-stated wait always wins outright and is not jittered: the whole
// point of honouring it (CB-03) is that the server said exactly how long to
// wait, and randomising a number it gave us would be the guess it exists to
// replace, with extra steps.
//
// Otherwise the wait is decorrelated jitter -- mw/retry.go's term and
// algorithm for it, duplicated here rather than imported. Reusing mw's
// unexported decorrelatedJitter would mean either exporting it from a file
// this task does not touch, or importing mw (an opt-in decorator a caller
// wraps around their OWN provider, used by nothing in this call path today)
// into internal/ops for one function; the ten-line algorithm is cheaper than
// either. Before this, CallLLM's own retry loop computed a pure exponential
// ladder from the attempt number alone, so every caller retrying the same
// failure against the same base backoff retried at the identical instant --
// a provider recovering from an outage met every one of them in the same
// synchronized wave the moment it came back up. Jitter spreads that wave out.
// A-008.
func nextRetryDelay(err error, base, prevWait, maxDelay time.Duration, randFloat func() float64) time.Duration {
	if wait, rateLimited := llm.RetryAfterFrom(err); rateLimited && wait > 0 {
		// The server's number is already bounded by llm.MaxRetryAfter, and the
		// caller's context deadline still cuts the wait short in waitForRetry.
		return wait
	}
	return decorrelatedJitter(base, prevWait, maxDelay, randFloat)
}

// decorrelatedJitter implements the AWS Architecture Blog's "decorrelated
// jitter" backoff, matching mw/retry.go's function of the same name: each
// wait is uniformly random between base and three times the PREVIOUS wait,
// capped at max. Unlike "full jitter" (uniform between 0 and an
// exponentially growing cap), each wait here is correlated with the previous
// one rather than with the attempt number, which is what stops a burst of
// concurrent callers -- all starting their backoff at the same moment -- from
// re-converging on the same retry instant a few attempts in.
func decorrelatedJitter(base, prev, maxDelay time.Duration, randFloat func() float64) time.Duration {
	if base <= 0 {
		base = time.Millisecond
	}
	upper := prev * 3
	if upper < base {
		upper = base
	}

	span := upper - base
	wait := base
	if span > 0 {
		wait += time.Duration(randFloat() * float64(span))
	}

	if maxDelay > 0 && wait > maxDelay {
		wait = maxDelay
	}
	return wait
}

// retryRandFloat returns a value in [0,1). A package variable rather than a
// direct math/rand/v2 call so a test can make the jitter's output
// deterministic instead of asserting on a range wide enough to hide a
// mistake -- mw/retry.go's randFloat field documents the identical reasoning
// for the same algorithm. math/rand/v2's top-level Float64 is safe for
// concurrent use, so this needs no lock of its own.
var retryRandFloat = rand.Float64

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

// applySteering attaches the caller's per-call instructions to the volatile
// segment -- the user message -- and returns it unchanged when there are none.
//
// It used to attach them to the system prompt, which is the segment providers
// cache. See CA-002 and the comment at the call site.
func applySteering(userPrompt, steering string) string {
	steering = strings.TrimSpace(steering)
	if steering == "" {
		return userPrompt
	}
	return strings.TrimSpace("Additional instructions:\n" + steering + "\n\n" + userPrompt)
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

// CA-006's diagnostic half.
//
// A prompt cache that is doing nothing looks exactly like one that is working:
// the calls succeed, the answers are right, and the only difference is the
// bill. The failure is usually silent and structural -- a prefix under the
// provider's minimum cacheable length, or a stable segment that is not
// actually stable -- so nothing errors and nobody finds out until somebody
// reads an invoice.
//
// So: count how many times each cache key has been sent, and say something
// once when a key that has been sent several times has never had a single
// cached token reported back.
const cacheReadWarnAfter = 3

// cacheReadTrackerCap bounds the map. This is a diagnostic, and a diagnostic
// that grows without limit in a long-running process is a worse bug than the
// one it reports. At the cap the table is dropped wholesale rather than
// evicted cleverly: losing the counts means at worst a warning arrives later
// than it could have.
const cacheReadTrackerCap = 512

var (
	cacheReadMu     sync.Mutex
	cacheReadCounts = map[string]int{}
	cacheReadWarned = map[string]bool{}
)

func noteCacheReads(cacheKey string, usage types.TokenUsage) {
	if cacheKey == "" {
		return
	}

	cacheReadMu.Lock()
	defer cacheReadMu.Unlock()

	if len(cacheReadCounts) >= cacheReadTrackerCap {
		cacheReadCounts = map[string]int{}
		cacheReadWarned = map[string]bool{}
	}

	if usage.CachedTokens > 0 {
		// It is working. Forget the key rather than keep counting it.
		delete(cacheReadCounts, cacheKey)
		delete(cacheReadWarned, cacheKey)
		return
	}

	cacheReadCounts[cacheKey]++
	if cacheReadCounts[cacheKey] < cacheReadWarnAfter || cacheReadWarned[cacheKey] {
		return
	}
	cacheReadWarned[cacheKey] = true

	// The key, not the prompt: the prompt is built from the caller's data and
	// this is a log line.
	logger.GetLogger().Warn(
		"prompt cache is reporting no cached tokens for a prefix that keeps repeating",
		"cacheKey", cacheKey,
		"calls", cacheReadCounts[cacheKey],
		"likelyCause", "the stable prefix may be below the provider's minimum cacheable length, or something per-call is still inside it")
}

// resetCacheReadTracking exists for tests, which would otherwise leak counts
// into each other and produce a warning whose cause is another test.
func resetCacheReadTracking() {
	cacheReadMu.Lock()
	defer cacheReadMu.Unlock()
	cacheReadCounts = map[string]int{}
	cacheReadWarned = map[string]bool{}
}

// publishFailedCall records a request that never produced an answer.
//
// Only successful calls used to publish a record, so a request that exhausted
// its retries and failed outright contributed *nothing* to the envelope: a
// caller reading Meta.Attempts on a failure saw zero, as though the provider
// had never been called, while the provider had in fact been called and
// billed as many times as the retry budget allowed. Every "how many attempts
// did that take" question was therefore answerable only for the requests that
// worked, which is the opposite of when it is asked.
//
// Found while building PL-008's per-item failure reporting, where a failed
// item reported no attempts at all.
//
// Usage and cost are left zero rather than guessed: a failed call's token
// consumption is not reported by the provider on most error paths, and
// inventing a number here would be the PR-001 mistake. The attempt count is
// measured and is what this exists to carry.
func publishFailedCall(ctx context.Context, provider llm.Provider, model string,
	opts types.OpOptions, start time.Time, providerCalls int,
	requestID, correlationID string) {

	publishCallRecord(ctx, &types.ResultMetadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
		RetryCount:    providerCalls - 1,
		StartTime:     start,
		EndTime:       time.Now(),
		Duration:      time.Since(start),
		Model:         model,
		Provider:      provider.Name(),
		Mode:          opts.Mode,
		Intelligence:  opts.Intelligence,
	})
}
