package schemaflux

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Client.providerConfig copies a caller's ProviderConfig field by field, and a
// hand-written copy list cannot fail when somebody adds a field to the struct
// it copies. It had already silently lost two: EndpointPolicy (SEC-004) and
// HTTPClient (SC-007), both opt-in and both security-relevant, so a caller
// switching endpoint enforcement on through the public API got a nil policy and
// no enforcement, while the check one layer down looked implemented and tested.
//
// This is A-014's bug in a second place (applyDefaults, internal/ops), so it
// gets A-014's guard: walk the struct by reflection rather than naming fields,
// because the only moment a list needed to fail is the moment somebody extended
// the struct without extending the list.

// nonZeroProviderConfig fills every field with a distinguishable non-zero
// value. Adding a field to llm.ProviderConfig without adding it here fails
// TestEveryProviderConfigFieldIsPopulatedInTheFixture, so the fixture cannot
// quietly fall behind the struct either.
func nonZeroProviderConfig() llm.ProviderConfig {
	return llm.ProviderConfig{
		APIKey:         "sk-override",
		BaseURL:        "https://override.example.com/v1",
		OrgID:          "org-override",
		Timeout:        11 * time.Second,
		MaxRetries:     7,
		RetryBackoff:   3 * time.Second,
		Debug:          true,
		ExtraHeaders:   map[string]string{"X-Override": "yes"},
		Store:          true,
		HTTPClient:     &http.Client{Timeout: 13 * time.Second},
		EndpointPolicy: &types.EndpointPolicy{},
	}
}

func TestEveryProviderConfigFieldIsPopulatedInTheFixture(t *testing.T) {
	value := reflect.ValueOf(nonZeroProviderConfig())
	structType := value.Type()

	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).IsZero() {
			t.Errorf("nonZeroProviderConfig leaves %s at its zero value, so no override test covers it",
				structType.Field(i).Name)
		}
	}
}

// The regression itself: everything a caller set on the override has to reach
// the config the provider is actually built from.
func TestProviderConfigOverrideCarriesEveryField(t *testing.T) {
	client := NewClient("sk-test")
	override := nonZeroProviderConfig()

	got := client.providerConfig("openai", override)

	inValue := reflect.ValueOf(override)
	outValue := reflect.ValueOf(got)
	structType := inValue.Type()

	for i := 0; i < inValue.NumField(); i++ {
		name := structType.Field(i).Name

		if outValue.Field(i).IsZero() {
			t.Errorf("providerConfig dropped %s entirely", name)
			continue
		}
		// Pointers are carried, not copied -- identity is the right assertion
		// for those, and DeepEqual on a *types.EndpointPolicy with an empty
		// allowlist would pass against a *different* empty policy, which is
		// exactly the substitution this is meant to catch.
		switch inValue.Field(i).Kind() {
		case reflect.Pointer:
			if inValue.Field(i).Pointer() != outValue.Field(i).Pointer() {
				t.Errorf("providerConfig replaced %s with a different value", name)
			}
		default:
			if !reflect.DeepEqual(inValue.Field(i).Interface(), outValue.Field(i).Interface()) {
				t.Errorf("providerConfig changed %s: %v -> %v",
					name, inValue.Field(i).Interface(), outValue.Field(i).Interface())
			}
		}
	}
}

// The specific control that was silently disabled, asserted on its own so a
// failure names the security consequence rather than a field name.
func TestEndpointPolicyReachesTheProviderConfig(t *testing.T) {
	policy := &types.EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}

	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{EndpointPolicy: policy})

	if got.EndpointPolicy == nil {
		t.Fatal("a caller-supplied EndpointPolicy never reached the provider config; SEC-004's check would run against nil and enforce nothing")
	}
	if got.EndpointPolicy != policy {
		t.Error("the EndpointPolicy that reached the provider config is not the one the caller supplied")
	}
}

// Not passing one must stay unenforced -- the field is opt-in, and a default
// that started refusing endpoints would break every existing caller.
func TestNoEndpointPolicyStaysUnenforced(t *testing.T) {
	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{})

	if got.EndpointPolicy != nil {
		t.Error("a policy appeared without the caller asking for one; EndpointPolicy is opt-in")
	}
}

// An override that mentions nothing must not erase what the client resolved
// for itself -- the other half of a merge, and the half a copy list tends to
// get right only by accident.
func TestAnEmptyOverrideKeepsTheClientsOwnSettings(t *testing.T) {
	client := NewClient("sk-test").WithTimeout(29 * time.Second).WithRetries(5).WithRetryBackoff(2 * time.Second)

	got := client.providerConfig("openai", llm.ProviderConfig{})

	if got.Timeout != 29*time.Second {
		t.Errorf("Timeout = %v; an empty override erased the client's own setting", got.Timeout)
	}
	if got.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d; an empty override erased the client's own setting", got.MaxRetries)
	}
	if got.RetryBackoff != 2*time.Second {
		t.Errorf("RetryBackoff = %v; an empty override erased the client's own setting", got.RetryBackoff)
	}
}

// ExtraHeaders is copied rather than aliased, so a caller mutating their own
// map after the call does not retroactively change what a provider sends.
func TestExtraHeadersAreCopiedNotAliased(t *testing.T) {
	headers := map[string]string{"X-Tenant": "acme"}

	got := NewClient("sk-test").providerConfig("openai", llm.ProviderConfig{ExtraHeaders: headers})
	headers["X-Tenant"] = "other"

	if got.ExtraHeaders["X-Tenant"] != "acme" {
		t.Error("mutating the caller's header map changed the config after the fact; the map was aliased, not copied")
	}
}
