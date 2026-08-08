package client

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	cfg "github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Client represents a configured schemaflux client instance.
type Client struct {
	apiKey       string
	provider     llm.Provider
	providerName string
	timeout      time.Duration
	maxRetries   int
	retryBackoff time.Duration
	logger       *telemetry.Logger
	debugMode    bool

	// levelBeforeDebug is the logger level WithDebug(true) raised from, so
	// WithDebug(false) can put it back. Without it, turning debug off left the
	// logger at debug forever -- the option had exactly one direction.
	levelBeforeDebug    telemetry.LogLevel
	hasLevelBeforeDebug bool

	// configErr records the last configuration failure, so a builder chain that
	// could not do what it was asked is not silently indistinguishable from one
	// that could. Read it with Err.
	configErr error

	// budget is this client's own spend ledger (IN-004). Nil means the
	// process-wide pricing budget applies, which is what every caller who never
	// asked for one gets, unchanged.
	budget ops.BudgetChecker

	// scheduler bounds this client's concurrency. Nil means unscheduled.
	scheduler *ops.Scheduler

	// dataPolicy constrains where this client's calls may be routed.
	dataPolicy types.DataPolicy

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

// WithScheduler bounds this client's concurrency independently of any other
// client's.
func (client *Client) WithScheduler(scheduler *ops.Scheduler) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.scheduler = scheduler
	return client
}

// WithDataPolicy constrains where this client's calls may be routed.
func (client *Client) WithDataPolicy(policy types.DataPolicy) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.dataPolicy = policy
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

// Err reports the last configuration failure, or nil. The builder methods
// return *Client so they can chain, which leaves them nowhere to put an error;
// this is where it goes.
func (client *Client) Err() error {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.configErr
}

// NewClient creates a new client with custom configuration
func NewClient(apiKey string) *Client {
	client := &Client{
		apiKey:       apiKey,
		providerName: "openai",
		timeout:      30 * time.Second,
		maxRetries:   3,
		retryBackoff: 1 * time.Second,
		logger:       telemetry.GetLogger(),
	}

	// Initialize with OpenAI provider by default. An empty key leaves the
	// client with no provider: substituting the mock here meant every operation
	// returned "Mock response for: ..." and the caller had no way to notice.
	// Ask for the mock deliberately with WithMockProvider.
	if apiKey != "" {
		provider, err := llm.CreateProvider("openai", client.providerConfig("openai", llm.ProviderConfig{}))
		if err == nil {
			client.provider = provider
		} else {
			client.logger.Warn("Failed to create OpenAI provider", "error", err)
		}
	} else {
		client.logger.Warn("NewClient was given no API key; the client has no provider. " +
			"Call WithProvider, WithProviderInstance, or WithMockProvider before using it.")
	}

	return client
}

// WithMockProvider attaches the local mock provider, which answers every
// operation with canned text. It exists for tests and offline demos, and has to
// be asked for: an empty key used to select it silently.
func (client *Client) WithMockProvider() *Client {
	provider, err := llm.CreateProvider("local", llm.ProviderConfig{})
	if err != nil {
		client.logger.Error("Failed to create the mock provider", "error", err)
		return client
	}

	client.mu.Lock()
	client.provider = provider
	client.providerName = "local"
	client.mu.Unlock()

	ops.SetDefaultProvider(provider)
	return client
}

// WithTimeout sets a custom timeout for the client
func (client *Client) WithTimeout(timeout time.Duration) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.timeout = timeout
	return client
}

// WithRetries sets the maximum number of retry attempts for provider calls.
func (client *Client) WithRetries(maxRetries int) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if maxRetries < 0 {
		maxRetries = 0
	}
	client.maxRetries = maxRetries
	return client
}

// WithRetryBackoff sets the base backoff between retry attempts.
func (client *Client) WithRetryBackoff(backoff time.Duration) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	if backoff > 0 {
		client.retryBackoff = backoff
	}
	return client
}

