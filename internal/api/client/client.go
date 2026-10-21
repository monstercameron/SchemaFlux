package client

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	cfg "github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Client is a configured schemaflux client.
//
// Its With... methods configure it in place and return the same client, so a
// client is configured completely before it is shared. They are safe to call
// from several goroutines, which makes them race-free and not isolated: a
// context taken with Context keeps the configuration it was taken with, and
// the next one sees whatever was set last.
//
// The zero Client is usable: it has no provider of its own, and every method
// works on it.
//
// A few With... methods write state that is shared by the whole process, and
// their documentation says which: WithProvider, WithProviderConfig,
// WithProviderInstance and WithMockProvider install the provider as the
// package's default provider, WithRequestTracking replaces the process's
// request-tracking configuration, and WithDebug raises the shared logger's
// level while any client holds it on. Every other method changes only this
// client.
type Client struct {
	apiKey       string
	provider     llm.Provider
	providerName string
	timeout      time.Duration
	maxRetries   int
	retryBackoff time.Duration
	logger       *telemetry.Logger
	debugMode    bool

	// spec is how provider was built, so that a change to timeout, maxRetries,
	// retryBackoff or debugMode can build it again. It is nil when the provider
	// was handed in with WithProviderInstance, which this client cannot rebuild,
	// and when there is no provider.
	spec *providerSpec

	// tuned records which of the three provider settings the caller set, so a
	// provider instance that cannot take them can be reported by Err.
	tunedTimeout, tunedRetries, tunedBackoff bool

	// debugHeldOn is the logger this client has raised to debug level with
	// WithDebug(true), and nil when it holds none.
	debugHeldOn *telemetry.Logger

	// buildErr is why the provider last asked for could not be built, and
	// settingErr is a setting that was refused. Together with the checks Err
	// makes of the present state, they are what Err reports, so a builder chain
	// that could not do what it was asked is not silently indistinguishable from
	// one that could.
	buildErr   error
	settingErr error

	// budget is this client's own spend ledger (IN-004). Nil means the
	// process-wide pricing budget applies, which is what every caller who never
	// asked for one gets, unchanged.
	budget ops.BudgetChecker

	// scheduler bounds this client's concurrency. Nil means unscheduled.
	scheduler *ops.Scheduler

	// model and effort are what this client's calls use when a call names
	// neither: see WithModel and WithEffort.
	model  string
	effort types.Effort

	// decisionObserver is told after every decision: see WithDecisionObserver.
	decisionObserver func(context.Context, ops.DecisionRecord)

	// dataPolicy constrains where this client's calls may be routed, once
	// WithDataPolicy has set one; dataPolicyDeclared says that it has.
	dataPolicy         types.DataPolicy
	dataPolicyDeclared bool

	mu sync.RWMutex
}

// WithBudget gives this client its own spend ceiling, separate from the
// process-wide one and from every other client's.
//
// This is the field IN-004 is actually about. Before it, two clients in one
// process shared a single budget, so a tenant that exhausted its allowance
// stopped calls belonging to a tenant that had not — and there was no way to
// express "this client may spend $5" at all.
//
// A ceiling of zero or less clears the budget and returns the client to the
// process-wide one, rather than pinning it to a ceiling it can never spend
// under, which is what an unguarded assignment would do.
func (client *Client) WithBudget(ceilingUSD float64) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if ceilingUSD <= 0 {
		client.budget = nil
		return client
	}
	client.budget = ops.NewClientBudget(ceilingUSD)
	return client
}

// Warm opens this client's connection to its provider ahead of its first call:
// the name lookup, the connection and the handshake are paid here, where a
// program can afford them, and not by the first request it makes. It sends no
// prompt and costs nothing.
//
// It reports whether there was anything to warm. A client with no provider of
// its own, or one whose provider has no connection to open, has nothing to
// warm, and that is not an error. A provider wrapped by the middleware of the
// mw package, a chain, is warmed through the wrappers; one wrapped by something
// that does not say what it wraps (Unwrap() llm.Provider) is not.
//
// The request that warms carries no key, none of the headers the provider was
// configured with, and no content, and does not follow a redirect; it is sent
// through the transport the provider's calls use, so the connection it opens is
// the one the first call finds. It is bounded in time when the context is not.
func (client *Client) Warm(ctx context.Context) (warmed bool, err error) {
	client.mu.RLock()
	provider := client.provider
	client.mu.RUnlock()
	if provider == nil {
		return false, nil
	}
	return llm.Warm(ctx, provider)
}

// WithModel names the model every call on this client's context uses, in place
// of the tier's mapping. A model named on a call still wins. An empty name
// puts the tier's mapping back in charge.
//
// It is for a program that makes many kinds of call and wants them all on one
// model: each operation's options are built a different way, and some are not
// the caller's to build, so the one place that reaches all of them is the
// client. The only other was the SCHEMAFLUX_MODEL environment variable, which
// is one setting for the process.
func (client *Client) WithModel(model string) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.model = strings.TrimSpace(model)
	return client
}

