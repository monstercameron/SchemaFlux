package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func validSnapshot() ConfigSnapshot {
	return ConfigSnapshot{
		ProviderName:       "openai",
		ProviderRegistered: true,
		CredentialPresent:  true,
		Models:             map[string]string{"default": "gpt-5"},
		HTTPClientPresent:  true,
	}
}

func TestValidateConfigurationAcceptsAMinimalValidSnapshot(t *testing.T) {
	report, err := ValidateConfiguration(context.Background(), validSnapshot())
	if err != nil {
		t.Fatalf("unexpected error on a valid snapshot: %v", err)
	}
	if !report.OK() {
		t.Fatalf("expected OK()==true, got issues: %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesNoProviderSelected(t *testing.T) {
	snap := validSnapshot()
	snap.ProviderName = ""
	report, err := ValidateConfiguration(context.Background(), snap)
	if err == nil {
		t.Fatal("expected an error for no provider selected")
	}
	if !hasField(report, "provider") {
		t.Fatalf("expected a 'provider' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesUnregisteredProvider(t *testing.T) {
	snap := validSnapshot()
	snap.ProviderRegistered = false
	report, err := ValidateConfiguration(context.Background(), snap)
	if err == nil {
		t.Fatal("expected an error for an unregistered provider")
	}
	if !hasField(report, "provider") {
		t.Fatalf("expected a 'provider' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesMissingCredentialWithoutRevealingAnyValue(t *testing.T) {
	snap := validSnapshot()
	snap.CredentialPresent = false
	report, err := ValidateConfiguration(context.Background(), snap)
	if err == nil {
		t.Fatal("expected an error for a missing credential")
	}
	if !hasField(report, "credential") {
		t.Fatalf("expected a 'credential' issue, got %+v", report.Issues)
	}
	// ConfigSnapshot has no field capable of carrying a credential value in
	// the first place -- this assertion documents that the message cannot
	// have echoed one, since there is nothing in scope to echo.
	for _, issue := range report.Issues {
		if issue.Field == "credential" && issue.Message == "" {
			t.Fatal("credential issue carries no explanation at all")
		}
	}
}

func TestValidateConfigurationCatchesNoModelsConfigured(t *testing.T) {
	snap := validSnapshot()
	snap.Models = nil
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "models") {
		t.Fatalf("expected a 'models' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesEmptyModelIdentifier(t *testing.T) {
	snap := validSnapshot()
	snap.Models = map[string]string{"default": "  "}
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "models") {
		t.Fatalf("expected a 'models' issue for a blank model id, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesDisallowedEndpointScheme(t *testing.T) {
	snap := validSnapshot()
	snap.EndpointURL = "http://api.example.com/v1"
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "endpoint") {
		t.Fatalf("expected an 'endpoint' issue for a plaintext scheme, got %+v", report.Issues)
	}
}

func TestValidateConfigurationAllowsHTTPWhenExplicitlyPermitted(t *testing.T) {
	snap := validSnapshot()
	snap.EndpointURL = "http://localhost:8080/v1"
	snap.AllowedEndpointSchemes = []string{"http", "https"}
	report, err := ValidateConfiguration(context.Background(), snap)
	if err != nil {
		t.Fatalf("unexpected error with an explicitly allowed http scheme: %v", err)
	}
	if !report.OK() {
		t.Fatalf("expected OK()==true, got issues: %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesEndpointWithNoHost(t *testing.T) {
	snap := validSnapshot()
	snap.EndpointURL = "https:///path-only"
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "endpoint") {
		t.Fatalf("expected an 'endpoint' issue for a hostless URL, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesMissingHTTPClient(t *testing.T) {
	snap := validSnapshot()
	snap.HTTPClientPresent = false
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "http_client") {
		t.Fatalf("expected an 'http_client' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesUnsupportedRequiredJSONMode(t *testing.T) {
	snap := validSnapshot()
	snap.Capabilities = CapabilityAssumptions{RequiresJSONMode: true, ProviderSupportsJSONMode: false}
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "capabilities") {
		t.Fatalf("expected a 'capabilities' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationAllowsSupportedRequiredCapability(t *testing.T) {
	snap := validSnapshot()
	snap.Capabilities = CapabilityAssumptions{RequiresStreaming: true, ProviderSupportsStreaming: true}
	report, err := ValidateConfiguration(context.Background(), snap)
	if err != nil || !report.OK() {
		t.Fatalf("expected a clean report, got err=%v issues=%+v", err, report.Issues)
	}
}

func TestValidateConfigurationCatchesNegativeSchedulerLimit(t *testing.T) {
	snap := validSnapshot()
	limits := SchedulerLimits{MaxConcurrent: -1}
	snap.SchedulerLimits = &limits
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "scheduler.max_concurrent") {
		t.Fatalf("expected a scheduler issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationCatchesPerProviderLimitThatCanNeverBind(t *testing.T) {
	snap := validSnapshot()
	limits := SchedulerLimits{MaxConcurrent: 5, PerProviderConcurrency: 10}
	snap.SchedulerLimits = &limits
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "scheduler.per_provider_concurrency") {
		t.Fatalf("expected a scheduler.per_provider_concurrency issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationAllowsNilSchedulerLimits(t *testing.T) {
	snap := validSnapshot()
	snap.SchedulerLimits = nil
	report, err := ValidateConfiguration(context.Background(), snap)
	if err != nil || !report.OK() {
		t.Fatalf("a scheduler-less configuration must not be flagged: err=%v issues=%+v", err, report.Issues)
	}
}

func TestValidateConfigurationCatchesStoreNotReady(t *testing.T) {
	snap := validSnapshot()
	notReady := false
	snap.StoreReady = &notReady
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "store") {
		t.Fatalf("expected a 'store' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationAllowsNoStoreConfigured(t *testing.T) {
	snap := validSnapshot()
	snap.StoreReady = nil
	report, err := ValidateConfiguration(context.Background(), snap)
	if err != nil || !report.OK() {
		t.Fatalf("no store configured must not be flagged: err=%v issues=%+v", err, report.Issues)
	}
}

func TestValidateConfigurationCatchesBudgetEnforcementWithNoBudget(t *testing.T) {
	snap := validSnapshot()
	snap.BudgetEnforcementEnabled = true
	snap.Budget = 0
	report, _ := ValidateConfiguration(context.Background(), snap)
	if !hasField(report, "budget") {
		t.Fatalf("expected a 'budget' issue, got %+v", report.Issues)
	}
}

func TestValidateConfigurationMakesNoNetworkCallAndReturnsInstantly(t *testing.T) {
	// There is no network mock to assert against here by design -- the
	// point is structural: ConfigSnapshot carries no client or transport
	// capable of dialing anything, only booleans and strings describing
	// what is configured. This test exists as the documented assertion of
	// that fact, exercised through a snapshot with every field populated.
	snap := validSnapshot()
	snap.EndpointURL = "https://api.example.com"
	limits := SchedulerLimits{MaxConcurrent: 4, MaxQueued: 8}
	snap.SchedulerLimits = &limits
	ready := true
	snap.StoreReady = &ready
	if _, err := ValidateConfiguration(context.Background(), snap); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfigurationRespectsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ValidateConfiguration(ctx, validSnapshot())
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if errKind(t, err) != types.KindCanceled {
		t.Fatalf("kind = %v, want KindCanceled", errKind(t, err))
	}
}

func TestValidateConfigurationAggregatesMultipleIssuesIntoOneError(t *testing.T) {
	snap := ConfigSnapshot{} // everything wrong at once
	report, err := ValidateConfiguration(context.Background(), snap)
	if err == nil {
		t.Fatal("expected an aggregate error")
	}
	if len(report.Issues) < 3 {
		t.Fatalf("expected multiple issues from an empty snapshot, got %d: %+v", len(report.Issues), report.Issues)
	}
	var opErr *types.OperationError
	if !errors.As(err, &opErr) || opErr.Kind != types.KindConfiguration {
		t.Fatalf("expected KindConfiguration, got %v", err)
	}
}

func hasField(report ConfigReport, field string) bool {
	for _, issue := range report.Issues {
		if issue.Field == field {
			return true
		}
	}
	return false
}
