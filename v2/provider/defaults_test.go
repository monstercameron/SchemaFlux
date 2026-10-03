package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// generating are the built-in names that write text, each with the key variable
// and the address that make it buildable without a Config key.
var builtInNames = []string{"openai", "anthropic", "openrouter", "cerebras", "deepseek", "qwen", "zai", "jev", "jev-openrouter", "jev-litellm"}

func buildable(name string, cfg Config) Config {
	cfg.APIKey = testKey
	if name == "jev-litellm" && cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:4000"
	}
	return cfg
}

func TestABuiltProviderRetriesThreeTimesWithAOneSecondBackoff(t *testing.T) {
	t.Parallel()
	for _, name := range slicesClone(builtInNames) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := mustNew(t, name, buildable(name, Config{}))
			retrier, ok := p.(Retrier)
			if !ok {
				t.Fatalf("%s is not a Retrier", name)
			}
			retries, backoff := retrier.RetryPolicy()
			if retries != 3 || backoff != time.Second {
				t.Errorf("RetryPolicy() = (%d, %v), want (3, 1s)", retries, backoff)
			}
		})
	}
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func TestTheRetrySettingsOfTheConfigAreTheProvidersOwn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         Config
		wantRetries int
		wantBackoff time.Duration
	}{
		{"nothing set", Config{}, 3, time.Second},
		{"one retry", Config{Retries: 1}, 1, time.Second},
		{"five retries", Config{Retries: 5}, 5, time.Second},
		{"none is a negative number", Config{Retries: -1}, -1, time.Second},
		{"none is any negative number", Config{Retries: -40}, -1, time.Second},
		{"a longer backoff", Config{RetryBackoff: 4 * time.Second}, 3, 4 * time.Second},
		{"a shorter backoff", Config{RetryBackoff: 10 * time.Millisecond}, 3, 10 * time.Millisecond},
		{"both set", Config{Retries: 2, RetryBackoff: 250 * time.Millisecond}, 2, 250 * time.Millisecond},
		{"none with a backoff", Config{Retries: -1, RetryBackoff: 5 * time.Second}, -1, 5 * time.Second},
		{"a timeout changes neither", Config{Timeout: 5 * time.Second}, 3, time.Second},
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "jev"} {
		for _, tt := range tests {
			t.Run(name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				p := mustNew(t, name, buildable(name, tt.cfg))
				retries, backoff := p.(Retrier).RetryPolicy()
				if retries != tt.wantRetries || backoff != tt.wantBackoff {
					t.Errorf("RetryPolicy() = (%d, %v), want (%d, %v)", retries, backoff, tt.wantRetries, tt.wantBackoff)
				}
			})
		}
	}
}

func TestTheHTTPTimeoutOfABuiltProviderIsThirtySecondsUnlessTheConfigSaysOtherwise(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
		want time.Duration
	}{
		{"nothing set", Config{}, 30 * time.Second},
		{"a shorter timeout", Config{Timeout: 5 * time.Second}, 5 * time.Second},
		{"a longer timeout", Config{Timeout: 2 * time.Minute}, 2 * time.Minute},
		{"retries do not change it", Config{Retries: -1}, 30 * time.Second},
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "jev"} {
		for _, tt := range tests {
			t.Run(name+" "+tt.name, func(t *testing.T) {
				t.Parallel()
				probe := newDeadlineProbe()
				cfg := buildable(name, tt.cfg)
				cfg.HTTPClient = probe.client
				p := mustNew(t, name, cfg)
				req := schemaflux.Request{Model: "m", User: "hi"}
				if name == "jev" {
					req.Decision = &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionYesNo, Ask: "ok?"}}}
				}
				_, _ = p.Complete(t.Context(), req)
				left, bounded := probe.remaining()
				if !bounded {
					t.Fatal("the request went out with no deadline")
				}
				if left > tt.want || left < tt.want-5*time.Second {
					t.Errorf("the request had %v left, want a little under %v", left, tt.want)
				}
			})
		}
	}
}