// WithEffort names how much the model reasons before it answers, for every
// call on this client's context. An effort named on a call still wins, and
// this takes the place of an operation's own preference. EffortDefault leaves
// the choice to the call and the operation.
//
// The effort is a preference, as an operation's own is: it is sent on a route
// whose model takes it and left off a route that does not, so a client with a
// provider that takes no effort at all, Anthropic or any chat-completions
// provider, still works, and so does an alternate in a fallback chain. An
// effort named on a call itself is a requirement, and a route that cannot send
// it refuses the call with the efforts it does take. Reasoning is where most of
// a short call's time goes, and what it buys is arithmetic and multi-step
// inference: a program whose calls are judgments of short text can turn it
// down and answer in a fraction of the time. Measure before relying on it.
func (client *Client) WithEffort(effort types.Effort) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.effort = effort
	return client
}

// WithDecisionObserver registers a function that is told, after every decision
// made on this client's context (Is, Which, WhichOrNone, Rate, Assess and their
// Result forms, and the combinators that ask one), what was asked and what was
// read. A program whose control flow is decided by a model can say from it why
// it took a path.
//
// It is called once for each decision, when the decision has succeeded and when
// it has failed; a failed decision reports its questions and its error, and no
// answers. It is called on the goroutine that made the decision, after the
// provider's answer has been read and while no lock of this library's is held,
// and decisions that run at once call it at once. A panic in it is recovered and
// logged, without the record, and does not change what the decision returns.
//
// The record holds the caller's questions and the model's answers, and goes to
// this function and nowhere else: it is not logged, and no error carries it.
// With no observer registered no record is built. A nil function removes the
// observer. A context taken before the change keeps the observer it was given.
func (client *Client) WithDecisionObserver(observer func(context.Context, ops.DecisionRecord)) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.decisionObserver = observer
	return client
}

// WithScheduler bounds this client's concurrency independently of any other
// client's. Every request a call on this client's context sends to a provider
// takes a slot in the scheduler first: one for each attempt of a buffered
// call, given back for the wait before a retry, and one for the life of a
// stream. A call the scheduler does not admit fails with ErrAdmissionRejected,
// ErrShutdown, or the caller's own cancellation, sends nothing, and is not
// retried.
//
// The scheduler's limits on tokens and cost in flight are fed an estimate for
// each request: the prompt and the output ceiling in tokens, and their cost at
// this library's price for the model. A model with no price in the table is
// estimated at nothing, so a ceiling on cost does not hold it back. The
// per-tenant limit is not fed anything, since a client is one tenant; give
// each tenant a client and a scheduler.
//
// A nil scheduler removes the bound. Before LX-126 the scheduler was stored
// and never consulted.
func (client *Client) WithScheduler(scheduler *ops.Scheduler) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.scheduler = scheduler
	return client
}

// WithDataPolicy constrains where this client's calls may be routed. Before
// LX-126 the policy was stored and never consulted.
//
// A call on this client's context is refused, with ErrPolicyViolation and
// before anything is sent, unless all of these hold:
//
//   - its provider is named in AllowedProviders;
//   - its route, the provider and model, has declared where it runs and what
//     it retains with DeclareRoute;
//   - every region the route may run in is in AllowedRegions;
//   - the route retains content for no longer than MaxRetentionDays.
//
// The policy does not fail open. A list left empty allows nothing, so a policy
// has to name its providers and its regions, and with MaxRetentionDays unset
// only a route that retains nothing is allowed. The zero DataPolicy therefore
// refuses every call.
//
// Two more fields are enforced. AllowResultCaching, when false, makes mw.Cache
// pass this client's calls through without reading or storing an answer.
// MinimumContract is the lowest contract level an answer may be delivered at,
// and it is enforced where a contract level is worked out, which is every
// function that returns a Result: ExtractResult, SummarizeResult,
// RewriteResult, TranslateResult, ExpandResult, SortResult, and RunOpResult
// and RunOpBatchResult. A plain Extract or Classify works out no contract
// level and is not held to it.
//
// Three are not enforced by anything in a call: Classification, which is a
// label; AllowTrainingUse, because no route declares whether it trains on
// what it is sent; and ContentLogging, because the logger's level is set once
// for the process (telemetry.SetContentLoggingPolicy) and not per client.
//
// What is checked before a request is sent is the provider this client hands
// it to. For a provider that routes onward: mw.Fallback holds each of its
// alternates to this policy before using it, and an answer that names a
// provider the policy does not allow is refused with ErrPolicyViolation,
// though by then the request has been sent.
func (client *Client) WithDataPolicy(policy types.DataPolicy) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	// The lists are copied. The policy held here used to share them with the
	// caller, so changing a slice after the call changed the policy, for this
	// client and for calls already in flight.
	policy.AllowedProviders = append([]string(nil), policy.AllowedProviders...)
	policy.AllowedRegions = append([]string(nil), policy.AllowedRegions...)
	client.dataPolicy = policy
	client.dataPolicyDeclared = true
	return client
}

// WithoutDataPolicy removes the client's data policy, so its calls are not
// restricted. It is not the same as setting an empty policy, which refuses
// every call.
func (client *Client) WithoutDataPolicy() *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPolicy = types.DataPolicy{}
	client.dataPolicyDeclared = false
	return client
}

// Spent reports what this client has recorded spending, and whether that figure
// is complete. It is false when any call used an unpriced model, in which case
// the number is a floor rather than a total — a distinction that matters
// precisely when somebody is deciding whether they are near a ceiling.
//
// A client with no budget of its own reports (0, false): it has no ledger, so
// it has no figure, and reporting a confident zero would be a lie about the
// process-wide spend it is actually contributing to.
func (client *Client) Spent() (usd float64, complete bool) {
	client.mu.RLock()
	budget := client.budget
	client.mu.RUnlock()

	ledger, ok := budget.(*ops.ClientBudget)
	if !ok || ledger == nil {
		return 0, false
	}
	return ledger.Spent()
}

