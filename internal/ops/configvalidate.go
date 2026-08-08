package ops

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/monstercameron/schemaflux/internal/types"
)

// M12/SC-008. ValidateConfiguration(ctx) — non-billable readiness: provider
// registration, credential presence without revealing values, model maps,
// endpoint scheme and host policy, HTTP client presence, capability
// assumptions, scheduler limits, store readiness, and contradictory
// settings.
//
// This file is deliberately written against an explicit ConfigSnapshot
// rather than against *Client: client.go is out of scope for this change
// (see the task's file constraints), and the fields ValidateConfiguration
// needs to see -- provider registration, credential presence, the model
// map, and so on -- live inside *Client's unexported state. Wiring this in
// is a one-line addition for whoever owns client.go: populate a
// ConfigSnapshot from the *Client's fields and call ValidateConfiguration.
// Until then, the checking logic exists and is fully tested on its own,
// which is worth more than deferring it entirely until the seam is
// available.
//
// Every check here is local: no network call, no provider round trip. A
// caller wanting to confirm a provider actually answers wants a different,
// explicitly billable function (SC-008's own "a separate ProbeProviders may
// make real calls and must say that it bills"), which this file does not
// provide -- inventing one here would be scope creep past what SC-008 asks
// this task to build.

// ConfigSnapshot is the read-only view ValidateConfiguration checks. It
// never carries a credential's value, only whether one is present --
// "without revealing values" is enforced by this struct's shape, not by
// runtime redaction: the value has nowhere to go because there is no field
// for it.
type ConfigSnapshot struct {
	// ProviderName is the configured provider's name, or empty if none was
	// selected.
	ProviderName string

	// ProviderRegistered reports whether ProviderName resolves to a known
	// provider factory. False even for a non-empty ProviderName is a
	// configuration error the caller cannot fix by waiting.
	ProviderRegistered bool

	// CredentialPresent reports whether a credential (API key, token) was
	// supplied for the provider. It is a boolean on purpose.
	CredentialPresent bool

	// Models maps a logical name (a tier, or "default") to the resolved
	// provider model identifier. Empty means nothing was configured to
	// call.
	Models map[string]string

	// EndpointURL is the caller-supplied endpoint override, or empty to use
	// the provider's built-in default (which this snapshot does not need to
	// know, since a caller who did not override anything cannot have
	// misconfigured the override).
	EndpointURL string

	// AllowedEndpointSchemes lists schemes EndpointURL may use. A nil or
	// empty slice defaults to {"https"} -- refusing a plaintext endpoint by
	// default is the safer failure mode for a library whose payloads are
	// invoices and tickets (see AGENTS.md's "never log or embed the
	// caller's payload"; sending them in cleartext is the same category of
	// mistake at the transport layer instead of the log line).
	AllowedEndpointSchemes []string

	// HTTPClientPresent reports whether an *http.Client (or equivalent
	// transport) is configured. This snapshot intentionally holds a bool,
	// not the client itself: this file has no use for the client beyond
	// knowing it exists, and holding the real thing would tempt a future
	// edit to reach into it and make a network call, which is exactly what
	// "non-billable" rules out.
	HTTPClientPresent bool

	// Capabilities records what the current configuration assumes the
	// provider supports vs. what it is declared to support, so a
	// requirement with nothing behind it is caught before the first call
	// that needs it.
	Capabilities CapabilityAssumptions

	// SchedulerLimits is the scheduler configuration in effect, or nil if
	// no Scheduler is being used at all -- which is not itself a
	// configuration error.
	SchedulerLimits *SchedulerLimits

	// StoreReady is nil if no store is configured (not an error: many
	// configurations have none), or points at whether a configured store
	// reported itself ready.
	StoreReady *bool

	// BudgetEnforcementEnabled and Budget mirror pricing.SetBudgetEnforcement
	// and the ceiling it enforces against -- checked here for the specific
	// contradiction of enforcement being on with nothing to enforce.
	BudgetEnforcementEnabled bool
	Budget                   float64
}

// CapabilityAssumptions is the minimal shape ValidateConfiguration checks
// for now: does something in the configuration require a capability the
// provider is not declared to support. CP-001 (M14) is where a full
// capability model belongs; this only catches the contradictions a
// configuration can state about itself today.
type CapabilityAssumptions struct {
	RequiresJSONMode          bool
	ProviderSupportsJSONMode  bool
	RequiresStreaming         bool
	ProviderSupportsStreaming bool
}

// ConfigIssue is one finding. Kind is always types.KindConfiguration --
// every issue here is, definitionally, a configuration problem -- but is
// still carried per-issue rather than only on the aggregate error, so a
// caller inspecting a ConfigReport does not have to reconstruct it.
type ConfigIssue struct {
	Field   string
	Message string
}

// ConfigReport is every issue ValidateConfiguration found. A report with no
// issues is a ready configuration.
type ConfigReport struct {
	Issues []ConfigIssue
}

// OK reports whether the configuration is free of findings.
func (r ConfigReport) OK() bool { return len(r.Issues) == 0 }