func TestInternalConfigCarriesEveryFieldOfTheConfig(t *testing.T) {
	t.Parallel()
	client := &http.Client{}
	headers := map[string]string{"X-One": "1", "X-Two": "2"}
	full := Config{
		APIKey: " key ", BaseURL: " https://example.test/v1 ", OrgID: "org", Timeout: 7 * time.Second, Retries: 4,
		RetryBackoff: 3 * time.Second, Headers: headers, Store: true, ServiceTier: "flex", OmitTemperature: true,
		HTTPClient: client, Endpoint: &EndpointPolicy{Schemes: []string{"https"}, Hosts: []string{"example.test"}, AllowPrivate: true},
		Models: Models{Smart: "s", Fast: "f", Quick: "q"},
	}
	got := internalConfig(full)

	want := llm.ProviderConfig{
		APIKey: "key", BaseURL: "https://example.test/v1", OrgID: "org", Timeout: 7 * time.Second, MaxRetries: 4,
		RetryBackoff: 3 * time.Second, ExtraHeaders: headers, Store: true, ServiceTier: "flex", OmitTemperature: true,
		HTTPClient:     client,
		Models:         [3]string{"s", "f", "q"},
		EndpointPolicy: &types.EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"example.test"}, AllowPrivateAddresses: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("internalConfig =\n%+v\nwant\n%+v", got, want)
	}

	t.Run("no field of the internal configuration is left out by accident", func(t *testing.T) {
		t.Parallel()
		// Debug is the one field of the internal configuration the Config does not
		// carry: nothing reads it.
		fields := reflect.ValueOf(got)
		for i := 0; i < fields.NumField(); i++ {
			name := fields.Type().Field(i).Name
			if name == "Debug" {
				continue
			}
			if fields.Field(i).IsZero() {
				t.Errorf("a Config with every field set gives internal field %s its zero value", name)
			}
		}
	})
	t.Run("the headers are copied", func(t *testing.T) {
		t.Parallel()
		headers := map[string]string{"X-One": "1"}
		internal := internalConfig(Config{Headers: headers})
		headers["X-One"] = "changed"
		headers["X-Late"] = "late"
		if internal.ExtraHeaders["X-One"] != "1" || len(internal.ExtraHeaders) != 1 {
			t.Errorf("changing the caller's map changed the provider's: %v", internal.ExtraHeaders)
		}
	})
	t.Run("the endpoint policy is copied", func(t *testing.T) {
		t.Parallel()
		policy := &EndpointPolicy{Schemes: []string{"https"}, Hosts: []string{"a.test"}}
		internal := internalConfig(Config{Endpoint: policy})
		policy.Hosts[0] = "b.test"
		if internal.EndpointPolicy.AllowedHosts[0] != "a.test" {
			t.Error("changing the caller's policy changed the provider's")
		}
	})
	t.Run("a negative number of retries is none", func(t *testing.T) {
		t.Parallel()
		if got := internalConfig(Config{Retries: -3}).MaxRetries; got != 0 {
			t.Errorf("MaxRetries = %d, want 0", got)
		}
	})
}

func TestConfigFromInternalIsTheInverseOfInternalConfig(t *testing.T) {
	t.Parallel()
	client := &http.Client{}
	tests := []struct {
		name string
		cfg  Config
	}{
		{"nothing set", Config{Retries: -1}},
		{"a key and an address", Config{APIKey: "k", BaseURL: "https://example.test", Retries: -1}},
		{"retries", Config{Retries: 6}},
		{"timeouts", Config{Timeout: time.Minute, RetryBackoff: time.Second, Retries: -1}},
		{"headers", Config{Headers: map[string]string{"A": "b"}, Retries: -1}},
		{"an organisation", Config{OrgID: "org", Retries: -1}},
		{"store and temperature", Config{Store: true, OmitTemperature: true, Retries: -1}},
		{"a service tier", Config{ServiceTier: "auto", Retries: -1}},
		{"a client", Config{HTTPClient: client, Retries: -1}},
		{"an endpoint policy", Config{Endpoint: &EndpointPolicy{Schemes: []string{"https"}, Hosts: []string{"a.test"}, AllowPrivate: true}, Retries: -1}},
		{"models", Config{Models: Models{Smart: "a", Fast: "b", Quick: "c"}, Retries: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := configFromInternal(internalConfig(tt.cfg))
			if !reflect.DeepEqual(got, tt.cfg) {
				t.Errorf("round trip gave\n%+v\nwant\n%+v", got, tt.cfg)
			}
		})
	}
}