// Err reports what is wrong with the client as it is now, or nil. The builder
// methods return *Client so they can chain, which leaves them nowhere to put an
// error; this is where it goes.
//
// It is the present state and not a history. A failure to build a provider stays
// until a provider is built; a missing model stops being reported when the
// client is given one with WithModel, or when SCHEMAFLUX_MODEL names one; a
// refused setting stops being reported when a valid one replaces it.
//
// Err also reports a setting the client cannot apply: WithTimeout, WithRetries
// and WithRetryBackoff configure a provider this client builds, and a provider
// handed in with WithProviderInstance is configured by whoever built it.
func (client *Client) Err() error {
	client.mu.RLock()
	defer client.mu.RUnlock()

	if client.buildErr != nil {
		return client.buildErr
	}
	if client.settingErr != nil {
		return client.settingErr
	}
	if client.provider != nil && client.spec == nil {
		var ignored []string
		if client.tunedTimeout {
			ignored = append(ignored, "WithTimeout")
		}
		if client.tunedRetries {
			ignored = append(ignored, "WithRetries")
		}
		if client.tunedBackoff {
			ignored = append(ignored, "WithRetryBackoff")
		}
		if len(ignored) > 0 {
			return types.Newf(types.KindConfiguration, "client",
				"%s cannot be applied to a provider supplied with WithProviderInstance; configure that provider where it is built, or let the client build it with WithProvider",
				strings.Join(ignored, ", "))
		}
	}
	if client.spec != nil && client.model == "" {
		// A provider the library has no model for is configured, not usable: every
		// call fails at its first request with a message the caller reads long
		// after they set this up. The provider stays installed, because the caller
		// may be about to name a model.
		if err := cfg.ValidateModelResolution(client.providerName); err != nil {
			return err
		}
	}
	return nil
}

// log returns the client's logger, or the process logger for a client that has
// none, which is the zero Client. The caller holds client.mu.
func (client *Client) log() *telemetry.Logger {
	if client.logger != nil {
		return client.logger
	}
	return telemetry.GetLogger()
}

// providerSpec is the request a provider was built from: the provider's name and
// what the caller passed beside it. The client's own timeout, retries, backoff
// and debug setting are read when it is built, so building it again from the
// same spec picks up whatever they are by then.
type providerSpec struct {
	name     string
	override llm.ProviderConfig
}

// cloneProviderConfig copies what a caller can change after handing it over: the
// header map and the endpoint policy. WithDataPolicy copies its lists for the
// same reason -- a policy the client keeps must not change when the caller edits
// their own value.
func cloneProviderConfig(config llm.ProviderConfig) llm.ProviderConfig {
	if len(config.ExtraHeaders) > 0 {
		config.ExtraHeaders = cloneStringMap(config.ExtraHeaders)
	}
	if config.EndpointPolicy != nil {
		policy := *config.EndpointPolicy
		policy.AllowedSchemes = append([]string(nil), policy.AllowedSchemes...)
		policy.AllowedHosts = append([]string(nil), policy.AllowedHosts...)
		config.EndpointPolicy = &policy
	}
	return config
}

// NewClient creates a client that uses OpenAI with apiKey. An explicit key wins
// over every environment variable: the environment supplies a key only to a
// client that was given none.
func NewClient(apiKey string) *Client {
	client := newClient(apiKey)

	// Initialize with OpenAI provider by default. An empty key leaves the
	// client with no provider: substituting the mock here meant every operation
	// returned "Mock response for: ..." and the caller had no way to notice.
	// Ask for the mock deliberately with WithMockProvider.
	if apiKey != "" {
		spec := &providerSpec{name: "openai"}
		provider, err := llm.CreateProvider(spec.name, client.providerConfig(spec.name, spec.override))
		if err == nil {
			client.provider = provider
			client.spec = spec
		} else {
			client.buildErr = fmt.Errorf("configuring provider %q: %w", spec.name, err)
			client.logger.Warn("Failed to create OpenAI provider", "error", err)
		}
	} else {
		client.logger.Warn("NewClient was given no API key; the client has no provider. " +
			"Call WithProvider, WithProviderInstance, or WithMockProvider before using it.")
	}

	return client
}

// newClient is a client with its defaults and no provider.
func newClient(apiKey string) *Client {
	return &Client{
		apiKey:       apiKey,
		providerName: "openai",
		timeout:      30 * time.Second,
		maxRetries:   3,
		retryBackoff: 1 * time.Second,
		logger:       telemetry.GetLogger(),
	}
}

// defaultProviderOwner is the client that last installed the package's default
// provider. A client that rebuilds its provider replaces the default only while
// it is still the owner, so retuning one client never takes the default away
// from another.
var defaultProviderOwner struct {
	mu     sync.Mutex
	client *Client
}

// installDefaultProvider makes provider the package's default provider, on
// behalf of owner.
func installDefaultProvider(owner *Client, provider llm.Provider) {
	defaultProviderOwner.mu.Lock()
	defer defaultProviderOwner.mu.Unlock()
	ops.SetDefaultProvider(provider)
	defaultProviderOwner.client = owner
}