// WithProvider sets a custom provider for the client
func (client *Client) WithProvider(providerName string) *Client {
	return client.WithProviderConfig(providerName, llm.ProviderConfig{})
}

// WithProviderConfig sets a custom provider for the client using explicit provider configuration.
func (client *Client) WithProviderConfig(providerName string, config llm.ProviderConfig) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()

	providerName = normalizeProviderName(providerName)

	provider, err := llm.CreateProvider(providerName, client.providerConfig(providerName, config))
	if err != nil {
		// The name used to be assigned before the provider was built, so a
		// failed switch left the client reporting the new provider's name while
		// still running the old one -- or the mock. Name and provider now move
		// together or not at all.
		client.configErr = fmt.Errorf("configuring provider %q: %w", providerName, err)
		client.logger.Error("Failed to configure provider; the previous one is still in use",
			"provider", providerName, "previous", client.providerName, "error", err)
		return client
	}

	client.configErr = nil
	client.providerName = providerName
	client.provider = provider
	ops.SetDefaultProvider(provider)

	// A provider this library has no model for is configured, not usable: every
	// operation will fail at its first call with a message the caller reads long
	// after they set this up. Say so now, while they are looking at the
	// configuration. The provider is still installed -- the caller may be about
	// to set SCHEMAFLUX_MODEL, and refusing to attach it would lose the rest of
	// the configuration to a problem they can fix.
	if err := cfg.ValidateModelResolution(providerName); err != nil {
		client.configErr = err
		client.logger.Warn("Provider configured with no resolvable model",
			"provider", providerName, "error", err)
		return client
	}

	client.logger.Info("Provider configured", "provider", providerName)

	return client
}

// WithProviderInstance sets an already-constructed provider on the client.
func (client *Client) WithProviderInstance(provider llm.Provider) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()

	if provider == nil {
		client.logger.Warn("Ignoring nil provider instance")
		return client
	}

	client.provider = provider
	client.providerName = provider.Name()
	ops.SetDefaultProvider(provider)
	client.logger.Info("Provider configured", "provider", provider.Name(), "mode", "instance")
	return client
}

// WithDebug enables debug mode for the client
func (client *Client) WithDebug(enabled bool) *Client {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.debugMode = enabled

	if enabled {
		// Record the level once. Two WithDebug(true) calls must not make the
		// remembered level "debug", or the restore becomes a no-op.
		if !client.hasLevelBeforeDebug {
			client.levelBeforeDebug = client.logger.Level()
			client.hasLevelBeforeDebug = true
		}
		client.logger.SetLevel(telemetry.DebugLevel)
		return client
	}

	if client.hasLevelBeforeDebug {
		client.logger.SetLevel(client.levelBeforeDebug)
		client.hasLevelBeforeDebug = false
	}
	return client
}