func TestWithDefaultsFillsOnlyWhatIsZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{"zero", Config{}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"a timeout is kept", Config{Timeout: time.Second}, Config{Timeout: time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"a backoff is kept", Config{RetryBackoff: time.Minute}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Minute, Retries: 3}},
		{"retries are kept", Config{Retries: 9}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 9}},
		{"none is kept", Config{Retries: -1}, Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: -1}},
		{"a key is untouched", Config{APIKey: "k"}, Config{APIKey: "k", Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"applying it twice changes nothing", Config{Retries: -1}.withDefaults(), Config{Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: -1}},
		{"every field set", Config{Timeout: 1, RetryBackoff: 2, Retries: 3}, Config{Timeout: 1, RetryBackoff: 2, Retries: 3}},
		{"the models are untouched", Config{Models: Models{Fast: "f"}}, Config{Models: Models{Fast: "f"}, Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
		{"a base URL is untouched", Config{BaseURL: "u"}, Config{BaseURL: "u", Timeout: 30 * time.Second, RetryBackoff: time.Second, Retries: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.in.withDefaults(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("withDefaults() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTheModelsOfAConfigAreTheProvidersTierModels(t *testing.T) {
	t.Parallel()
	models := Models{Smart: "smart-model", Fast: "fast-model", Quick: "quick-model"}
	tests := []struct {
		name   string
		models Models
		tier   types.Speed
		want   string
	}{
		{"smart", models, types.Smart, "smart-model"},
		{"fast", models, types.Fast, "fast-model"},
		{"quick", models, types.Quick, "quick-model"},
		{"an empty entry is the built-in model", Models{Smart: "only-smart"}, types.Fast, config.BuiltInModel("openai", types.Fast)},
		{"a set entry beside an empty one", Models{Smart: "only-smart"}, types.Smart, "only-smart"},
		{"no models at all", Models{}, types.Quick, config.BuiltInModel("openai", types.Quick)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := mustNew(t, "openai", buildable("openai", Config{Models: tt.models}))
			internal := p.(interface{ InternalProvider() llm.Provider }).InternalProvider()
			got, ok := llm.ModelFor(internal, tt.tier)
			if !ok || got != tt.want {
				t.Errorf("ModelFor(%v) = %q, %v, want %q", tt.tier, got, ok, tt.want)
			}
		})
	}
}

func TestTheOtherSettingsOfTheConfigReachTheRequest(t *testing.T) {
	t.Parallel()
	send := func(t *testing.T, cfg Config) seen {
		t.Helper()
		srv := serve(t, replying(200, responsesOK))
		p := providerAt(t, "openai", srv.URL, cfg)
		if _, err := p.Complete(t.Context(), schemaflux.Request{Model: "custom-model", System: "s", User: "u", Temperature: 0.7}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		return srv.last(t)
	}
	bodyOf := func(t *testing.T, got seen) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal([]byte(got.body), &body); err != nil {
			t.Fatalf("the body is not JSON: %v", err)
		}
		return body
	}

	t.Run("the headers are added to every request", func(t *testing.T) {
		t.Parallel()
		got := send(t, Config{Headers: map[string]string{"X-Team": "blue", "X-Trace": "on"}})
		if got.header.Get("X-Team") != "blue" || got.header.Get("X-Trace") != "on" {
			t.Errorf("headers = %v", got.header)
		}
	})
	t.Run("the organisation goes in the OpenAI-Organization header", func(t *testing.T) {
		t.Parallel()
		if got := send(t, Config{OrgID: "org-7"}); got.header.Get("OpenAI-Organization") != "org-7" {
			t.Errorf("headers = %v", got.header)
		}
	})
	t.Run("no organisation sends no header", func(t *testing.T) {
		t.Parallel()
		if got := send(t, Config{}); got.header.Get("OpenAI-Organization") != "" {
			t.Errorf("headers = %v", got.header)
		}
	})
	t.Run("the response is not stored unless asked", func(t *testing.T) {
		t.Parallel()
		if got := bodyOf(t, send(t, Config{})); got["store"] != false {
			t.Errorf("store = %v, want false", got["store"])
		}
	})
	t.Run("Store asks the provider to keep the response", func(t *testing.T) {
		t.Parallel()
		if got := bodyOf(t, send(t, Config{Store: true})); got["store"] != true {
			t.Errorf("store = %v, want true", got["store"])
		}
	})
	t.Run("the temperature is sent unless OmitTemperature", func(t *testing.T) {
		t.Parallel()
		with := bodyOf(t, send(t, Config{}))
		without := bodyOf(t, send(t, Config{OmitTemperature: true}))
		if _, sent := with["temperature"]; !sent {
			t.Skip("this model's table sends no temperature at this address; nothing to omit")
		}
		if _, sent := without["temperature"]; sent {
			t.Error("OmitTemperature sent a temperature")
		}
	})
	t.Run("a service tier is refused where the provider cannot send it", func(t *testing.T) {
		t.Parallel()
		_, err := newFrom(lookupOf(), "openai", Config{APIKey: testKey, BaseURL: "http://127.0.0.1:1", ServiceTier: "flex"})
		if !errors.Is(err, schemaflux.ErrConfiguration) {
			t.Errorf("got %v, want a configuration error", err)
		}
	})
	t.Run("the headers of a Config are the provider's own after the map changes", func(t *testing.T) {
		t.Parallel()
		headers := map[string]string{"X-Team": "blue"}
		srv := serve(t, replying(200, responsesOK))
		p := providerAt(t, "openai", srv.URL, Config{Headers: headers})
		headers["X-Team"] = "red"
		if _, err := ask(p); err != nil {
			t.Fatal(err)
		}
		if got := srv.last(t).header.Get("X-Team"); got != "blue" {
			t.Errorf("X-Team = %q, want the value the provider was built with", got)
		}
	})
}