// refreshDefaultProvider replaces the default provider with the rebuilt
// provider of owner, if owner installed the one being replaced.
func refreshDefaultProvider(owner *Client, provider llm.Provider) {
	defaultProviderOwner.mu.Lock()
	defer defaultProviderOwner.mu.Unlock()
	if defaultProviderOwner.client == owner {
		ops.SetDefaultProvider(provider)
	}
}

// rebuildProvider builds the provider again from its spec, so a changed setting
// reaches a provider that already exists. It does nothing when there is no spec:
// either there is no provider yet, and the setting is read when one is built, or
// the provider was handed in and is not this client's to rebuild. The caller
// holds client.mu for writing.
func (client *Client) rebuildProvider() {
	if client.spec == nil {
		return
	}
	provider, err := llm.CreateProvider(client.spec.name, client.providerConfig(client.spec.name, client.spec.override))
	if err != nil {
		client.buildErr = fmt.Errorf("configuring provider %q: %w", client.spec.name, err)
		client.log().Error("Failed to apply the new setting to the provider; the previous one is still in use",
			"provider", client.spec.name, "error", err)
		return
	}
	client.provider = provider
	refreshDefaultProvider(client, provider)
}

// WithMockProvider attaches the local mock provider, which answers every
// operation with canned text. It exists for tests and offline demos, and has to
// be asked for: an empty key used to select it silently.
//
// Like WithProvider, it makes the provider the package's default provider.
func (client *Client) WithMockProvider() *Client {
	return client.WithProviderConfig("local", llm.ProviderConfig{})
}

// WithTimeout sets the HTTP timeout of the requests this client's provider
// sends, whether the provider was chosen before or after this call. A timeout of
// zero or less is refused, and Err says so: it would mean no timeout at all.
//
// It applies to a provider this client builds. A provider supplied with
// WithProviderInstance is configured by its owner, and Err reports the setting
// as one that could not be applied.
//
// A client replaces the provider it built, so a provider obtained from it
// earlier does not take the new timeout. If this client installed the package's
// default provider, the replacement becomes the default.
func (client *Client) WithTimeout(timeout time.Duration) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if timeout <= 0 {
		client.settingErr = types.Newf(types.KindConfiguration, "client",
			"WithTimeout needs a positive duration; zero or less would mean no timeout")
		return client
	}
	client.settingErr = nil
	client.tunedTimeout = true
	client.timeout = timeout
	client.rebuildProvider()
	return client
}

// WithRetries sets the maximum number of retry attempts for provider calls. Zero
// means a failed request is not repeated, and a negative number is taken as
// zero. It reaches a provider the client has already built, and is held for the
// next one; see WithTimeout for what that does and does not cover.
func (client *Client) WithRetries(maxRetries int) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if maxRetries < 0 {
		maxRetries = 0
	}
	client.tunedRetries = true
	client.maxRetries = maxRetries
	client.rebuildProvider()
	return client
}

// WithRetryBackoff sets the base backoff between retry attempts. Zero or less
// leaves the backoff as it was, because a zero backoff is a retry storm and not
// a setting. It reaches a provider the client has already built, and is held for
// the next one; see WithTimeout for what that does and does not cover.
func (client *Client) WithRetryBackoff(backoff time.Duration) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if backoff > 0 {
		client.tunedBackoff = true
		client.retryBackoff = backoff
		client.rebuildProvider()
	}
	return client
}

// WithProvider chooses the provider the client uses, by name, built with the
// client's key and settings and the environment's base URL. It makes the
// provider the package's default provider; see WithProviderConfig.
func (client *Client) WithProvider(providerName string) *Client {
	return client.WithProviderConfig(providerName, llm.ProviderConfig{})
}

// WithProviderConfig chooses the provider the client uses, by name and with
// explicit provider configuration. What the configuration leaves unset comes
// from the client's own settings, and from the environment for the key and the
// base URL. The client remembers the request, so a later WithTimeout,
// WithRetries, WithRetryBackoff or WithDebug builds the provider again with the
// new setting.
//
// It writes process-wide state: the new provider becomes the package's default
// provider, the one an operation reaches when its context carries no client's.
// An operation that is given the client's Context is not affected by what any
// other client does; one that is not is served by whichever client installed a
// provider last. If the provider cannot be built the previous one stays in use,
// the default is not changed, and Err says why.
func (client *Client) WithProviderConfig(providerName string, config llm.ProviderConfig) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()

	providerName = llm.NormalizeProviderName(providerName)
	spec := &providerSpec{name: providerName, override: cloneProviderConfig(config)}

	provider, err := llm.CreateProvider(providerName, client.providerConfig(providerName, spec.override))
	if err != nil {
		// The name used to be assigned before the provider was built, so a
		// failed switch left the client reporting the new provider's name while
		// still running the old one -- or the mock. Name and provider now move
		// together or not at all.
		client.buildErr = fmt.Errorf("configuring provider %q: %w", providerName, err)
		client.log().Error("Failed to configure provider; the previous one is still in use",
			"provider", providerName, "previous", client.providerName, "error", err)
		return client
	}

	client.buildErr = nil
	client.providerName = providerName
	client.provider = provider
	client.spec = spec
	installDefaultProvider(client, provider)

	// A provider this library has no model for is configured, not usable: every
	// operation will fail at its first call with a message the caller reads long
	// after they set this up. Err reports it, and so does this line, while the
	// caller is looking at the configuration. The provider is still installed --
	// the caller may be about to name a model, and refusing to attach it would
	// lose the rest of the configuration to a problem they can fix.
	if client.model == "" {
		if err := cfg.ValidateModelResolution(providerName); err != nil {
			client.log().Warn("Provider configured with no resolvable model",
				"provider", providerName, "error", err)
			return client
		}
	}

	client.log().Info("Provider configured", "provider", providerName)

	return client
}