// WithRequestTracking configures global request and correlation tracking behavior.
func (client *Client) WithRequestTracking(cfg requesttracking.Config) *Client {
	requesttracking.Configure(cfg)
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
// A client with no provider leaves ctx unchanged, so the package default still
// applies and nothing that works today stops working.
func (client *Client) Context(ctx context.Context) context.Context {
	client.mu.RLock()
	snapshot := ops.ExecConfig{
		Provider:   client.provider,
		Budget:     client.budget,
		Scheduler:  client.scheduler,
		DataPolicy: client.dataPolicy,
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
	mu.Lock()
	defer mu.Unlock()

	apiKey := key
	if apiKey == "" {
		apiKey = os.Getenv("SCHEMAFLUX_API_KEY")
	}

	provider := "openai"
	if p := os.Getenv("SCHEMAFLUX_PROVIDER"); p != "" {
		provider = p
	}

	timeout := 30 * time.Second
	if t := os.Getenv("SCHEMAFLUX_TIMEOUT"); t != "" {
		if d, err := time.ParseDuration(t); err == nil {
			timeout = d
		}
	}

	debugMode := false
	if d := os.Getenv("SCHEMAFLUX_DEBUG"); d == "true" || d == "1" {
		debugMode = true
	}

	if apiKey == "" {
		apiKey = resolveProviderAPIKey(provider, "")
	}

	if apiKey == "" {
		// The mock provider is a legitimate choice, but only when it was asked
		// for. Selecting it because a credential is missing turns a
		// configuration error into plausible-looking fake output.
		if normalizeProviderName(provider) != "local" {
			return fmt.Errorf(
				"schemaflux: no API key for provider %q; set SCHEMAFLUX_API_KEY or a provider-specific key, or set SCHEMAFLUX_PROVIDER=local to use the mock provider deliberately",
				provider)
		}
		defaultClient = NewClient("").WithMockProvider()
		return nil
	}

	client := NewClient(apiKey).
		WithTimeout(timeout).
		WithProvider(provider).
		WithDebug(debugMode)
	defaultClient = client

	// Init has an error return and a configuration failure to report, so report
	// it. The client is still installed: the failure is "no model for this
	// provider", which a caller can fix with an environment variable without
	// rebuilding anything, and discarding their provider would help nobody.
	return client.Err()
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
		ops.SetDefaultProvider(nil)
		return
	}

	client.mu.RLock()
	provider := client.provider
	client.mu.RUnlock()

	if provider != nil {
		ops.SetDefaultProvider(provider)
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
func ConfigureLogging(cfg telemetry.LoggerConfig) *telemetry.Logger {
	log := telemetry.ConfigureLogger(cfg)
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
func ConfigureRequestTracking(cfg requesttracking.Config) {
	requesttracking.Configure(cfg)
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

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (client *Client) providerConfig(providerName string, override llm.ProviderConfig) llm.ProviderConfig {
	cfg := llm.ProviderConfig{
		APIKey:       resolveProviderAPIKey(providerName, client.apiKey),
		BaseURL:      resolveProviderBaseURL(providerName),
		Timeout:      client.timeout,
		MaxRetries:   client.maxRetries,
		RetryBackoff: client.retryBackoff,
		Debug:        client.debugMode,
	}

	if override.APIKey != "" {
		cfg.APIKey = override.APIKey
	}
	if override.BaseURL != "" {
		cfg.BaseURL = override.BaseURL
	}
	if override.OrgID != "" {
		cfg.OrgID = override.OrgID
	}
	if override.Timeout > 0 {
		cfg.Timeout = override.Timeout
	}
	if override.MaxRetries > 0 || client.maxRetries == 0 {
		cfg.MaxRetries = override.MaxRetries
	}
	if override.RetryBackoff > 0 {
		cfg.RetryBackoff = override.RetryBackoff
	}
	if override.Debug {
		cfg.Debug = true
	}
	if len(override.ExtraHeaders) > 0 {
		cfg.ExtraHeaders = cloneStringMap(override.ExtraHeaders)
	}
	if override.Store {
		cfg.Store = true
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
		cfg.EndpointPolicy = override.EndpointPolicy
	}
	if override.HTTPClient != nil {
		cfg.HTTPClient = override.HTTPClient
	}

	return cfg
}

func resolveProviderAPIKey(providerName, fallback string) string {
	for _, envVar := range providerAPIKeyEnvVars(providerName) {
		if value := os.Getenv(envVar); value != "" {
			return value
		}
	}
	if fallback != "" {
		return fallback
	}
	if value := os.Getenv("SCHEMAFLUX_API_KEY"); value != "" {
		return value
	}
	return ""
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
	switch normalizeProviderName(providerName) {
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
		return []string{"SCHEMAFLUX_API_KEY"}
	}
}

func providerBaseURLEnvVars(providerName string) []string {
	switch normalizeProviderName(providerName) {
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
