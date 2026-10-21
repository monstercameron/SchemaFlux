package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/pricing"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
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
	// Checked here as well as in CallLLM because the test caller hook below
	// does not go through CallLLM.
	if err := contextOver(ctx); err != nil {
		return "", err
	}
	if err := checkCallOptions(opts); err != nil {
		return "", err
	}

	// The client the call runs on is fixed here, before anything reads it: a
	// default replaced while the call is in flight must not give it the
	// provider of one reader and the budget of the next.
	ctx, _ = PinExec(ctx)

	provider, caller, err := routeFor(ctx)
	if err != nil {
		return "", err
	}
	// Use custom caller if set (for testing). It answers before the provider
	// the same lookup names for planning.
	if caller != nil {
		return caller(ctx, systemPrompt, userPrompt, opts)
	}
	return CallLLM(ctx, provider, systemPrompt, userPrompt, opts)
}

// noProvider is what a call gets when nothing names a provider for it. It has
// the sentinel's text, and errors.Is still matches ErrNoProvider; the kind is
// configuration, because the way out is the caller's own: Init, or the
// environment.
var noProvider = types.Classified(types.KindConfiguration, ErrNoProvider)

// routeFor is the one place that decides which provider a call under ctx goes
// to. It returns the provider, or the test caller hook, or the reason there is
// neither.
//
// The order is, first to last: the provider on the snapshot the context
// carries, or on the default snapshot when it carries none (IN-004: a client's
// own snapshot wins over everything process-wide, and over the narrower
// WithProvider seam, so a client that set both does not get whichever one this
// function happened to read first); the snapshot's own reason for having no
// provider; the provider WithProvider attached; the process-wide caller hook
// and then the process-wide default provider; and last noProvider.
//
// The snapshot's reason comes before the three process-level routes because a
// client that cannot make a call has said so, and a provider that happens to be
// on the context, or a hook or default left in the process by something else
// that is linked into the program, would otherwise answer for it: the caller
// would get answers from a provider they did not configure, and never learn the
// client's own configuration was wrong.
//
// When the hook is set it answers the call, and the process-wide provider is
// returned beside it so that planning, which asks only which route a call
// would take, reads the same provider it always did.
func routeFor(ctx context.Context) (provider llm.Provider, caller LLMCaller, err error) {
	cfg := execConfigFrom(ctx)
	if cfg != nil && cfg.Provider != nil {
		return cfg.Provider, nil, nil
	}
	if cfg != nil && cfg.Err != nil {
		return nil, nil, cfg.Err
	}
	if p := providerFromContext(ctx); p != nil {
		return p, nil, nil
	}
	caller, provider = currentHooks()
	if caller != nil || provider != nil {
		return provider, caller, nil
	}
	// Name the way out. "no LLM provider configured" is true but leaves the
	// caller nowhere to go, and it is what they see when Init returned an
	// error they discarded.
	return nil, nil, noProvider
}