// WithProviderInstance sets an already-constructed provider on the client. A nil
// provider is ignored.
//
// It writes process-wide state: the provider becomes the package's default
// provider, as with WithProviderConfig.
//
// The client does not configure a provider it was handed. WithTimeout,
// WithRetries and WithRetryBackoff do not reach it, and Err reports each of them
// that was set.
func (client *Client) WithProviderInstance(provider llm.Provider) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()

	if provider == nil {
		client.log().Warn("Ignoring nil provider instance")
		return client
	}

	client.provider = provider
	client.providerName = provider.Name()
	client.spec = nil
	client.buildErr = nil
	installDefaultProvider(client, provider)
	client.log().Info("Provider configured", "provider", provider.Name(), "mode", "instance")
	return client
}

// debugHolds counts, for each logger, how many clients have raised it to debug
// level with WithDebug(true), and the level it had before the first of them did.
// The level was saved by each client separately, so two clients that turned debug
// on and then off in the order on, on, off, off left a logger shared by the whole
// process at debug.
var debugHolds = struct {
	mu       sync.Mutex
	byLogger map[*telemetry.Logger]*debugHold
}{byLogger: map[*telemetry.Logger]*debugHold{}}

type debugHold struct {
	holders     int
	levelBefore telemetry.LogLevel
}

func acquireDebug(logger *telemetry.Logger) {
	debugHolds.mu.Lock()
	defer debugHolds.mu.Unlock()
	hold := debugHolds.byLogger[logger]
	if hold == nil {
		hold = &debugHold{levelBefore: logger.Level()}
		debugHolds.byLogger[logger] = hold
	}
	hold.holders++
	logger.SetLevel(telemetry.DebugLevel)
}

// releaseDebug gives back one client's hold. The last one puts the level back,
// unless something else has set it since: a level somebody chose while debug was
// on is not the library's to overwrite.
func releaseDebug(logger *telemetry.Logger) {
	debugHolds.mu.Lock()
	defer debugHolds.mu.Unlock()
	hold := debugHolds.byLogger[logger]
	if hold == nil {
		return
	}
	hold.holders--
	if hold.holders > 0 {
		return
	}
	delete(debugHolds.byLogger, logger)
	if logger.Level() == telemetry.DebugLevel {
		logger.SetLevel(hold.levelBefore)
	}
}

// WithDebug turns debug mode on or off for this client: its provider is built
// with debug set, and the logger is raised to debug level.
//
// The logger is shared by the whole process, so the raise is counted. The level
// stays at debug while any client has debug on, and goes back to what it was
// before the first of them turned it on when the last one turns it off, in
// whatever order they do so. Turning debug off on a client that never turned it
// on changes nothing. A level set with SetLogLevel while debug is on is kept.
func (client *Client) WithDebug(enabled bool) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.debugMode != enabled {
		client.debugMode = enabled
		client.rebuildProvider()
	}

	if enabled {
		if client.debugHeldOn == nil {
			client.debugHeldOn = client.log()
			acquireDebug(client.debugHeldOn)
		}
		return client
	}

	if client.debugHeldOn != nil {
		releaseDebug(client.debugHeldOn)
		client.debugHeldOn = nil
	}
	return client
}

// releaseDebugHold gives back the hold this client has on the shared logger's
// debug level, if it has one, and leaves its debug setting as it is. Init calls it
// on the client it replaces: a client nobody can reach any more cannot turn debug
// off, and its hold would keep the logger at debug for the life of the process.
func (client *Client) releaseDebugHold() {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.debugHeldOn != nil {
		releaseDebug(client.debugHeldOn)
		client.debugHeldOn = nil
	}
}

// WithRequestTracking configures request and correlation tracking for the whole
// process: it replaces the configuration every client and every operation reads,
// and is the same as ConfigureRequestTracking.
func (client *Client) WithRequestTracking(config requesttracking.Config) *Client {
	requesttracking.Configure(config)
	return client
}