// Err returns a single classified error summarizing every issue, or nil if
// there were none. Field names and short reasons only -- no configuration
// value is echoed into the message, since a misconfigured endpoint URL or
// model name can itself be sensitive in some deployments and the checks
// below never need to quote it back to explain what is wrong.
func (r ConfigReport) Err() error {
	if r.OK() {
		return nil
	}
	fields := make([]string, 0, len(r.Issues))
	for _, issue := range r.Issues {
		fields = append(fields, issue.Field)
	}
	sort.Strings(fields)
	return &types.OperationError{
		Kind:    types.KindConfiguration,
		Op:      "ValidateConfiguration",
		Message: fmt.Sprintf("%d configuration issue(s): %s", len(r.Issues), strings.Join(fields, ", ")),
	}
}

// ValidateConfiguration checks snap against every rule this file knows and
// returns every issue found, plus the same findings as a single classified
// error via ConfigReport.Err() for a caller that just wants a pass/fail.
// It makes no network call and reads only what snap already carries.
func ValidateConfiguration(ctx context.Context, snap ConfigSnapshot) (ConfigReport, error) {
	var report ConfigReport
	add := func(field, format string, args ...any) {
		report.Issues = append(report.Issues, ConfigIssue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if err := ctx.Err(); err != nil {
		return report, classifyBlockingErr(err)
	}

	if snap.ProviderName == "" {
		add("provider", "no provider selected")
	} else if !snap.ProviderRegistered {
		add("provider", "provider %q is not a registered provider", snap.ProviderName)
	}

	if !snap.CredentialPresent {
		add("credential", "no credential configured for the provider")
	}

	if len(snap.Models) == 0 {
		add("models", "no model is configured for any tier")
	} else {
		for tier, model := range snap.Models {
			if strings.TrimSpace(model) == "" {
				add("models", "tier %q maps to an empty model identifier", tier)
			}
		}
	}

	if snap.EndpointURL != "" {
		validateEndpoint(snap, add)
	}

	if !snap.HTTPClientPresent {
		add("http_client", "no HTTP client or transport is configured")
	}

	if snap.Capabilities.RequiresJSONMode && !snap.Capabilities.ProviderSupportsJSONMode {
		add("capabilities", "configuration requires JSON mode but the provider is not declared to support it")
	}
	if snap.Capabilities.RequiresStreaming && !snap.Capabilities.ProviderSupportsStreaming {
		add("capabilities", "configuration requires streaming but the provider is not declared to support it")
	}

	if snap.SchedulerLimits != nil {
		validateSchedulerLimits(*snap.SchedulerLimits, add)
	}

	if snap.StoreReady != nil && !*snap.StoreReady {
		add("store", "a store is configured but reports itself not ready")
	}

	if snap.BudgetEnforcementEnabled && snap.Budget <= 0 {
		add("budget", "budget enforcement is enabled but the budget is zero or negative")
	}

	return report, report.Err()
}

func validateEndpoint(snap ConfigSnapshot, add func(field, format string, args ...any)) {
	parsed, err := url.Parse(snap.EndpointURL)
	if err != nil {
		add("endpoint", "endpoint URL does not parse")
		return
	}
	if parsed.Host == "" {
		add("endpoint", "endpoint URL has no host")
	}

	allowed := snap.AllowedEndpointSchemes
	if len(allowed) == 0 {
		allowed = []string{"https"}
	}
	schemeOK := false
	for _, s := range allowed {
		if strings.EqualFold(parsed.Scheme, s) {
			schemeOK = true
			break
		}
	}
	if !schemeOK {
		add("endpoint", "endpoint scheme %q is not in the allowed set %v", parsed.Scheme, allowed)
	}
}

func validateSchedulerLimits(limits SchedulerLimits, add func(field, format string, args ...any)) {
	if limits.MaxConcurrent < 0 {
		add("scheduler.max_concurrent", "negative MaxConcurrent")
	}
	if limits.MaxQueued < 0 {
		add("scheduler.max_queued", "negative MaxQueued")
	}
	if limits.MaxInFlightTokens < 0 {
		add("scheduler.max_inflight_tokens", "negative MaxInFlightTokens")
	}
	if limits.MaxInFlightCost < 0 {
		add("scheduler.max_inflight_cost", "negative MaxInFlightCost")
	}
	if limits.PerProviderConcurrency < 0 {
		add("scheduler.per_provider_concurrency", "negative PerProviderConcurrency")
	}
	if limits.PerTenantConcurrency < 0 {
		add("scheduler.per_tenant_concurrency", "negative PerTenantConcurrency")
	}
	// A per-provider or per-tenant ceiling that is stricter than nothing but
	// looser than the global ceiling is fine; one that is set ABOVE a
	// nonzero global MaxConcurrent can never bind and is very likely a typo
	// -- the caller meant the smaller number to be the constraint.
	if limits.MaxConcurrent > 0 && limits.PerProviderConcurrency > limits.MaxConcurrent {
		add("scheduler.per_provider_concurrency", "PerProviderConcurrency (%d) exceeds MaxConcurrent (%d) and can never take effect", limits.PerProviderConcurrency, limits.MaxConcurrent)
	}
	if limits.MaxConcurrent > 0 && limits.PerTenantConcurrency > limits.MaxConcurrent {
		add("scheduler.per_tenant_concurrency", "PerTenantConcurrency (%d) exceeds MaxConcurrent (%d) and can never take effect", limits.PerTenantConcurrency, limits.MaxConcurrent)
	}
}
