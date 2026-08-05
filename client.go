package schemaflux

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/telemetry"
	openai "github.com/sashabaranov/go-openai"
)

// Client represents a configured schemaflux client instance.
type Client struct {
	openaiClient *openai.Client // Legacy OpenAI client
	apiKey       string
	provider     llm.Provider
	providerName string
	timeout      time.Duration
	maxRetries   int
	retryBackoff time.Duration
	logger       *telemetry.Logger
	debugMode    bool
	mu           sync.RWMutex
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

	// Initialize with OpenAI provider by default
	if apiKey != "" {
		client.openaiClient = openai.NewClient(apiKey)
		provider, err := llm.CreateProvider("openai", client.providerConfig("openai", llm.ProviderConfig{}))
		if err == nil {
			client.provider = provider
		} else {
			client.logger.Warn("Failed to create OpenAI provider", "error", err)
		}
	} else {
		localProvider, _ := llm.CreateProvider("local", llm.ProviderConfig{})
		client.provider = localProvider
	}

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
	client.providerName = providerName

	provider, err := llm.CreateProvider(providerName, client.providerConfig(providerName, config))
	if err != nil {
		client.logger.Warn("Failed to create provider, using default", "provider", providerName, "error", err)
		return client
	}

	client.provider = provider
	ops.SetDefaultProvider(provider)
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
		client.logger.SetLevel(telemetry.DebugLevel)
	}
	return client
}

// WithRequestTracking configures global request and correlation tracking behavior.
func (client *Client) WithRequestTracking(cfg requesttracking.Config) *Client {
	requesttracking.Configure(cfg)
	return client
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
		defaultClient = NewClient("")
		return nil
	}

	defaultClient = NewClient(apiKey).
		WithTimeout(timeout).
		WithProvider(provider).
		WithDebug(debugMode)
	return nil
}

// GetDefaultClient returns the default client
func GetDefaultClient() *Client {
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
	if defaultClient != nil {
		return defaultClient.logger
	}
	return telemetry.GetLogger()
}

// ConfigureLogging replaces the global logger configuration and keeps the default client in sync.
func ConfigureLogging(cfg telemetry.LoggerConfig) *telemetry.Logger {
	log := telemetry.ConfigureLogger(cfg)
	if defaultClient != nil {
		defaultClient.logger = log
	}
	return log
}

// SetLogLevel updates the global logger level.
func SetLogLevel(level telemetry.LogLevel) {
	log := telemetry.GetLogger()
	log.SetLevel(level)
	if defaultClient != nil {
		defaultClient.logger = log
	}
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
		return []string{"SCHEMAFLUX_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"}
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