// Context returns ctx carrying this client's provider, so an operation invoked
// with the returned context reaches THIS client's provider rather than the
// package-level default (TI-002, IN-004).
//
// It exists because Go has no type-parameterised methods, so client.Extract[T]
// cannot be written and the provider became a process-wide global instead.
// Every Client construction overwrites that global, which means building a
// second client silently repointed the first one's operations at the second
// one's provider -- two clients in one process is a library embedded twice, a
// test running beside application code, or a tenant per client, and all three
// were broken.
//
// Pass the result as the operation's context:
//
//	opts := schemaflux.NewExtractOptions()
//	opts.OpOptions.Context = client.Context(ctx)
//	invoice, err := schemaflux.Extract[Invoice](body, opts)
//
// A client with no provider of its own still attaches its budget, scheduler
// and data policy; the package's default provider then serves the call.
func (client *Client) Context(ctx context.Context) context.Context {
	client.mu.RLock()
	snapshot := ops.ExecConfig{
		Provider:           client.provider,
		Budget:             client.budget,
		Scheduler:          client.scheduler,
		DataPolicy:         client.dataPolicy,
		DataPolicyDeclared: client.dataPolicyDeclared,
		Model:              client.model,
		Effort:             client.effort,
		DecisionObserver:   client.decisionObserver,
	}
	client.mu.RUnlock()

	// IN-004: the snapshot is taken under the lock and copied into the context,
	// so a call already in flight keeps the configuration it started with even
	// if this client is reconfigured afterwards. That is what makes the
	// snapshot immutable in the sense that matters — not that the struct has no
	// setters, but that nothing can change a call's configuration mid-call.
	//
	// WithProvider is still applied, because it is the narrower seam that
	// existing tests and callers already use and it costs nothing to keep both
	// readers satisfied by one call to this method.
	ctx = ops.WithExecConfig(ctx, &snapshot)
	return ops.WithProvider(ctx, snapshot.Provider)
}

// Global configuration
var (
	defaultClient *Client
	mu            sync.RWMutex
)

// Init initializes the schemaflux library with the provided API key.
//
// It returns an error when no credential can be resolved and the configured
// provider needs one. Falling through to the mock provider in that case would
// return text such as "Mock response for: ..." from every operation, which
// parses into zero-valued structs and is indistinguishable from a working
// deployment until someone reads the output.
//
// Adding the error return is source-compatible: Go permits discarding the
// result of a function call, so existing `Init(key)` statements still compile.
func Init(key string) error {
	// The settings the library reads from the environment are kept after the
	// first read. Initialising is the caller saying "configure from the
	// environment as it is now", so a variable set since an earlier use of the
	// library in this process has to count. InitWithEnv reaches this after it
	// has loaded its file.
	cfg.ReloadEnvironment()

	mu.Lock()
	defer mu.Unlock()

	provider := "openai"
	if p := os.Getenv("SCHEMAFLUX_PROVIDER"); p != "" {
		provider = p
	}

	// A timeout the environment names has to be one that can be used: a zero
	// deadline expires as it is set, so accepting it would fail every call that
	// carries no deadline of its own. Refuse it here, where the caller is
	// configuring, and name the variable.
	timeout, err := cfg.TimeoutFromEnv()
	if err != nil {
		return err
	}

	debugMode := cfg.GetDebugMode()

	// The key the caller gave wins. With none, the environment is the fallback:
	// the provider's own variables first, then SCHEMAFLUX_API_KEY.
	apiKey := key
	if apiKey == "" {
		apiKey = resolveProviderAPIKey(provider, "")
	}

	if apiKey == "" {
		// The mock provider is a legitimate choice, but only when it was asked
		// for. Selecting it because a credential is missing turns a
		// configuration error into plausible-looking fake output.
		if llm.NormalizeProviderName(provider) != "local" {
			return fmt.Errorf(
				"schemaflux: no API key for provider %q; set SCHEMAFLUX_API_KEY or a provider-specific key, or set SCHEMAFLUX_PROVIDER=local to use the mock provider deliberately",
				provider)
		}
		replaceDefaultClient(NewClient("").WithMockProvider())
		return nil
	}

	// The client is built bare and the provider chosen once, so Init does not
	// construct an OpenAI provider only to replace it.
	client := newClient(apiKey).
		WithTimeout(timeout).
		WithProvider(provider).
		WithDebug(debugMode)
	replaceDefaultClient(client)

	// Init has an error return and a configuration failure to report, so report
	// it. The client is still installed: the failure is "no model for this
	// provider", which a caller can fix with an environment variable without
	// rebuilding anything, and discarding their provider would help nobody.
	return client.Err()
}

// replaceDefaultClient makes next the default client, and releases the debug hold
// of the one it replaces. The caller holds mu.
func replaceDefaultClient(next *Client) {
	previous := defaultClient
	defaultClient = next
	if previous != nil && previous != next {
		previous.releaseDebugHold()
	}
}

// SetDefaultClient replaces the default client, or clears it when given nil.
//
// It exists so a test double can be installed and removed: schemafluxtest.Install
// needs to put the previous client back, and without this the only way to
// restore state after a test was to re-run Init and hope the environment still
// held whatever it held before.
//
// Setting the provider on a client also sets the package-level provider the
// operations read, which is why this is a global rather than a handle — see
// TODOS.md TI-002.
func SetDefaultClient(client *Client) {
	mu.Lock()
	defer mu.Unlock()
	defaultClient = client

	if client == nil {
		installDefaultProvider(nil, nil)
		return
	}

	client.mu.RLock()
	provider := client.provider
	client.mu.RUnlock()

	if provider != nil {
		installDefaultProvider(client, provider)
	}
}

// GetDefaultClient returns the default client, or nil if the library has not
// been initialised.
func GetDefaultClient() *Client {
	mu.RLock()
	defer mu.RUnlock()
	return defaultClient
}