// CallLLM executes an LLM request using the provided provider.
//
// OB-006: this is also where a buffered request is emitted to the installed
// observers (a streamed one is emitted by runStream). Every provider reaches
// the model through here -- the package default, a client's own instance, one
// attached to the context by WithProvider, and anything a caller handed to
// RegisterProvider or RegisterProviderFactory -- so observing at this one site
// is what makes a caller's own transport as visible to a host's tracing as the
// providers this repository ships.
//
// The observed unit is the whole logical request, not one attempt: retries
// are reported as OperationResult.Attempts and as a note for each, rather than
// as extra requests, so a host does not count a retried request several times
// in its own latency figures. Whether the answer was then usable is not this
// unit's to say: that is the outcome of the operation the request serves
// (observeOperation).
//
// What it does not observe as a request, deliberately: the pre-flight
// refusals above the emission point -- a context that is already over, no
// model mapping, an exhausted budget, a trust-boundary violation. Nothing was
// attempted on those paths, and a request reporting zero attempts and zero
// duration for a request that never existed is a reader's problem rather than
// a reader's information. They still return classified errors to the caller,
// and the operation they were made for reports them.
func CallLLM(ctx context.Context, provider llm.Provider, systemPrompt, userPrompt string, opts types.OpOptions) (answer string, callErr error) {
	// LX-061. A context that is already cancelled or past its deadline sends
	// nothing. Whether a provider notices a dead context is up to the provider,
	// and one that does not would be sent a request, and billed for it, on
	// behalf of a caller who had already stopped waiting.
	if err := contextOver(ctx); err != nil {
		return "", err
	}
	if err := checkCallOptions(opts); err != nil {
		return "", err
	}

	// Pinned for the same reason as in callLLM: this is also an entry, for the
	// callers that name their provider themselves.
	ctx, _ = PinExec(ctx)

	log := loggerFor(ctx)

	// Determine model. An empty result means the provider has no mapping and is
	// not OpenAI, so there is no defensible default: sending an OpenAI model ID
	// to another provider produces a 400 that reads as the caller's mistake.
	//
	// TC-005: an explicit pin wins over the tier mapping. The tier is a
	// preference that floats as models are released, which is the right default
	// and the wrong thing for a caller reproducing a result or holding a
	// regression suite still. A pin is not validated against a provider's
	// catalogue -- this library does not have one, and refusing a model it has
	// simply never heard of would make every newly released model unusable
	// until somebody updated a list here.
	//
	// A client can name the model for all of its calls, which takes the place
	// of the tier's mapping and gives way to a pin on the call (resolveModel).
	model := resolveModel(ctx, opts, provider)
	if model == "" {
		// Classified, and it says what to do with a client as well as with
		// the environment (DX-020).
		return "", config.NoDefaultModelError(provider.Name())
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
	//
	// A decision declines the reinforcement block on the context (LAT-004).
	stableSystemPrompt := dec12StrengthenSystemPrompt(ctx, systemPrompt, responseFormat)

	// TC-001: the caller's steering must never end up inside the segment
	// this library sends as system policy. applySteering (below) always
	// places it in the user message instead, so this ordinarily passes on
	// every call; it exists to catch a regression in this file, refusing
	// the request outright rather than sending a caller's per-call text as
	// though it were fixed policy.
	if err := verifyTrustBoundary(stableSystemPrompt, opts.Steering); err != nil {
		log.Error("Refusing the request: trust boundary violated",
			"provider", provider.Name(), "error", loggedError(err))
		return "", err
	}

	// LX-105: the caller's reasoning effort, or failing that the operation's
	// preference, marked as one.
	effort, effortPreferred := requestEffort(ctx, opts)

	req := llm.CompletionRequest{
		Model:                    model,
		SystemPrompt:             stableSystemPrompt,
		UserPrompt:               applySteering(userPrompt, opts.Steering),
		Temperature:              float64(temperature),
		MaxTokens:                maxTokens,
		ResponseFormat:           responseFormat,
		JSONSchema:               opts.JSONSchema,
		SchemaName:               opts.SchemaName,
		PromptCacheKey:           promptCacheKeyFor(opts, model, responseFormat, stableSystemPrompt),
		WebSearch:                opts.WebSearch,
		ReasoningEffort:          effort,
		ReasoningEffortPreferred: effortPreferred,
		// LX-100: what the operation is deciding, typed, when it is deciding
		// something. A generating provider does not read it.
		Decision: requestDecision(ctx),
	}

	resp, err := callRequest(ctx, provider, req, opts)
	if err != nil {
		return "", err
	}
	// An operation declares no tools. The calls of a reply are the caller's to
	// run, and this function returns text only, so a reply that holds some is
	// refused here rather than answered with the calls left out.
	if len(resp.ToolCalls) > 0 {
		return "", errToolCallsForAnOperation
	}
	return resp.Content, nil
}

// callRequest sends one request through everything a call has: the client's data
// policy and budget before it, the scheduler, the retries, the observers and the
// call record, and it returns the whole answer. CallLLM is the entry of an
// operation, which wants the text; the entry of a conversation's turn (CallTurn)
// wants the tool calls as well, and has to get them through the same path rather
// than a copy of it.
//
// req is complete, with its model resolved, and ctx is the context its entry
// pinned. opts is what the call record and the observers say about the call: the
// request is built from it by the entry, and nothing here reads it to change the
// request. An error comes with an empty response.
func callRequest(ctx context.Context, provider llm.Provider, req llm.CompletionRequest, opts types.OpOptions) (_ llm.CompletionResponse, callErr error) {
	log := loggerFor(ctx)
	model := req.Model
	responseFormat := req.ResponseFormat
	maxTokens := req.MaxTokens

	// Budgets are checked before the request, not after the invoice. With
	// enforcement off -- the default -- this always allows the call and the
	// alert callback does the talking.
	//
	// IN-004: a client's own ledger replaces the process-wide one entirely
	// rather than being checked in addition to it. Checking both would mean a
	// client could be refused by a budget belonging to a different tenant, which
	// is the multi-tenancy failure this seam exists to remove -- and a client
	// that declared its own ceiling has said what it wants enforced.
	//
	// LX-126: the client's data policy comes first. A route the policy forbids
	// is not sent to, whatever the budget says, and nothing is charged for it.
	if err := checkDataPolicy(ctx, provider, model); err != nil {
		log.Error("Refusing the request: the client's data policy does not allow this route",
			"provider", provider.Name(), "model", model, "error", loggedError(err))
		return llm.CompletionResponse{}, err
	}
	if err := checkBudgetFor(ctx, provider, model); err != nil {
		log.Error("Refusing the request: budget exhausted",
			"provider", provider.Name(), "model", model, "error", loggedError(err))
		return llm.CompletionResponse{}, err
	}

	start := time.Now()
	ctx, tracking := requesttracking.Ensure(ctx, opts.RequestID, opts.CorrelationID)
	requestID := tracking.RequestID
	correlationID := tracking.CorrelationID
	opts.RequestID = requestID
	opts.CorrelationID = correlationID

	// The arguments of a log call are boxed before the call checks the level,
	// which was eight allocations for a line that is off by default.
	if log.Enabled(ctx, slog.LevelDebug) {
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
	}

	maxRetries, retryBackoff := provider.RetryPolicy()
	// A client that names its own retries decides over the provider's: positive
	// is how many, negative is none. Zero leaves the provider's policy.
	if n := execRetries(ctx); n > 0 {
		maxRetries = n
	} else if n < 0 {
		maxRetries = 0
	}
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

	// The count is held to a ceiling before one is added to it: a count at the
	// top of the integers wrapped to a negative number of attempts, the loop
	// below never ran, and the empty response it had not received was read as an
	// answer that did not parse.
	maxRetries = min(maxRetries, MaxRetries)
	attempts := maxRetries + 1
	var (
		resp llm.CompletionResponse
		err  error
		// providerCalls counts what was actually sent, which is what the
		// envelope reports and what the bill reflects. Without it a request
		// that succeeded on its third try reported one attempt.
		providerCalls int
	)

	// The observer's start event for the request, and a deferred finish that
	// fires on every exit below -- the success, the exhausted-retry failure,
	// the cancelled-during-backoff failure, and a panic alike. A defer rather
	// than a call at each return because the next return added to this
	// function would otherwise be the one that silently drops its span, and a
	// trace missing exactly the failures is worse than no trace.
	//
	// ctx is reassigned from the observer's return: an observer that opened a
	// span puts it there, which is what puts every provider call below inside
	// it. The finish reaches the observers this start called, whatever is
	// installed by then (telemetry.ObserveOperationEnd).
	startEvent := telemetry.OperationEvent{
		Operation:      operationNameFor(ctx, opts),
		Provider:       provider.Name(),
		Model:          model,
		RequestID:      requestID,
		CorrelationID:  correlationID,
		Mode:           opts.Mode.String(),
		Intelligence:   opts.Intelligence.String(),
		SchemaName:     opts.SchemaName,
		ResponseFormat: responseFormat,
		Repair:         isRepairRequest(ctx),
	}
	ctx = observeStart(ctx, startEvent)

	// providerElapsed accumulates only the time inside provider.Complete, so
	// the finish event can report the provider's share of the wall clock
	// apart from this function's own backoff.
	var providerElapsed time.Duration
	// answered is whether the last attempt got an answer from the provider,
	// usable or not. returned is set before the return that hands the answer
	// back: a function that leaves with neither it nor an error is unwinding
	// from a panic, in the provider or in this library.
	var answered, returned bool
	// refused holds the answers of this request that were produced, paid for
	// and refused, so its finish can report what every answered attempt used
	// (observe.go).
	var refused refusedAnswers
	// Provider and Model are deliberately NOT seeded from the start event.
	// They mean "what answered", and on a failure nothing did -- seeding them
	// with what was *asked* would report a provider that never responded as
	// the one that did, which is the library-reporting-success-while-wrong
	// shape one field down. What was asked for is on the start event, where it
	// is true.
	finish := telemetry.OperationResult{Operation: startEvent.Operation}
	defer func() {
		finish.Duration = time.Since(start)
		finish.ProviderDuration = providerElapsed
		finish.Attempts = providerCalls
		switch {
		case callErr != nil:
			// The kind, from the one place allowed to decide what a failure
			// is (classify.go). Never callErr.Error(): a provider's message
			// quotes the request back, and an observer's output is an export.
			finish.ErrorKind = requestErrorKind(callErr, answered, resp)
		case !returned:
			// A panic. It is not recovered here: the request is reported as
			// failed and the panic goes on to whoever it was going to. It
			// used to be reported as a success, since there was no error.
			finish.ErrorKind = telemetry.ErrorKindInternal
		}
		if finish.ErrorKind != "" {
			// A request that failed is counted too, by its kind. The request
			// counter held only the requests that were answered.
			telemetry.RecordLLMFailure(startEvent.Provider, model, opts.Mode, opts.Intelligence, finish.ErrorKind)
		}
		if callErr != nil && refused.any() {
			// Every answer this request got was refused, and each was paid for.
			finish.Usage = refused.usage
		}
		observeEnd(ctx, finish)
	}()

	// timing is where this call's time went: the wait for a slot before each
	// attempt, and what the provider reports of its HTTP call for each attempt
	// that got an answer. timed is whether anything was measured at all.
	var (
		timing types.Timing
		timed  bool
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
		// A retry is not sent on a context that ended while the last attempt
		// was in flight or during the wait: the wait below can return on its
		// timer when the timer and the context are both ready.
		if attempt > 1 {
			if over := contextOver(ctx); over != nil {
				publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
				return llm.CompletionResponse{}, over
			}
			// The attempt that was thrown away has been charged, so the budget
			// can have run out since the check that let this call start.
			if spent := checkBudget(ctx); spent != nil {
				log.Error("Refusing the retry: budget exhausted",
					"provider", provider.Name(), "model", model, "attempt", attempt, "error", loggedError(spent))
				publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
				return llm.CompletionResponse{}, spent
			}
		}
		// LX-126: each attempt takes a slot in the client's scheduler, when
		// it has one, and gives it back when the attempt ends, so the wait
		// before a retry holds nothing.
		var admitted bool
		asked := time.Now()
		resp, admitted, err = throughScheduler(ctx, provider, req, func(runCtx context.Context) (llm.CompletionResponse, error) {
			// With a scheduler, the time from asking for a slot to being given
			// one. With none this is the cost of a function call.
			if ExecScheduler(ctx) != nil {
				timing.Queued += time.Since(asked)
				timed = true
			}
			providerCalls++
			attemptStart := time.Now()
			answer, callErr := provider.Complete(runCtx, req)
			// Measured around the provider call only, so the backoff this
			// loop adds between attempts, and the wait for a slot, stay out
			// of the provider's figure.
			providerElapsed += time.Since(attemptStart)
			return answer, callErr
		})
		if !admitted {
			// The scheduler's own answer: no room, closed, or the caller gave
			// up waiting. Nothing was sent, and it is not retried: a caller
			// who bounded the queue asked for exactly this refusal.
			log.Error("LLM request was not admitted by the client's scheduler",
				"requestID", requestID,
				"correlationID", correlationID,
				"provider", provider.Name(),
				"model", model,
				"attempt", attempt,
				"error", loggedError(err),
			)
			publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
			return llm.CompletionResponse{}, err
		}
		answered = err == nil
		if answered {
			// A provider responded on this attempt, whatever the response
			// turns out to be worth, and the finish event says what it said of
			// itself: who answered, how generation stopped and what was used.
			// Recorded before anything can refuse the answer, so a truncation
			// or an answer from a route the client's policy forbids still
			// reports the facts that diagnose it, beside the error kind. Only
			// what the provider itself said: an empty field means the provider
			// named nothing, not that this library forgot to look.
			finish.FinishReason = resp.FinishReason
			finish.Usage = resp.Usage
			if resp.Provider != "" {
				finish.Provider = resp.Provider
			}
			if resp.Model != "" {
				finish.Model = resp.Model
			}
		}
		if err == nil {
			// LX-126: who answered. A provider that routes onward can send the
			// request to a provider the client's policy does not allow, and
			// the answer is where that shows. It is charged, since it was
			// produced, and it is not retried: the next attempt would go the
			// same way.
			if policyErr := checkAnswerRoute(ctx, provider, model, resp); policyErr != nil {
				log.Error("Refusing the answer: it came from a provider the client's data policy does not allow",
					"requestID", requestID,
					"correlationID", correlationID,
					"provider", provider.Name(),
					"model", model,
					"error", loggedError(policyErr),
				)
				chargeBudget(ctx, provider, model, resp)
				trackRefusedAnswer(provider, model, resp, requestID, correlationID)
				publishRefusedAttempt(ctx, provider, model, resp, requestID, correlationID)
				publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
				return llm.CompletionResponse{}, policyErr
			}

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
			//
			// LX-100: a decision provider answers typed and writes no content.
			// Its answers are written here in the form the decision reader
			// takes, before anything asks whether there is content.
			if typedErr := typedAnswerContent(req, &resp); typedErr != nil {
				err = typedErr
			} else if kind := llm.ClassifyCompletion(resp); kind == types.KindOutputTruncated {
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
			// An answer that was produced and then refused here still cost
			// what it cost, to the client's ledger and to the process-wide one.
			if err != nil {
				chargeBudget(ctx, provider, model, resp)
				trackRefusedAnswer(provider, model, resp, requestID, correlationID)
				publishRefusedAttempt(ctx, provider, model, resp, requestID, correlationID)
				if telemetry.HasObserver() {
					refused.add(provider.Name(), model, resp)
				}
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
				"error", loggedError(err),
			)
			publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
			return llm.CompletionResponse{}, classifiedProviderFailure(withKindOfTheAnswer(emptyBecauseOfTheFinish(err, answered, resp), answered, resp), provider.Name(), model, requestID)
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
			"error", loggedError(err),
		)
		// The retry, to the observers of this request: which attempt failed,
		// the kind of failure and the wait, never the error's text.
		telemetry.ObserveOperationNote(ctx, telemetry.Note{
			Name:      telemetry.NoteRetry,
			Attempt:   attempt,
			ErrorKind: requestErrorKind(err, answered, resp),
			Wait:      delay,
		})

		if sleepErr := waitForRetry(ctx, delay); sleepErr != nil {
			publishFailedCall(ctx, provider, model, opts, start, providerCalls, requestID, correlationID)
			return llm.CompletionResponse{}, sleepErr
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
	noteCacheReads(log, req.PromptCacheKey, usage)
	cost := pricing.CalculateCostLogged(log, &usage, actualModel, actualProvider)

	// IN-004: a client's ledger records what its own calls cost. The `priced`
	// flag is passed through rather than letting a zero speak for itself --
	// an unpriced model reports zero, and a ledger that accumulates those keeps
	// reporting a spend of nothing while real money is being spent.
	chargeBudget(ctx, provider, model, resp)

	metadata := &types.ResultMetadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
		// Retries, not attempts: RetryCount is the count beyond the first, and
		// the envelope adds the one back. A request that succeeded on its third
		// try used to report a single attempt, because nothing wrote this down.
		RetryCount:     providerCalls - 1,
		TraceID:        telemetry.GetTraceID(ctx),
		SpanID:         telemetry.GetSpanID(ctx),
		StartTime:      start,
		EndTime:        time.Now(),
		Duration:       time.Since(start),
		Model:          actualModel,
		RequestedModel: model,
		ObservedModel:  resp.Model,
		Provider:       actualProvider,
		Mode:           opts.Mode,
		Intelligence:   opts.Intelligence,
		Timing:         callTiming(timing, timed, resp),
		TokenUsage:     &usage,
		CostInfo:       cost,
		CacheHit:       resp.Cached,
		Hedged:         resp.Hedged,
		ServiceTier:    resp.ServiceTier,
		FinishReason:   resp.FinishReason,
		Custom: map[string]any{
			"response_format": responseFormat,
			// Which contract produced this answer. A stored result that cannot
			// say is a result nobody can reproduce, compare, or trust a year
			// later. S-002.
			"schema_id": opts.SchemaID,
		},
	}
	// LX-100: how a decision was answered, on the record and for the operation
	// that asked: typed or as JSON, and whether a decision provider was asked
	// first.
	noteDecisionAnswer(ctx, resp, metadata)

	// Publish the record for whoever wants an envelope. The alternative was to
	// change the return type of every operation at once; this lets Result[T]
	// arrive operation by operation without a flag day, and the plain and
	// detailed forms still execute the identical path -- which is the property
	// that matters, because two return types that execute differently is how
	// the two drift.
	publishCallRecord(ctx, metadata)

	// What actually answered, which the start event could not know. The model
	// a provider serves a request with is not always the one it was asked for,
	// and a host reconciling cost against a span needs the served one. The
	// finish reason is the provider's own enum, and is the only thing that
	// distinguishes a complete answer from a truncated one. The cost is that
	// of this answer, the one that was accepted.
	finish.Provider = actualProvider
	finish.Model = actualModel
	finish.FinishReason = resp.FinishReason
	finish.Usage, finish.Cost = refused.plus(usage, cost)
	returned = true

	// A cache hit was not bought, and the process-wide ledger is a record of
	// what was.
	if !resp.Cached {
		pricing.TrackCost(cost, metadata)
	}
	telemetry.RecordLLMMetrics(metadata)

	// A request that went well is a debug line, and is not built, or written
	// to standard error under the logger's lock, before the answer is returned
	// unless somebody asked for it.
	if log.Enabled(ctx, slog.LevelDebug) {
		log.Debug("LLM request completed",
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
	}

	return resp, nil
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

// errEmptyCompletion is the refusal of an answer that carries no content. It
// is a malformed output, and it is still retried: isRetryableLLMError reads it
// before it asks the taxonomy, which would call a malformed output permanent.
var errEmptyCompletion = types.Classified(types.KindMalformedOutput, errors.New("provider returned empty completion content"))

// errToolCallsForAnOperation is the refusal of a reply to an operation's request
// that asks for tools. An operation declares none, so a provider that answers
// with a call has broken its contract (the providers of this library refuse such
// a reply themselves). It is a malformed output, and it is not retried: the same
// request would be answered the same way.
var errToolCallsForAnOperation = types.Classified(types.KindMalformedOutput, errors.New("provider answered with tool calls to a request that declared no tools"))

// validateLLMCompletion refuses a reply that says nothing. A reply that asks for
// tools is an answer whatever its text: a model that wants a tool run answers
// with the call and no words, and that reply used to be refused as empty and sent
// again, paid for each time. The calls were checked against the tools the request
// declared, and their ids and arguments, by the provider that read them.
func validateLLMCompletion(resp llm.CompletionResponse) error {
	if len(resp.ToolCalls) > 0 {
		return nil
	}
	if strings.TrimSpace(resp.Content) == "" {
		return errEmptyCompletion
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

	// An answer with no content is deterministic for a provider that sent
	// nothing and a transient fault for one that dropped it, and this has always
	// been tried again. It has a kind now, and the kind says a repeat is not
	// worth making, so it is decided here before the taxonomy is asked.
	if errors.Is(err, errEmptyCompletion) {
		return true
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

// checkCallOptions refuses options no request can be made under, before one is
// made. A Threshold that is not a number is one: every comparison with NaN is
// false, so it passes any range check and then orders nothing, and an infinite
// one is no confidence. The options reach a call from operations that validate
// them and from callers that pass a bare types.OpOptions, so the check is made
// where both arrive.
func checkCallOptions(opts types.OpOptions) error {
	if math.IsNaN(opts.Threshold) || math.IsInf(opts.Threshold, 0) {
		return types.Newf(types.KindConfiguration, "llm.call", "Threshold has to be a number from 0 to 1, and is not a finite number")
	}
	return nil
}

// contextOver reports a context that is already cancelled or past its deadline
// as a classified error, and nil for a live or nil context. The cause is kept,
// so errors.Is(err, context.Canceled) and context.DeadlineExceeded still hold.
func contextOver(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	cause := ctx.Err()
	if cause == nil {
		return nil
	}
	return &types.OperationError{Kind: llm.Classify(cause), Op: "llm.call", Cause: cause}
}

// waitForRetry waits out a backoff, or returns the classified reason the
// context ended first. It returned the bare context error, so a call cancelled
// during a backoff was the one failure of this function with no kind.
func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return contextOver(ctx)
	case <-timer.C:
		return nil
	}
}

// applySteering attaches the caller's per-call instructions to the volatile
// segment -- the user message -- and returns it unchanged when there are none.
//
// It used to attach them to the system prompt, which is the segment providers
// cache. See CA-002 and the comment at the call site.
//
// TC-001: steering is TrustApplicationData -- caller-supplied, per call, by
// code this library does not control -- and DelimitUntrusted wraps it in an
// explicit boundary before it goes anywhere near the request. It used to be
// concatenated in bare: "Additional instructions:\n" + steering + "\n\n" +
// userPrompt, which is indistinguishable, once assembled, from an
// instruction this library wrote itself. The "Additional instructions:"
// label stays -- it is still the right label for what this is -- but the
// text after it is now unambiguously marked as the caller's, with its own
// attempt at forging that boundary neutralized by sanitizeMarkers.
func applySteering(userPrompt, steering string) string {
	steering = strings.TrimSpace(steering)
	if steering == "" {
		return userPrompt
	}
	delimited := DelimitUntrusted(PromptSegment{Trust: TrustApplicationData, Content: steering})
	return strings.TrimSpace("Additional instructions:\n" + delimited + "\n\n" + userPrompt)
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
const promptReinforcementEnvVar = config.PromptReinforcementVariable

// promptReinforcementEnabled reports whether the reinforcement block is added.
// Anything but an explicit "0"/"false"/"off" leaves it on, because turning it
// off by accident changes behaviour silently.
//
// The variable is read once, by the first call that needs it
// (config.ReloadEnvironment reads it again). It was read on every call, and on
// Windows a read of the environment allocates twice.
func promptReinforcementEnabled() bool {
	return config.PromptReinforcementEnabled()
}

// strengthenSystemPrompt assembles the system/policy prompt every operation
// sends: this library's own reinforcement rules, plus the operation's own
// prompt text.
//
// TC-001: assembled through BuildSystemPrompt rather than plain string
// concatenation, with every segment explicitly tagged TrustPolicy (the
// reinforcement blocks, fixed and identical for every caller) or
// TrustDeveloperInstruction (systemPrompt, the operation's own text). The
// error path is unreachable through this function as written -- every
// segment below is one of the two trusted levels by construction -- and the
// fallback on it reproduces the exact bytes plain concatenation always
// produced, so a hypothetical future edit that adds an untrusted segment
// here fails loudly (BuildSystemPrompt refuses it) rather than silently
// changing what gets sent.
//
// The environment switch turns the block off for every call of the process, and
// a client's own setting (ExecConfig.OmitReinforcement) for the calls it makes.
func strengthenSystemPrompt(ctx context.Context, systemPrompt, responseFormat string) string {
	if !promptReinforcementEnabled() || execOmitReinforcement(ctx) {
		return strings.TrimSpace(systemPrompt)
	}

	baseRules := strings.TrimSpace(`Perform the semantic task faithfully using the provided input.
Do not merely restate schemas, field names, or type descriptions.
Infer, compare, rank, validate, transform, or summarize based on the actual content.`)

	segments := []PromptSegment{{Trust: TrustPolicy, Content: baseRules}}

	if responseFormat == "json" {
		jsonRules := strings.TrimSpace(`After reasoning about the task, return only the final JSON answer.
Do not include markdown fences, prose, placeholders, or schema descriptions.
Every field must be populated with task-relevant values supported by the input or clearly inferred from it.`)
		segments = append(segments, PromptSegment{Trust: TrustPolicy, Content: jsonRules})
	}

	segments = append(segments, PromptSegment{Trust: TrustDeveloperInstruction, Content: systemPrompt})

	built, err := BuildSystemPrompt(segments...)
	if err != nil {
		// Unreachable as this function is written -- see the doc comment
		// above. If it is ever reached, that is the trust boundary catching
		// a regression in this very function; falling back to the plain
		// concatenation this function always used to do keeps behaviour
		// identical to before TC-001 rather than sending an empty prompt.
		if responseFormat == "json" {
			return strings.TrimSpace(baseRules + "\n\n" + segments[1].Content + "\n\n" + systemPrompt)
		}
		return strings.TrimSpace(baseRules + "\n\n" + systemPrompt)
	}
	return built
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
	// A deadline the caller already set is the budget. DX-008.
	//
	// This used to layer the default timeout on unconditionally, and
	// context.WithTimeout takes the earlier of the two -- so a caller's SHORTER
	// deadline survived and a LONGER one was silently cut to 30 seconds. That
	// contradicts A-004's Revised line, "a deadline always wins", in the one
	// direction where the caller loses.
	//
	// Worse, it was not even uniform. Extract and Generate route through the
	// generic op runner, which never touched the context, so a 90-second
	// deadline reached the provider intact; Summarize and Choose came through
	// here and saw 30. Same library, same call shape, two different answers to
	// "how long do I get", and nothing anywhere saying which applied. Reported
	// from ArticleFlux, whose interest pass runs on a deliberate 90-second
	// budget -- raised from 20 after the entity pass timed out every time -- and
	// whose digest and categorise paths were being cut to 30 without a word.
	//
	// The default is a floor for callers who expressed no opinion, not a
	// ceiling on callers who did. A caller who wants both states the shorter
	// one, which is what a deadline already means.
	//
	// The client a call runs on is fixed here, by the operation that starts it,
	// whether or not the caller bounded it, so that everything the operation
	// reads is read from one client.
	caller, _ = PinExec(caller)
	if _, alreadyBounded := caller.Deadline(); alreadyBounded {
		return context.WithCancel(caller)
	}
	// A client that names a Timeout binds every operation whatever the call
	// site passed, and the default applies to the callers that name none.
	if client := execTimeout(caller); client > 0 {
		timeout = client
	} else if timeout <= 0 {
		timeout = config.GetTimeout()
	}
	return context.WithTimeout(caller, timeout)
}

// clientDeadline gives a call the client's deadline on the paths that derive
// none of their own (Extract and the operation runners, which read the context
// they were given). It returns ctx and a no-op when the snapshot names no
// Timeout or ctx already has a deadline, and allocates nothing then.
func clientDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return ctx, noCancel
	}
	if _, alreadyBounded := ctx.Deadline(); alreadyBounded {
		return ctx, noCancel
	}
	client := execTimeout(ctx)
	if client <= 0 {
		return ctx, noCancel
	}
	return context.WithTimeout(ctx, client)
}

// noCancel is the cancel function of a deadline that was not made.
func noCancel() {}

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

func noteCacheReads(log *logger.Logger, cacheKey string, usage types.TokenUsage) {
	if cacheKey == "" {
		return
	}

	calls, warn := countCacheRead(cacheKey, usage)
	if !warn {
		return
	}

	// Written after the lock is released: a log sink that is slow held every
	// call's cache bookkeeping up behind it. The key, not the prompt: the prompt
	// is built from the caller's data and this is a log line. It goes to the
	// logger of the call that made it due, so that a client that discards, or
	// logs elsewhere, is not written about on the process logger.
	log.Warn(
		"prompt cache is reporting no cached tokens for a prefix that keeps repeating",
		"cacheKey", cacheKey,
		"calls", calls,
		"likelyCause", "the stable prefix may be below the provider's minimum cacheable length, or something per-call is still inside it")
}

// countCacheRead records one call under the lock and reports how many calls
// the key has had and whether the diagnostic is due.
func countCacheRead(cacheKey string, usage types.TokenUsage) (calls int, warn bool) {
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
		return 0, false
	}

	cacheReadCounts[cacheKey]++
	calls = cacheReadCounts[cacheKey]
	if calls < cacheReadWarnAfter || cacheReadWarned[cacheKey] {
		return calls, false
	}
	cacheReadWarned[cacheKey] = true
	return calls, true
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
		RequestID:      requestID,
		CorrelationID:  correlationID,
		RetryCount:     providerCalls - 1,
		StartTime:      start,
		EndTime:        time.Now(),
		Duration:       time.Since(start),
		Model:          model,
		RequestedModel: model,
		// ObservedModel stays empty: the call failed, so no provider ever said
		// what answered. Drift is unknown for a failed request, not absent.
		Provider:     provider.Name(),
		Mode:         opts.Mode,
		Intelligence: opts.Intelligence,
	})
}

// callTiming is where a call's time went, for its record: what was measured
// while waiting for a slot, with what the provider measured of the answer that
// was accepted. It is nil when neither measured anything, so that a record
// with no timing is not read as a call that took no time.
//
// An attempt that was thrown away is in the call's Duration and not here: the
// provider's timing comes back with a response, and a failed attempt has none.
// So for a call that was retried, Duration is longer than the network time by
// the failed attempts and the pauses between them, and Timing.Outside counts
// them as outside the network.
//
// The timing of a response that is marked cached is ignored. A cache answers
// from memory, and one that replays the timing of the call that filled it (the
// library's own did, until it was found out) reports the original call's
// network time for a hit that took microseconds. The wait for a slot is still
// this call's own and is kept. A provider that timed a call and did not say
// how many is taken to have timed one.
func callTiming(queued types.Timing, timed bool, resp llm.CompletionResponse) *types.Timing {
	answer := resp.Timing
	if resp.Cached {
		answer = types.Timing{}
	}
	if !timed && !answer.Measured() {
		return nil
	}
	total := answer
	if total.Calls == 0 && total.Network > 0 {
		total.Calls = 1
	}
	total.Queued += queued.Queued
	return &total
}

// chargeBudget records what one response cost against the client's own budget,
// when the context carries one.
//
// It is called for every response a provider produced, whether or not the
// answer could be used: a truncated answer is tokens generated and billed. The
// buffered path recorded only the answer it accepted, so the attempts it threw
// away, and a request that ended in one, cost the client nothing on its own
// ledger.
//
// A response that reports no usage is recorded as spend of unknown size. It was
// recorded as priced at zero, which is the one thing it is known not to be: a
// ledger that takes "no figure" for "no cost" goes on reporting a spend of
// nothing while money is spent.
//
// A response served from a cache is not recorded at all: no provider was called
// for it. The cache zeroes its usage, which would otherwise read as unknown
// spend and turn a client's total into a floor for a call that cost nothing.
func chargeBudget(ctx context.Context, provider llm.Provider, model string, resp llm.CompletionResponse) {
	budget := ExecBudget(ctx)
	if budget == nil || resp.Cached {
		return
	}
	usage := resp.Usage
	if !reportsUsage(usage) {
		budget.Record(0, false)
		return
	}
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}
	if cost := pricing.CalculateCostLogged(loggerFor(ctx), &usage, actualModel, actualProvider); cost != nil {
		budget.Record(cost.TotalCost, cost.Priced)
	}
}

// checkBudget asks whichever budget governs this call whether it may proceed.
//
// IN-004: a client's own ledger replaces the process-wide one entirely rather
// than being checked in addition to it. Checking both would mean a client
// could be refused by a budget belonging to a different tenant, which is the
// multi-tenancy failure this seam exists to remove -- and a client that
// declared its own ceiling has said what it wants enforced. A ledger that was
// told to keep the process ceiling (ClientBudget.KeepProcessCeiling) is checked
// first and then the process-wide ceiling as well, so a budget given only to
// count lifts nothing that the process set.
func checkBudget(ctx context.Context) error {
	if budget := ExecBudget(ctx); budget != nil {
		if err := budget.Check(); err != nil {
			return err
		}
		if kept, ok := budget.(interface{ keepsProcess() bool }); !ok || !kept.keepsProcess() {
			return nil
		}
	}
	err := pricing.CheckBudget()
	if err != nil && types.KindOf(err) == types.KindUnknown {
		// The process budget's refusal is a plain error beside its sentinel;
		// it is a spent budget whatever builds it.
		return types.Classified(types.KindBudgetExceeded, err)
	}
	return err
}

// checkBudgetFor is checkBudget for a request that is about to be sent to model:
// it also refuses the request when the client's ledger has a ceiling and the
// model has no price. The spend of such a call is not known, so the ceiling could
// not hold it, and a ceiling that does not bind is the failure the ledger exists
// to prevent. The message names the model, cut to the length a log holds one to,
// and never a value of the caller's.
func checkBudgetFor(ctx context.Context, provider llm.Provider, model string) error {
	if err := checkBudget(ctx); err != nil {
		return err
	}
	budget, ok := ExecBudget(ctx).(interface{ refusesUnpricedCalls() bool })
	if !ok || !budget.refusesUnpricedCalls() {
		return nil
	}
	if cost := pricing.CalculateCostLogged(loggerFor(ctx), &types.TokenUsage{}, model, provider.Name()); cost != nil && cost.Priced {
		return nil
	}
	return types.Newf(types.KindBudgetExceeded, "client.budget",
		"the model %q has no price, so a call to it cannot be held to the spend ceiling; register a price for it, or allow unpriced calls on the Budget",
		types.Bound(model, types.MaxModelName))
}

// emptyBecauseOfTheFinish gives an answer that was refused for carrying no
// content the kind its finish reason names, when the finish says why it is
// empty: an answer withheld by the provider's content filter is a policy
// violation, which is what it was called before the empty answer had a kind of
// its own, and not a malformed output; it is marked as a refusal, since the
// kind alone does not tell it from a data policy that forbids the route. The
// retry decision is made before this, on the error as it was.
func emptyBecauseOfTheFinish(err error, answered bool, resp llm.CompletionResponse) error {
	if err == nil || !answered || !errors.Is(err, errEmptyCompletion) {
		return err
	}
	if kind := llm.ClassifyCompletion(resp); kind != types.KindUnknown && kind != types.KindMalformedOutput {
		return classifiedByTheFinish(kind, err)
	}
	return err
}

// withKindIfNone gives err the kind when nothing in its chain has one. An error
// that has a kind is returned as it is, so what errors.Is and types.KindOf
// answer for it today is not changed by a kind chosen here; its text and its
// chain are err's own.
func withKindIfNone(kind types.ErrorKind, err error) error {
	if err == nil || types.KindOf(err) != types.KindUnknown {
		return err
	}
	return types.Classified(kind, err)
}

// classifiedProviderFailure gives the error a provider's request ended in a
// kind when it has none, with the route it failed on. The provider's own error
// is an *llm.APIError, a rate limit, a network error, a context error or a
// plain one, and all of them leave here with a kind: what llm.Classify makes
// of them, or an unavailable provider for what it does not know. An error that
// has a kind is returned as it is. The text and the chain are the error's own.
func classifiedProviderFailure(err error, provider, model, requestID string) error {
	if err == nil || types.KindOf(err) != types.KindUnknown {
		return err
	}
	kind := llm.Classify(err)
	if kind == types.KindUnknown {
		kind = types.KindProviderUnavailable
	}
	retryAfter, _ := llm.RetryAfterFrom(err)
	return types.ClassifiedCall(kind, err, provider, model, requestID, retryAfter)
}

// publishRefusedAttempt puts what an answer that was produced and refused cost
// into the call's record, so that the usage and cost of a call reconcile with
// the ledgers it was charged to (chargeBudget, trackRefusedAnswer). The record
// is marked UsageOnly: it counts no attempt, since the record of the request
// it belonged to does, and it carries no identity. A cached answer cost
// nothing, and one that reports no usage has no figure to add.
func publishRefusedAttempt(ctx context.Context, provider llm.Provider, model string,
	resp llm.CompletionResponse, requestID, correlationID string) {

	usage := resp.Usage
	if resp.Cached || !reportsUsage(usage) {
		return
	}
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}
	publishCallRecord(ctx, &types.ResultMetadata{
		UsageOnly:     true,
		RequestID:     requestID,
		CorrelationID: correlationID,
		Model:         actualModel,
		Provider:      actualProvider,
		TokenUsage:    &usage,
		CostInfo:      pricing.CalculateCostLogged(loggerFor(ctx), &usage, actualModel, actualProvider),
		FinishReason:  resp.FinishReason,
		ServiceTier:   resp.ServiceTier,
	})
}

// trackRefusedAnswer tells the process-wide ledger what an answer cost when the
// answer was produced and then refused: a truncated one, or an attempt thrown
// away before a retry. Only the accepted answer was tracked, so for a caller
// with no client budget these cost nothing on any ledger. An answer that
// reports no usage has no figure to add and is not tracked.
func trackRefusedAnswer(provider llm.Provider, model string, resp llm.CompletionResponse, requestID, correlationID string) {
	usage := resp.Usage
	if resp.Cached || !reportsUsage(usage) {
		return
	}
	actualModel := resp.Model
	if actualModel == "" {
		actualModel = model
	}
	actualProvider := resp.Provider
	if actualProvider == "" {
		actualProvider = provider.Name()
	}
	cost := pricing.CalculateCost(&usage, actualModel, actualProvider)
	pricing.TrackCost(cost, &types.ResultMetadata{
		RequestID:     requestID,
		CorrelationID: correlationID,
		EndTime:       time.Now(),
		Model:         actualModel,
		Provider:      actualProvider,
		TokenUsage:    &usage,
		Custom:        map[string]any{"refused": true},
	})
}