// InitWithEnv initializes SchemaFlux from environment variables, first loading
// any named .env files. With no arguments it loads ./.env when present.
//
// Values already set in the process environment win: a .env file supplies
// defaults, it does not override an explicit export. A named path that does not
// exist is an error; the default ./.env is optional.
func InitWithEnv(paths ...string) error {
	if len(paths) == 0 {
		if _, err := os.Stat(defaultEnvFile); err == nil {
			if err := godotenv.Load(defaultEnvFile); err != nil {
				return fmt.Errorf("schemaflux: loading %s: %w", defaultEnvFile, err)
			}
		}
	} else {
		for _, path := range paths {
			if err := godotenv.Load(path); err != nil {
				return fmt.Errorf("schemaflux: loading %s: %w", path, err)
			}
		}
	}

	return Init("")
}

const defaultEnvFile = ".env"

// GetLogger returns the default logger for the schemaflux package.
func GetLogger() *telemetry.Logger {
	if client := GetDefaultClient(); client != nil {
		client.mu.RLock()
		defer client.mu.RUnlock()
		return client.logger
	}
	return telemetry.GetLogger()
}

// ConfigureLogging replaces the global logger configuration and keeps the default client in sync.
func ConfigureLogging(loggerConfig telemetry.LoggerConfig) *telemetry.Logger {
	log := telemetry.ConfigureLogger(loggerConfig)
	setDefaultClientLogger(log)
	return log
}

// SetLogLevel updates the global logger level.
func SetLogLevel(level telemetry.LogLevel) {
	log := telemetry.GetLogger()
	log.SetLevel(level)
	setDefaultClientLogger(log)
}

// setDefaultClientLogger keeps the default client's logger in step, under the
// same locks Init writes it with. These accessors used to read defaultClient
// unguarded while Init wrote it, which -race reports as soon as the two run
// concurrently.
func setDefaultClientLogger(log *telemetry.Logger) {
	client := GetDefaultClient()
	if client == nil {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	client.logger = log
}

// GetLogEntries returns captured log history for review.
func GetLogEntries() []telemetry.LogEntry {
	return telemetry.GetLogger().Entries()
}

// ResetLogEntries clears captured log history.
func ResetLogEntries() {
	telemetry.GetLogger().ResetEntries()
}

// ConfigureRequestTracking replaces the global request tracking configuration.
func ConfigureRequestTracking(trackingConfig requesttracking.Config) {
	requesttracking.Configure(trackingConfig)
}

// GetRequestTrackingConfig returns the active request tracking configuration.
func GetRequestTrackingConfig() requesttracking.Config {
	return requesttracking.GetConfig()
}

// WithRequestID returns a context carrying the given request ID.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return requesttracking.WithRequestID(ctx, requestID)
}

// WithCorrelationID returns a context carrying the given correlation ID.
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return requesttracking.WithCorrelationID(ctx, correlationID)
}

// WithRequestTrackingMetadata returns a context carrying request tracking metadata.
func WithRequestTrackingMetadata(ctx context.Context, metadata requesttracking.Metadata) context.Context {
	return requesttracking.WithMetadata(ctx, metadata)
}

// RequestTrackingFromContext returns request tracking metadata from context.
func RequestTrackingFromContext(ctx context.Context) requesttracking.Metadata {
	return requesttracking.FromContext(ctx)
}

// InjectRequestTracking writes tracking headers into a carrier map.
func InjectRequestTracking(ctx context.Context, carrier map[string]string) {
	requesttracking.Inject(ctx, carrier)
}

// ExtractRequestTracking reads tracking headers from a carrier map into context.
func ExtractRequestTracking(ctx context.Context, carrier map[string]string) context.Context {
	return requesttracking.Extract(ctx, carrier)
}

// The settings of a client that was never given any, which is the zero Client
// and no other: NewClient sets them, and WithTimeout refuses a zero.
const (
	defaultClientTimeout = 30 * time.Second
	defaultClientRetries = 3
	defaultClientBackoff = 1 * time.Second
)

func (client *Client) providerConfig(providerName string, override llm.ProviderConfig) llm.ProviderConfig {
	timeout, retries, backoff := client.timeout, client.maxRetries, client.retryBackoff
	if timeout <= 0 {
		timeout = defaultClientTimeout
	}
	if retries == 0 && !client.tunedRetries {
		retries = defaultClientRetries
	}
	if backoff <= 0 {
		backoff = defaultClientBackoff
	}

	built := llm.ProviderConfig{
		APIKey:       resolveProviderAPIKey(providerName, client.apiKey),
		BaseURL:      resolveProviderBaseURL(providerName),
		Timeout:      timeout,
		MaxRetries:   retries,
		RetryBackoff: backoff,
		Debug:        client.debugMode,
	}

	if override.APIKey != "" {
		built.APIKey = override.APIKey
	}
	if override.BaseURL != "" {
		built.BaseURL = override.BaseURL
	}
	if override.OrgID != "" {
		built.OrgID = override.OrgID
	}
	if override.Timeout > 0 {
		built.Timeout = override.Timeout
	}
	if override.MaxRetries > 0 {
		built.MaxRetries = override.MaxRetries
	}
	if override.RetryBackoff > 0 {
		built.RetryBackoff = override.RetryBackoff
	}
	if override.Debug {
		built.Debug = true
	}
	if len(override.ExtraHeaders) > 0 {
		built.ExtraHeaders = cloneStringMap(override.ExtraHeaders)
	}
	if override.Store {
		built.Store = true
	}
	if override.OmitTemperature {
		built.OmitTemperature = true
	}
	// LAT-008. The tier was a field of the public ProviderConfig that this
	// function did not copy, so a client given one sent none.
	if override.ServiceTier != "" {
		built.ServiceTier = override.ServiceTier
	}
	// A client's own model for a tier: copied whole, so a tier the override
	// leaves empty keeps the configured model for that tier.
	if override.Models != ([3]string{}) {
		built.Models = override.Models
	}
	// SEC-004 and SC-007 were dropped here. Both are opt-in fields a caller sets
	// on the public ProviderConfig (schemaflux.go aliases the type), and neither
	// was copied -- so `WithProviderConfig("openai", ProviderConfig{EndpointPolicy: p})`
	// reached llm.CreateProvider with a nil policy and the endpoint check, which
	// exists and works one layer down, simply never ran. A security control that
	// silently does nothing through the most visible way of switching it on is
	// worse than one that is absent, because the caller believes it is enforced.
	//
	// The general defect is the copy list itself: thirteen named fields, which
	// cannot fail when somebody adds a fourteenth. That is A-014's bug exactly
	// (applyDefaults, internal/ops), and it is guarded the same way --
	// TestProviderConfigOverrideCarriesEveryField walks the struct by reflection,
	// so the next field added here fails a test instead of being silently lost.
	if override.EndpointPolicy != nil {
		built.EndpointPolicy = override.EndpointPolicy
	}
	if override.HTTPClient != nil {
		built.HTTPClient = override.HTTPClient
	}

	return built
}

// resolveProviderAPIKey picks the key a provider is built with. A key the caller
// gave explicitly wins over every environment variable, the provider-specific
// ones included: a client built for one tenant's key must not bill the process's.
// With no explicit key, the provider's own variables come first and
// SCHEMAFLUX_API_KEY last.
func resolveProviderAPIKey(providerName, explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, envVar := range providerAPIKeyEnvVars(providerName) {
		if value := os.Getenv(envVar); value != "" {
			return value
		}
	}
	return os.Getenv("SCHEMAFLUX_API_KEY")
}

func resolveProviderBaseURL(providerName string) string {
	for _, envVar := range providerBaseURLEnvVars(providerName) {
		if value := os.Getenv(envVar); value != "" {
			return value
		}
	}
	return ""
}

func providerAPIKeyEnvVars(providerName string) []string {
	switch llm.NormalizeProviderName(providerName) {
	case "openai":
		// "OPENAI" is accepted last: it is a common .env spelling, and rejecting
		// it means a file that plainly contains the key still resolves to nothing.
		return []string{"SCHEMAFLUX_OPENAI_API_KEY", "OPENAI_API_KEY", "OPENAI"}
	case "anthropic":
		return []string{"SCHEMAFLUX_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"}
	case "openrouter":
		return []string{"SCHEMAFLUX_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"}
	case "cerebras":
		// "CEREBRAS" for the same reason bare "OPENAI" is accepted above.
		return []string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY", "CEREBRAS"}
	case "deepseek":
		return []string{"SCHEMAFLUX_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"}
	case "qwen":
		return []string{"SCHEMAFLUX_QWEN_API_KEY", "QWEN_API_KEY", "DASHSCOPE_API_KEY"}
	case "zai":
		return []string{"SCHEMAFLUX_ZAI_API_KEY", "ZAI_API_KEY", "GLM_API_KEY"}
	default:
		if preset, ok := decisionPreset(providerName); ok {
			return append([]string(nil), preset.KeyVariables...)
		}
		return []string{"SCHEMAFLUX_API_KEY"}
	}
}

// decisionPreset returns the preset of the decision provider with this name:
// "jev", "jev-openrouter" or "jev-litellm". Their variables are read from the
// preset, which is where the provider itself reads them, and not repeated here:
// a table of names that drifted from the preset would send a key to the wrong
// provider, or leave a provider with the shared key when it has one of its own.
func decisionPreset(providerName string) (llm.SystemOnePreset, bool) {
	name := llm.NormalizeProviderName(providerName)
	for _, preset := range []llm.SystemOnePreset{llm.SystemOneTypeSafe, llm.SystemOneOpenRouter, llm.SystemOneLiteLLM} {
		if preset.Name == name {
			return preset, true
		}
	}
	return llm.SystemOnePreset{}, false
}

func providerBaseURLEnvVars(providerName string) []string {
	switch llm.NormalizeProviderName(providerName) {
	case "openai":
		return []string{"SCHEMAFLUX_OPENAI_BASE_URL", "OPENAI_BASE_URL"}
	case "anthropic":
		return []string{"SCHEMAFLUX_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"}
	case "openrouter":
		return []string{"SCHEMAFLUX_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"}
	case "cerebras":
		return []string{"SCHEMAFLUX_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"}
	case "deepseek":
		return []string{"SCHEMAFLUX_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"}
	case "qwen":
		return []string{"SCHEMAFLUX_QWEN_BASE_URL", "QWEN_BASE_URL", "DASHSCOPE_BASE_URL"}
	case "zai":
		return []string{"SCHEMAFLUX_ZAI_BASE_URL", "ZAI_BASE_URL", "GLM_BASE_URL"}
	default:
		if preset, ok := decisionPreset(providerName); ok {
			return append([]string(nil), preset.BaseURLVariables...)
		}
		return nil
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
